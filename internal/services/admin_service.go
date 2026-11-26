package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/cache"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/realtime"
	"github.com/venu-live/venu-api/internal/repositories"
	"github.com/venu-live/venu-api/internal/whatsapp"
)

type AdminService struct {
	repo       *repositories.TicketingRepository
	roleCache  *cache.StaffRoleCache
	notifier   whatsapp.Notifier
	appBaseURL string
	logger     *slog.Logger
	adminHub   *realtime.AdminHub
}

func (s *AdminService) SetAdminHub(hub *realtime.AdminHub) {
	s.adminHub = hub
}

func NewAdminService(
	repo *repositories.TicketingRepository,
	roleCache *cache.StaffRoleCache,
	notifier whatsapp.Notifier,
	appBaseURL string,
	logger *slog.Logger,
) *AdminService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminService{
		repo:       repo,
		roleCache:  roleCache,
		notifier:   notifier,
		appBaseURL: strings.TrimRight(appBaseURL, "/"),
		logger:     logger,
	}
}

type CreateStaffRequest struct {
	Username string       `json:"username"`
	Password string       `json:"password"`
	Role     db.StaffRole `json:"role"`
}

type StaffAccountView struct {
	ID        uuid.UUID    `json:"id"`
	Username  string       `json:"username"`
	Role      db.StaffRole `json:"role"`
	IsActive  bool         `json:"is_active"`
	CreatedAt time.Time    `json:"created_at,omitempty"`
}

func (s *AdminService) CreateStaff(ctx context.Context, req CreateStaffRequest) (StaffAccountView, error) {
	username := strings.TrimSpace(req.Username)
	if len(username) < 3 || len(username) > 100 {
		return StaffAccountView{}, httpx.NewBadRequest("username must be between 3 and 100 characters", nil)
	}
	if !isValidStaffRole(req.Role) {
		return StaffAccountView{}, httpx.NewBadRequest("role must be ADMIN, SCANNER, or BARTENDER", nil)
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return StaffAccountView{}, httpx.NewBadRequest(err.Error(), err)
	}

	staff, err := s.repo.CreateStaff(ctx, db.CreateStaffParams{
		Username:     username,
		PasswordHash: hash,
		Role:         req.Role,
	})
	if err != nil {
		return StaffAccountView{}, fmt.Errorf("svc_admin_create_staff: %w", err)
	}

	return StaffAccountView{
		ID:        staff.ID,
		Username:  staff.Username,
		Role:      staff.Role,
		IsActive:  staff.IsActive,
		CreatedAt: staff.CreatedAt,
	}, nil
}

func (s *AdminService) UpdateStaffPassword(ctx context.Context, staffID uuid.UUID, newPassword string) (StaffAccountView, error) {
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return StaffAccountView{}, httpx.NewBadRequest(err.Error(), err)
	}

	row, err := s.repo.UpdateStaffPassword(ctx, db.UpdateStaffPasswordParams{
		ID:           staffID,
		PasswordHash: hash,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StaffAccountView{}, httpx.NewNotFound("active staff account not found")
		}
		return StaffAccountView{}, fmt.Errorf("svc_admin_update_staff_password: %w", err)
	}

	// Evict server-side role cache immediately so password rotation takes effect right away.
	if s.roleCache != nil {
		s.roleCache.Invalidate(staffID)
	}

	return StaffAccountView{
		ID:        row.ID,
		Username:  row.Username,
		Role:      row.Role,
		IsActive:  row.IsActive,
		CreatedAt: row.CreatedAt,
	}, nil
}

func (s *AdminService) DeactivateStaff(ctx context.Context, staffID, actorID uuid.UUID) (StaffAccountView, error) {
	if staffID == actorID {
		return StaffAccountView{}, httpx.NewBadRequest("administrators cannot deactivate their own account", nil)
	}

	target, err := s.repo.GetStaffByID(ctx, staffID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StaffAccountView{}, httpx.NewNotFound("active staff account not found")
		}
		return StaffAccountView{}, fmt.Errorf("svc_admin_deactivate_staff_lookup: %w", err)
	}
	if !target.IsActive {
		return StaffAccountView{}, httpx.NewNotFound("active staff account not found")
	}
	if target.Role == db.StaffRoleADMIN {
		count, err := s.repo.CountActiveAdmins(ctx)
		if err != nil {
			return StaffAccountView{}, fmt.Errorf("svc_admin_count_admins: %w", err)
		}
		if count <= 1 {
			return StaffAccountView{}, httpx.NewBadRequest("cannot deactivate the last active administrator", nil)
		}
	}

	row, err := s.repo.DeactivateStaff(ctx, staffID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StaffAccountView{}, httpx.NewNotFound("active staff account not found")
		}
		return StaffAccountView{}, fmt.Errorf("svc_admin_deactivate_staff: %w", err)
	}

	// Instant revocation in server-side cache — zero stale-access window.
	if s.roleCache != nil {
		s.roleCache.Invalidate(staffID)
	}

	return StaffAccountView{
		ID:       row.ID,
		Username: row.Username,
		Role:     row.Role,
		IsActive: row.IsActive,
	}, nil
}

func (s *AdminService) ListStaff(ctx context.Context) ([]StaffAccountView, error) {
	rows, err := s.repo.ListStaff(ctx)
	if err != nil {
		return nil, fmt.Errorf("svc_admin_list_staff: %w", err)
	}
	out := make([]StaffAccountView, len(rows))
	for i, r := range rows {
		out[i] = StaffAccountView{
			ID:        r.ID,
			Username:  r.Username,
			Role:      r.Role,
			IsActive:  r.IsActive,
			CreatedAt: r.CreatedAt,
		}
	}
	return out, nil
}

// --- Manual UPI Payment Verification Queue & Approval (Triggers WhatsApp Ticket Image + Message) ---

