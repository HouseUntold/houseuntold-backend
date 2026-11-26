package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/repositories"
)

type GateService struct {
	repo     *repositories.TicketingRepository
	qrSigner *auth.QRSigner
	logger   *slog.Logger
}

func NewGateService(repo *repositories.TicketingRepository, qrSigner *auth.QRSigner, logger *slog.Logger) *GateService {
	if logger == nil {
		logger = slog.Default()
	}
	return &GateService{
		repo:     repo,
		qrSigner: qrSigner,
		logger:   logger,
	}
}

type GateTicketView struct {
	Valid            bool             `json:"valid"`
	Reason           string           `json:"reason,omitempty"`
	TicketID         uuid.UUID        `json:"ticket_id"`
	EventID          uuid.UUID        `json:"event_id"`
	EventTitle       string           `json:"event_title"`
	EventStartsAt    time.Time        `json:"event_starts_at"`
	EventEndsAt      time.Time        `json:"event_ends_at"`
	EventLocation    string           `json:"event_location"`
	CategoryName     string           `json:"category_name"`
	CategoryTier     string           `json:"category_tier,omitempty"`
	EntryCount       int32            `json:"entry_count"`
	BookingStatus    db.BookingStatus `json:"booking_status"`
	IsCheckedIn      bool             `json:"is_checked_in"`
	CheckedInAt      *time.Time       `json:"checked_in_at,omitempty"`
	PrimaryGuestName string           `json:"primary_guest_name"`
	Guests           []GuestView      `json:"guests"`
}

// ValidateQR verifies the HMAC-SHA256 signature and expiry, returning admission details without mutating DB state.
func (s *GateService) ValidateQR(ctx context.Context, qrToken string) (GateTicketView, error) {
	now := time.Now().UTC()
	row, err := s.verifyAndLoadTicket(ctx, qrToken, now)
	if err != nil {
		return GateTicketView{}, err
	}

	guests, err := s.repo.ListGuestsByTicketID(ctx, row.ID)
	if err != nil {
		return GateTicketView{}, fmt.Errorf("svc_gate_list_guests: %w", err)
	}

	return buildGateTicketView(row, guests, now), nil
}

// CheckInTicket atomically marks a confirmed ticket as checked in, expiring the gate QR immediately.
func (s *GateService) CheckInTicket(ctx context.Context, qrToken string, gatekeeperID uuid.UUID) (GateTicketView, error) {
	now := time.Now().UTC()
	details, err := s.verifyAndLoadTicket(ctx, qrToken, now)
	if err != nil {
		return GateTicketView{}, err
	}
	if details.BookingStatus != db.BookingStatusCONFIRMED {
		return GateTicketView{}, httpx.NewConflict("booking payment has not been verified by admin", nil)
	}
	if now.After(details.EventEndsAt) {
		return GateTicketView{}, httpx.NewConflict("event has already ended", nil)
	}

	updated, err := s.repo.CheckInTicketAtomic(ctx, details.QrTokenDigest, gatekeeperID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GateTicketView{}, httpx.NewConflict("gate QR already scanned and expired", nil)
		}
		return GateTicketView{}, fmt.Errorf("svc_gate_checkin_atomic: %w", err)
	}

	guests, err := s.repo.ListGuestsByTicketID(ctx, details.ID)
	if err != nil {
		return GateTicketView{}, fmt.Errorf("svc_gate_checkin_guests: %w", err)
	}

	details.IsCheckedIn = updated.IsCheckedIn
	details.CheckedInAt = updated.CheckedInAt

	view := buildGateTicketView(details, guests, now)
	view.Valid = true
	view.Reason = "checked_in_gate_qr_expired"
	return view, nil
}

func (s *GateService) verifyAndLoadTicket(ctx context.Context, qrToken string, now time.Time) (db.GetTicketDetailsByQRDigestRow, error) {
	if strings.TrimSpace(qrToken) == "" {
		return db.GetTicketDetailsByQRDigestRow{}, httpx.NewBadRequest("qr_token is required", nil)
	}

	payload, err := s.qrSigner.VerifyToken(qrToken, now)
	if err != nil {
		if errors.Is(err, auth.ErrExpiredQRToken) {
			return db.GetTicketDetailsByQRDigestRow{}, httpx.NewBadRequest("gate QR code has expired", err)
		}
		return db.GetTicketDetailsByQRDigestRow{}, httpx.NewUnauthorized("invalid or forged gate QR code", err)
	}

	digest := s.qrSigner.ComputeDigest(payload.TicketID)
	row, err := s.repo.GetTicketDetailsByQRDigest(ctx, digest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.GetTicketDetailsByQRDigestRow{}, httpx.NewNotFound("ticket not found")
		}
		return db.GetTicketDetailsByQRDigestRow{}, fmt.Errorf("svc_gate_lookup: %w", err)
	}
	if row.ID != payload.TicketID || row.EventID != payload.EventID {
		return db.GetTicketDetailsByQRDigestRow{}, httpx.NewUnauthorized("ticket QR payload mismatch", nil)
	}
	return row, nil
}

func buildGateTicketView(row db.GetTicketDetailsByQRDigestRow, guests []db.TicketGuest, now time.Time) GateTicketView {
	guestViews := make([]GuestView, 0, len(guests))
	for _, g := range guests {
		guestViews = append(guestViews, GuestView{
			ID:               g.ID,
			Name:             g.Name,
			Age:              g.Age.Int32,
			Gender:           g.Gender,
			IsPrimaryContact: g.IsPrimaryContact,
			FreeShotRedeemed: g.FreeShotRedeemed,
		})
	}

	valid := true
	reason := "ready_for_entry"

	if row.BookingStatus != db.BookingStatusCONFIRMED {
		valid = false
		reason = "booking_unconfirmed"
	} else if row.IsCheckedIn {
		valid = false
		reason = "already_checked_in"
	} else if now.After(row.EventEndsAt) {
		valid = false
		reason = "event_ended"
	}

	view := GateTicketView{
		Valid:            valid,
		Reason:           reason,
		TicketID:         row.ID,
		EventID:          row.EventID,
		EventTitle:       row.EventTitle,
		EventStartsAt:    row.EventStartsAt,
		EventEndsAt:      row.EventEndsAt,
		EventLocation:    row.EventLocation,
		CategoryName:     row.CategoryName,
		EntryCount:       row.EntryCount,
		BookingStatus:    row.BookingStatus,
		IsCheckedIn:      row.IsCheckedIn,
		PrimaryGuestName: PrimaryGuestName(guests),
		Guests:           guestViews,
	}
	if row.CategoryTier.Valid {
		view.CategoryTier = row.CategoryTier.String
	}
	if row.CheckedInAt.Valid {
		t := row.CheckedInAt.Time
		view.CheckedInAt = &t
	}
	return view
}
