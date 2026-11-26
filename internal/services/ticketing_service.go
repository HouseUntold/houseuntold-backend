package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/cache"
	"github.com/venu-live/venu-api/internal/config"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/realtime"
	"github.com/venu-live/venu-api/internal/repositories"
	seating "github.com/venu-live/venu-api/internal/services/seating"
	"github.com/venu-live/venu-api/internal/ticketimg"
	"github.com/venu-live/venu-api/internal/whatsapp"
)

const maxBarCodeCollisionRetries = 10

func (s *TicketingService) publicAPIBaseURL() string {
	if s.cfg.PublicAPIURL != "" {
		return s.cfg.PublicAPIURL
	}
	return s.cfg.AppBaseURL
}

type TicketingService struct {
	cfg       *config.Config
	repo      *repositories.TicketingRepository
	tokens    *auth.TokenManager
	qrSigner  *auth.QRSigner
	roleCache *cache.StaffRoleCache
	notifier  whatsapp.Notifier
	logger    *slog.Logger
	seating   *seating.Service
	adminHub  *realtime.AdminHub
}

func NewTicketingService(
	cfg *config.Config,
	repo *repositories.TicketingRepository,
	tokens *auth.TokenManager,
	qrSigner *auth.QRSigner,
	roleCache *cache.StaffRoleCache,
	notifier whatsapp.Notifier,
	logger *slog.Logger,
) *TicketingService {
	if logger == nil {
		logger = slog.Default()
	}
	return &TicketingService{
		cfg:       cfg,
		repo:      repo,
		tokens:    tokens,
		qrSigner:  qrSigner,
		roleCache: roleCache,
		notifier:  notifier,
		logger:    logger,
	}
}

func (s *TicketingService) SetSeatingService(svc *seating.Service) {
	s.seating = svc
}

func (s *TicketingService) SetAdminHub(hub *realtime.AdminHub) {
	s.adminHub = hub
}

func (s *TicketingService) Health(ctx context.Context) error {
	return s.repo.Ping(ctx)
}

func (s *TicketingService) PoolStats() repositories.PoolStats {
	return s.repo.PoolStats()
}

// --- Staff Authentication (Username / Password + Server-Side Role Cache) ---

type StaffSessionResult struct {
	Staff  StaffSummary   `json:"staff"`
	Tokens auth.TokenPair `json:"-"`
}

type StaffSummary struct {
	ID       uuid.UUID    `json:"id"`
	Username string       `json:"username"`
	Role     db.StaffRole `json:"role"`
}

func (s *TicketingService) StaffLogin(ctx context.Context, username, password string) (StaffSessionResult, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return StaffSessionResult{}, httpx.NewBadRequest("username and password are required", nil)
	}

	staff, err := s.repo.GetStaffByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StaffSessionResult{}, httpx.NewUnauthorized("invalid username or password", nil)
		}
		return StaffSessionResult{}, fmt.Errorf("svc_staff_login: %w", err)
	}

	if !staff.IsActive {
		return StaffSessionResult{}, httpx.NewUnauthorized("staff account is deactivated", nil)
	}

	valid, err := auth.VerifyPassword(password, staff.PasswordHash)
	if err != nil || !valid {
		return StaffSessionResult{}, httpx.NewUnauthorized("invalid username or password", nil)
	}

	pair, err := s.tokens.IssueStaffTokens(staff)
	if err != nil {
		return StaffSessionResult{}, fmt.Errorf("svc_staff_login_tokens: %w", err)
	}

	if s.roleCache != nil {
		s.roleCache.Set(staff)
	}

	return StaffSessionResult{
		Staff: StaffSummary{
			ID:       staff.ID,
			Username: staff.Username,
			Role:     staff.Role,
		},
		Tokens: pair,
	}, nil
}

func (s *TicketingService) RefreshSession(ctx context.Context, refreshToken string) (StaffSessionResult, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return StaffSessionResult{}, httpx.NewUnauthorized("refresh token is required", nil)
	}

	claims, err := s.tokens.VerifyRefreshToken(refreshToken)
	if err != nil {
		return StaffSessionResult{}, httpx.NewUnauthorized("refresh token invalid or expired", err)
	}

	staff, err := s.repo.GetStaffByID(ctx, claims.StaffID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StaffSessionResult{}, httpx.NewUnauthorized("staff account not found", nil)
		}
		return StaffSessionResult{}, fmt.Errorf("svc_refresh_session: %w", err)
	}
	if !staff.IsActive {
		if s.roleCache != nil {
			s.roleCache.Invalidate(staff.ID)
		}
		return StaffSessionResult{}, httpx.NewUnauthorized("staff account is deactivated", nil)
	}
	if staff.TokenVersion != claims.TokenVersion {
		if s.roleCache != nil {
			s.roleCache.Invalidate(staff.ID)
		}
		return StaffSessionResult{}, httpx.NewUnauthorized("refresh token has already been used", nil)
	}

	staff, err = s.repo.RotateStaffTokenVersion(ctx, db.RotateStaffTokenVersionParams{
		ID:           claims.StaffID,
		TokenVersion: claims.TokenVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StaffSessionResult{}, httpx.NewUnauthorized("refresh token has already been used", nil)
		}
		return StaffSessionResult{}, fmt.Errorf("svc_refresh_rotate: %w", err)
	}

	pair, err := s.tokens.IssueStaffTokens(staff)
	if err != nil {
		return StaffSessionResult{}, fmt.Errorf("svc_refresh_tokens: %w", err)
	}

	if s.roleCache != nil {
		s.roleCache.Set(staff)
	}

	return StaffSessionResult{
		Staff: StaffSummary{
			ID:       staff.ID,
			Username: staff.Username,
			Role:     staff.Role,
		},
		Tokens: pair,
	}, nil
}

func (s *TicketingService) LogoutStaff(ctx context.Context, staffID uuid.UUID) error {
	if s.roleCache != nil && staffID != uuid.Nil {
		s.roleCache.Invalidate(staffID)
	}
	if staffID == uuid.Nil {
		return nil
	}
	if err := s.repo.IncrementStaffTokenVersion(ctx, staffID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("svc_logout_revoke: %w", err)
	}
	return nil
}

// --- Public Event Details & Dynamic First 99 Pricing ---

type PublicEventView struct {
	ID              uuid.UUID                   `json:"id"`
	Title           string                      `json:"title"`
	StartsAt        time.Time                   `json:"starts_at"`
	EndsAt          time.Time                   `json:"ends_at"`
	Location        string                      `json:"location"`
	VenueName       string                      `json:"venue_name,omitempty"`
	LocationArea    string                      `json:"location_area,omitempty"`
	TransitInfo     string                      `json:"transit_info,omitempty"`
	AdvisoryNotice  string                      `json:"advisory_notice,omitempty"`
	Description     string                      `json:"description,omitempty"`
	Status          db.EventStatus              `json:"status"`
	TierCount       int64                       `json:"tier_count"`
	MinPrice        int64                       `json:"min_price"`
	MaxPrice        int64                       `json:"max_price"`
	IsFirst99Active bool                        `json:"is_first_99_active"`
	Tiers           []EventTierView             `json:"tiers"`
	Seats           []seating.SeatInventoryView `json:"seats,omitempty"`
}