type BookingVerificationItem struct {
	BookingID        uuid.UUID        `json:"booking_id"`
	TicketID         uuid.UUID        `json:"ticket_id"`
	EventID          uuid.UUID        `json:"event_id"`
	EventTitle       string           `json:"event_title"`
	CategoryName     string           `json:"category_name"`
	CategoryTier     string           `json:"category_tier,omitempty"`
	EntryCount       int32            `json:"entry_count"`
	GuestCount       int32            `json:"guest_count"`
	ExpectedAmount   int32            `json:"expected_amount"`
	TierBasePrice    int32            `json:"tier_base_price"`
	FnbCredit        int32            `json:"fnb_credit"`
	UPITransactionID string           `json:"upi_transaction_id,omitempty"`
	Status           db.BookingStatus `json:"status"`
	ContactPhone     string           `json:"contact_phone"`
	ContactEmail     string           `json:"contact_email"`
	BarCode          string           `json:"bar_code"`
	PaymentQRData    string           `json:"payment_qr_data,omitempty"`
	SeatSection      string           `json:"seat_section,omitempty"`
	SeatTableNumber  int32            `json:"seat_table_number,omitempty"`
	RejectionReason  string           `json:"rejection_reason,omitempty"`
	VerifiedAt       *time.Time       `json:"verified_at,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	Guests           []GuestView      `json:"guests,omitempty"`
}

func (s *AdminService) ListBookingsForVerification(
	ctx context.Context,
	status db.BookingStatus,
	eventID uuid.UUID,
	limit, offset int32,
) ([]BookingVerificationItem, error) {
	if status == "" {
		status = db.BookingStatusPENDING
	}
	switch status {
	case db.BookingStatusPENDING, db.BookingStatusCONFIRMED, db.BookingStatusFAILED:
	default:
		return nil, httpx.NewBadRequest("status must be PENDING, CONFIRMED, or FAILED", nil)
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := s.repo.ListBookingsForVerification(ctx, db.ListBookingsForVerificationParams{
		Status:     status,
		EventID:    eventID,
		PageLimit:  limit,
		PageOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("svc_admin_list_bookings: %w", err)
	}

	items := make([]BookingVerificationItem, 0, len(rows))
	for _, r := range rows {
		item := BookingVerificationItem{
			BookingID:      r.ID,
			TicketID:       r.TicketID,
			EventID:        r.EventID,
			EventTitle:     r.EventTitle,
			CategoryName:   r.CategoryName,
			EntryCount:     r.EntryCount,
			GuestCount:     r.EntryCount,
			ExpectedAmount: r.TotalAmount,
			TierBasePrice:  r.TierBasePrice,
			FnbCredit:      r.TierFnbCredit,
			Status:         r.Status,
			ContactPhone:   r.ContactPhone,
			ContactEmail:   r.ContactEmail,
			BarCode:        r.BarCode,
			CreatedAt:      r.CreatedAt,
		}
		if r.CategoryTier.Valid {
			item.CategoryTier = r.CategoryTier.String
		}
		if r.UpiTransactionID.Valid {
			item.UPITransactionID = r.UpiTransactionID.String
		}
		if r.RejectionReason.Valid {
			item.RejectionReason = r.RejectionReason.String
		}
		if r.VerifiedAt.Valid {
			t := r.VerifiedAt.Time
			item.VerifiedAt = &t
		}

		if guests, err := s.repo.ListGuestsByTicketID(ctx, r.TicketID); err == nil && len(guests) > 0 {
			item.GuestCount = int32(len(guests))
			gviews := make([]GuestView, 0, len(guests))
			for _, g := range guests {
				gviews = append(gviews, GuestView{
					ID:               g.ID,
					Name:             g.Name,
					Age:              g.Age.Int32,
					Gender:           g.Gender,
					IsPrimaryContact: g.IsPrimaryContact,
					FreeShotRedeemed: g.FreeShotRedeemed,
				})
			}
			item.Guests = gviews
		}

		if seat, err := s.repo.GetSeatReservationByBookingID(ctx, r.ID); err == nil {
			item.SeatSection = seat.Section
			item.SeatTableNumber = seat.TableNumber
		}
		items = append(items, item)
	}
	return items, nil
}

type VerifyBookingRequest struct {
	Action          string `json:"action"` // APPROVE or REJECT
	RejectionReason string `json:"rejection_reason,omitempty"`
}

type VerifyBookingResponse struct {
	BookingID        uuid.UUID        `json:"booking_id"`
	TicketID         uuid.UUID        `json:"ticket_id"`
	Status           db.BookingStatus `json:"status"`
	ExpectedAmount   int32            `json:"expected_amount"`
	UPITransactionID string           `json:"upi_transaction_id,omitempty"`
	WhatsAppQueued   bool             `json:"whatsapp_queued"`
	VerifiedAt       time.Time        `json:"verified_at"`
}

// VerifyBookingPayment atomically approves or rejects a pending UPI booking under row-level lock.
// On approval, dispatches the rendered PNG ticket image + confirmation message via WhatsApp Cloud API.
func (s *AdminService) VerifyBookingPayment(
	ctx context.Context,
	bookingID, adminID uuid.UUID,
	req VerifyBookingRequest,
) (VerifyBookingResponse, error) {
	action := strings.ToUpper(strings.TrimSpace(req.Action))
	if action != "APPROVE" && action != "REJECT" {
		return VerifyBookingResponse{}, httpx.NewBadRequest("action must be APPROVE or REJECT", nil)
	}

	var resp VerifyBookingResponse
	var waMsg *whatsapp.TicketPassMessage

	err := s.repo.WithTx(ctx, func(txCtx context.Context, q db.Querier) error {
		booking, err := q.GetBookingByIDForUpdate(txCtx, bookingID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewNotFound("booking not found")
			}
			return fmt.Errorf("tx_admin_lock_booking: %w", err)
		}

		if booking.Status != db.BookingStatusPENDING {
			return httpx.NewConflict(fmt.Sprintf("booking is already %s", booking.Status), nil)
		}

		adminUUID := pgtype.UUID{Bytes: adminID, Valid: true}

		if action == "REJECT" {
			reason := strings.TrimSpace(req.RejectionReason)
			if reason == "" {
				reason = "UPI payment could not be verified"
			}
			if len(reason) > 255 {
				return httpx.NewBadRequest("rejection_reason must be at most 255 characters", nil)
			}
			rejected, err := q.RejectBookingAtomic(txCtx, db.RejectBookingAtomicParams{
				ID:              booking.ID,
				VerifiedBy:      adminUUID,
				RejectionReason: pgtype.Text{String: reason, Valid: true},
			})
			if err != nil {
				return fmt.Errorf("tx_admin_reject_booking: %w", err)
			}
			ticket, err := q.GetTicketByBookingID(txCtx, booking.ID)
			if err != nil {
				return fmt.Errorf("tx_admin_reject_get_ticket: %w", err)
			}
			if err := q.ReleaseSeatReservationByBookingID(txCtx, booking.ID); err != nil {
				return fmt.Errorf("tx_admin_reject_release_seat: %w", err)
			}
			resp = VerifyBookingResponse{
				BookingID:        rejected.ID,
				TicketID:         ticket.ID,
				Status:           rejected.Status,
				ExpectedAmount:   rejected.TotalAmount,
				UPITransactionID: rejected.UpiTransactionID.String,
				WhatsAppQueued:   false,
				VerifiedAt:       rejected.VerifiedAt.Time,
			}
			return nil
		}

		// APPROVE flow: require submitted UPI transaction ID for paid bookings.
		if booking.TotalAmount > 0 && (!booking.UpiTransactionID.Valid || strings.TrimSpace(booking.UpiTransactionID.String) == "") {
			return httpx.NewBadRequest("cannot approve paid booking before patron submits a UPI transaction ID", nil)
		}

		approved, err := q.ApproveBookingAtomic(txCtx, db.ApproveBookingAtomicParams{
			ID:         booking.ID,
			VerifiedBy: adminUUID,
		})
		if err != nil {
			return fmt.Errorf("tx_admin_approve_booking: %w", err)
		}

		ticket, err := q.GetTicketByBookingID(txCtx, approved.ID)
		if err != nil {
			return fmt.Errorf("tx_admin_approve_get_ticket: %w", err)
		}
		details, err := q.GetTicketDetailsByID(txCtx, ticket.ID)
		if err != nil {
			return fmt.Errorf("tx_admin_approve_ticket_details: %w", err)
		}
		guests, err := q.ListGuestsByTicketID(txCtx, ticket.ID)
		if err != nil {
			return fmt.Errorf("tx_admin_approve_guests: %w", err)
		}

		primaryName := PrimaryGuestName(db.TicketGuestsFromListRows(guests))
		ticketURL := fmt.Sprintf("%s/orders/track?ticket_id=%s", s.appBaseURL, ticket.ID.String())

		waMsg = &whatsapp.TicketPassMessage{
			TicketID:       ticket.ID.String(),
			RecipientPhone: approved.ContactPhone,
			PrimaryGuest:   primaryName,
			EventTitle:     details.EventTitle,
			EventLocation:  details.EventLocation,
			EventStartsAt:  details.EventStartsAt,
			CategoryName:   details.CategoryName,
			TicketURL:      ticketURL,
			BarCode:        ticket.BarCode,
			EntryCount:     details.EntryCount,
			FoodBalance:    ticket.FoodBalance,
		}

		resp = VerifyBookingResponse{
			BookingID:        approved.ID,
			TicketID:         ticket.ID,
			Status:           approved.Status,
			ExpectedAmount:   approved.TotalAmount,
			UPITransactionID: approved.UpiTransactionID.String,
			WhatsAppQueued:   s.notifier != nil,
			VerifiedAt:       approved.VerifiedAt.Time,
		}
		return nil
	})
	if err != nil {
		return VerifyBookingResponse{}, err
	}

	if s.adminHub != nil {
		s.adminHub.Broadcast(map[string]any{
			"type":       "BOOKING_VERIFIED",
			"booking_id": resp.BookingID,
			"ticket_id":  resp.TicketID,
			"status":     string(resp.Status),
		})
	}

	if waMsg != nil && s.notifier != nil {
		s.notifier.EnqueueTicketConfirmation(*waMsg)
	}
	return resp, nil
}

// --- Event & Tier Management ---

type CreateEventRequest struct {
	Title          string         `json:"title"`
	StartsAt       time.Time      `json:"starts_at"`
	EndsAt         time.Time      `json:"ends_at"`
	Location       string         `json:"location"`
	VenueName      string         `json:"venue_name,omitempty"`
	LocationArea   string         `json:"location_area,omitempty"`
	TransitInfo    string         `json:"transit_info,omitempty"`
	AdvisoryNotice string         `json:"advisory_notice,omitempty"`
	Description    string         `json:"description,omitempty"`
	Status         db.EventStatus `json:"status"`
	Artists        *[]ArtistInput `json:"artists,omitempty"`
}

func optionalAdminText(value *string, max int, field string) (pgtype.Text, error) {
	if value == nil {
		return pgtype.Text{}, nil
	}
	v := strings.TrimSpace(*value)
	if len(v) > max {
		return pgtype.Text{}, httpx.NewBadRequest(field+" is too long", nil)
	}
	return pgtype.Text{String: v, Valid: true}, nil
}

func (s *AdminService) CreateEvent(ctx context.Context, req CreateEventRequest) (db.Event, error) {
	title := strings.TrimSpace(req.Title)
	location := strings.TrimSpace(req.Location)
	if title == "" || len(title) > 255 || location == "" || len(location) > 255 {
		return db.Event{}, httpx.NewBadRequest("title and location are required (max 255 chars)", nil)
	}
	if req.StartsAt.IsZero() || req.EndsAt.IsZero() || !req.EndsAt.After(req.StartsAt) {
		return db.Event{}, httpx.NewBadRequest("ends_at must be strictly after starts_at", nil)
	}

	status := req.Status
	if status == "" {
		status = db.EventStatusDRAFT
	}
	if !isValidEventStatus(status) {
		return db.Event{}, httpx.NewBadRequest("invalid event status", nil)
	}

	venueName := strings.TrimSpace(req.VenueName)
	if venueName == "" {
		venueName = "Hotel Sky Blue"
	}
	locationArea := strings.TrimSpace(req.LocationArea)
	if locationArea == "" {
		locationArea = "Ravet- Pune"
	}
	transitInfo := strings.TrimSpace(req.TransitInfo)
	if transitInfo == "" {
		transitInfo = "Near Ravet BRT Road"
	}
	advisoryNotice := strings.TrimSpace(req.AdvisoryNotice)
	if advisoryNotice == "" {
		advisoryNotice = "Valid government photo ID required."
	}
	if len(venueName) > 255 || len(locationArea) > 255 || len(transitInfo) > 255 || len(advisoryNotice) > 2000 || len(req.Description) > 10000 {
		return db.Event{}, httpx.NewBadRequest("event detail fields exceed their maximum length", nil)
	}

	var ev db.Event
	err := s.repo.WithTx(ctx, func(txCtx context.Context, q db.Querier) error {
		created, err := q.CreateEvent(txCtx, db.CreateEventParams{
			Title:          title,
			StartsAt:       req.StartsAt.UTC(),
			EndsAt:         req.EndsAt.UTC(),
			Location:       location,
			VenueName:      venueName,
			LocationArea:   locationArea,
			TransitInfo:    transitInfo,
			AdvisoryNotice: advisoryNotice,
			Description:    req.Description,
			Status:         status,
		})
		if err != nil {
			return fmt.Errorf("tx_admin_create_event: %w", err)
		}
		ev = created
		if req.Artists == nil {
			return nil
		}
		for _, input := range *req.Artists {
			name, category, description, err := normalizeArtistFields(input.Name, input.Category, input.Description)
			if err != nil {
				return err
			}
			if _, err := q.CreateArtist(txCtx, db.CreateArtistParams{
				EventID:     ev.ID,
				Name:        name,
				Category:    category,
				Description: description,
			}); err != nil {
				return fmt.Errorf("tx_admin_create_artist: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return db.Event{}, err
	}
	return ev, nil
}

type CreateTicketTierRequest struct {
	Name           string `json:"name"`
	Category       string `json:"category,omitempty"`
	BasePrice      int32  `json:"base_price"`
	FnbCredit      int32  `json:"fnb_credit"`
	EntryCount     int32  `json:"entry_count"`
	IsGenderStrict bool   `json:"is_gender_strict"`
}

func (s *AdminService) CreateTicketTier(ctx context.Context, eventID uuid.UUID, req CreateTicketTierRequest) (db.TicketCategory, error) {
	if _, err := s.repo.GetEventByID(ctx, eventID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.TicketCategory{}, httpx.NewNotFound("event not found")
		}
		return db.TicketCategory{}, fmt.Errorf("svc_admin_tier_get_event: %w", err)
	}

	name := strings.ToUpper(strings.TrimSpace(req.Name))
	if name == "" || len(name) > 100 {
		return db.TicketCategory{}, httpx.NewBadRequest("tier name is required (max 100 chars)", nil)
	}
	if req.BasePrice < 0 || req.FnbCredit < 0 {
		return db.TicketCategory{}, httpx.NewBadRequest("base_price and fnb_credit must be non-negative", nil)
	}
	switch req.EntryCount {
	case 1, 2, 5, 8, 10, 15:
	default:
		return db.TicketCategory{}, httpx.NewBadRequest("entry_count must be 1, 2, 5, 8, 10, or 15", nil)
	}

	genderStrict := req.IsGenderStrict
	if name == "COUPLE" {
		if req.EntryCount != 2 {
			return db.TicketCategory{}, httpx.NewBadRequest("COUPLE tier must have entry_count = 2", nil)
		}
		genderStrict = true
	} else if name == "STAG_FEMALE" || name == "STAG_MALE" || name == "STAG" {
		if req.EntryCount != 1 {
			return db.TicketCategory{}, httpx.NewBadRequest("stag tier must have entry_count = 1", nil)
		}
	}

	catText := pgtype.Text{Valid: false}
	if trimmed := strings.ToUpper(strings.TrimSpace(req.Category)); trimmed != "" {
		if len(trimmed) > 50 {
			return db.TicketCategory{}, httpx.NewBadRequest("category must be at most 50 characters", nil)
		}
		catText = pgtype.Text{String: trimmed, Valid: true}
	}
	if strings.HasPrefix(name, "GROUP") && !catText.Valid {
		return db.TicketCategory{}, httpx.NewBadRequest("GROUP tiers require a category (A, B, or C)", nil)
	}

	tier, err := s.repo.CreateTicketCategory(ctx, db.CreateTicketCategoryParams{
		EventID:        eventID,
		Name:           name,
		Category:       catText,
		BasePrice:      req.BasePrice,
		FnbCredit:      req.FnbCredit,
		EntryCount:     req.EntryCount,
		IsGenderStrict: genderStrict,
	})
	if err != nil {
		return db.TicketCategory{}, fmt.Errorf("svc_admin_create_tier: %w", err)
	}
	return tier, nil
}

type AdminEventView struct {
	ID                uuid.UUID      `json:"id"`
	Title             string         `json:"title"`
	StartsAt          time.Time      `json:"starts_at"`
	EndsAt            time.Time      `json:"ends_at"`
	Location          string         `json:"location"`
	Status            db.EventStatus `json:"status"`
	CreatedAt         time.Time      `json:"created_at"`
	ConfirmedBookings int64          `json:"confirmed_bookings"`
	TicketRevenue     int64          `json:"ticket_revenue"`
	CheckedInTickets  int64          `json:"checked_in_tickets"`
	IsFirst99Active   bool           `json:"is_first_99_active"`
}

func (s *AdminService) ListAdminEvents(ctx context.Context) ([]AdminEventView, error) {
	rows, err := s.repo.ListAdminEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("svc_admin_list_events: %w", err)
	}
	events := make([]AdminEventView, 0, len(rows))
	for _, row := range rows {
		var first99Active bool
		if err := s.repo.Read(ctx, func(readCtx context.Context, q db.Querier) error {
			var readErr error
			first99Active, readErr = q.GetEventFirst99Active(readCtx, row.ID)
			return readErr
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("svc_admin_list_events_pricing_flag: %w", err)
		}
		events = append(events, AdminEventView{
			ID:                row.ID,
			Title:             row.Title,
			StartsAt:          row.StartsAt,
			EndsAt:            row.EndsAt,
			Location:          row.Location,
			Status:            row.Status,
			CreatedAt:         row.CreatedAt,
			ConfirmedBookings: row.ConfirmedBookings,
			TicketRevenue:     row.TicketRevenue,
			CheckedInTickets:  row.CheckedInTickets,
			IsFirst99Active:   first99Active,
		})
	}
	return events, nil
}

func (s *AdminService) SetEventFirst99Active(ctx context.Context, id uuid.UUID, active bool) (db.SetEventFirst99ActiveRow, error) {
	var row db.SetEventFirst99ActiveRow
	err := s.repo.WithTx(ctx, func(txCtx context.Context, q db.Querier) error {
		var txErr error
		row, txErr = q.SetEventFirst99Active(txCtx, db.SetEventFirst99ActiveParams{
			ID:              id,
			IsFirst99Active: active,
		})
		return txErr
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.SetEventFirst99ActiveRow{}, httpx.NewNotFound("event not found")
		}
		return db.SetEventFirst99ActiveRow{}, fmt.Errorf("svc_admin_set_first_99_active: %w", err)
	}
	return row, nil
}

func (s *AdminService) SoftDeleteEvent(ctx context.Context, id uuid.UUID) error {
	if _, err := s.repo.SoftDeleteEvent(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewNotFound("event not found")
		}
		return fmt.Errorf("svc_admin_soft_delete_event: %w", err)
	}
	return nil
}

type AuditLogView struct {
	ID         int64      `json:"id"`
	RequestID  string     `json:"request_id"`
	Method     string     `json:"method"`
	Path       string     `json:"path"`
	Route      string     `json:"route"`
	StatusCode int32      `json:"status_code"`
	DurationMS int32      `json:"duration_ms"`
	ActorID    *uuid.UUID `json:"actor_id,omitempty"`
	ActorRole  string     `json:"actor_role,omitempty"`
	IP         string     `json:"ip"`
	UserAgent  string     `json:"user_agent"`
	CreatedAt  time.Time  `json:"created_at"`
}

type AuditLogFilter struct {
	ActorID uuid.UUID
	Path    string
	Limit   int32
	Offset  int32
}

func (s *AdminService) ListAPIAuditLogs(ctx context.Context, filter AuditLogFilter) ([]AuditLogView, error) {
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 50
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	params := db.ListAPIAuditLogsParams{
		ActorID:    pgtype.UUID{Bytes: filter.ActorID, Valid: filter.ActorID != uuid.Nil},
		Path:       pgtype.Text{Valid: false},
		PageLimit:  filter.Limit,
		PageOffset: filter.Offset,
	}
	if trimmed := strings.TrimSpace(filter.Path); trimmed != "" {
		if len(trimmed) > 200 {
			return nil, httpx.NewBadRequest("path filter must be at most 200 characters", nil)
		}
		params.Path = pgtype.Text{String: trimmed, Valid: true}
	}
	rows, err := s.repo.ListAPIAuditLogs(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("svc_admin_list_audit_logs: %w", err)
	}
	logs := make([]AuditLogView, 0, len(rows))
	for _, row := range rows {
		view := AuditLogView{
			ID:         row.ID,
			RequestID:  row.RequestID,
			Method:     row.Method,
			Path:       row.Path,
			Route:      row.Route,
			StatusCode: row.StatusCode,
			DurationMS: row.DurationMs,
			ActorRole:  row.ActorRole.String,
			IP:         row.Ip,
			UserAgent:  row.UserAgent,
			CreatedAt:  row.CreatedAt,
		}
		if row.ActorID.Valid {
			actorID := uuid.UUID(row.ActorID.Bytes)
			view.ActorID = &actorID
		}
		logs = append(logs, view)
	}
	return logs, nil
}

type TableBookingBreakdownView struct {
	Section        string `json:"section"`
	TableSize      int32  `json:"table_size"`
	TotalTables    int32  `json:"total_tables"`
	ReservedTables int32  `json:"reserved_tables"`
}

type EventStatsView struct {
	EventID               uuid.UUID                   `json:"event_id"`
	EventTitle            string                      `json:"event_title"`
	ConfirmedBookings     int64                       `json:"confirmed_bookings"`
	TicketRevenue         int64                       `json:"ticket_revenue"`
	TotalTickets          int64                       `json:"total_tickets"`
	CheckedInTickets      int64                       `json:"checked_in_tickets"`
	TotalFnbAllocated     int64                       `json:"total_fnb_allocated"`
	TotalFnbRedeemed      int64                       `json:"total_fnb_redeemed"`
	TotalFnbRemaining     int64                       `json:"total_fnb_remaining"`
	BarTransactions       int64                       `json:"bar_transactions"`
	TotalBarBill          int64                       `json:"total_bar_bill"`
	TotalWalletDeducted   int64                       `json:"total_wallet_deducted"`
	TotalCounterDelta     int64                       `json:"total_counter_delta"`
	TotalGuests           int64                       `json:"total_guests"`
	MaleGuests            int64                       `json:"male_guests"`
	FemaleGuests          int64                       `json:"female_guests"`
	OtherGuests           int64                       `json:"other_guests"`
	FreeShotsRedeemed     int64                       `json:"free_shots_redeemed"`
	ConfirmedFemaleStags  int64                       `json:"confirmed_female_stags"`
	TotalCouples          int64                       `json:"total_couples"`
	TotalStagMale         int64                       `json:"total_stag_male"`
	TotalStagFemale       int64                       `json:"total_stag_female"`
	TotalTequilaRedeemed  int64                       `json:"total_tequila_redeemed"`
	TotalFnbCreditUsed    int64                       `json:"total_fnb_credit_used"`
	ExtraCashCollected    int64                       `json:"extra_cash_collected"`
	ExtraUpiCollected     int64                       `json:"extra_upi_collected"`
	TableBookingBreakdown []TableBookingBreakdownView `json:"table_booking_breakdown"`
	TopBarItems           []TopBarItemView            `json:"top_bar_items"`
	BarSalesByBartender   []BartenderSaleView         `json:"bar_sales_by_bartender"`
	RecentGateEntries     []RecentGateEntryView       `json:"recent_gate_entries"`
}

type TopBarItemView struct {
	ItemName        string `json:"item_name"`
	ItemCategory    string `json:"item_category"`
	Quantity        int64  `json:"quantity"`
	UnitPriceAtSale int32  `json:"unit_price_at_sale"`
	Revenue         int64  `json:"revenue"`
}

type BartenderSaleView struct {
	BartenderID       uuid.UUID `json:"bartender_id"`
	BartenderUsername string    `json:"bartender_username"`
	Transactions      int64     `json:"transactions"`
	TotalBill         int64     `json:"total_bill"`
	WalletDeducted    int64     `json:"wallet_deducted"`
	DeltaCollected    int64     `json:"delta_collected"`
}

type RecentGateEntryView struct {
	TicketID           uuid.UUID  `json:"ticket_id"`
	BarCode            string     `json:"bar_code"`
	CheckedInAt        *time.Time `json:"checked_in_at,omitempty"`
	PrimaryGuest       string     `json:"primary_guest,omitempty"`
	GatekeeperUsername string     `json:"gatekeeper_username,omitempty"`
	CategoryName       string     `json:"category_name"`
}

func (s *AdminService) GetEventStats(ctx context.Context, eventID uuid.UUID) (EventStatsView, error) {
	return s.getEventStats(ctx, eventID, nil)
}

func (s *AdminService) getEventStats(ctx context.Context, eventID uuid.UUID, tableRowsOverride []db.ListSeatInventoryByEventIDsRow) (EventStatsView, error) {
	ev, err := s.repo.GetEventByID(ctx, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EventStatsView{}, httpx.NewNotFound("event not found")
		}
		return EventStatsView{}, fmt.Errorf("svc_admin_stats_event: %w", err)
	}

	var (
		bookingStats   db.GetEventBookingStatsRow
		barStats       db.GetEventBarRevenueStatsRow
		genderStats    db.GetEventGenderStatsRow
		groupStats     db.GetV2GroupStatsByEventRow
		financialStats db.GetV2FinancialStatsByEventRow
		femaleStags    int64
		tableRows      []db.ListSeatInventoryByEventIDsRow
		topBarRows     []db.GetTopBarItemsByEventRow
		bartenderRows  []db.GetBarSalesByBartenderRow
		recentGateRows []db.GetRecentGateEntriesRow
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		bookingStats, err = s.repo.GetEventBookingStats(gctx, eventID)
		return err
	})
	g.Go(func() error {
		var err error
		barStats, err = s.repo.GetEventBarRevenueStats(gctx, eventID)
		return err
	})
	g.Go(func() error {
		var err error
		genderStats, err = s.repo.GetEventGenderStats(gctx, eventID)
		return err
	})
	g.Go(func() error {
		var err error
		femaleStags, err = s.repo.CountConfirmedFemaleStagByEvent(gctx, eventID)
		return err
	})
	g.Go(func() error {
		return s.repo.Read(gctx, func(readCtx context.Context, q db.Querier) error {
			var readErr error
			groupStats, readErr = q.GetV2GroupStatsByEvent(readCtx, eventID)
			return readErr
		})
	})
	g.Go(func() error {
		return s.repo.Read(gctx, func(readCtx context.Context, q db.Querier) error {
			var readErr error
			financialStats, readErr = q.GetV2FinancialStatsByEvent(readCtx, eventID)
			return readErr
		})
	})
	if tableRowsOverride != nil {
		tableRows = tableRowsOverride
	} else {
		g.Go(func() error {
			return s.repo.Read(gctx, func(readCtx context.Context, q db.Querier) error {
				var readErr error
				tableRows, readErr = q.ListSeatInventoryByEventIDs(readCtx, []uuid.UUID{eventID})
				return readErr
			})
		})
	}
	g.Go(func() error {
		var err error
		topBarRows, err = s.repo.GetTopBarItemsByEvent(gctx, db.GetTopBarItemsByEventParams{
			EventID: eventID,
			Limit:   10,
		})
		return err
	})
	g.Go(func() error {
		var err error
		bartenderRows, err = s.repo.GetBarSalesByBartender(gctx, eventID)
		return err
	})
	g.Go(func() error {
		var err error
		recentGateRows, err = s.repo.GetRecentGateEntries(gctx, db.GetRecentGateEntriesParams{
			EventID: eventID,
			Limit:   20,
		})
		return err
	})

	if err := g.Wait(); err != nil {
		return EventStatsView{}, fmt.Errorf("svc_admin_stats_aggregate: %w", err)
	}

	topBarItems := make([]TopBarItemView, 0, len(topBarRows))
	for _, row := range topBarRows {
		topBarItems = append(topBarItems, TopBarItemView{
			ItemName:        row.ItemName,
			ItemCategory:    row.ItemCategory,
			Quantity:        row.Quantity,
			UnitPriceAtSale: row.UnitPriceAtSale,
			Revenue:         row.Revenue,
		})
	}
	bartenderSales := make([]BartenderSaleView, 0, len(bartenderRows))
	for _, row := range bartenderRows {
		bartenderSales = append(bartenderSales, BartenderSaleView{
			BartenderID:       row.BartenderID,
			BartenderUsername: row.BartenderUsername,
			Transactions:      row.Transactions,
			TotalBill:         row.TotalBill,
			WalletDeducted:    row.WalletDeducted,
			DeltaCollected:    row.DeltaCollected,
		})
	}
	recentGateEntries := make([]RecentGateEntryView, 0, len(recentGateRows))
	for _, row := range recentGateRows {
		entry := RecentGateEntryView{
			TicketID:           row.TicketID,
			BarCode:            row.BarCode,
			PrimaryGuest:       row.PrimaryGuest.String,
			GatekeeperUsername: row.GatekeeperUsername.String,
			CategoryName:       row.CategoryName,
		}
		if row.CheckedInAt.Valid {
			t := row.CheckedInAt.Time
			entry.CheckedInAt = &t
		}
		recentGateEntries = append(recentGateEntries, entry)
	}
	tableBreakdown := make([]TableBookingBreakdownView, 0, len(tableRows))
	for _, row := range tableRows {

		tableBreakdown = append(tableBreakdown, TableBookingBreakdownView{
			Section:        row.Section,
			TableSize:      row.TableSize,
			TotalTables:    row.TotalTables,
			ReservedTables: row.ReservedTables,
		})
	}

	return EventStatsView{
		EventID:               ev.ID,
		EventTitle:            ev.Title,
		ConfirmedBookings:     bookingStats.ConfirmedBookings,
		TicketRevenue:         bookingStats.TicketRevenue,
		TotalTickets:          bookingStats.TotalTickets,
		CheckedInTickets:      bookingStats.CheckedInTickets,
		TotalFnbAllocated:     bookingStats.TotalFnbAllocated,
		TotalFnbRedeemed:      bookingStats.TotalFnbRedeemed,
		TotalFnbRemaining:     bookingStats.TotalFnbRemaining,
		BarTransactions:       barStats.TotalTransactions,
		TotalBarBill:          barStats.TotalBarBill,
		TotalWalletDeducted:   barStats.TotalWalletDeducted,
		TotalCounterDelta:     barStats.TotalUpiDelta,
		TotalGuests:           genderStats.TotalGuests,
		MaleGuests:            genderStats.MaleGuests,
		FemaleGuests:          genderStats.FemaleGuests,
		OtherGuests:           genderStats.OtherGuests,
		FreeShotsRedeemed:     genderStats.FreeShotsRedeemed,
		ConfirmedFemaleStags:  femaleStags,
		TotalCouples:          groupStats.TotalCouples,
		TotalStagMale:         groupStats.TotalStagMale,
		TotalStagFemale:       groupStats.TotalStagFemale,
		TotalTequilaRedeemed:  groupStats.TotalTequilaRedeemed,
		TotalFnbCreditUsed:    financialStats.FnbCreditUsed,
		ExtraCashCollected:    financialStats.ExtraCashCollected,
		ExtraUpiCollected:     financialStats.ExtraUpiCollected,
		TableBookingBreakdown: tableBreakdown,
		TopBarItems:           topBarItems,
		BarSalesByBartender:   bartenderSales,
		RecentGateEntries:     recentGateEntries,
	}, nil
}

type AdminStatsView struct {
	TotalRevenue          int64                       `json:"total_revenue"`
	ConfirmedBookings     int64                       `json:"confirmed_bookings"`
	TotalTickets          int64                       `json:"total_tickets"`
	CheckedInTickets      int64                       `json:"checked_in_tickets"`
	FnbRedeemed           int64                       `json:"fnb_redeemed"`
	FnbRemaining          int64                       `json:"fnb_remaining"`
	BarTransactions       int64                       `json:"bar_transactions"`
	TotalGuests           int64                       `json:"total_guests"`
	TotalCouples          int64                       `json:"total_couples"`
	TotalStagMale         int64                       `json:"total_stag_male"`
	TotalStagFemale       int64                       `json:"total_stag_female"`
	TotalTequilaRedeemed  int64                       `json:"total_tequila_redeemed"`
	TotalFnbCreditUsed    int64                       `json:"total_fnb_credit_used"`
	ExtraCashCollected    int64                       `json:"extra_cash_collected"`
	ExtraUpiCollected     int64                       `json:"extra_upi_collected"`
	ActiveEvents          int64                       `json:"active_events"`
	TableBookingBreakdown []TableBookingBreakdownView `json:"table_booking_breakdown"`
	PerEventPerformance   []EventStatsView            `json:"per_event_performance"`
	TopBarItems           []TopBarItemView            `json:"top_bar_items"`
	RecentGateEntries     []RecentGateEntryView       `json:"recent_gate_entries"`
}

func (s *AdminService) GetAdminStats(ctx context.Context) (AdminStatsView, error) {
	events, err := s.repo.ListAdminEvents(ctx)
	if err != nil {
		return AdminStatsView{}, fmt.Errorf("svc_admin_stats_events: %w", err)
	}

	eventIDs := make([]uuid.UUID, 0, len(events))
	for _, ev := range events {
		eventIDs = append(eventIDs, ev.ID)
	}

	perEvent := make([]EventStatsView, len(events))
	tableRowsByEvent := make(map[uuid.UUID][]db.ListSeatInventoryByEventIDsRow)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	if len(eventIDs) > 0 {
		g.Go(func() error {
			return s.repo.Read(gctx, func(readCtx context.Context, q db.Querier) error {
				rows, err := q.ListSeatInventoryByEventIDs(readCtx, eventIDs)
				if err != nil {
					return err
				}
				for _, row := range rows {
					tableRowsByEvent[row.EventID] = append(tableRowsByEvent[row.EventID], row)
				}
				return nil
			})
		})
	}
	for i, ev := range events {
		i, ev := i, ev
		g.Go(func() error {
			stats, err := s.getEventStats(gctx, ev.ID, tableRowsByEvent[ev.ID])
			if err != nil {
				return err
			}
			perEvent[i] = stats
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return AdminStatsView{}, fmt.Errorf("svc_admin_stats_aggregate: %w", err)
	}

	type barItemKey struct {
		name     string
		category string
		price    int32
	}
	items := make(map[barItemKey]*TopBarItemView)
	type tableKey struct {
		section string
		size    int32
	}
	tableMap := make(map[tableKey]TableBookingBreakdownView)
	out := AdminStatsView{
		ActiveEvents:          int64(len(events)),
		TableBookingBreakdown: []TableBookingBreakdownView{},
		PerEventPerformance:   perEvent,
		TopBarItems:           []TopBarItemView{},
		RecentGateEntries:     []RecentGateEntryView{},
	}
	for _, stats := range perEvent {
		out.TotalRevenue += stats.TicketRevenue
		out.ConfirmedBookings += stats.ConfirmedBookings
		out.TotalTickets += stats.TotalTickets
		out.CheckedInTickets += stats.CheckedInTickets
		out.FnbRedeemed += stats.TotalFnbRedeemed
		out.FnbRemaining += stats.TotalFnbRemaining
		out.BarTransactions += stats.BarTransactions
		out.TotalGuests += stats.TotalGuests
		out.TotalCouples += stats.TotalCouples
		out.TotalStagMale += stats.TotalStagMale
		out.TotalStagFemale += stats.TotalStagFemale
		out.TotalTequilaRedeemed += stats.TotalTequilaRedeemed
		out.TotalFnbCreditUsed += stats.TotalFnbCreditUsed
		out.ExtraCashCollected += stats.ExtraCashCollected
		out.ExtraUpiCollected += stats.ExtraUpiCollected
		out.RecentGateEntries = append(out.RecentGateEntries, stats.RecentGateEntries...)
		for _, row := range stats.TableBookingBreakdown {
			key := tableKey{section: row.Section, size: row.TableSize}
			agg := tableMap[key]
			agg.Section = row.Section
			agg.TableSize = row.TableSize
			agg.TotalTables += row.TotalTables
			agg.ReservedTables += row.ReservedTables
			tableMap[key] = agg
		}
		for _, item := range stats.TopBarItems {
			key := barItemKey{name: item.ItemName, category: item.ItemCategory, price: item.UnitPriceAtSale}
			if agg, ok := items[key]; ok {
				agg.Quantity += item.Quantity
				agg.Revenue += item.Revenue
				continue
			}
			copyItem := item
			items[key] = &copyItem
		}
	}
	for _, item := range items {
		out.TopBarItems = append(out.TopBarItems, *item)
	}
	sort.Slice(out.TopBarItems, func(i, j int) bool {
		if out.TopBarItems[i].Revenue != out.TopBarItems[j].Revenue {
			return out.TopBarItems[i].Revenue > out.TopBarItems[j].Revenue
		}
		return out.TopBarItems[i].ItemName < out.TopBarItems[j].ItemName
	})
	if len(out.TopBarItems) > 10 {
		out.TopBarItems = out.TopBarItems[:10]
	}
	for _, row := range tableMap {
		out.TableBookingBreakdown = append(out.TableBookingBreakdown, row)
	}
	sort.Slice(out.TableBookingBreakdown, func(i, j int) bool {
		if out.TableBookingBreakdown[i].Section != out.TableBookingBreakdown[j].Section {
			return out.TableBookingBreakdown[i].Section < out.TableBookingBreakdown[j].Section
		}
		return out.TableBookingBreakdown[i].TableSize < out.TableBookingBreakdown[j].TableSize
	})
	sort.Slice(out.RecentGateEntries, func(i, j int) bool {
		a, b := out.RecentGateEntries[i].CheckedInAt, out.RecentGateEntries[j].CheckedInAt
		if a == nil || b == nil {
			return a != nil
		}
		return a.After(*b)
	})
	if len(out.RecentGateEntries) > 20 {
		out.RecentGateEntries = out.RecentGateEntries[:20]
	}
	return out, nil
}

func isValidStaffRole(role db.StaffRole) bool {
	switch role {
	case db.StaffRoleADMIN, db.StaffRoleSCANNER, db.StaffRoleBARTENDER:
		return true
	default:
		return false
	}
}

func isValidEventStatus(status db.EventStatus) bool {
	switch status {
	case db.EventStatusDRAFT, db.EventStatusONSALE, db.EventStatusSOLDOUT, db.EventStatusENDED:
		return true
	default:
		return false
	}
}

func (s *AdminService) ListHelpMessages(ctx context.Context, resolved *bool, limit, offset int32) ([]db.HelpMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var resBool pgtype.Bool
	if resolved != nil {
		resBool = pgtype.Bool{Bool: *resolved, Valid: true}
	}
	msgs, err := s.repo.ListHelpMessages(ctx, db.ListHelpMessagesParams{
		IsResolved: resBool,
		PageLimit:  limit,
		PageOffset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("svc_admin_list_help_messages: %w", err)
	}
	return msgs, nil
}

func (s *AdminService) ResolveHelpMessage(ctx context.Context, id uuid.UUID, adminID uuid.UUID) (db.HelpMessage, error) {
	msg, err := s.repo.ResolveHelpMessage(ctx, db.ResolveHelpMessageParams{
		ID:         id,
		ResolvedBy: pgtype.UUID{Bytes: adminID, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.HelpMessage{}, httpx.NewNotFound("help message not found or already resolved")
		}
		return db.HelpMessage{}, fmt.Errorf("svc_admin_resolve_help_message: %w", err)
	}
	return msg, nil
}

type UpdateEventRequest struct {
	Title          *string         `json:"title,omitempty"`
	StartsAt       *time.Time      `json:"starts_at,omitempty"`
	EndsAt         *time.Time      `json:"ends_at,omitempty"`
	Location       *string         `json:"location,omitempty"`
	VenueName      *string         `json:"venue_name,omitempty"`
	LocationArea   *string         `json:"location_area,omitempty"`
	TransitInfo    *string         `json:"transit_info,omitempty"`
	AdvisoryNotice *string         `json:"advisory_notice,omitempty"`
	Description    *string         `json:"description,omitempty"`
	Status         *db.EventStatus `json:"status,omitempty"`
	Artists        *[]ArtistInput  `json:"artists,omitempty"`
}

type ArtistInput struct {
	ID          *uuid.UUID `json:"id,omitempty"`
	Name        string     `json:"name"`
	Category    string     `json:"category"`
	Description string     `json:"description"`
}

type UpdateTicketTierRequest struct {
	Name           *string `json:"name,omitempty"`
	Category       *string `json:"category,omitempty"`
	BasePrice      *int32  `json:"base_price,omitempty"`
	FnbCredit      *int32  `json:"fnb_credit,omitempty"`
	EntryCount     *int32  `json:"entry_count,omitempty"`
	IsGenderStrict *bool   `json:"is_gender_strict,omitempty"`
}

func (s *AdminService) UpdateTicketTier(ctx context.Context, tierID uuid.UUID, req UpdateTicketTierRequest) (db.TicketCategory, error) {
	curr, err := s.repo.GetCategoryByID(ctx, tierID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.TicketCategory{}, httpx.NewNotFound("tier not found")
		}
		return db.TicketCategory{}, fmt.Errorf("svc_admin_update_tier_lookup: %w", err)
	}

	name := curr.Name
	if req.Name != nil {
		name = strings.ToUpper(strings.TrimSpace(*req.Name))
		if name == "" || len(name) > 255 {
			return db.TicketCategory{}, httpx.NewBadRequest("tier name is required (max 255 chars)", nil)
		}
	}
	basePrice := curr.BasePrice
	if req.BasePrice != nil {
		if *req.BasePrice < 0 {
			return db.TicketCategory{}, httpx.NewBadRequest("base_price and fnb_credit must be non-negative", nil)
		}
		basePrice = *req.BasePrice
	}
	fnbCredit := curr.FnbCredit
	if req.FnbCredit != nil {
		if *req.FnbCredit < 0 {
			return db.TicketCategory{}, httpx.NewBadRequest("base_price and fnb_credit must be non-negative", nil)
		}
		fnbCredit = *req.FnbCredit
	}
	entryCount := curr.EntryCount
	if req.EntryCount != nil {
		switch *req.EntryCount {
		case 1, 2, 5, 8, 10, 15:
			entryCount = *req.EntryCount
		default:
			return db.TicketCategory{}, httpx.NewBadRequest("entry_count must be 1, 2, 5, 8, 10, or 15", nil)
		}
	}
	category := curr.Category
	if req.Category != nil {
		value := strings.ToUpper(strings.TrimSpace(*req.Category))
		switch value {
		case "A", "B", "C", "MAIN_FLOOR":
			category = pgtype.Text{String: value, Valid: true}
		default:
			return db.TicketCategory{}, httpx.NewBadRequest("invalid tier category", nil)
		}
	}
	genderStrict := curr.IsGenderStrict
	if req.IsGenderStrict != nil {
		genderStrict = *req.IsGenderStrict
	}
	if name == "COUPLE" {
		if entryCount != 2 {
			return db.TicketCategory{}, httpx.NewBadRequest("COUPLE tier must have entry_count = 2", nil)
		}
		genderStrict = true
	} else if name == "STAG_FEMALE" || name == "STAG_MALE" || name == "STAG" {
		if entryCount != 1 {
			return db.TicketCategory{}, httpx.NewBadRequest("stag tier must have entry_count = 1", nil)
		}
	}
	if strings.HasPrefix(name, "GROUP") {
		if !category.Valid || (category.String != "A" && category.String != "B" && category.String != "C") {
			return db.TicketCategory{}, httpx.NewBadRequest("GROUP tiers require category A, B, or C", nil)
		}
	}

	tier, err := s.repo.UpdateTicketCategory(ctx, db.UpdateTicketCategoryParams{
		ID:             tierID,
		Name:           pgtype.Text{String: name, Valid: true},
		Category:       category,
		BasePrice:      pgtype.Int4{Int32: basePrice, Valid: true},
		FnbCredit:      pgtype.Int4{Int32: fnbCredit, Valid: true},
		EntryCount:     pgtype.Int4{Int32: entryCount, Valid: true},
		IsGenderStrict: pgtype.Bool{Bool: genderStrict, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.TicketCategory{}, httpx.NewNotFound("tier not found")
		}
		return db.TicketCategory{}, fmt.Errorf("svc_admin_update_tier: %w", err)
	}
	return tier, nil
}

type UpdateTierPriceRequest struct {
	BasePrice int32 `json:"base_price"`
	FnbCredit int32 `json:"fnb_credit"`
}

type CreateArtistRequest struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

func (s *AdminService) UpdateEvent(ctx context.Context, id uuid.UUID, req UpdateEventRequest) (db.Event, error) {
	var event db.Event
	err := s.repo.WithTx(ctx, func(txCtx context.Context, q db.Querier) error {
		curr, err := q.GetEventByIDForUpdate(txCtx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewNotFound("event not found")
			}
			return fmt.Errorf("tx_admin_update_event_lookup: %w", err)
		}

		effStarts := curr.StartsAt
		if req.StartsAt != nil {
			effStarts = req.StartsAt.UTC()
		}
		effEnds := curr.EndsAt
		if req.EndsAt != nil {
			effEnds = req.EndsAt.UTC()
		}
		if !effEnds.After(effStarts) {
			return httpx.NewBadRequest("ends_at must be strictly after starts_at", nil)
		}

		var title pgtype.Text
		if req.Title != nil {
			t := strings.TrimSpace(*req.Title)
			if t == "" || len(t) > 255 {
				return httpx.NewBadRequest("title cannot be empty (max 255 chars)", nil)
			}
			title = pgtype.Text{String: t, Valid: true}
		}
		var location pgtype.Text
		if req.Location != nil {
			l := strings.TrimSpace(*req.Location)
			if l == "" || len(l) > 255 {
				return httpx.NewBadRequest("location cannot be empty (max 255 chars)", nil)
			}
			location = pgtype.Text{String: l, Valid: true}
		}
		var startsAt pgtype.Timestamptz
		if req.StartsAt != nil {
			startsAt = pgtype.Timestamptz{Time: req.StartsAt.UTC(), Valid: true}
		}
		var endsAt pgtype.Timestamptz
		if req.EndsAt != nil {
			endsAt = pgtype.Timestamptz{Time: req.EndsAt.UTC(), Valid: true}
		}
		var status db.NullEventStatus
		if req.Status != nil {
			if !isValidEventStatus(*req.Status) {
				return httpx.NewBadRequest("invalid event status", nil)
			}
			status = db.NullEventStatus{EventStatus: *req.Status, Valid: true}
		}

		venueName, err := optionalAdminText(req.VenueName, 255, "venue_name")
		if err != nil {
			return err
		}
		locationArea, err := optionalAdminText(req.LocationArea, 255, "location_area")
		if err != nil {
			return err
		}
		transitInfo, err := optionalAdminText(req.TransitInfo, 255, "transit_info")
		if err != nil {
			return err
		}
		advisoryNotice, err := optionalAdminText(req.AdvisoryNotice, 2000, "advisory_notice")
		if err != nil {
			return err
		}
		description, err := optionalAdminText(req.Description, 10000, "description")
		if err != nil {
			return err
		}

		event, err = q.UpdateEvent(txCtx, db.UpdateEventParams{
			ID:             id,
			Title:          title,
			StartsAt:       startsAt,
			EndsAt:         endsAt,
			Location:       location,
			VenueName:      venueName,
			LocationArea:   locationArea,
			TransitInfo:    transitInfo,
			AdvisoryNotice: advisoryNotice,
			Description:    description,
			Status:         status,
		})
		if err != nil {
			return fmt.Errorf("tx_admin_update_event: %w", err)
		}

		if req.Artists == nil {
			return nil
		}
		existingRows, err := q.ListArtistsByEvent(txCtx, id)
		if err != nil {
			return fmt.Errorf("tx_admin_update_event_artists_lookup: %w", err)
		}
		existing := make(map[uuid.UUID]struct{}, len(existingRows))
		for _, artist := range existingRows {
			existing[artist.ID] = struct{}{}
		}
		kept := make(map[uuid.UUID]struct{}, len(*req.Artists))
		for _, input := range *req.Artists {
			name, category, description, err := normalizeArtistFields(input.Name, input.Category, input.Description)
			if err != nil {
				return err
			}
			if input.ID != nil {
				if _, ok := existing[*input.ID]; !ok {
					return httpx.NewNotFound("artist not found")
				}
				if _, err := q.UpdateArtist(txCtx, db.UpdateArtistParams{
					ID:          *input.ID,
					EventID:     id,
					Name:        name,
					Category:    category,
					Description: description,
				}); err != nil {
					return fmt.Errorf("tx_admin_update_artist: %w", err)
				}
				kept[*input.ID] = struct{}{}
				continue
			}
			if _, err := q.CreateArtist(txCtx, db.CreateArtistParams{
				EventID:     id,
				Name:        name,
				Category:    category,
				Description: description,
			}); err != nil {
				return fmt.Errorf("tx_admin_create_artist: %w", err)
			}
		}
		for artistID := range existing {
			if _, ok := kept[artistID]; ok {
				continue
			}
			if err := q.DeleteArtist(txCtx, artistID); err != nil {
				return fmt.Errorf("tx_admin_delete_artist: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return db.Event{}, err
	}
	return event, nil
}

func (s *AdminService) UpdateTierPrice(ctx context.Context, tierID uuid.UUID, req UpdateTierPriceRequest) (db.TicketCategory, error) {
	if req.BasePrice < 0 || req.FnbCredit < 0 {
		return db.TicketCategory{}, httpx.NewBadRequest("base_price and fnb_credit must be non-negative", nil)
	}
	cat, err := s.repo.UpdateTicketCategoryPrice(ctx, db.UpdateTicketCategoryPriceParams{
		ID:        tierID,
		BasePrice: req.BasePrice,
		FnbCredit: req.FnbCredit,
	})
	if err != nil {
		return db.TicketCategory{}, fmt.Errorf("svc_admin_update_tier_price: %w", err)
	}
	return cat, nil
}

func normalizeArtistFields(name, category, description string) (string, string, string, error) {
	name = strings.TrimSpace(name)
	category = strings.TrimSpace(category)
	if name == "" || len(name) > 255 {
		return "", "", "", httpx.NewBadRequest("artist name is required (max 255 chars)", nil)
	}
	if category == "" || len(category) > 100 {
		return "", "", "", httpx.NewBadRequest("artist category is required (max 100 chars)", nil)
	}
	return name, category, strings.TrimSpace(description), nil
}

func (s *AdminService) CreateArtist(ctx context.Context, eventID uuid.UUID, req CreateArtistRequest) (db.EventArtist, error) {
	name, category, description, err := normalizeArtistFields(req.Name, req.Category, req.Description)
	if err != nil {
		return db.EventArtist{}, err
	}
	artist, err := s.repo.CreateArtist(ctx, db.CreateArtistParams{
		EventID:     eventID,
		Name:        name,
		Category:    category,
		Description: description,
	})
	if err != nil {
		return db.EventArtist{}, fmt.Errorf("svc_admin_create_artist: %w", err)
	}
	return artist, nil
}

func (s *AdminService) ListArtists(ctx context.Context, eventID uuid.UUID) ([]db.EventArtist, error) {
	return s.repo.ListArtistsByEvent(ctx, eventID)
}

func (s *AdminService) DeleteArtist(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeleteArtist(ctx, id)
}

type CheckoutLeadView struct {
	ID               uuid.UUID `json:"id"`
	EventTitle       string    `json:"event_title"`
	CategoryName     string    `json:"category_name"`
	ContactPhone     string    `json:"contact_phone"`
	WhatsAppNumber   string    `json:"whatsapp_number,omitempty"`
	ContactEmail     string    `json:"contact_email,omitempty"`
	PrimaryName      string    `json:"primary_name,omitempty"`
	PrimaryAge       int32     `json:"primary_age,omitempty"`
	Gender           string    `json:"gender,omitempty"`
	CoupleCount      int32     `json:"couple_count"`
	StagMaleCount    int32     `json:"stag_male_count"`
	StagFemaleCount  int32     `json:"stag_female_count"`
	GuestCount       int32     `json:"guest_count"`
	SeatSection      string    `json:"seat_section,omitempty"`
	SeatTableSize    int32     `json:"seat_table_size,omitempty"`
	UPITransactionID string    `json:"upi_transaction_id,omitempty"`
	BookingID        *uuid.UUID `json:"booking_id,omitempty"`
	Status           string    `json:"status"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (s *AdminService) ListCheckoutLeads(ctx context.Context, includeCompleted bool, eventID uuid.UUID, query string, limit, offset int32) ([]CheckoutLeadView, error) {
	if limit <= 0 || limit > 100 { limit = 50 }
	if offset < 0 { offset = 0 }
	rows, err := s.repo.ListCheckoutLeads(ctx, includeCompleted, eventID, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("svc_admin_list_leads: %w", err)
	}
	out := make([]CheckoutLeadView, 0, len(rows))
	for _, r := range rows {
		v := CheckoutLeadView{ID: r.ID, EventTitle: r.EventTitle, CategoryName: r.CategoryName, ContactPhone: r.ContactPhone, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
		if r.WhatsappNumber.Valid { v.WhatsAppNumber = r.WhatsappNumber.String }
		if r.ContactEmail.Valid { v.ContactEmail = r.ContactEmail.String }
		if r.PrimaryName.Valid { v.PrimaryName = r.PrimaryName.String }
		if r.PrimaryAge.Valid { v.PrimaryAge = r.PrimaryAge.Int32 }
		if r.Gender.Valid { v.Gender = r.Gender.String }
		if r.UpiTransactionID.Valid { v.UPITransactionID = r.UpiTransactionID.String }
		if r.ErrorMessage.Valid { v.ErrorMessage = r.ErrorMessage.String }
		if r.BookingID.Valid { id := uuid.UUID(r.BookingID.Bytes); v.BookingID = &id }
		var sel struct {
			Group *struct {
				Couples    int32 `json:"couples"`
				StagMale   int32 `json:"stag_male"`
				StagFemale int32 `json:"stag_female"`
			} `json:"group"`
			SeatSelection *struct {
				Section   string `json:"section"`
				TableSize int32  `json:"table_size"`
			} `json:"seat_selection"`
			Guests []any `json:"guests"`
		}
		_ = json.Unmarshal(r.SelectionJson, &sel)
		if sel.Group != nil {
			v.CoupleCount, v.StagMaleCount, v.StagFemaleCount = sel.Group.Couples, sel.Group.StagMale, sel.Group.StagFemale
			v.GuestCount = sel.Group.Couples*2 + sel.Group.StagMale + sel.Group.StagFemale
		} else if len(sel.Guests) > 0 { v.GuestCount = int32(len(sel.Guests)) }
		if sel.SeatSelection != nil { v.SeatSection = sel.SeatSelection.Section; v.SeatTableSize = sel.SeatSelection.TableSize }
		out = append(out, v)
	}
	return out, nil
}
