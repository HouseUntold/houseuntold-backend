# House Untold Backend Architecture (Latest State)

Direct UPI QR + Manual Admin Verification + WhatsApp Cloud PNG Ticket Pass + Dual Barcodes (Single-Use HMAC Gate QR + Multi-Use 5-Char Code-39 1D Bartender Barcode).

## 1. Backend Architecture

```mermaid
flowchart LR
    Client["Guest Web / Gate Scanner / Bar POS / Admin Console"]
    Router["Chi Router (internal/server/router.go)<br/>25 Routes under /api"]

    subgraph MW["Middleware Pipeline"]
        SEC["SecurityHeaders + CORS<br/>(SameSite=Strict, CORP=same-origin, no-store)"]
        RL["PostgresRateLimiter<br/>(UPSERT rate_limits, staff:id / ip:addr, 3s ctx)"]
        AUTH["RBAC.Authenticate<br/>(JWT + CSRF Origin + StaffRoleCache)"]
        RBAC["RBAC.RequireRole<br/>(ADMIN / SCANNER / BARTENDER)"]
    end

    CACHE[("StaffRoleCache<br/>(In-Memory TTL + Instant Eviction)")]

    subgraph Handlers["internal/handlers"]
        TH["TicketingHandler<br/>(Health, Events, Checkout, UTR, Tickets, PNG, Auth)"]
        GH["GateHandler<br/>(Validate QR, Atomic Check-In)"]
        BH["BarHandler<br/>(Menu CRUD, 1D Search, History, Redeem)"]
        AH["AdminHandler<br/>(Staff, UPI Queue/Verify, Events, Tiers, Stats)"]
    end

    subgraph Services["internal/services"]
        TS["TicketingService"]
        GS["GateService"]
        BS["BarService"]
        AS["AdminService"]
    end

    IMG["ticketimg.RenderTicketPNG<br/>(960x480 PNG + Code-39 1D Barcode)"]
    WA["whatsapp.CloudNotifier<br/>(Bounded Workers -> Meta Graph Media + Image Msg)"]
    REPO["TicketingRepository<br/>(WithTx + 3s context.WithTimeout)"]
    SQLC["internal/db (sqlc Querier)<br/>43 Explicit-Column Queries"]
    PG[("PostgreSQL (pgxpool)<br/>Row Locks + Unique Indexes")]

    Client --> Router --> SEC
    SEC -->|Public & Auth| RL --> TH
    SEC -->|Protected| AUTH --> RBAC
    AUTH <--> CACHE
    AUTH -.->|Cache Miss| SQLC
    RBAC -->|Gate / Bar| RL
    RL --> GH & BH
    RBAC -->|Admin| AH

    TH --> TS
    GH --> GS
    BH --> BS
    AH --> AS

    TS & AS --> WA
    WA --> IMG
    TS --> IMG
    TS & AS -.->|Invalidate on Logout/Pwd/Deactivate| CACHE

    TS & GS & BS & AS --> REPO --> SQLC --> PG
    RL --> SQLC
```

## 2. End-to-End Sequence Diagram (Direct UPI -> Admin Verify -> WhatsApp PNG -> Gate -> Bar)

```mermaid
sequenceDiagram
    autonumber
    actor Guest
    participant API as House Untold API (Router / Handler / Service)
    participant DB as PostgreSQL (sqlc + 3s Tx)
    actor Admin as Admin Console
    participant WA as Meta WhatsApp Cloud API
    actor Gate as Gate Scanner
    actor Bar as Bartender POS

    Note over Guest,DB: 1. Guest Checkout & Direct UPI QR Payment
    Guest->>API: GET /api/events/{id}
    API->>DB: GetEventByID + ListEventCategories + CountConfirmedFemaleStagByEvent
    API-->>Guest: 200 {upi_vpa, tiers with live First-99 pricing}

    Guest->>API: POST /api/checkout/init (event, category, guests)
    API->>DB: BEGIN Tx (3s ctx)
    opt STAG_FEMALE (First-99 Rule)
        API->>DB: GetEventByIDForUpdate (Row Lock) + CountConfirmedFemaleStagByEvent
    end
    API->>DB: CreateBooking (PENDING, or CONFIRMED if ₹0)
    API->>DB: CreateTicket (HMAC qr_token_digest + unique 5-char [A-Z][0-9]{4} bar_code)
    API->>DB: CreateTicketGuest (N rows) + COMMIT
    API-->>Guest: 201 {booking_id, status: PENDING, upi_payment: {vpa, amount_inr, intent_uri}}

    Guest->>Guest: Pays exact amount via UPI App & copies UTR
    Guest->>API: POST /api/checkout/{booking_id}/upi-reference {upi_transaction_id}
    API->>DB: SubmitBookingUPITransactionID (unique partial index on UTR)
    API-->>Guest: 200 {status: PENDING, upi_transaction_id}

    Note over Admin,WA: 2. Admin Manual UPI Verification & WhatsApp Ticket Image Delivery
    Admin->>API: GET /api/admin/bookings?status=PENDING
    API->>DB: ListBookingsForVerification (UTR + expected_amount + tier_base_price)
    API-->>Admin: 200 {bookings: [...]}

    Admin->>API: POST /api/admin/bookings/{id}/verify {action: "APPROVE"}
    API->>DB: BEGIN Tx -> GetBookingByIDForUpdate -> ApproveBookingAtomic -> COMMIT
    API-->>Admin: 200 {status: CONFIRMED, whatsapp_queued: true}
    API->>WA: Async Worker: RenderTicketPNG (Code-39 1D) -> POST /media -> POST /messages (image + caption)
    WA-->>Guest: WhatsApp Ticket PNG Pass + Gate QR Link + 5-Char 1D Bar Code

    Note over Gate,DB: 3. Single-Use Gate Check-In (Gate QR Expires Immediately)
    Guest->>Gate: Presents live Gate QR (from GET /api/tickets/{uuid})
    Gate->>API: POST /api/gate/check-in {qr_token}
    API->>API: VerifyToken (HMAC-SHA256 + exp) & ComputeDigest
    API->>DB: GetTicketDetailsByQRDigest + CheckInTicketAtomic (WHERE is_checked_in = FALSE)
    DB-->>API: Updated Ticket (or 0 rows -> 409 Conflict on replay)
    API-->>Gate: 200 {valid: true, reason: "checked_in_gate_qr_expired", entry_count, guests}

    Note over Bar,DB: 4. Multi-Use 1D Barcode Counter POS Redemption (Until Event End)
    Guest->>Bar: Shows 5-char 1D Code-39 barcode (e.g., K4829)
    Bar->>API: POST /api/counter/redeem {bar_code, items} + Idempotency-Key
    API->>DB: GetActiveMenuItemsByIDs (zero-trust server pricing)
    API->>DB: BEGIN Tx (3s ctx)
    API->>DB: GetBarTransactionByIdempotencyKey (replay protection)
    API->>DB: GetTicketForUpdateByBarCode (B-tree index + FOR UPDATE OF t)
    API->>DB: Check CONFIRMED & NOW() <= event.ends_at
    API->>DB: DeductTicketFoodBalanceAtomic (WHERE food_balance >= wallet_deducted)
    API->>DB: CreateBarTransaction (balance_before, balance_after, delta_to_collect) + CreateTransactionLineItem
    API->>DB: COMMIT
    API-->>Bar: 200 {total_bill, wallet_deducted, delta_to_collect, remaining_balance}
```