func (s *TicketingService) ListPublicEvents(ctx context.Context, status string, limit, offset int32) ([]PublicEventView, error) {
	status = strings.ToUpper(strings.TrimSpace(status))
	if status == "" {
		status = "ON_SALE"
	}
	switch status {
	case "ALL", "ON_SALE", "SOLD_OUT", "ENDED":
	default:
		return nil, httpx.NewBadRequest("status must be ALL, ON_SALE, SOLD_OUT, or ENDED", nil)
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var events []db.ListPublicEventsRow
	err := s.repo.Read(ctx, func(tctx context.Context, q db.Querier) error {
		var err error
		events, err = q.ListPublicEvents(tctx, db.ListPublicEventsParams{
			Status:     status,
			PageLimit:  limit,
			PageOffset: offset,
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("svc_list_public_events: %w", err)
	}

	views := make([]PublicEventView, 0, len(events))
	for _, event := range events {
		view := PublicEventView{
			ID:        event.ID,
			Title:     event.Title,
			StartsAt:  event.StartsAt,
			EndsAt:    event.EndsAt,
			Location:  event.Location,
			Status:    event.Status,
			TierCount: event.TierCount,
			MinPrice:  event.MinPrice,
			MaxPrice:  event.MaxPrice,
			Tiers:     []EventTierView{},
		}
		details, err := s.GetEventDetails(ctx, event.ID)
		if err != nil {
			return nil, err
		}
		view.VenueName = details.VenueName
		view.LocationArea = details.LocationArea
		view.TransitInfo = details.TransitInfo
		view.AdvisoryNotice = details.AdvisoryNotice
		view.Description = details.Description
		view.Tiers = details.Tiers
		view.Seats = details.Seats
		view.IsFirst99Active = details.IsFirst99Active
		views = append(views, view)
	}
	return views, nil
}

type EventTierView struct {
	ID                 uuid.UUID `json:"id"`
	Name               string    `json:"name"`
	Category           string    `json:"category,omitempty"`
	BasePrice          int32     `json:"base_price"`
	EffectivePrice     int32     `json:"effective_price"`
	FnbCredit          int32     `json:"fnb_credit"`
	RawFnbCredit       int32     `json:"raw_fnb_credit"`
	EntryCount         int32     `json:"entry_count"`
	IsGenderStrict     bool      `json:"is_gender_strict"`
	First99Applied     bool      `json:"first_99_applied"`
	FreeSlotsRemaining int64     `json:"free_slots_remaining,omitempty"`
	IncludesFreeShot   bool      `json:"includes_free_shot"`
}

type EventDetailsView struct {
	ID              uuid.UUID                   `json:"id"`
	Title           string                      `json:"title"`
	StartsAt        time.Time                   `json:"starts_at"`
	EndsAt          time.Time                   `json:"ends_at"`
	Location        string                      `json:"location"`
	VenueName       string                      `json:"venue_name,omitempty"`
	LocationArea    string                      `json:"location_area,omitempty"`
	TransitInfo     string                      `json:"transit_info,omitempty"`
	AdvisoryNotice  string                      `json:"advisory_notice,omitempty"`
	Description     string                      `json:"description,omitempty"`
	Artists         []db.EventArtist            `json:"artists,omitempty"`
	Status          db.EventStatus              `json:"status"`
	UPIVPA          string                      `json:"upi_vpa"`
	UPIMerchantName string                      `json:"upi_merchant_name"`
	UPIQRImageURL   string                      `json:"upi_qr_image_url,omitempty"`
	Tiers           []EventTierView             `json:"tiers"`
	Seats           []seating.SeatInventoryView `json:"seats,omitempty"`
	IsFirst99Active bool                        `json:"is_first_99_active"`
}

func (s *TicketingService) GetEventDetails(ctx context.Context, eventID uuid.UUID) (EventDetailsView, error) {
	event, err := s.repo.GetEventByID(ctx, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EventDetailsView{}, httpx.NewNotFound("event not found")
		}
		return EventDetailsView{}, fmt.Errorf("svc_get_event: %w", err)
	}

	categories, err := s.repo.ListEventCategories(ctx, eventID)
	if err != nil {
		return EventDetailsView{}, fmt.Errorf("svc_list_categories: %w", err)
	}

	confirmedFemales, err := s.repo.CountConfirmedFemaleStagByEvent(ctx, eventID)
	if err != nil {
		return EventDetailsView{}, fmt.Errorf("svc_count_female_stag: %w", err)
	}

	var first99Active bool
	if err := s.repo.Read(ctx, func(readCtx context.Context, q db.Querier) error {
		var readErr error
		first99Active, readErr = q.GetEventFirst99Active(readCtx, eventID)
		return readErr
	}); err != nil {
		return EventDetailsView{}, fmt.Errorf("svc_get_event_pricing_flag: %w", err)
	}

	remainingFree := s.cfg.First99FemaleLimit - confirmedFemales
	if remainingFree < 0 {
		remainingFree = 0
	}

	tiers := make([]EventTierView, 0, len(categories))
	for _, cat := range categories {
		pricing := CalculateCheckoutPricing(cat, confirmedFemales, s.cfg.First99FemaleLimit, first99Active)
		tv := EventTierView{
			ID:               cat.ID,
			Name:             cat.Name,
			BasePrice:        cat.BasePrice,
			EffectivePrice:   pricing.TotalAmount,
			FnbCredit:        pricing.FnbCredit,
			RawFnbCredit:     cat.FnbCredit,
			EntryCount:       cat.EntryCount,
			IsGenderStrict:   cat.IsGenderStrict,
			First99Applied:   pricing.First99Applied,
			IncludesFreeShot: pricing.FreeShotPerk,
		}
		if cat.Category.Valid {
			tv.Category = cat.Category.String
		}
		if pricing.First99Applied {
			tv.FreeSlotsRemaining = remainingFree
		}
		tiers = append(tiers, tv)
	}

	var seats []seating.SeatInventoryView
	if s.seating != nil {
		snapshot, err := s.seating.Snapshot(ctx, eventID)
		if err != nil {
			return EventDetailsView{}, err
		}
		seats = snapshot.Groups
	}

	artists, _ := s.repo.ListArtistsByEvent(ctx, eventID)

	return EventDetailsView{
		ID:              event.ID,
		Title:           event.Title,
		StartsAt:        event.StartsAt,
		EndsAt:          event.EndsAt,
		Location:        event.Location,
		VenueName:       event.VenueName,
		LocationArea:    event.LocationArea,
		TransitInfo:     event.TransitInfo,
		AdvisoryNotice:  event.AdvisoryNotice,
		Description:     event.Description,
		Artists:         artists,
		Status:          event.Status,
		UPIVPA:          s.cfg.UPIVPA,
		UPIMerchantName: s.cfg.UPIMerchantName,
		UPIQRImageURL:   s.cfg.UPIQRImageURL,
		Tiers:           tiers,
		Seats:           seats,
		IsFirst99Active: first99Active,
	}, nil
}

// --- Guest Checkout Initialization (Direct UPI QR + UTR Submission, Atomic First-99 Lock) ---

type InitCheckoutRequest struct {
	EventID          uuid.UUID                   `json:"event_id"`
	CategoryID       uuid.UUID                   `json:"category_id"`
	ContactPhone     string                      `json:"contact_phone"`
	ContactEmail     string                      `json:"contact_email,omitempty"`
	PrimaryName      string                      `json:"primary_name,omitempty"`
	PrimaryAge       int32                       `json:"primary_age,omitempty"`
	WhatsAppNumber   string                      `json:"whatsapp_number,omitempty"`
	Gender           string                      `json:"gender,omitempty"`
	UPITransactionID string                      `json:"upi_transaction_id,omitempty"`
	SeatSelection    *seating.SeatSelectionInput `json:"seat_selection,omitempty"`
	Guests           []GuestInput                `json:"guests,omitempty"`
	Group            *GroupBookingInput          `json:"group,omitempty"`
}

type UPIPaymentInstructions struct {
	VPA          string `json:"vpa"`
	MerchantName string `json:"merchant_name"`
	AmountINR    int32  `json:"amount_inr"`
	IntentURI    string `json:"intent_uri"`
	QRImageURL   string `json:"qr_image_url,omitempty"`
}

type InitCheckoutResponse struct {
	BookingID        uuid.UUID               `json:"booking_id"`
	TicketID         uuid.UUID               `json:"ticket_id"`
	Status           db.BookingStatus        `json:"status"`
	TotalAmount      int32                   `json:"total_amount"`
	Currency         string                  `json:"currency"`
	FnbCredit        int32                   `json:"fnb_credit"`
	EntryCount       int32                   `json:"entry_count"`
	AttendeeCount    int32                   `json:"attendee_count"`
	PrimaryGuestName string                  `json:"primary_guest_name,omitempty"`
	PaymentQRData    string                  `json:"payment_qr_data,omitempty"`
	First99Applied   bool                    `json:"first_99_applied"`
	UPITransactionID string                  `json:"upi_transaction_id,omitempty"`
	UPIPayment       *UPIPaymentInstructions `json:"upi_payment,omitempty"`
	TicketURL        string                  `json:"ticket_url,omitempty"`
	TicketImageURL   string                  `json:"ticket_image_url,omitempty"`
	BarCode          string                  `json:"bar_code,omitempty"`
	GateQRToken      string                  `json:"gate_qr_token,omitempty"`
	SeatSection      string                  `json:"seat_section,omitempty"`
	SeatTableSize    int32                   `json:"seat_table_size,omitempty"`
	SeatTableNumber  int32                   `json:"seat_table_number,omitempty"`
	TequilaEligible  int32                   `json:"tequila_eligible_count,omitempty"`
}

func (s *TicketingService) InitGuestCheckout(ctx context.Context, req InitCheckoutRequest, idempotencyKey string) (InitCheckoutResponse, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 255 {
		return InitCheckoutResponse{}, httpx.NewBadRequest("Idempotency-Key header must be 8-255 characters", nil)
	}
	if req.EventID == uuid.Nil || req.CategoryID == uuid.Nil {
		return InitCheckoutResponse{}, httpx.NewBadRequest("event_id and category_id are required", nil)
	}
	phoneSource := strings.TrimSpace(req.ContactPhone)
	if phoneSource == "" {
		phoneSource = req.WhatsAppNumber
	}
	phone, err := auth.NormalizeIndianMobile(phoneSource)
	if err != nil {
		_ = s.repo.UpsertCheckoutLeadAttempt(ctx, repositories.CheckoutLeadAttempt{
			IdempotencyKey: idempotencyKey, EventID: req.EventID, CategoryID: req.CategoryID,
			ContactPhone: strings.TrimSpace(phoneSource), WhatsAppNumber: strings.TrimSpace(req.WhatsAppNumber),
			ContactEmail: strings.TrimSpace(req.ContactEmail), PrimaryName: strings.TrimSpace(req.PrimaryName),
			PrimaryAge: req.PrimaryAge, Gender: strings.ToUpper(strings.TrimSpace(req.Gender)),
			SelectionJSON: map[string]any{"category_id": req.CategoryID, "guests": req.Guests, "group": req.Group, "seat_selection": req.SeatSelection},
		})
		_ = s.repo.MarkCheckoutLeadResult(ctx, idempotencyKey, nil, "INVALID", strings.TrimSpace(req.UPITransactionID), err.Error())
		return InitCheckoutResponse{}, httpx.NewBadRequest(err.Error(), nil)
	}
	req.ContactPhone = phone
	if strings.TrimSpace(req.WhatsAppNumber) != "" {
		whatsapp, waErr := auth.NormalizeIndianMobile(req.WhatsAppNumber)
		if waErr != nil {
			return InitCheckoutResponse{}, httpx.NewBadRequest(waErr.Error(), nil)
		}
		req.WhatsAppNumber = whatsapp
	} else {
		req.WhatsAppNumber = phone
	}
	email := strings.TrimSpace(req.ContactEmail)
	if email == "" {
		email = phone + "@patron.venu"
		req.ContactEmail = email
	}
	if len(email) > 255 {
		return InitCheckoutResponse{}, httpx.NewBadRequest("contact_email must be at most 255 characters", nil)
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return InitCheckoutResponse{}, httpx.NewBadRequest("valid contact_email is required", nil)
	}

	cat, err := s.repo.GetCategoryByID(ctx, req.CategoryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InitCheckoutResponse{}, httpx.NewNotFound("ticket category not found")
		}
		return InitCheckoutResponse{}, fmt.Errorf("svc_checkout_get_cat: %w", err)
	}
	categories, err := s.repo.ListEventCategories(ctx, req.EventID)
	if err != nil {
		return InitCheckoutResponse{}, fmt.Errorf("svc_checkout_list_categories: %w", err)
	}
	pricingConfig := EventPricingFromCategories(categories)
	if cat.EventID != req.EventID {
		return InitCheckoutResponse{}, httpx.NewBadRequest("category does not belong to specified event", nil)
	}

	if req.Group != nil {
		if req.Group.Couples < 0 || req.Group.StagMale < 0 || req.Group.StagFemale < 0 {
			return InitCheckoutResponse{}, httpx.NewBadRequest("group counts cannot be negative", nil)
		}
		guestCount := req.Group.Couples*2 + req.Group.StagMale + req.Group.StagFemale
		if guestCount == 0 || guestCount > 50 {
			return InitCheckoutResponse{}, httpx.NewBadRequest("group must contain between 1 and 50 guests", nil)
		}
		leadName := strings.TrimSpace(req.PrimaryName)
		if leadName == "" {
			leadName = "Guest " + phone
		}
		req.Guests = BuildGroupGuests(*req.Group, leadName, req.PrimaryAge)
	} else if len(req.Guests) == 0 {
		leadName := strings.TrimSpace(req.PrimaryName)
		if leadName == "" {
			leadName = "Guest " + phone
		}
		leadAge := req.PrimaryAge
		catUpper := strings.ToUpper(strings.TrimSpace(cat.Name))
		leadGender := db.GenderTypeMALE
		if strings.ToUpper(strings.TrimSpace(req.Gender)) == "FEMALE" || catUpper == "STAG_FEMALE" {
			leadGender = db.GenderTypeFEMALE
		}
		if catUpper == "STAG_MALE" {
			leadGender = db.GenderTypeMALE
		}
		req.Guests = []GuestInput{
			{
				Name:             leadName,
				Age:              leadAge,
				Gender:           leadGender,
				IsPrimaryContact: true,
			},
		}
		for i := 1; i < int(cat.EntryCount); i++ {
			gGender := leadGender
			if (catUpper == "COUPLE" || cat.IsGenderStrict) && cat.EntryCount == 2 {
				if leadGender == db.GenderTypeMALE {
					gGender = db.GenderTypeFEMALE
				} else {
					gGender = db.GenderTypeMALE
				}
			}
			req.Guests = append(req.Guests, GuestInput{
				Name:             leadName,
				Age:              leadAge,
				Gender:           gGender,
				IsPrimaryContact: false,
			})
		}
	}

	upiTxnID := ""
	if strings.TrimSpace(req.UPITransactionID) != "" {
		normalizedUTR, utrErr := auth.NormalizeUPIUTR(req.UPITransactionID)
		if utrErr != nil {
			_ = s.repo.MarkCheckoutLeadResult(ctx, idempotencyKey, nil, "INVALID", "", utrErr.Error())
			return InitCheckoutResponse{}, httpx.NewBadRequest(utrErr.Error(), nil)
		}
		upiTxnID = normalizedUTR
		req.UPITransactionID = normalizedUTR
	}

	leadSelection := map[string]any{"category_id": req.CategoryID, "guests": req.Guests, "group": req.Group, "seat_selection": req.SeatSelection}
	_ = s.repo.UpsertCheckoutLeadAttempt(ctx, repositories.CheckoutLeadAttempt{
		IdempotencyKey: idempotencyKey, EventID: req.EventID, CategoryID: req.CategoryID,
		ContactPhone: req.ContactPhone, WhatsAppNumber: req.WhatsAppNumber, ContactEmail: req.ContactEmail,
		PrimaryName: strings.TrimSpace(req.PrimaryName), PrimaryAge: req.PrimaryAge,
		Gender: strings.ToUpper(strings.TrimSpace(req.Gender)), SelectionJSON: leadSelection, UPITransactionID: upiTxnID,
	})

	if req.Group == nil {
		if err := ValidateCategoryGuests(cat, req.Guests); err != nil {
			_ = s.repo.MarkCheckoutLeadResult(ctx, idempotencyKey, nil, "INVALID", "", err.Error())
			return InitCheckoutResponse{}, err
		}
	}

	isGroup := req.Group == nil && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(cat.Name)), "GROUP")
	var groupSelection seating.SeatSelectionInput
	if isGroup {
		if s.seating == nil {
			return InitCheckoutResponse{}, httpx.NewConflict("group seating is not configured", nil)
		}
		if req.SeatSelection == nil {
			return InitCheckoutResponse{}, httpx.NewBadRequest("seat_selection is required for group bookings", nil)
		}
		groupSelection = *req.SeatSelection
		groupSelection.Section = strings.ToUpper(strings.TrimSpace(groupSelection.Section))
		if groupSelection.Section != "A" && groupSelection.Section != "B" && groupSelection.Section != "C" {
			return InitCheckoutResponse{}, httpx.NewBadRequest("seat selection section must be A, B, or C", nil)
		}
		if groupSelection.TableSize != 5 && groupSelection.TableSize != 8 && groupSelection.TableSize != 10 && groupSelection.TableSize != 15 {
			return InitCheckoutResponse{}, httpx.NewBadRequest("seat selection table_size must be 5, 8, 10, or 15", nil)
		}
		if cat.Category.Valid && cat.Category.String != groupSelection.Section {
			return InitCheckoutResponse{}, httpx.NewBadRequest("seat selection section does not match ticket category", nil)
		}
		if cat.EntryCount != groupSelection.TableSize {
			return InitCheckoutResponse{}, httpx.NewBadRequest("seat selection table_size does not match ticket category", nil)
		}
	} else if req.SeatSelection != nil {
		return InitCheckoutResponse{}, httpx.NewBadRequest("seat_selection is only valid for group bookings", nil)
	}

	hasPrimary := false
	for _, g := range req.Guests {
		if g.IsPrimaryContact {
			hasPrimary = true
			break
		}
	}
	if !hasPrimary {
		req.Guests[0].IsPrimaryContact = true
	}

	var resp InitCheckoutResponse
	var waMsg *whatsapp.TicketPassMessage

	// Retry on the rare chance a randomly generated 5-char 1D bartender barcode collides under 100+ concurrent checkouts.
	var seatReservation db.EventSeatReservation
	for attempt := 0; attempt < maxBarCodeCollisionRetries; attempt++ {
		waMsg = nil
		seatReservation = db.EventSeatReservation{}
		err = s.repo.WithTx(ctx, func(txCtx context.Context, q db.Querier) error {
			var event db.Event
			var confirmedFemaleCount int64
			categoryName := strings.ToUpper(strings.TrimSpace(cat.Name))
			needsFemaleSlot := categoryName == "STAG_FEMALE" || (req.Group != nil && req.Group.StagFemale > 0)

			if needsFemaleSlot {
				event, err = q.GetEventByIDForUpdate(txCtx, req.EventID)
				if err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return httpx.NewNotFound("event not found")
					}
					return fmt.Errorf("tx_lock_event: %w", err)
				}
				confirmedFemaleCount, err = q.CountConfirmedFemaleStagByEvent(txCtx, req.EventID)
				if err != nil {
					return fmt.Errorf("tx_count_female_stag: %w", err)
				}
			} else {
				event, err = q.GetEventByID(txCtx, req.EventID)
				if err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return httpx.NewNotFound("event not found")
					}
					return fmt.Errorf("tx_get_event: %w", err)
				}
			}

			if event.Status != db.EventStatusONSALE {
				return httpx.NewConflict("event is not currently on sale", nil)
			}
			if time.Now().UTC().After(event.EndsAt) {
				return httpx.NewConflict("event has already ended", nil)
			}

			first99Active, err := q.GetEventFirst99Active(txCtx, req.EventID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.NewNotFound("event not found")
				}
				return fmt.Errorf("tx_get_event_pricing_flag: %w", err)
			}

			pricing := CalculateCheckoutPricing(cat, confirmedFemaleCount, s.cfg.First99FemaleLimit, first99Active)
			tequilaEligible := int32(0)
			if req.Group != nil {
				tequilaEligible = req.Group.StagFemale
			} else if strings.ToUpper(strings.TrimSpace(cat.Name)) == "STAG_FEMALE" {
				tequilaEligible = int32(len(req.Guests))
			}

			groupPricing := GroupPricing{}
			if req.Group != nil {
				groupPricing = CalculateGroupPricing(GroupPricingInput{
					Group:                *req.Group,
					First99Active:        first99Active,
					ConfirmedFemaleCount: confirmedFemaleCount,
					First99Limit:         s.cfg.First99FemaleLimit,
					CouplePrice:          pricingConfig.CouplePrice,
					CoupleFnb:            pricingConfig.CoupleFnb,
					MalePrice:            pricingConfig.MalePrice,
					MaleFnb:              pricingConfig.MaleFnb,
					FemalePrice:          pricingConfig.FemalePrice,
					FemaleFnb:            pricingConfig.FemaleFnb,
				})
				pricing = CheckoutPricing{
					TotalAmount:    groupPricing.TotalAmount,
					FnbCredit:      groupPricing.FnbCredit,
					First99Applied: groupPricing.First99Applied,
					FreeShotPerk:   tequilaEligible > 0,
				}
			}

			bookingStatus := db.BookingStatusPENDING
			upiText := pgtype.Text{Valid: false}
			if pricing.TotalAmount == 0 || (pricing.TotalAmount == 1 && pricing.First99Applied) {
				bookingStatus = db.BookingStatusCONFIRMED
			} else if upiTxnID != "" {
				upiText = pgtype.Text{String: upiTxnID, Valid: true}
			}

			ticketID := uuid.New()
			barCode, err := auth.GenerateBarCode()
			if err != nil {
				return fmt.Errorf("tx_generate_barcode: %w", err)
			}
			digest := s.qrSigner.ComputeDigest(ticketID)

			booking, err := q.CreateBooking(txCtx, db.CreateBookingParams{
				EventID:          event.ID,
				CategoryID:       cat.ID,
				ContactPhone:     phone,
				ContactEmail:     email,
				TotalAmount:      pricing.TotalAmount,
				UpiTransactionID: upiText,
				Status:           bookingStatus,
				IdempotencyKey:   pgtype.Text{String: idempotencyKey, Valid: true},
			})
			if err != nil {
				return fmt.Errorf("tx_create_booking: %w", err)
			}
			if req.Group != nil {
				if err := q.SetBookingGroupCounts(txCtx, db.SetBookingGroupCountsParams{
					ID:                       booking.ID,
					CoupleCount:              req.Group.Couples,
					StagMaleCount:            req.Group.StagMale,
					StagFemaleCount:          req.Group.StagFemale,
					FreeTequilaEligibleCount: tequilaEligible,
				}); err != nil {
					return fmt.Errorf("tx_set_booking_group_counts: %w", err)
				}
			} else if tequilaEligible > 0 {
				if err := q.SetBookingFreeTequilaEligible(txCtx, db.SetBookingFreeTequilaEligibleParams{
					ID:                       booking.ID,
					FreeTequilaEligibleCount: tequilaEligible,
				}); err != nil {
					return fmt.Errorf("tx_set_booking_tequila_eligible: %w", err)
				}
			}

			ticket, err := q.CreateTicket(txCtx, db.CreateTicketParams{
				ID:                 ticketID,
				BookingID:          booking.ID,
				EventID:            event.ID,
				QrTokenDigest:      digest,
				BarCode:            barCode,
				FoodBalance:        pricing.FnbCredit,
				InitialFoodBalance: pricing.FnbCredit,
			})
			if err != nil {
				return fmt.Errorf("tx_create_ticket: %w", err)
			}

			if isGroup {
				seatReservation, err = s.seating.Acquire(txCtx, q, event.ID, groupSelection, booking.ID, ticket.ID)
				if err != nil {
					return err
				}
			}

			primaryName := strings.TrimSpace(req.Guests[0].Name)
			for _, g := range req.Guests {
				name := strings.TrimSpace(g.Name)
				if g.IsPrimaryContact {
					primaryName = name
				}
				if _, err := q.CreateTicketGuest(txCtx, db.CreateTicketGuestParams{
					TicketID:         ticket.ID,
					Name:             name,
					Age:              pgtype.Int4{Int32: g.Age, Valid: g.Age > 0},
					Gender:           g.Gender,
					IsPrimaryContact: g.IsPrimaryContact,
					FreeShotEligible: g.Gender == db.GenderTypeFEMALE && (req.Group == nil || req.Group.Couples == 0),
				}); err != nil {
					return fmt.Errorf("tx_create_guest: %w", err)
				}
			}

			ticketURL := fmt.Sprintf("%s/orders/track?ticket_id=%s", s.cfg.AppBaseURL, ticket.ID.String())
			ticketImgURL := fmt.Sprintf("%s/api/tickets/%s/image", s.publicAPIBaseURL(), ticket.ID.String())

			paymentQR := ""
			if pricing.TotalAmount > 0 {
				note := fmt.Sprintf("Ticket %s", booking.ID.String()[:8])
				paymentQR = auth.BuildUPIIntentURI(s.cfg.UPIVPA, s.cfg.UPIMerchantName, note, pricing.TotalAmount)
			}

			entryCount := cat.EntryCount
			if req.Group != nil {
				entryCount = groupPricing.GuestCount
			}

			resp = InitCheckoutResponse{
				BookingID:        booking.ID,
				TicketID:         ticket.ID,
				Status:           bookingStatus,
				TotalAmount:      pricing.TotalAmount,
				Currency:         "INR",
				FnbCredit:        pricing.FnbCredit,
				EntryCount:       entryCount,
				AttendeeCount:    entryCount,
				PrimaryGuestName: primaryName,
				PaymentQRData:    paymentQR,
				First99Applied:   pricing.First99Applied,
				UPITransactionID: upiTxnID,
				SeatSection:      seatReservation.Section,
				SeatTableSize:    seatReservation.TableSize,
				SeatTableNumber:  seatReservation.TableNumber,
				TequilaEligible:  tequilaEligible,
			}

			if bookingStatus == db.BookingStatusCONFIRMED {
				qrExp := event.EndsAt.Add(s.cfg.TicketQRTTL)
				resp.TicketURL = ticketURL
				resp.TicketImageURL = ticketImgURL
				resp.BarCode = ticket.BarCode
				resp.GateQRToken = s.qrSigner.SignToken(ticket.ID, event.ID, time.Now().UTC(), qrExp)
				waMsg = &whatsapp.TicketPassMessage{
					TicketID:       ticket.ID.String(),
					RecipientPhone: phone,
					PrimaryGuest:   primaryName,
					EventTitle:     event.Title,
					EventLocation:  event.Location,
					EventStartsAt:  event.StartsAt,
					CategoryName:   cat.Name,
					TicketURL:      ticketURL,
					BarCode:        ticket.BarCode,
					EntryCount:     entryCount,
					FoodBalance:    ticket.FoodBalance,
				}
			} else {
				note := fmt.Sprintf("House Untold %s", booking.ID.String()[:8])
				resp.UPIPayment = &UPIPaymentInstructions{
					VPA:          s.cfg.UPIVPA,
					MerchantName: s.cfg.UPIMerchantName,
					AmountINR:    pricing.TotalAmount,
					IntentURI:    auth.BuildUPIIntentURI(s.cfg.UPIVPA, s.cfg.UPIMerchantName, note, pricing.TotalAmount),
					QRImageURL:   s.cfg.UPIQRImageURL,
				}
			}
			return nil
		})

		if err == nil {
			break
		}
		if isBarCodeUniqueViolation(err) {
			continue
		}
		if isUPITxnUniqueViolation(err) {
			_ = s.repo.MarkCheckoutLeadResult(ctx, idempotencyKey, nil, "FAILED", upiTxnID, "duplicate UTR")
			return InitCheckoutResponse{}, httpx.NewConflict("this UTR has already been used for another booking", err)
		}
		if isCheckoutIdempotencyViolation(err) {
			_ = s.repo.MarkCheckoutLeadResult(ctx, idempotencyKey, nil, "DUPLICATE", upiTxnID, "duplicate idempotency key")
			return InitCheckoutResponse{}, httpx.NewConflict("checkout with this Idempotency-Key has already been submitted", err)
		}
		_ = s.repo.MarkCheckoutLeadResult(ctx, idempotencyKey, nil, "FAILED", upiTxnID, err.Error())
		return InitCheckoutResponse{}, err
	}
	if err != nil {
		_ = s.repo.MarkCheckoutLeadResult(ctx, idempotencyKey, nil, "FAILED", upiTxnID, err.Error())
		return InitCheckoutResponse{}, fmt.Errorf("svc_checkout_exhausted_barcode_retries: %w", err)
	}
	leadStatus := "PENDING"
	if resp.Status == db.BookingStatusCONFIRMED {
		leadStatus = "CONFIRMED"
	}
	_ = s.repo.MarkCheckoutLeadResult(ctx, idempotencyKey, &resp.BookingID, leadStatus, resp.UPITransactionID, "")
	if isGroup && s.seating != nil {
		s.seating.BroadcastLatest(context.Background(), req.EventID)
	}
	if s.adminHub != nil {
		s.adminHub.Broadcast(map[string]any{
			"type":         "BOOKING_CREATED",
			"booking_id":   resp.BookingID,
			"event_id":     req.EventID,
			"total_amount": resp.TotalAmount,
			"primary_name": resp.PrimaryGuestName,
		})
	}

	if waMsg != nil && s.notifier != nil {
		s.notifier.EnqueueTicketConfirmation(*waMsg)
	}
	return resp, nil
}

