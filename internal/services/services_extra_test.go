package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/cache"
	"github.com/venu-live/venu-api/internal/config"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/repositories"
)

type serviceQuerier struct{ db.Querier }

func newTestRepository() *repositories.TicketingRepository {
	return repositories.NewTicketingRepositoryWithQuerier(serviceQuerier{}, nil, nil, time.Second)
}

func testConfig() *config.Config {
	return &config.Config{
		AppBaseURL:         "https://venu.test",
		First99FemaleLimit: 99,
		UPIVPA:             "venu@upi",
		UPIMerchantName:    "House Untold",
		TicketQRTTL:        time.Hour,
	}
}

func assertStatusError(t *testing.T, err error, want int) {
	t.Helper()
	var appErr *httpx.AppError
	if !errors.As(err, &appErr) || appErr.Status != want {
		t.Fatalf("want HTTP %d error, got %v", want, err)
	}
}

func TestPrimaryGuestName(t *testing.T) {
	if got := PrimaryGuestName(nil); got != "Guest" {
		t.Fatalf("empty guests got %q", got)
	}
	guests := []db.TicketGuest{
		{Name: "  First  "},
		{Name: "  Primary  ", IsPrimaryContact: true},
	}
	if got := PrimaryGuestName(guests); got != "Primary" {
		t.Fatalf("primary selection got %q", got)
	}
	guests[1].Name = "   "
	if got := PrimaryGuestName(guests); got != "First" {
		t.Fatalf("fallback got %q", got)
	}
}

func TestPostgresConstraintErrorHelpers(t *testing.T) {
	pgErr := func(constraint string) error {
		return &pgconn.PgError{Code: "23505", ConstraintName: constraint}
	}
	if !isBarCodeUniqueViolation(pgErr("tickets_bar_code_key")) || isBarCodeUniqueViolation(pgErr("bookings_upi_key")) {
		t.Fatal("barcode constraint detection failed")
	}
	if !isUPITxnUniqueViolation(pgErr("bookings_upi_txn_key")) || isUPITxnUniqueViolation(pgErr("tickets_bar_code_key")) {
		t.Fatal("UPI constraint detection failed")
	}
	if !isIdempotencyKeyViolation(pgErr("bar_transactions_idempotency_key")) || isIdempotencyKeyViolation(pgErr("other_key")) {
		t.Fatal("idempotency constraint detection failed")
	}
	if isBarCodeUniqueViolation(errors.New("plain")) || isUPITxnUniqueViolation(errors.New("plain")) || isIdempotencyKeyViolation(errors.New("plain")) {
		t.Fatal("non-PG errors must not match")
	}
}

func TestStaffAndEventValidators(t *testing.T) {
	for _, role := range []db.StaffRole{db.StaffRoleADMIN, db.StaffRoleSCANNER, db.StaffRoleBARTENDER} {
		if !isValidStaffRole(role) {
			t.Fatalf("valid staff role rejected: %s", role)
		}
	}
	if isValidStaffRole(db.StaffRole("NOPE")) {
		t.Fatal("invalid staff role accepted")
	}

	for _, status := range []db.EventStatus{db.EventStatusDRAFT, db.EventStatusONSALE, db.EventStatusSOLDOUT, db.EventStatusENDED} {
		if !isValidEventStatus(status) {
			t.Fatalf("valid event status rejected: %s", status)
		}
	}
	if isValidEventStatus(db.EventStatus("NOPE")) {
		t.Fatal("invalid event status accepted")
	}
}

func TestServiceValidationGuards(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository()
	tokens := auth.NewTokenManager("access", "refresh", "aud", "iss", time.Minute, time.Hour)
	signer := auth.NewQRSigner("secret")
	cacheStore := cache.NewStaffRoleCache(time.Minute)
	logger := discardLogger()

	ticketing := NewTicketingService(testConfig(), repo, tokens, signer, cacheStore, nil, logger)
	admin := NewAdminService(repo, cacheStore, nil, "https://venu.test/", logger)
	barSvc := NewBarService(repo, logger)

	if _, err := ticketing.StaffLogin(ctx, " ", "password"); err == nil {
		t.Fatal("empty username accepted")
	}
	if _, err := ticketing.RefreshSession(ctx, " "); err == nil {
		t.Fatal("empty refresh token accepted")
	}
	if _, err := ticketing.InitGuestCheckout(ctx, InitCheckoutRequest{}, ""); err == nil {
		t.Fatal("missing idempotency key accepted")
	}
	if _, err := ticketing.InitGuestCheckout(ctx, InitCheckoutRequest{}, "test-idem-key"); err == nil {
		t.Fatal("missing IDs accepted")
	}
	if _, err := ticketing.SubmitUPITransactionID(ctx, uuid.New(), "+919999999999", "bad"); err == nil {
		t.Fatal("invalid UPI txn accepted")
	}
	if _, err := admin.CreateStaff(ctx, CreateStaffRequest{Username: "ab", Password: "password123", Role: db.StaffRoleADMIN}); err == nil {
		t.Fatal("short username accepted")
	}
	if _, err := admin.CreateStaff(ctx, CreateStaffRequest{Username: "valid-user", Password: "password123", Role: db.StaffRole("NOPE")}); err == nil {
		t.Fatal("invalid role accepted")
	}
	if _, err := admin.CreateStaff(ctx, CreateStaffRequest{Username: "valid-user", Password: "short", Role: db.StaffRoleADMIN}); err == nil {
		t.Fatal("short password accepted")
	}
	id := uuid.New()
	if _, err := admin.DeactivateStaff(ctx, id, id); err == nil {
		t.Fatal("self-deactivation accepted")
	}
	if _, err := admin.ListBookingsForVerification(ctx, db.BookingStatus("NOPE"), uuid.Nil, 10, 0); err == nil {
		t.Fatal("invalid booking status accepted")
	}
	if _, err := barSvc.CreateMenuItem(ctx, CreateMenuItemRequest{Name: " ", Category: db.MenuCategoryDRINK, UnitPrice: 100}); err == nil {
		t.Fatal("empty menu item name accepted")
	}
	if _, err := barSvc.CreateMenuItem(ctx, CreateMenuItemRequest{Name: "Tea", Category: db.MenuCategory("NOPE"), UnitPrice: 100}); err == nil {
		t.Fatal("invalid menu category accepted")
	}
	if _, err := barSvc.CreateMenuItem(ctx, CreateMenuItemRequest{Name: "Tea", Category: db.MenuCategoryDRINK, UnitPrice: 0}); err == nil {
		t.Fatal("zero menu price accepted")
	}
	if _, err := barSvc.SearchTicketByBarCode(ctx, "bad"); err == nil {
		t.Fatal("invalid barcode accepted")
	}
	if _, err := barSvc.GetTicketBarHistory(ctx, "bad", 10); err == nil {
		t.Fatal("invalid history barcode accepted")
	}
}