func bookingFromLockedRow(row db.GetBookingByIDForUpdateRow) db.Booking {
	return db.Booking{
		ID: row.ID, EventID: row.EventID, CategoryID: row.CategoryID,
		ContactPhone: row.ContactPhone, ContactEmail: row.ContactEmail,
		TotalAmount: row.TotalAmount, UpiTransactionID: row.UpiTransactionID,
		Status: row.Status, VerifiedBy: row.VerifiedBy, VerifiedAt: row.VerifiedAt,
		RejectionReason: row.RejectionReason, IdempotencyKey: row.IdempotencyKey,
		CreatedAt: row.CreatedAt,
	}
}

func bookingFromSubmitRow(row db.SubmitBookingUPITransactionIDRow) db.Booking {
	return db.Booking{
		ID: row.ID, EventID: row.EventID, CategoryID: row.CategoryID,
		ContactPhone: row.ContactPhone, ContactEmail: row.ContactEmail,
		TotalAmount: row.TotalAmount, UpiTransactionID: row.UpiTransactionID,
		Status: row.Status, VerifiedBy: row.VerifiedBy, VerifiedAt: row.VerifiedAt,
		RejectionReason: row.RejectionReason, IdempotencyKey: row.IdempotencyKey,
		CreatedAt: row.CreatedAt,
	}
}

// SubmitUPITransactionID records the patron's UPI UTR/transaction ID on a pending booking for admin verification.
func (s *TicketingService) SubmitUPITransactionID(ctx context.Context, bookingID uuid.UUID, contactPhone, upiTransactionID string) (db.Booking, error) {
	txnID, err := auth.NormalizeUPIUTR(upiTransactionID)
	if err != nil {
		return db.Booking{}, httpx.NewBadRequest(err.Error(), nil)
	}

	phone := ""
	if strings.TrimSpace(contactPhone) != "" {
		phone, err = auth.NormalizeIndianMobile(contactPhone)
		if err != nil {
			return db.Booking{}, httpx.NewBadRequest(err.Error(), nil)
		}
	}
	var booking db.Booking
	err = s.repo.WithTx(ctx, func(txCtx context.Context, q db.Querier) error {
		existing, err := q.GetBookingByIDForUpdate(txCtx, bookingID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewNotFound("booking not found")
			}
			return fmt.Errorf("svc_submit_upi_lookup: %w", err)
		}

		if existing.Status == db.BookingStatusCONFIRMED {
			booking = bookingFromLockedRow(existing)
			return nil
		}
		if existing.Status != db.BookingStatusPENDING {
			if existing.RejectionReason.Valid && existing.RejectionReason.String != "" {
				return httpx.NewConflict(fmt.Sprintf("booking was rejected: %s", existing.RejectionReason.String), nil)
			}
			return httpx.NewConflict("booking is no longer pending verification", nil)
		}
		if existing.UpiTransactionID.Valid && existing.UpiTransactionID.String == txnID {
			booking = bookingFromLockedRow(existing)
			return nil
		}

		submitPhone := existing.ContactPhone
		if submitPhone == "" {
			submitPhone = phone
		}
		row, err := q.SubmitBookingUPITransactionID(txCtx, db.SubmitBookingUPITransactionIDParams{
			ID:           bookingID,
			ContactPhone: submitPhone,
			UpiTransactionID: pgtype.Text{
				String: txnID,
				Valid:  true,
			},
		})
		if err != nil {
			if isUPITxnUniqueViolation(err) {
				return httpx.NewConflict("this UTR has already been used for another booking", err)
			}
			return fmt.Errorf("svc_submit_upi_txn: %w", err)
		}
		booking = bookingFromSubmitRow(row)
		return nil
	})
	if err == nil {
		_ = s.repo.MarkCheckoutLeadUTR(ctx, bookingID, txnID)
	}
	if err != nil {
		return db.Booking{}, err
	}

	if s.adminHub != nil {
		s.adminHub.Broadcast(map[string]any{
			"type":       "UTR_SUBMITTED",
			"booking_id": bookingID,
			"upi_txn_id": txnID,
			"phone":      booking.ContactPhone,
		})
	}
	return booking, nil
}