func TestBarIdempotencyKeyIsPayloadBound(t *testing.T) {
	itemA, itemB := uuid.New(), uuid.New()
	freeShot := uuid.New()
	quantities := map[uuid.UUID]int32{itemA: 1, itemB: 2}

	base := deriveBarIdempotencyKey("client-key", "K4829", quantities, nil)
	samePayload := deriveBarIdempotencyKey("client-key", "K4829", map[uuid.UUID]int32{itemB: 2, itemA: 1}, nil)
	if base == "" || base != samePayload {
		t.Fatalf("same payload must produce the same key: %q %q", base, samePayload)
	}

	for name, other := range map[string]string{
		"raw key":   deriveBarIdempotencyKey("other-key", "K4829", quantities, nil),
		"barcode":   deriveBarIdempotencyKey("client-key", "K4830", quantities, nil),
		"quantity":  deriveBarIdempotencyKey("client-key", "K4829", map[uuid.UUID]int32{itemA: 1, itemB: 3}, nil),
		"free shot": deriveBarIdempotencyKey("client-key", "K4829", quantities, &freeShot),
	} {
		if other == base {
			t.Fatalf("%s change must alter idempotency key", name)
		}
	}
}

func TestGateServiceValidationGuards(t *testing.T) {
	ctx := context.Background()
	signer := auth.NewQRSigner("secret")
	svc := NewGateService(newTestRepository(), signer, discardLogger())

	if _, err := svc.ValidateQR(ctx, " "); err == nil {
		t.Fatal("empty QR accepted")
	}
	if _, err := svc.ValidateQR(ctx, "forged.token"); err == nil {
		t.Fatal("forged QR accepted")
	}
	if _, err := svc.CheckInTicket(ctx, " ", uuid.Nil); err == nil {
		t.Fatal("empty QR accepted in check-in")
	}
}

func TestBuildGateTicketView(t *testing.T) {
	now := time.Now().UTC()
	row := db.GetTicketDetailsByQRDigestRow{
		ID:            uuid.New(),
		EventID:       uuid.New(),
		BookingStatus: db.BookingStatusCONFIRMED,
		EventEndsAt:   now.Add(time.Hour),
		CategoryName:  "STAG_MALE",
		CategoryTier:  pgtype.Text{String: "PREMIUM", Valid: true},
		CheckedInAt:   pgtype.Timestamptz{Time: now, Valid: true},
	}
	guests := []db.TicketGuest{{ID: uuid.New(), Name: "Aarav", IsPrimaryContact: true}}

	view := buildGateTicketView(row, guests, now)
	if !view.Valid || view.Reason != "ready_for_entry" || view.CategoryTier != "PREMIUM" || view.CheckedInAt == nil {
		t.Fatalf("ready view failed: %+v", view)
	}
	if len(view.Guests) != 1 || view.Guests[0].Name != "Aarav" {
		t.Fatalf("guest mapping failed: %+v", view.Guests)
	}

	row.BookingStatus = db.BookingStatusPENDING
	if v := buildGateTicketView(row, guests, now); v.Valid || v.Reason != "booking_unconfirmed" {
		t.Fatalf("unconfirmed view failed: %+v", v)
	}
	row.BookingStatus = db.BookingStatusCONFIRMED
	row.IsCheckedIn = true
	if v := buildGateTicketView(row, guests, now); v.Valid || v.Reason != "already_checked_in" {
		t.Fatalf("checked-in view failed: %+v", v)
	}
	row.IsCheckedIn = false
	row.EventEndsAt = now.Add(-time.Second)
	if v := buildGateTicketView(row, guests, now); v.Valid || v.Reason != "event_ended" {
		t.Fatalf("event-ended view failed: %+v", v)
	}
}

func TestNewServicesUseDefaults(t *testing.T) {
	repo := newTestRepository()
	if NewGateService(repo, nil, nil).logger == nil || NewBarService(repo, nil).logger == nil || NewAdminService(repo, nil, nil, "", nil).logger == nil {
		t.Fatal("nil logger must be replaced")
	}
	if got := NewAdminService(repo, nil, nil, "https://venu.test///", nil).appBaseURL; got != "https://venu.test" {
		t.Fatalf("base URL not normalized: %q", got)
	}
}

func TestValidationErrorStatuses(t *testing.T) {
	assertStatusError(t, httpx.NewBadRequest("x", nil), http.StatusBadRequest)
	assertStatusError(t, httpx.NewUnauthorized("x", nil), http.StatusUnauthorized)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