// --- Public Ticket Pass Lookup & PNG Ticket Image Rendering ---

type GuestView struct {
	ID               uuid.UUID     `json:"id"`
	Name             string        `json:"name"`
	Age              int32         `json:"age"`
	Gender           db.GenderType `json:"gender"`
	IsPrimaryContact bool          `json:"is_primary_contact"`
	FreeShotRedeemed bool          `json:"free_shot_redeemed"`
}

type PublicGuestView struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	AgeBand          string    `json:"age_band"`
	Gender           string    `json:"gender"`
	IsPrimaryContact bool      `json:"is_primary_contact"`
	FreeShotRedeemed bool      `json:"free_shot_redeemed"`
}

type TicketCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type TicketSearchPage struct {
	Tickets    []PublicTicketView
	NextCursor TicketCursor
	HasMore    bool
}

type PublicTicketView struct {
	ID                  uuid.UUID         `json:"id"`
	BookingID           uuid.UUID         `json:"booking_id"`
	EventID             uuid.UUID         `json:"event_id"`
	BookingStatus       db.BookingStatus  `json:"booking_status"`
	EventTitle          string            `json:"event_title"`
	EventStartsAt       time.Time         `json:"event_starts_at"`
	EventEndsAt         time.Time         `json:"event_ends_at"`
	EventLocation       string            `json:"event_location"`
	CategoryName        string            `json:"category_name"`
	CategoryTier        string            `json:"category_tier,omitempty"`
	EntryCount          int32             `json:"entry_count"`
	AttendeeCount       int32             `json:"attendee_count"`
	PrimaryGuestName    string            `json:"primary_guest_name,omitempty"`
	TotalAmount         int32             `json:"total_amount"`
	BarCode             string            `json:"bar_code,omitempty"` // 5-char 1D linear bartender barcode (multi-use until event end)
	BarCodeFormat       string            `json:"bar_code_format,omitempty"`
	GateQRToken         string            `json:"gate_qr_token,omitempty"` // Single-use gate QR (immediately expires & hidden once scanned)
	PaymentQRData       string            `json:"payment_qr_data,omitempty"`
	TicketImageURL      string            `json:"ticket_image_url,omitempty"`
	FoodBalance         int32             `json:"food_balance"`
	InitialFoodBalance  int32             `json:"initial_food_balance"`
	FnBExpired          bool              `json:"fnb_expired"`
	IsCheckedIn         bool              `json:"is_checked_in"`
	CheckedInAt         *time.Time        `json:"checked_in_at,omitempty"`
	Guests              []PublicGuestView `json:"guests"`
	ContactPhone        string            `json:"contact_phone,omitempty"`
	UPITransactionID    string            `json:"upi_transaction_id,omitempty"`
	Section             string            `json:"section,omitempty"`
	SeatTableSize       int32             `json:"seat_table_size,omitempty"`
	SeatTableNumber     int32             `json:"seat_table_number,omitempty"`
	CoupleCount         int32             `json:"couple_count"`
	StagMaleCount       int32             `json:"stag_male_count"`
	StagFemaleCount     int32             `json:"stag_female_count"`
	IsWhatsappSent      bool              `json:"is_whatsapp_sent"`
	FreeTequilaEligible int32             `json:"free_tequila_eligible_count"`
	FreeTequilaRedeemed int32             `json:"free_tequila_redeemed_count"`
}

func normalizeTicketLookupQuery(raw string) string {
	q := strings.TrimSpace(raw)
	if q == "" {
		return ""
	}
	if m := UUID_RE.FindString(q); m != "" {
		return m
	}
	phone := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, q)
	if len(phone) == 10 {
		return "+91" + phone
	}
	if len(phone) == 12 && strings.HasPrefix(phone, "91") {
		return "+" + phone
	}
	if len(phone) == 13 && strings.HasPrefix(phone, "091") {
		return "+" + phone[1:]
	}
	return q
}

var UUID_RE = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func (s *TicketingService) LookupPublicTicket(ctx context.Context, raw string) (PublicTicketView, error) {
	query := normalizeTicketLookupQuery(raw)
	if query == "" {
		return PublicTicketView{}, httpx.NewBadRequest("ticket id, mobile number, or UTR is required", nil)
	}
	if id, err := uuid.Parse(query); err == nil {
		return s.GetTicketByUUID(ctx, id)
	}
	var ids []uuid.UUID
	if err := s.repo.Read(ctx, func(readCtx context.Context, q db.Querier) error {
		var readErr error
		ids, readErr = q.LookupPublicTickets(readCtx, query)
		return readErr
	}); err != nil {
		return PublicTicketView{}, fmt.Errorf("svc_lookup_public_ticket: %w", err)
	}
	if len(ids) == 0 {
		return PublicTicketView{}, httpx.NewNotFound("ticket not found")
	}
	return s.GetTicketByUUID(ctx, ids[0])
}

func (s *TicketingService) GetTicketByUUID(ctx context.Context, ticketID uuid.UUID) (PublicTicketView, error) {
	details, err := s.repo.GetTicketDetailsByID(ctx, ticketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicTicketView{}, httpx.NewNotFound("ticket not found")
		}
		return PublicTicketView{}, fmt.Errorf("svc_get_ticket_by_uuid: %w", err)
	}

	guests, err := s.repo.ListGuestsByTicketID(ctx, ticketID)
	if err != nil {
		return PublicTicketView{}, fmt.Errorf("svc_get_ticket_guests: %w", err)
	}

	var bookingDetails db.GetTicketBookingDetailsRow
	if err := s.repo.Read(ctx, func(readCtx context.Context, q db.Querier) error {
		var readErr error
		bookingDetails, readErr = q.GetTicketBookingDetails(readCtx, ticketID)
		return readErr
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return PublicTicketView{}, fmt.Errorf("svc_get_ticket_booking_details: %w", err)
		}
	}

	guestViews := make([]PublicGuestView, 0, len(guests))
	for _, g := range guests {
		guestAge := int32(0)
		if g.Age.Valid {
			guestAge = g.Age.Int32
		}
		guestViews = append(guestViews, PublicGuestView{
			ID:               g.ID,
			Name:             g.Name,
			AgeBand:          maskGuestAge(guestAge),
			Gender:           "REDACTED",
			IsPrimaryContact: g.IsPrimaryContact,
			FreeShotRedeemed: g.FreeShotRedeemed,
		})
	}

	primaryName := PrimaryGuestName(guests)
	now := time.Now().UTC()
	view := PublicTicketView{
		ID:                  details.ID,
		BookingID:           details.BookingID,
		EventID:             details.EventID,
		BookingStatus:       details.BookingStatus,
		EventTitle:          details.EventTitle,
		EventStartsAt:       details.EventStartsAt,
		EventEndsAt:         details.EventEndsAt,
		EventLocation:       details.EventLocation,
		CategoryName:        details.CategoryName,
		EntryCount:          details.EntryCount,
		AttendeeCount:       details.EntryCount,
		PrimaryGuestName:    primaryName,
		TotalAmount:         details.TotalAmount,
		FoodBalance:         details.FoodBalance,
		InitialFoodBalance:  details.InitialFoodBalance,
		FnBExpired:          now.After(details.EventEndsAt),
		IsCheckedIn:         details.IsCheckedIn,
		Guests:              guestViews,
		ContactPhone:        bookingDetails.ContactPhone,
		Section:             bookingDetails.Section,
		SeatTableSize:       bookingDetails.SeatTableSize,
		SeatTableNumber:     bookingDetails.SeatTableNumber,
		CoupleCount:         bookingDetails.CoupleCount,
		StagMaleCount:       bookingDetails.StagMaleCount,
		StagFemaleCount:     bookingDetails.StagFemaleCount,
		IsWhatsappSent:      bookingDetails.IsWhatsappSent,
		FreeTequilaEligible: bookingDetails.FreeTequilaEligibleCount,
		FreeTequilaRedeemed: bookingDetails.FreeTequilaRedeemedCount,
	}
	if bookingDetails.StagFemaleCount > 0 && view.FreeTequilaEligible > bookingDetails.StagFemaleCount {
		view.FreeTequilaEligible = bookingDetails.StagFemaleCount
		if view.FreeTequilaRedeemed > view.FreeTequilaEligible {
			view.FreeTequilaRedeemed = view.FreeTequilaEligible
		}
	}
	if details.CategoryTier.Valid {
		view.CategoryTier = details.CategoryTier.String
	}
	if bookingDetails.UpiTransactionID.Valid {
		view.UPITransactionID = bookingDetails.UpiTransactionID.String
	}
	if details.CheckedInAt.Valid {
		t := details.CheckedInAt.Time
		view.CheckedInAt = &t
	}

	if details.BookingStatus == db.BookingStatusPENDING && details.TotalAmount > 0 {
		note := fmt.Sprintf("Ticket %s", details.BookingID.String()[:8])
		view.PaymentQRData = auth.BuildUPIIntentURI(s.cfg.UPIVPA, s.cfg.UPIMerchantName, note, details.TotalAmount)
	}

	// Only expose barcodes & ticket image once Admin has verified payment (CONFIRMED).
	// Gate QR is omitted immediately after gate check-in; 1D bartender barcode remains until event end.
	if details.BookingStatus == db.BookingStatusCONFIRMED {
		view.BarCode = details.BarCode
		view.BarCodeFormat = "CODE39_1D"
		view.TicketImageURL = fmt.Sprintf("%s/api/tickets/%s/image", s.publicAPIBaseURL(), details.ID.String())
		if !details.IsCheckedIn {
			qrExp := details.EventEndsAt.Add(s.cfg.TicketQRTTL)
			view.GateQRToken = s.qrSigner.SignToken(details.ID, details.EventID, now, qrExp)
		}
	}

	return view, nil
}

func (s *TicketingService) MarkBookingWhatsAppSent(ctx context.Context, ticketID uuid.UUID) (PublicTicketView, error) {
	var bookingID uuid.UUID
	if err := s.repo.Read(ctx, func(readCtx context.Context, q db.Querier) error {
		var readErr error
		bookingID, readErr = q.GetBookingIDByTicketID(readCtx, ticketID)
		return readErr
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicTicketView{}, httpx.NewNotFound("ticket not found")
		}
		return PublicTicketView{}, fmt.Errorf("svc_mark_wa_sent_lookup: %w", err)
	}

	if err := s.repo.WithTx(ctx, func(txCtx context.Context, q db.Querier) error {
		_, err := q.MarkBookingWhatsAppSent(txCtx, bookingID)
		return err
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicTicketView{}, httpx.NewNotFound("booking not found")
		}
		return PublicTicketView{}, fmt.Errorf("svc_mark_wa_sent: %w", err)
	}
	return s.GetTicketByUUID(ctx, ticketID)
}

func (s *TicketingService) SearchTickets(ctx context.Context, query string, waPending bool, limit int32, cursor TicketCursor) (TicketSearchPage, error) {
	query = strings.TrimSpace(query)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	if cursor.ID == uuid.Nil {
		cursor = TicketCursor{}
	}

	ids := make([]uuid.UUID, 0, limit+1)
	pageCursors := make([]TicketCursor, 0, limit+1)
	if waPending {
		var rows []db.SearchPendingWhatsAppTicketsRow
		err := s.repo.Read(ctx, func(readCtx context.Context, q db.Querier) error {
			var readErr error
			rows, readErr = q.SearchPendingWhatsAppTickets(readCtx, db.SearchPendingWhatsAppTicketsParams{
				SearchQuery:     query,
				CursorCreatedAt: cursor.CreatedAt,
				CursorID:        cursor.ID,
				PageLimit:       limit + 1,
			})
			return readErr
		})
		if err != nil {
			return TicketSearchPage{}, fmt.Errorf("svc_search_wa_pending_tickets: %w", err)
		}
		for _, row := range rows {
			ids = append(ids, row.ID)
			pageCursors = append(pageCursors, TicketCursor{CreatedAt: row.CursorCreatedAt, ID: row.CursorID})
		}
	} else {
		rows, err := s.repo.SearchConfirmedTickets(ctx, db.SearchConfirmedTicketsParams{
			SearchQuery:     query,
			CursorCreatedAt: cursor.CreatedAt,
			CursorID:        cursor.ID,
			PageLimit:       limit + 1,
		})
		if err != nil {
			return TicketSearchPage{}, fmt.Errorf("svc_search_tickets: %w", err)
		}
		for _, row := range rows {
			ids = append(ids, row.ID)
			pageCursors = append(pageCursors, TicketCursor{CreatedAt: row.CursorCreatedAt, ID: row.CursorID})
		}
	}

	hasMore := int32(len(ids)) > limit
	if hasMore {
		ids = ids[:limit]
		pageCursors = pageCursors[:limit]
	}
	views := make([]PublicTicketView, 0, len(ids))
	for _, id := range ids {
		view, err := s.GetTicketByUUID(ctx, id)
		if err != nil {
			return TicketSearchPage{}, fmt.Errorf("svc_search_ticket_detail: %w", err)
		}
		views = append(views, view)
	}
	page := TicketSearchPage{Tickets: views, HasMore: hasMore}
	if hasMore && len(pageCursors) > 0 {
		page.NextCursor = pageCursors[len(pageCursors)-1]
	}
	return page, nil
}

func (s *TicketingService) RenderTicketImagePNG(ctx context.Context, ticketID uuid.UUID) ([]byte, error) {
	details, err := s.repo.GetTicketDetailsByID(ctx, ticketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NewNotFound("ticket not found")
		}
		return nil, fmt.Errorf("svc_render_ticket_img_lookup: %w", err)
	}
	if details.BookingStatus != db.BookingStatusCONFIRMED {
		return nil, httpx.NewConflict("ticket pass image is available only after payment verification", nil)
	}

	guests, err := s.repo.ListGuestsByTicketID(ctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("svc_render_ticket_img_guests: %w", err)
	}
	primaryName := PrimaryGuestName(guests)

	return ticketimg.RenderTicketPNG(ticketimg.TicketImageData{
		TicketID:      details.ID.String(),
		EventTitle:    details.EventTitle,
		EventLocation: details.EventLocation,
		EventStartsAt: details.EventStartsAt,
		CategoryName:  details.CategoryName,
		PrimaryGuest:  primaryName,
		EntryCount:    details.EntryCount,
		FoodBalance:   details.FoodBalance,
		BarCode:       details.BarCode,
		GateCheckedIn: details.IsCheckedIn,
	})
}

func isBarCodeUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return strings.Contains(pgErr.ConstraintName, "bar_code")
	}
	return false
}

func isUPITxnUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return strings.Contains(pgErr.ConstraintName, "upi_txn") || strings.Contains(pgErr.ConstraintName, "upi_transaction_id")
	}
	return false
}

func isCheckoutIdempotencyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return strings.Contains(pgErr.ConstraintName, "idempotency")
	}
	return false
}

func maskGuestAge(age int32) string {
	if age <= 0 {
		return "UNKNOWN"
	}
	lower := (age / 5) * 5
	return fmt.Sprintf("%d-%d", lower, lower+4)
}

type CreateHelpRequest struct {
	Name    string     `json:"name"`
	Phone   string     `json:"phone"`
	Message string     `json:"message"`
	EventID *uuid.UUID `json:"event_id,omitempty"`
}

type CreateHelpResponse struct {
	ID        uuid.UUID `json:"id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *TicketingService) SubmitHelpMessage(ctx context.Context, req CreateHelpRequest) (CreateHelpResponse, error) {
	name := strings.TrimSpace(req.Name)
	msg := strings.TrimSpace(req.Message)
	if name == "" || len(name) > 255 {
		return CreateHelpResponse{}, httpx.NewBadRequest("name is required (max 255 chars)", nil)
	}
	phone, err := auth.NormalizeIndianMobile(req.Phone)
	if err != nil {
		return CreateHelpResponse{}, httpx.NewBadRequest(err.Error(), nil)
	}
	if msg == "" || len(msg) > 1000 {
		return CreateHelpResponse{}, httpx.NewBadRequest("message is required (max 1000 chars)", nil)
	}
	var evUUID pgtype.UUID
	if req.EventID != nil {
		evUUID = pgtype.UUID{Bytes: *req.EventID, Valid: true}
	}
	res, err := s.repo.CreateHelpMessage(ctx, db.CreateHelpMessageParams{
		Name:    name,
		Phone:   phone,
		Message: msg,
		EventID: evUUID,
	})
	if err != nil {
		return CreateHelpResponse{}, fmt.Errorf("svc_submit_help_message: %w", err)
	}
	if s.adminHub != nil {
		s.adminHub.Broadcast(map[string]any{
			"type":    "HELP_SUBMITTED",
			"help_id": res.ID,
			"name":    name,
			"phone":   phone,
		})
	}
	return CreateHelpResponse{
		ID:        res.ID,
		Message:   "Help request submitted successfully. We will reach out to you shortly.",
		CreatedAt: res.CreatedAt,
	}, nil
}
