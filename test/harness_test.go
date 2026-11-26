package test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/cache"
	"github.com/venu-live/venu-api/internal/config"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/handlers"
	"github.com/venu-live/venu-api/internal/middleware"
	"github.com/venu-live/venu-api/internal/ratelimit"
	"github.com/venu-live/venu-api/internal/realtime"
	"github.com/venu-live/venu-api/internal/repositories"
	"github.com/venu-live/venu-api/internal/server"
	"github.com/venu-live/venu-api/internal/services"
	"github.com/venu-live/venu-api/internal/services/seating"
	"github.com/venu-live/venu-api/internal/whatsapp"
)

// recordingNotifier captures WhatsApp ticket confirmations dispatched during checkout/admin approval.
type recordingNotifier struct {
	mu   sync.Mutex
	msgs []whatsapp.TicketPassMessage
}

func (n *recordingNotifier) EnqueueTicketConfirmation(msg whatsapp.TicketPassMessage) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, msg)
}

func (n *recordingNotifier) CloseAndWait(context.Context) error { return nil }

func (n *recordingNotifier) Messages() []whatsapp.TicketPassMessage {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]whatsapp.TicketPassMessage, len(n.msgs))
	copy(out, n.msgs)
	return out
}

// memStore implements db.Querier in memory with PostgreSQL-faithful uniqueness and atomic update semantics.
type memStore struct {
	mu         sync.Mutex
	txMu       sync.Mutex
	pingErr    error
	staff      map[uuid.UUID]db.StaffAccount
	events     map[uuid.UUID]db.Event
	categories map[uuid.UUID]db.TicketCategory
	bookings   map[uuid.UUID]db.Booking
	tickets    map[uuid.UUID]db.Ticket
	guests     map[uuid.UUID]db.TicketGuest
	menuItems  map[uuid.UUID]db.MenuItem
	barTxs     map[uuid.UUID]db.BarTransaction
	barTxOrder []uuid.UUID
	lineItems  []db.TransactionLineItem
	rateLimits map[string]int32

	seatInventory    map[string]db.EventSeatingInventory
	seatReservations map[uuid.UUID]db.EventSeatReservation
	auditLogs        []db.ApiAuditLog
	helpMessages     map[uuid.UUID]db.HelpMessage
	artists          map[uuid.UUID]db.EventArtist
}

var _ db.Querier = (*memStore)(nil)

func newMemStore() *memStore {
	return &memStore{
		staff:      make(map[uuid.UUID]db.StaffAccount),
		events:     make(map[uuid.UUID]db.Event),
		categories: make(map[uuid.UUID]db.TicketCategory),
		bookings:   make(map[uuid.UUID]db.Booking),
		tickets:    make(map[uuid.UUID]db.Ticket),
		guests:     make(map[uuid.UUID]db.TicketGuest),
		menuItems:  make(map[uuid.UUID]db.MenuItem),
		barTxs:     make(map[uuid.UUID]db.BarTransaction),
		rateLimits: make(map[string]int32),

		seatInventory:    make(map[string]db.EventSeatingInventory),
		seatReservations: make(map[uuid.UUID]db.EventSeatReservation),
		helpMessages:     make(map[uuid.UUID]db.HelpMessage),
		artists:          make(map[uuid.UUID]db.EventArtist),
	}
}

func (s *memStore) withTx(ctx context.Context, fn func(context.Context, db.Querier) error) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	return fn(ctx, s)
}

func (s *memStore) ping(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pingErr
}

func (s *memStore) setPingErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pingErr = err
}

func pgUniqueErr(constraint string) error {
	return &pgconn.PgError{Code: "23505", ConstraintName: constraint}
}

func seatKey(eventID uuid.UUID, section string, tableSize int32) string {
	return fmt.Sprintf("%s|%s|%d", eventID, section, tableSize)
}

// --- Staff Queries ---

func (s *memStore) GetStaffByUsername(_ context.Context, username string) (db.GetStaffByUsernameRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.staff {
		if st.Username == username {
			return getStaffByUsernameRow(st), nil
		}
	}
	return db.GetStaffByUsernameRow{}, pgx.ErrNoRows
}

func (s *memStore) GetStaffByID(_ context.Context, id uuid.UUID) (db.GetStaffByIDRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.staff[id]
	if !ok {
		return db.GetStaffByIDRow{}, pgx.ErrNoRows
	}
	return getStaffByIDRow(st), nil
}

func (s *memStore) ListStaff(_ context.Context) ([]db.ListStaffRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]db.ListStaffRow, 0, len(s.staff))
	for _, st := range s.staff {
		rows = append(rows, db.ListStaffRow{
			ID:           st.ID,
			Username:     st.Username,
			Role:         st.Role,
			IsActive:     st.IsActive,
			TokenVersion: st.TokenVersion,
			CreatedAt:    st.CreatedAt,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	return rows, nil
}

func (s *memStore) ListCheckoutLeads(_ context.Context, _ db.ListCheckoutLeadsParams) ([]db.ListCheckoutLeadsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []db.ListCheckoutLeadsRow{}, nil
}

func (s *memStore) LookupPublicTickets(_ context.Context, _ string) ([]uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []uuid.UUID{}, nil
}

func (s *memStore) MarkCheckoutLeadResult(_ context.Context, _ db.MarkCheckoutLeadResultParams) error {
	return nil
}

func (s *memStore) MarkCheckoutLeadUTR(_ context.Context, _ db.MarkCheckoutLeadUTRParams) error {
	return nil
}

func (s *memStore) UpsertCheckoutLeadAttempt(_ context.Context, _ db.UpsertCheckoutLeadAttemptParams) error {
	return nil
}

func (s *memStore) CreateStaff(_ context.Context, arg db.CreateStaffParams) (db.CreateStaffRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.staff {
		if st.Username == arg.Username {
			return db.CreateStaffRow{}, pgUniqueErr("staff_accounts_username_key")
		}
	}
	st := db.StaffAccount{
		ID:           uuid.New(),
		Username:     arg.Username,
		PasswordHash: arg.PasswordHash,
		Role:         arg.Role,
		IsActive:     true,
		CreatedAt:    time.Now().UTC(),
	}
	s.staff[st.ID] = st
	return createStaffRow(st), nil
}

func getStaffByUsernameRow(st db.StaffAccount) db.GetStaffByUsernameRow {
	return db.GetStaffByUsernameRow{
		ID:           st.ID,
		Username:     st.Username,
		PasswordHash: st.PasswordHash,
		Role:         st.Role,
		IsActive:     st.IsActive,
		TokenVersion: st.TokenVersion,
		CreatedAt:    st.CreatedAt,
	}
}

func getStaffByIDRow(st db.StaffAccount) db.GetStaffByIDRow {
	return db.GetStaffByIDRow{
		ID:           st.ID,
		Username:     st.Username,
		PasswordHash: st.PasswordHash,
		Role:         st.Role,
		IsActive:     st.IsActive,
		TokenVersion: st.TokenVersion,
		CreatedAt:    st.CreatedAt,
	}
}

func createStaffRow(st db.StaffAccount) db.CreateStaffRow {
	return db.CreateStaffRow{
		ID:           st.ID,
		Username:     st.Username,
		PasswordHash: st.PasswordHash,
		Role:         st.Role,
		IsActive:     st.IsActive,
		TokenVersion: st.TokenVersion,
		CreatedAt:    st.CreatedAt,
	}
}

func (s *memStore) UpdateStaffPassword(_ context.Context, arg db.UpdateStaffPasswordParams) (db.UpdateStaffPasswordRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.staff[arg.ID]
	if !ok || !st.IsActive {
		return db.UpdateStaffPasswordRow{}, pgx.ErrNoRows
	}
	st.PasswordHash = arg.PasswordHash
	st.TokenVersion++
	s.staff[st.ID] = st
	return db.UpdateStaffPasswordRow{
		ID:           st.ID,
		Username:     st.Username,
		PasswordHash: st.PasswordHash,
		Role:         st.Role,
		IsActive:     st.IsActive,
		TokenVersion: st.TokenVersion,
		CreatedAt:    st.CreatedAt,
	}, nil
}

func (s *memStore) CountActiveAdmins(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, st := range s.staff {
		if st.Role == db.StaffRoleADMIN && st.IsActive {
			count++
		}
	}
	return count, nil
}

func (s *memStore) DeactivateStaff(_ context.Context, id uuid.UUID) (db.DeactivateStaffRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.staff[id]
	if !ok || !st.IsActive {
		return db.DeactivateStaffRow{}, pgx.ErrNoRows
	}
	st.IsActive = false
	st.TokenVersion++
	s.staff[st.ID] = st
	return db.DeactivateStaffRow{
		ID:           st.ID,
		Username:     st.Username,
		PasswordHash: st.PasswordHash,
		Role:         st.Role,
		IsActive:     st.IsActive,
		TokenVersion: st.TokenVersion,
		CreatedAt:    st.CreatedAt,
	}, nil
}

func (s *memStore) IncrementStaffTokenVersion(_ context.Context, id uuid.UUID) (db.IncrementStaffTokenVersionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.staff[id]
	if !ok || !st.IsActive {
		return db.IncrementStaffTokenVersionRow{}, pgx.ErrNoRows
	}
	st.TokenVersion++
	s.staff[id] = st
	return incrementStaffTokenVersionRow(st), nil
}

func (s *memStore) RotateStaffTokenVersion(_ context.Context, arg db.RotateStaffTokenVersionParams) (db.RotateStaffTokenVersionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.staff[arg.ID]
	if !ok || !st.IsActive || st.TokenVersion != arg.TokenVersion {
		return db.RotateStaffTokenVersionRow{}, pgx.ErrNoRows
	}
	st.TokenVersion++
	s.staff[st.ID] = st
	return rotateStaffTokenVersionRow(st), nil
}

func incrementStaffTokenVersionRow(st db.StaffAccount) db.IncrementStaffTokenVersionRow {
	return db.IncrementStaffTokenVersionRow{
		ID:           st.ID,
		Username:     st.Username,
		PasswordHash: st.PasswordHash,
		Role:         st.Role,
		IsActive:     st.IsActive,
		TokenVersion: st.TokenVersion,
		CreatedAt:    st.CreatedAt,
	}
}

func rotateStaffTokenVersionRow(st db.StaffAccount) db.RotateStaffTokenVersionRow {
	return db.RotateStaffTokenVersionRow{
		ID:           st.ID,
		Username:     st.Username,
		PasswordHash: st.PasswordHash,
		Role:         st.Role,
		IsActive:     st.IsActive,
		TokenVersion: st.TokenVersion,
		CreatedAt:    st.CreatedAt,
	}
}

// --- Event & Category Queries ---

func eventDeleted(ev db.Event) bool {
	return ev.DeletedAt.Valid
}

func (s *memStore) GetEventByID(_ context.Context, id uuid.UUID) (db.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev, ok := s.events[id]
	if !ok || eventDeleted(ev) {
		return db.Event{}, pgx.ErrNoRows
	}
	return ev, nil
}

func (s *memStore) GetEventByIDForUpdate(ctx context.Context, id uuid.UUID) (db.Event, error) {
	return s.GetEventByID(ctx, id)
}

func (s *memStore) SoftDeleteEvent(_ context.Context, id uuid.UUID) (db.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev, ok := s.events[id]
	if !ok || eventDeleted(ev) {
		return db.Event{}, pgx.ErrNoRows
	}
	ev.DeletedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	s.events[id] = ev
	return ev, nil
}

func (s *memStore) CreateEvent(_ context.Context, arg db.CreateEventParams) (db.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := db.Event{
		ID:              uuid.New(),
		Title:           arg.Title,
		StartsAt:        arg.StartsAt,
		EndsAt:          arg.EndsAt,
		Location:        arg.Location,
		VenueName:       arg.VenueName,
		LocationArea:    arg.LocationArea,
		TransitInfo:     arg.TransitInfo,
		AdvisoryNotice:  arg.AdvisoryNotice,
		Description:     arg.Description,
		Status:          arg.Status,
		CreatedAt:       time.Now().UTC(),
		IsFirst99Active: true,
	}
	s.events[ev.ID] = ev
	return ev, nil
}

func (s *memStore) ListEventCategories(_ context.Context, eventID uuid.UUID) ([]db.TicketCategory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.TicketCategory, 0)
	for _, cat := range s.categories {
		if cat.EventID == eventID {
			out = append(out, cat)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BasePrice < out[j].BasePrice })
	return out, nil
}

func (s *memStore) GetCategoryByID(_ context.Context, id uuid.UUID) (db.TicketCategory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cat, ok := s.categories[id]
	if !ok {
		return db.TicketCategory{}, pgx.ErrNoRows
	}
	return cat, nil
}

func (s *memStore) CreateTicketCategory(_ context.Context, arg db.CreateTicketCategoryParams) (db.TicketCategory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cat := db.TicketCategory{
		ID:             uuid.New(),
		EventID:        arg.EventID,
		Name:           arg.Name,
		Category:       arg.Category,
		BasePrice:      arg.BasePrice,
		FnbCredit:      arg.FnbCredit,
		EntryCount:     arg.EntryCount,
		IsGenderStrict: arg.IsGenderStrict,
	}
	s.categories[cat.ID] = cat
	return cat, nil
}

func (s *memStore) CountConfirmedFemaleStagByEvent(_ context.Context, eventID uuid.UUID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, ticket := range s.tickets {
		booking, ok := s.bookings[ticket.BookingID]
		if !ok || booking.EventID != eventID || booking.Status != db.BookingStatusCONFIRMED {
			continue
		}
		cat, ok := s.categories[booking.CategoryID]
		if !ok || !strings.EqualFold(cat.Name, "STAG_FEMALE") {
			continue
		}
		for _, guest := range s.guests {
			if guest.TicketID == ticket.ID && guest.Gender == db.GenderTypeFEMALE {
				count++
			}
		}
	}
	return count, nil
}

// --- Booking, Ticket & Guest Queries ---

func (s *memStore) CreateBooking(_ context.Context, arg db.CreateBookingParams) (db.CreateBookingRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if arg.UpiTransactionID.Valid && arg.UpiTransactionID.String != "" {
		for _, b := range s.bookings {
			if b.UpiTransactionID.Valid && b.UpiTransactionID.String == arg.UpiTransactionID.String {
				return db.CreateBookingRow{}, pgUniqueErr("idx_bookings_upi_txn_id")
			}
		}
	}
	if arg.IdempotencyKey.Valid && arg.IdempotencyKey.String != "" {
		for _, b := range s.bookings {
			if b.IdempotencyKey.Valid && b.IdempotencyKey.String == arg.IdempotencyKey.String {
				return db.CreateBookingRow{}, pgUniqueErr("idx_bookings_idempotency_key")
			}
		}
	}
	b := db.Booking{
		ID:               uuid.New(),
		EventID:          arg.EventID,
		CategoryID:       arg.CategoryID,
		ContactPhone:     arg.ContactPhone,
		ContactEmail:     arg.ContactEmail,
		TotalAmount:      arg.TotalAmount,
		UpiTransactionID: arg.UpiTransactionID,
		Status:           arg.Status,
		IdempotencyKey:   arg.IdempotencyKey,
		CreatedAt:        time.Now().UTC(),
	}
	s.bookings[b.ID] = b
	return createBookingRow(b), nil
}

func (s *memStore) SubmitBookingUPITransactionID(_ context.Context, arg db.SubmitBookingUPITransactionIDParams) (db.SubmitBookingUPITransactionIDRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bookings[arg.ID]
	if !ok || b.Status != db.BookingStatusPENDING || b.ContactPhone != arg.ContactPhone {
		return db.SubmitBookingUPITransactionIDRow{}, pgx.ErrNoRows
	}
	for id, other := range s.bookings {
		if id != arg.ID && other.UpiTransactionID.Valid && other.UpiTransactionID.String == arg.UpiTransactionID.String {
			return db.SubmitBookingUPITransactionIDRow{}, pgUniqueErr("idx_bookings_upi_txn_id")
		}
	}
	b.UpiTransactionID = arg.UpiTransactionID
	s.bookings[b.ID] = b
	return submitBookingUPITransactionIDRow(b), nil
}

func createBookingRow(b db.Booking) db.CreateBookingRow {
	return db.CreateBookingRow{
		ID:               b.ID,
		EventID:          b.EventID,
		CategoryID:       b.CategoryID,
		ContactPhone:     b.ContactPhone,
		ContactEmail:     b.ContactEmail,
		TotalAmount:      b.TotalAmount,
		UpiTransactionID: b.UpiTransactionID,
		Status:           b.Status,
		VerifiedBy:       b.VerifiedBy,
		VerifiedAt:       b.VerifiedAt,
		RejectionReason:  b.RejectionReason,
		IdempotencyKey:   b.IdempotencyKey,
		CreatedAt:        b.CreatedAt,
	}
}

func submitBookingUPITransactionIDRow(b db.Booking) db.SubmitBookingUPITransactionIDRow {
	return db.SubmitBookingUPITransactionIDRow{
		ID:               b.ID,
		EventID:          b.EventID,
		CategoryID:       b.CategoryID,
		ContactPhone:     b.ContactPhone,
		ContactEmail:     b.ContactEmail,
		TotalAmount:      b.TotalAmount,
		UpiTransactionID: b.UpiTransactionID,
		Status:           b.Status,
		VerifiedBy:       b.VerifiedBy,
		VerifiedAt:       b.VerifiedAt,
		RejectionReason:  b.RejectionReason,
		IdempotencyKey:   b.IdempotencyKey,
		CreatedAt:        b.CreatedAt,
	}
}

func (s *memStore) GetBookingByIDForUpdate(_ context.Context, id uuid.UUID) (db.GetBookingByIDForUpdateRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bookings[id]
	if !ok {
		return db.GetBookingByIDForUpdateRow{}, pgx.ErrNoRows
	}
	return getBookingByIDForUpdateRow(b), nil
}

func (s *memStore) ApproveBookingAtomic(_ context.Context, arg db.ApproveBookingAtomicParams) (db.ApproveBookingAtomicRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bookings[arg.ID]
	if !ok || b.Status != db.BookingStatusPENDING {
		return db.ApproveBookingAtomicRow{}, pgx.ErrNoRows
	}
	b.Status = db.BookingStatusCONFIRMED
	b.VerifiedBy = arg.VerifiedBy
	b.VerifiedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	b.RejectionReason = pgtype.Text{Valid: false}
	s.bookings[b.ID] = b
	return approveBookingAtomicRow(b), nil
}

func (s *memStore) RejectBookingAtomic(_ context.Context, arg db.RejectBookingAtomicParams) (db.RejectBookingAtomicRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bookings[arg.ID]
	if !ok || b.Status != db.BookingStatusPENDING {
		return db.RejectBookingAtomicRow{}, pgx.ErrNoRows
	}
	b.Status = db.BookingStatusFAILED
	b.VerifiedBy = arg.VerifiedBy
	b.VerifiedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	b.RejectionReason = arg.RejectionReason
	s.bookings[b.ID] = b
	return rejectBookingAtomicRow(b), nil
}

func getBookingByIDForUpdateRow(b db.Booking) db.GetBookingByIDForUpdateRow {
	return db.GetBookingByIDForUpdateRow{
		ID:               b.ID,
		EventID:          b.EventID,
		CategoryID:       b.CategoryID,
		ContactPhone:     b.ContactPhone,
		ContactEmail:     b.ContactEmail,
		TotalAmount:      b.TotalAmount,
		UpiTransactionID: b.UpiTransactionID,
		Status:           b.Status,
		VerifiedBy:       b.VerifiedBy,
		VerifiedAt:       b.VerifiedAt,
		RejectionReason:  b.RejectionReason,
		IdempotencyKey:   b.IdempotencyKey,
		CreatedAt:        b.CreatedAt,
	}
}

func approveBookingAtomicRow(b db.Booking) db.ApproveBookingAtomicRow {
	return db.ApproveBookingAtomicRow{
		ID:               b.ID,
		EventID:          b.EventID,
		CategoryID:       b.CategoryID,
		ContactPhone:     b.ContactPhone,
		ContactEmail:     b.ContactEmail,
		TotalAmount:      b.TotalAmount,
		UpiTransactionID: b.UpiTransactionID,
		Status:           b.Status,
		VerifiedBy:       b.VerifiedBy,
		VerifiedAt:       b.VerifiedAt,
		RejectionReason:  b.RejectionReason,
		IdempotencyKey:   b.IdempotencyKey,
		CreatedAt:        b.CreatedAt,
	}
}

func rejectBookingAtomicRow(b db.Booking) db.RejectBookingAtomicRow {
	return db.RejectBookingAtomicRow{
		ID:               b.ID,
		EventID:          b.EventID,
		CategoryID:       b.CategoryID,
		ContactPhone:     b.ContactPhone,
		ContactEmail:     b.ContactEmail,
		TotalAmount:      b.TotalAmount,
		UpiTransactionID: b.UpiTransactionID,
		Status:           b.Status,
		VerifiedBy:       b.VerifiedBy,
		VerifiedAt:       b.VerifiedAt,
		RejectionReason:  b.RejectionReason,
		IdempotencyKey:   b.IdempotencyKey,
		CreatedAt:        b.CreatedAt,
	}
}

func (s *memStore) ListBookingsForVerification(_ context.Context, arg db.ListBookingsForVerificationParams) ([]db.ListBookingsForVerificationRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.ListBookingsForVerificationRow, 0)
	for _, b := range s.bookings {
		if b.Status != arg.Status {
			continue
		}
		if arg.EventID != uuid.Nil && b.EventID != arg.EventID {
			continue
		}
		cat := s.categories[b.CategoryID]
		ev := s.events[b.EventID]
		if eventDeleted(ev) {
			continue
		}
		var tk db.Ticket
		for _, t := range s.tickets {
			if t.BookingID == b.ID {
				tk = t
				break
			}
		}
		out = append(out, db.ListBookingsForVerificationRow{
			ID:               b.ID,
			EventID:          b.EventID,
			CategoryID:       b.CategoryID,
			ContactPhone:     b.ContactPhone,
			ContactEmail:     b.ContactEmail,
			TotalAmount:      b.TotalAmount,
			UpiTransactionID: b.UpiTransactionID,
			Status:           b.Status,
			VerifiedBy:       b.VerifiedBy,
			VerifiedAt:       b.VerifiedAt,
			RejectionReason:  b.RejectionReason,
			CreatedAt:        b.CreatedAt,
			CategoryName:     cat.Name,
			CategoryTier:     cat.Category,
			TierBasePrice:    cat.BasePrice,
			TierFnbCredit:    cat.FnbCredit,
			EntryCount:       cat.EntryCount,
			EventTitle:       ev.Title,
			TicketID:         tk.ID,
			BarCode:          tk.BarCode,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	start := int(arg.PageOffset)
	if start >= len(out) {
		return []db.ListBookingsForVerificationRow{}, nil
	}
	end := start + int(arg.PageLimit)
	if end > len(out) {
		end = len(out)
	}
	return out[start:end], nil
}

func (s *memStore) CreateTicket(_ context.Context, arg db.CreateTicketParams) (db.CreateTicketRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tickets {
		if t.BarCode == arg.BarCode {
			return db.CreateTicketRow{}, pgUniqueErr("tickets_bar_code_key")
		}
	}
	t := db.Ticket{
		ID:                 arg.ID,
		BookingID:          arg.BookingID,
		EventID:            arg.EventID,
		QrTokenDigest:      arg.QrTokenDigest,
		BarCode:            arg.BarCode,
		FoodBalance:        arg.FoodBalance,
		InitialFoodBalance: arg.InitialFoodBalance,
		IsCheckedIn:        false,
	}
	s.tickets[t.ID] = t
	return db.CreateTicketRow{
		ID:                 t.ID,
		BookingID:          t.BookingID,
		EventID:            t.EventID,
		QrTokenDigest:      t.QrTokenDigest,
		BarCode:            t.BarCode,
		FoodBalance:        t.FoodBalance,
		InitialFoodBalance: t.InitialFoodBalance,
		IsCheckedIn:        t.IsCheckedIn,
		CheckedInAt:        t.CheckedInAt,
	}, nil
}

func (s *memStore) CreateTicketGuest(_ context.Context, arg db.CreateTicketGuestParams) (db.CreateTicketGuestRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := db.TicketGuest{
		ID:               uuid.New(),
		TicketID:         arg.TicketID,
		Name:             arg.Name,
		Age:              arg.Age,
		Gender:           arg.Gender,
		IsPrimaryContact: arg.IsPrimaryContact,
		FreeShotEligible: arg.FreeShotEligible,
		FreeShotRedeemed: false,
	}
	s.guests[g.ID] = g
	return db.CreateTicketGuestRow{
		ID:               g.ID,
		TicketID:         g.TicketID,
		Name:             g.Name,
		Age:              g.Age,
		Gender:           g.Gender,
		IsPrimaryContact: g.IsPrimaryContact,
		FreeShotEligible: g.FreeShotEligible,
		FreeShotRedeemed: g.FreeShotRedeemed,
	}, nil
}

func (s *memStore) GetTicketByBookingID(_ context.Context, bookingID uuid.UUID) (db.GetTicketByBookingIDRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tickets {
		if t.BookingID == bookingID {
			return db.GetTicketByBookingIDRow{
				ID:                 t.ID,
				BookingID:          t.BookingID,
				EventID:            t.EventID,
				QrTokenDigest:      t.QrTokenDigest,
				BarCode:            t.BarCode,
				FoodBalance:        t.FoodBalance,
				InitialFoodBalance: t.InitialFoodBalance,
				IsCheckedIn:        t.IsCheckedIn,
				CheckedInAt:        t.CheckedInAt,
			}, nil
		}
	}
	return db.GetTicketByBookingIDRow{}, pgx.ErrNoRows
}

func (s *memStore) GetTicketDetailsByID(_ context.Context, id uuid.UUID) (db.GetTicketDetailsByIDRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[id]
	if !ok {
		return db.GetTicketDetailsByIDRow{}, pgx.ErrNoRows
	}
	b := s.bookings[t.BookingID]
	cat := s.categories[b.CategoryID]
	ev := s.events[t.EventID]
	if eventDeleted(ev) {
		return db.GetTicketDetailsByIDRow{}, pgx.ErrNoRows
	}
	return db.GetTicketDetailsByIDRow{
		ID:                 t.ID,
		BookingID:          t.BookingID,
		EventID:            t.EventID,
		QrTokenDigest:      t.QrTokenDigest,
		BarCode:            t.BarCode,
		FoodBalance:        t.FoodBalance,
		InitialFoodBalance: t.InitialFoodBalance,
		IsCheckedIn:        t.IsCheckedIn,
		CheckedInAt:        t.CheckedInAt,
		BookingStatus:      b.Status,
		TotalAmount:        b.TotalAmount,
		UpiTransactionID:   b.UpiTransactionID,
		ContactPhone:       b.ContactPhone,
		ContactEmail:       b.ContactEmail,
		CategoryName:       cat.Name,
		CategoryTier:       cat.Category,
		EntryCount:         cat.EntryCount,
		EventTitle:         ev.Title,
		EventStartsAt:      ev.StartsAt,
		EventEndsAt:        ev.EndsAt,
		EventLocation:      ev.Location,
	}, nil
}

func (s *memStore) ListGuestsByTicketID(_ context.Context, ticketID uuid.UUID) ([]db.ListGuestsByTicketIDRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.ListGuestsByTicketIDRow, 0)
	for _, g := range s.guests {
		if g.TicketID == ticketID {
			out = append(out, listGuestsByTicketIDRow(g))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsPrimaryContact != out[j].IsPrimaryContact {
			return out[i].IsPrimaryContact
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *memStore) SearchConfirmedTickets(_ context.Context, arg db.SearchConfirmedTicketsParams) ([]db.SearchConfirmedTicketsRow, error) {
	return s.searchTickets(arg.SearchQuery, arg.PageLimit, arg.CursorCreatedAt, arg.CursorID, false)
}

func (s *memStore) SearchPendingWhatsAppTickets(_ context.Context, arg db.SearchPendingWhatsAppTicketsParams) ([]db.SearchPendingWhatsAppTicketsRow, error) {
	rows, err := s.searchTickets(arg.SearchQuery, arg.PageLimit, arg.CursorCreatedAt, arg.CursorID, true)
	if err != nil {
		return nil, err
	}
	out := make([]db.SearchPendingWhatsAppTicketsRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, db.SearchPendingWhatsAppTicketsRow(row))
	}
	return out, nil
}

func (s *memStore) searchTickets(query string, limit int32, cursorCreatedAt time.Time, cursorID uuid.UUID, waPending bool) ([]db.SearchConfirmedTicketsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query = strings.ToLower(query)
	type match struct {
		id        uuid.UUID
		bookingID uuid.UUID
		createdAt time.Time
	}
	matches := make([]match, 0)
	for _, ticket := range s.tickets {
		booking, ok := s.bookings[ticket.BookingID]
		if !ok || booking.Status != db.BookingStatusCONFIRMED || eventDeleted(s.events[ticket.EventID]) {
			continue
		}
		if waPending && booking.IsWhatsappSent {
			continue
		}
		if cursorID != uuid.Nil {
			before := booking.CreatedAt.Before(cursorCreatedAt)
			if booking.CreatedAt.Equal(cursorCreatedAt) {
				before = strings.Compare(booking.ID.String(), cursorID.String()) < 0
			}
			if !before {
				continue
			}
		}
		if query != "" {
			found := strings.Contains(strings.ToLower(booking.ContactPhone), query)
			if booking.UpiTransactionID.Valid {
				found = found || strings.Contains(strings.ToLower(booking.UpiTransactionID.String), query)
			}
			if !found {
				for _, guest := range s.guests {
					if guest.TicketID == ticket.ID && strings.Contains(strings.ToLower(guest.Name), query) {
						found = true
						break
					}
				}
			}
			if !found {
				continue
			}
		}
		matches = append(matches, match{id: ticket.ID, bookingID: booking.ID, createdAt: booking.CreatedAt})
	}
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].createdAt.Equal(matches[j].createdAt) {
			return matches[i].createdAt.After(matches[j].createdAt)
		}
		return strings.Compare(matches[i].bookingID.String(), matches[j].bookingID.String()) > 0
	})

	if int32(len(matches)) > limit {
		matches = matches[:limit]
	}
	rows := make([]db.SearchConfirmedTicketsRow, 0, len(matches))
	for _, m := range matches {
		rows = append(rows, db.SearchConfirmedTicketsRow{ID: m.id, CursorCreatedAt: m.createdAt, CursorID: m.bookingID})
	}
	return rows, nil
}

func listGuestsByTicketIDRow(g db.TicketGuest) db.ListGuestsByTicketIDRow {
	return db.ListGuestsByTicketIDRow{
		ID:               g.ID,
		TicketID:         g.TicketID,
		Name:             g.Name,
		Age:              g.Age,
		Gender:           g.Gender,
		IsPrimaryContact: g.IsPrimaryContact,
		FreeShotEligible: g.FreeShotEligible,
		FreeShotRedeemed: g.FreeShotRedeemed,
	}
}

// --- Gate Queries ---

func (s *memStore) GetTicketDetailsByQRDigest(_ context.Context, qrTokenDigest string) (db.GetTicketDetailsByQRDigestRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tickets {
		if t.QrTokenDigest == qrTokenDigest {
			b := s.bookings[t.BookingID]
			cat := s.categories[b.CategoryID]
			ev := s.events[t.EventID]
			if eventDeleted(ev) {
				return db.GetTicketDetailsByQRDigestRow{}, pgx.ErrNoRows
			}
			return db.GetTicketDetailsByQRDigestRow{
				ID:                 t.ID,
				BookingID:          t.BookingID,
				EventID:            t.EventID,
				QrTokenDigest:      t.QrTokenDigest,
				BarCode:            t.BarCode,
				FoodBalance:        t.FoodBalance,
				InitialFoodBalance: t.InitialFoodBalance,
				IsCheckedIn:        t.IsCheckedIn,
				CheckedInAt:        t.CheckedInAt,
				BookingStatus:      b.Status,
				CategoryName:       cat.Name,
				CategoryTier:       cat.Category,
				EntryCount:         cat.EntryCount,
				EventTitle:         ev.Title,
				EventStartsAt:      ev.StartsAt,
				EventEndsAt:        ev.EndsAt,
				EventLocation:      ev.Location,
			}, nil
		}
	}
	return db.GetTicketDetailsByQRDigestRow{}, pgx.ErrNoRows
}

func (s *memStore) CheckInTicketAtomic(_ context.Context, arg db.CheckInTicketAtomicParams) (db.CheckInTicketAtomicRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tickets {
		if t.QrTokenDigest != arg.QrTokenDigest || t.IsCheckedIn {
			continue
		}
		t.IsCheckedIn = true
		t.CheckedInAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
		t.CheckedInBy = arg.CheckedInBy
		s.tickets[t.ID] = t
		return db.CheckInTicketAtomicRow{
			ID:                 t.ID,
			BookingID:          t.BookingID,
			EventID:            t.EventID,
			QrTokenDigest:      t.QrTokenDigest,
			BarCode:            t.BarCode,
			FoodBalance:        t.FoodBalance,
			InitialFoodBalance: t.InitialFoodBalance,
			IsCheckedIn:        t.IsCheckedIn,
			CheckedInAt:        t.CheckedInAt,
		}, nil
	}
	return db.CheckInTicketAtomicRow{}, pgx.ErrNoRows
}

// --- Bar Menu, Wallet & Audit Queries ---

func (s *memStore) ListActiveMenuItems(_ context.Context) ([]db.MenuItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.MenuItem, 0)
	for _, m := range s.menuItems {
		if m.IsActive {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *memStore) CreateMenuItem(_ context.Context, arg db.CreateMenuItemParams) (db.MenuItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := db.MenuItem{
		ID:           uuid.New(),
		Name:         arg.Name,
		Category:     arg.Category,
		UnitPrice:    arg.UnitPrice,
		IsActive:     true,
		ServingLabel: arg.ServingLabel,
	}
	s.menuItems[m.ID] = m
	return m, nil
}

func (s *memStore) UpdateMenuItem(_ context.Context, arg db.UpdateMenuItemParams) (db.MenuItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.menuItems[arg.ID]
	if !ok || !m.IsActive {
		return db.MenuItem{}, pgx.ErrNoRows
	}
	m.Name = arg.Name
	m.Category = arg.Category
	m.UnitPrice = arg.UnitPrice
	m.ServingLabel = arg.ServingLabel
	s.menuItems[m.ID] = m
	return m, nil
}

func (s *memStore) DeactivateMenuItem(_ context.Context, id uuid.UUID) (db.MenuItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.menuItems[id]
	if !ok || !m.IsActive {
		return db.MenuItem{}, pgx.ErrNoRows
	}
	m.IsActive = false
	s.menuItems[m.ID] = m
	return m, nil
}

func (s *memStore) GetActiveMenuItemsByIDs(_ context.Context, ids []uuid.UUID) ([]db.MenuItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.MenuItem, 0, len(ids))
	for _, id := range ids {
		if m, ok := s.menuItems[id]; ok && m.IsActive {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *memStore) GetTicketDetailsByBarCode(_ context.Context, barCode string) (db.GetTicketDetailsByBarCodeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tickets {
		if t.BarCode == barCode {
			b := s.bookings[t.BookingID]
			cat := s.categories[b.CategoryID]
			ev := s.events[t.EventID]
			if eventDeleted(ev) {
				return db.GetTicketDetailsByBarCodeRow{}, pgx.ErrNoRows
			}
			return db.GetTicketDetailsByBarCodeRow{
				ID:                 t.ID,
				BookingID:          t.BookingID,
				EventID:            t.EventID,
				BarCode:            t.BarCode,
				FoodBalance:        t.FoodBalance,
				InitialFoodBalance: t.InitialFoodBalance,
				IsCheckedIn:        t.IsCheckedIn,
				CheckedInAt:        t.CheckedInAt,
				BookingStatus:      b.Status,
				CategoryName:       cat.Name,
				CategoryTier:       cat.Category,
				EntryCount:         cat.EntryCount,
				EventTitle:         ev.Title,
				EventStartsAt:      ev.StartsAt,
				EventEndsAt:        ev.EndsAt,
			}, nil
		}
	}
	return db.GetTicketDetailsByBarCodeRow{}, pgx.ErrNoRows
}

func (s *memStore) GetTicketForUpdateByBarCode(_ context.Context, barCode string) (db.GetTicketForUpdateByBarCodeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tickets {
		if t.BarCode == barCode {
			b := s.bookings[t.BookingID]
			ev := s.events[t.EventID]
			if eventDeleted(ev) {
				return db.GetTicketForUpdateByBarCodeRow{}, pgx.ErrNoRows
			}
			return db.GetTicketForUpdateByBarCodeRow{
				ID:                 t.ID,
				BookingID:          t.BookingID,
				EventID:            t.EventID,
				BarCode:            t.BarCode,
				FoodBalance:        t.FoodBalance,
				InitialFoodBalance: t.InitialFoodBalance,
				IsCheckedIn:        t.IsCheckedIn,
				BookingStatus:      b.Status,
				EventEndsAt:        ev.EndsAt,
			}, nil
		}
	}
	return db.GetTicketForUpdateByBarCodeRow{}, pgx.ErrNoRows
}

func (s *memStore) DeductTicketFoodBalanceAtomic(_ context.Context, arg db.DeductTicketFoodBalanceAtomicParams) (db.DeductTicketFoodBalanceAtomicRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[arg.ID]
	if !ok || t.FoodBalance < arg.FoodBalance {
		return db.DeductTicketFoodBalanceAtomicRow{}, pgx.ErrNoRows
	}
	t.FoodBalance -= arg.FoodBalance
	s.tickets[t.ID] = t
	return db.DeductTicketFoodBalanceAtomicRow{
		ID:                 t.ID,
		FoodBalance:        t.FoodBalance,
		InitialFoodBalance: t.InitialFoodBalance,
	}, nil
}

func (s *memStore) RedeemGuestFreeShotAtomic(_ context.Context, arg db.RedeemGuestFreeShotAtomicParams) (db.RedeemGuestFreeShotAtomicRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.guests[arg.ID]
	if !ok || g.TicketID != arg.TicketID || !g.FreeShotEligible || g.FreeShotRedeemed {
		return db.RedeemGuestFreeShotAtomicRow{}, pgx.ErrNoRows
	}
	g.FreeShotRedeemed = true
	s.guests[g.ID] = g
	return db.RedeemGuestFreeShotAtomicRow{
		ID:               g.ID,
		TicketID:         g.TicketID,
		Name:             g.Name,
		Gender:           g.Gender,
		FreeShotRedeemed: g.FreeShotRedeemed,
	}, nil
}

func (s *memStore) GetBarTransactionByIdempotencyKey(_ context.Context, key pgtype.Text) (db.GetBarTransactionByIdempotencyKeyRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tx := range s.barTxs {
		if tx.IdempotencyKey.Valid && key.Valid && tx.IdempotencyKey.String == key.String {
			return db.GetBarTransactionByIdempotencyKeyRow{
				ID:                 tx.ID,
				TicketID:           tx.TicketID,
				BartenderID:        tx.BartenderID,
				IdempotencyKey:     tx.IdempotencyKey,
				TotalBill:          tx.TotalBill,
				WalletDeducted:     tx.WalletDeducted,
				UpiDeltaPaid:       tx.UpiDeltaPaid,
				CashDeltaPaid:      tx.CashDeltaPaid,
				DeltaPaymentMethod: tx.DeltaPaymentMethod,
				DeltaCollectedAt:   tx.DeltaCollectedAt,
				BalanceBefore:      tx.BalanceBefore,
				BalanceAfter:       tx.BalanceAfter,
				CreatedAt:          tx.CreatedAt,
			}, nil
		}
	}
	return db.GetBarTransactionByIdempotencyKeyRow{}, pgx.ErrNoRows
}

func (s *memStore) CreateBarTransaction(_ context.Context, arg db.CreateBarTransactionParams) (db.CreateBarTransactionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if arg.IdempotencyKey.Valid && arg.IdempotencyKey.String != "" {
		for _, existing := range s.barTxs {
			if existing.IdempotencyKey.Valid && existing.IdempotencyKey.String == arg.IdempotencyKey.String {
				return db.CreateBarTransactionRow{}, pgUniqueErr("idx_bar_transactions_idempotency")
			}
		}
	}
	tx := db.BarTransaction{
		ID:                 uuid.New(),
		TicketID:           arg.TicketID,
		BartenderID:        arg.BartenderID,
		IdempotencyKey:     arg.IdempotencyKey,
		TotalBill:          arg.TotalBill,
		WalletDeducted:     arg.WalletDeducted,
		UpiDeltaPaid:       arg.UpiDeltaPaid,
		CashDeltaPaid:      arg.CashDeltaPaid,
		DeltaPaymentMethod: arg.DeltaPaymentMethod,
		DeltaCollectedAt:   arg.DeltaCollectedAt,
		BalanceBefore:      arg.BalanceBefore,
		BalanceAfter:       arg.BalanceAfter,
		CreatedAt:          time.Now().UTC(),
	}
	s.barTxs[tx.ID] = tx
	s.barTxOrder = append(s.barTxOrder, tx.ID)
	return db.CreateBarTransactionRow{
		ID:                 tx.ID,
		TicketID:           tx.TicketID,
		BartenderID:        tx.BartenderID,
		IdempotencyKey:     tx.IdempotencyKey,
		TotalBill:          tx.TotalBill,
		WalletDeducted:     tx.WalletDeducted,
		UpiDeltaPaid:       tx.UpiDeltaPaid,
		DeltaPaymentMethod: tx.DeltaPaymentMethod,
		DeltaCollectedAt:   tx.DeltaCollectedAt,
		BalanceBefore:      tx.BalanceBefore,
		BalanceAfter:       tx.BalanceAfter,
		CreatedAt:          tx.CreatedAt,
	}, nil
}

func (s *memStore) CreateTransactionLineItem(_ context.Context, arg db.CreateTransactionLineItemParams) (db.TransactionLineItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	li := db.TransactionLineItem{
		ID:              uuid.New(),
		TransactionID:   arg.TransactionID,
		MenuItemID:      arg.MenuItemID,
		Quantity:        arg.Quantity,
		UnitPriceAtSale: arg.UnitPriceAtSale,
	}
	s.lineItems = append(s.lineItems, li)
	return li, nil
}

func (s *memStore) ListBarTransactionsByTicketID(_ context.Context, arg db.ListBarTransactionsByTicketIDParams) ([]db.ListBarTransactionsByTicketIDRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.ListBarTransactionsByTicketIDRow, 0)
	for i := len(s.barTxOrder) - 1; i >= 0; i-- {
		tx := s.barTxs[s.barTxOrder[i]]
		if tx.TicketID != arg.TicketID {
			continue
		}
		uname := ""
		if st, ok := s.staff[tx.BartenderID]; ok {
			uname = st.Username
		}
		out = append(out, db.ListBarTransactionsByTicketIDRow{
			ID:                 tx.ID,
			TicketID:           tx.TicketID,
			BartenderID:        tx.BartenderID,
			BartenderUsername:  uname,
			IdempotencyKey:     tx.IdempotencyKey,
			TotalBill:          tx.TotalBill,
			WalletDeducted:     tx.WalletDeducted,
			UpiDeltaPaid:       tx.UpiDeltaPaid,
			DeltaPaymentMethod: tx.DeltaPaymentMethod,
			DeltaCollectedAt:   tx.DeltaCollectedAt,
			BalanceBefore:      tx.BalanceBefore,
			BalanceAfter:       tx.BalanceAfter,
			CreatedAt:          tx.CreatedAt,
		})
		if int32(len(out)) >= arg.Limit {
			break
		}
	}
	return out, nil
}

func (s *memStore) ListLineItemsByTransactionIDs(_ context.Context, txIDs []uuid.UUID) ([]db.ListLineItemsByTransactionIDsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := make(map[uuid.UUID]struct{}, len(txIDs))
	for _, id := range txIDs {
		wanted[id] = struct{}{}
	}
	out := make([]db.ListLineItemsByTransactionIDsRow, 0)
	for _, li := range s.lineItems {
		if _, ok := wanted[li.TransactionID]; !ok {
			continue
		}
		name := ""
		if m, ok := s.menuItems[li.MenuItemID]; ok {
			name = m.Name
		}
		out = append(out, db.ListLineItemsByTransactionIDsRow{
			ID:              li.ID,
			TransactionID:   li.TransactionID,
			MenuItemID:      li.MenuItemID,
			MenuItemName:    name,
			Quantity:        li.Quantity,
			UnitPriceAtSale: li.UnitPriceAtSale,
		})
	}
	return out, nil
}

// --- Analytics Queries ---

func (s *memStore) GetEventBookingStats(_ context.Context, eventID uuid.UUID) (db.GetEventBookingStatsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var row db.GetEventBookingStatsRow
	for _, b := range s.bookings {
		if b.EventID != eventID || b.Status != db.BookingStatusCONFIRMED {
			continue
		}
		row.ConfirmedBookings++
		row.TicketRevenue += int64(b.TotalAmount)
		for _, t := range s.tickets {
			if t.BookingID == b.ID {
				row.TotalTickets++
				row.TotalFnbAllocated += int64(t.InitialFoodBalance)
				row.TotalFnbRemaining += int64(t.FoodBalance)
				row.TotalFnbRedeemed += int64(t.InitialFoodBalance - t.FoodBalance)
				if t.IsCheckedIn {
					row.CheckedInTickets++
				}
			}
		}
	}
	return row, nil
}

func (s *memStore) GetEventBarRevenueStats(_ context.Context, eventID uuid.UUID) (db.GetEventBarRevenueStatsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var row db.GetEventBarRevenueStatsRow
	for _, tx := range s.barTxs {
		t, ok := s.tickets[tx.TicketID]
		if !ok || t.EventID != eventID {
			continue
		}
		row.TotalTransactions++
		row.TotalBarBill += int64(tx.TotalBill)
		row.TotalWalletDeducted += int64(tx.WalletDeducted)
		row.TotalUpiDelta += int64(tx.UpiDeltaPaid)
	}
	return row, nil
}

func (s *memStore) GetEventGenderStats(_ context.Context, eventID uuid.UUID) (db.GetEventGenderStatsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var row db.GetEventGenderStatsRow
	for _, g := range s.guests {
		t, ok := s.tickets[g.TicketID]
		if !ok || t.EventID != eventID {
			continue
		}
		if b, ok := s.bookings[t.BookingID]; !ok || b.Status != db.BookingStatusCONFIRMED {
			continue
		}
		row.TotalGuests++
		switch g.Gender {
		case db.GenderTypeMALE:
			row.MaleGuests++
		case db.GenderTypeFEMALE:
			row.FemaleGuests++
		case db.GenderTypeOTHER:
			row.OtherGuests++
		}
		if g.FreeShotRedeemed {
			row.FreeShotsRedeemed++
		}
	}
	return row, nil
}

// --- Rate Limit Queries ---

func (s *memStore) UpsertRateLimitHit(_ context.Context, arg db.UpsertRateLimitHitParams) (int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := arg.Identifier + "|" + arg.Endpoint + "|" + arg.WindowStart.UTC().Format(time.RFC3339)
	s.rateLimits[key]++
	return s.rateLimits[key], nil
}

func (s *memStore) AddRateLimitHits(_ context.Context, arg db.AddRateLimitHitsParams) (int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := arg.Identifier + "|" + arg.Endpoint + "|" + arg.WindowStart.UTC().Format(time.RFC3339)
	s.rateLimits[key] += arg.Hits
	return s.rateLimits[key], nil
}

func (s *memStore) DeleteExpiredRateLimits(_ context.Context, _ time.Time) error { return nil }

// --- Seating, Audit & Extended Analytics Queries ---

func (s *memStore) UpsertSeatingInventory(_ context.Context, arg db.UpsertSeatingInventoryParams) (db.EventSeatingInventory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := seatKey(arg.EventID, arg.Section, arg.TableSize)
	now := time.Now().UTC()
	inv, ok := s.seatInventory[key]
	if ok {
		inv.TotalTables = arg.TotalTables
		inv.TicketCategoryID = arg.TicketCategoryID
		inv.UpdatedAt = now
	} else {
		inv = db.EventSeatingInventory{
			ID:               uuid.New(),
			EventID:          arg.EventID,
			Section:          arg.Section,
			TableSize:        arg.TableSize,
			TotalTables:      arg.TotalTables,
			TicketCategoryID: arg.TicketCategoryID,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
	}
	s.seatInventory[key] = inv
	return inv, nil
}

func (s *memStore) GetSeatingInventoryForUpdate(_ context.Context, arg db.GetSeatingInventoryForUpdateParams) (db.EventSeatingInventory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.seatInventory[seatKey(arg.EventID, arg.Section, arg.TableSize)]
	if !ok {
		return db.EventSeatingInventory{}, pgx.ErrNoRows
	}
	return inv, nil
}

func (s *memStore) GetGroupTicketCategory(_ context.Context, arg db.GetGroupTicketCategoryParams) (db.TicketCategory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cat := range s.categories {
		if cat.EventID != arg.EventID || cat.EntryCount != arg.EntryCount || !strings.HasPrefix(cat.Name, "GROUP_") {
			continue
		}
		if arg.Category.Valid && (!cat.Category.Valid || cat.Category.String != arg.Category.String) {
			continue
		}
		return cat, nil
	}
	return db.TicketCategory{}, pgx.ErrNoRows
}

func (s *memStore) UpdateGroupTicketCategory(_ context.Context, arg db.UpdateGroupTicketCategoryParams) (db.TicketCategory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cat, ok := s.categories[arg.ID]
	if !ok {
		return db.TicketCategory{}, pgx.ErrNoRows
	}
	cat.BasePrice = arg.BasePrice
	cat.FnbCredit = arg.FnbCredit
	cat.IsGenderStrict = false
	s.categories[cat.ID] = cat
	return cat, nil
}

func (s *memStore) AcquireGroupSeatReservation(_ context.Context, arg db.AcquireGroupSeatReservationParams) (db.EventSeatReservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.seatInventory[seatKey(arg.EventID, arg.Section, arg.TableSize)]
	if !ok {
		return db.EventSeatReservation{}, pgx.ErrNoRows
	}
	var reserved int32
	for _, existing := range s.seatReservations {
		if existing.BookingID == arg.BookingID {
			return db.EventSeatReservation{}, pgUniqueErr("event_seat_reservations_booking_id_key")
		}
		if existing.EventID == arg.EventID && existing.Section == arg.Section && existing.TableSize == arg.TableSize {
			reserved++
		}
	}
	tableNumber := reserved + 1
	if tableNumber > inv.TotalTables {
		return db.EventSeatReservation{}, pgx.ErrNoRows
	}
	booking := db.EventSeatReservation{
		ID:          uuid.New(),
		EventID:     arg.EventID,
		Section:     arg.Section,
		TableSize:   arg.TableSize,
		TableNumber: tableNumber,
		BookingID:   arg.BookingID,
		TicketID:    arg.TicketID,
		CreatedAt:   time.Now().UTC(),
	}
	s.seatReservations[booking.ID] = booking
	return booking, nil
}

func (s *memStore) GetSeatReservationByBookingID(_ context.Context, bookingID uuid.UUID) (db.EventSeatReservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, reservation := range s.seatReservations {
		if reservation.BookingID == bookingID {
			return reservation, nil
		}
	}
	return db.EventSeatReservation{}, pgx.ErrNoRows
}

func (s *memStore) ReleaseSeatReservationByBookingID(_ context.Context, bookingID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, reservation := range s.seatReservations {
		if reservation.BookingID == bookingID {
			delete(s.seatReservations, id)
			return nil
		}
	}
	return nil
}

func (s *memStore) ListEventSeatInventory(_ context.Context, eventID uuid.UUID) ([]db.ListEventSeatInventoryRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.ListEventSeatInventoryRow, 0)
	for _, inv := range s.seatInventory {
		if inv.EventID != eventID {
			continue
		}
		row := db.ListEventSeatInventoryRow{
			Section:     inv.Section,
			TableSize:   inv.TableSize,
			TotalTables: inv.TotalTables,
		}
		for _, reservation := range s.seatReservations {
			if reservation.EventID == eventID && reservation.Section == inv.Section && reservation.TableSize == inv.TableSize {
				row.ReservedTables++
			}
		}
		row.AvailableTables = row.TotalTables - row.ReservedTables
		if inv.TicketCategoryID.Valid {
			if cat, ok := s.categories[inv.TicketCategoryID.Bytes]; ok {
				row.TableTotal = pgtype.Int4{Int32: cat.BasePrice, Valid: true}
				row.FnbCredit = pgtype.Int4{Int32: cat.FnbCredit, Valid: true}
				row.TicketCategoryID = inv.TicketCategoryID
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		return out[i].TableSize < out[j].TableSize
	})
	return out, nil
}

func (s *memStore) ListSeatInventoryByEventIDs(_ context.Context, eventIDs []uuid.UUID) ([]db.ListSeatInventoryByEventIDsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[uuid.UUID]struct{}, len(eventIDs))
	for _, id := range eventIDs {
		want[id] = struct{}{}
	}
	out := make([]db.ListSeatInventoryByEventIDsRow, 0)
	for _, inv := range s.seatInventory {
		if _, ok := want[inv.EventID]; !ok {
			continue
		}
		row := db.ListSeatInventoryByEventIDsRow{
			EventID: inv.EventID, Section: inv.Section, TableSize: inv.TableSize, TotalTables: inv.TotalTables,
		}
		for _, reservation := range s.seatReservations {
			if reservation.EventID == inv.EventID && reservation.Section == inv.Section && reservation.TableSize == inv.TableSize {
				row.ReservedTables++
			}
		}
		row.AvailableTables = row.TotalTables - row.ReservedTables
		if inv.TicketCategoryID.Valid {
			if cat, ok := s.categories[inv.TicketCategoryID.Bytes]; ok {
				row.TableTotal = pgtype.Int4{Int32: cat.BasePrice, Valid: true}
				row.FnbCredit = pgtype.Int4{Int32: cat.FnbCredit, Valid: true}
				row.TicketCategoryID = inv.TicketCategoryID
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EventID != out[j].EventID {
			return out[i].EventID.String() < out[j].EventID.String()
		}
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		return out[i].TableSize < out[j].TableSize
	})
	return out, nil
}

func (s *memStore) ListEventSeats(_ context.Context, eventID uuid.UUID) ([]db.ListEventSeatsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.ListEventSeatsRow, 0)
	for _, inv := range s.seatInventory {
		if inv.EventID != eventID {
			continue
		}
		var price, fnb pgtype.Int4
		if inv.TicketCategoryID.Valid {
			if cat, ok := s.categories[inv.TicketCategoryID.Bytes]; ok {
				price = pgtype.Int4{Int32: cat.BasePrice, Valid: true}
				fnb = pgtype.Int4{Int32: cat.FnbCredit, Valid: true}
			}
		}
		for tableNumber := int32(1); tableNumber <= inv.TotalTables; tableNumber++ {
			booked := false
			for _, reservation := range s.seatReservations {
				if reservation.EventID == eventID && reservation.Section == inv.Section && reservation.TableSize == inv.TableSize && reservation.TableNumber == tableNumber {
					booked = true
					break
				}
			}
			out = append(out, db.ListEventSeatsRow{
				Section:     inv.Section,
				TableSize:   inv.TableSize,
				TableNumber: tableNumber,
				IsBooked:    booked,
				TableTotal:  price,
				FnbCredit:   fnb,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		if out[i].TableSize != out[j].TableSize {
			return out[i].TableSize < out[j].TableSize
		}
		return out[i].TableNumber < out[j].TableNumber
	})
	return out, nil
}

func (s *memStore) CreateAPIAuditLog(_ context.Context, arg db.CreateAPIAuditLogParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditLogs = append(s.auditLogs, db.ApiAuditLog{
		ID:         int64(len(s.auditLogs) + 1),
		RequestID:  arg.RequestID,
		Method:     arg.Method,
		Path:       arg.Path,
		Route:      arg.Route,
		StatusCode: arg.StatusCode,
		DurationMs: arg.DurationMs,
		ActorID:    arg.ActorID,
		ActorRole:  arg.ActorRole,
		Ip:         arg.Ip,
		UserAgent:  arg.UserAgent,
		CreatedAt:  time.Now().UTC(),
	})
	return nil
}

func (s *memStore) ListAPIAuditLogs(_ context.Context, arg db.ListAPIAuditLogsParams) ([]db.ApiAuditLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := make([]db.ApiAuditLog, 0)
	for i := len(s.auditLogs) - 1; i >= 0; i-- {
		entry := s.auditLogs[i]
		if arg.ActorID.Valid && (!entry.ActorID.Valid || entry.ActorID.Bytes != arg.ActorID.Bytes) {
			continue
		}
		if arg.Path.Valid && !strings.Contains(entry.Path, arg.Path.String) {
			continue
		}
		filtered = append(filtered, entry)
	}
	start := int(arg.PageOffset)
	if start < 0 {
		start = 0
	}
	if start >= len(filtered) {
		return []db.ApiAuditLog{}, nil
	}
	end := start + int(arg.PageLimit)
	if end > len(filtered) || end <= start {
		end = len(filtered)
	}
	return filtered[start:end], nil
}

func (s *memStore) ListPublicEvents(_ context.Context, arg db.ListPublicEventsParams) ([]db.ListPublicEventsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]db.Event, 0)
	status := strings.ToUpper(strings.TrimSpace(arg.Status))
	for _, ev := range s.events {
		if eventDeleted(ev) || ev.Status == db.EventStatusDRAFT {
			continue
		}
		if status != "ALL" && !strings.EqualFold(string(ev.Status), status) {
			continue
		}
		events = append(events, ev)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].StartsAt.After(events[j].StartsAt) })
	start, end := pageBounds(len(events), arg.PageOffset, arg.PageLimit)
	out := make([]db.ListPublicEventsRow, 0, end-start)
	for _, ev := range events[start:end] {
		row := db.ListPublicEventsRow{
			ID:        ev.ID,
			Title:     ev.Title,
			StartsAt:  ev.StartsAt,
			EndsAt:    ev.EndsAt,
			Location:  ev.Location,
			Status:    ev.Status,
			CreatedAt: ev.CreatedAt,
		}
		prices := make([]int32, 0)
		for _, cat := range s.categories {
			if cat.EventID != ev.ID {
				continue
			}
			prices = append(prices, cat.BasePrice)
		}
		row.TierCount = int64(len(prices))
		if len(prices) > 0 {
			minPrice, maxPrice := prices[0], prices[0]
			for _, price := range prices[1:] {
				if price < minPrice {
					minPrice = price
				}
				if price > maxPrice {
					maxPrice = price
				}
			}
			row.MinPrice = int64(minPrice)
			row.MaxPrice = int64(maxPrice)
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *memStore) ListAdminEvents(_ context.Context) ([]db.ListAdminEventsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]db.Event, 0, len(s.events))
	for _, ev := range s.events {
		if eventDeleted(ev) {
			continue
		}
		events = append(events, ev)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].StartsAt.After(events[j].StartsAt) })
	out := make([]db.ListAdminEventsRow, 0, len(events))
	for _, ev := range events {
		row := db.ListAdminEventsRow{
			ID:        ev.ID,
			Title:     ev.Title,
			StartsAt:  ev.StartsAt,
			EndsAt:    ev.EndsAt,
			Location:  ev.Location,
			Status:    ev.Status,
			CreatedAt: ev.CreatedAt,
		}
		for _, booking := range s.bookings {
			if booking.EventID != ev.ID || booking.Status != db.BookingStatusCONFIRMED {
				continue
			}
			row.ConfirmedBookings++
			row.TicketRevenue += int64(booking.TotalAmount)
			for _, ticket := range s.tickets {
				if ticket.BookingID == booking.ID && ticket.IsCheckedIn {
					row.CheckedInTickets++
				}
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *memStore) GetTopBarItemsByEvent(_ context.Context, arg db.GetTopBarItemsByEventParams) ([]db.GetTopBarItemsByEventRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type itemKey struct {
		menuID uuid.UUID
		price  int32
	}
	agg := make(map[itemKey]*db.GetTopBarItemsByEventRow)
	for _, line := range s.lineItems {
		tx, ok := s.barTxs[line.TransactionID]
		if !ok {
			continue
		}
		ticket, ok := s.tickets[tx.TicketID]
		if !ok || ticket.EventID != arg.EventID {
			continue
		}
		menu, ok := s.menuItems[line.MenuItemID]
		if !ok {
			continue
		}
		key := itemKey{menuID: line.MenuItemID, price: line.UnitPriceAtSale}
		row, ok := agg[key]
		if !ok {
			row = &db.GetTopBarItemsByEventRow{
				ItemName:        menu.Name,
				ItemCategory:    string(menu.Category),
				UnitPriceAtSale: line.UnitPriceAtSale,
			}
			agg[key] = row
		}
		row.Quantity += int64(line.Quantity)
		row.Revenue += int64(line.Quantity) * int64(line.UnitPriceAtSale)
	}
	out := make([]db.GetTopBarItemsByEventRow, 0, len(agg))
	for _, row := range agg {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Revenue != out[j].Revenue {
			return out[i].Revenue > out[j].Revenue
		}
		return out[i].ItemName < out[j].ItemName
	})
	if len(out) > int(arg.Limit) {
		out = out[:arg.Limit]
	}
	return out, nil
}

func (s *memStore) GetBarSalesByBartender(_ context.Context, eventID uuid.UUID) ([]db.GetBarSalesByBartenderRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agg := make(map[uuid.UUID]*db.GetBarSalesByBartenderRow)
	for _, tx := range s.barTxs {
		ticket, ok := s.tickets[tx.TicketID]
		if !ok || ticket.EventID != eventID {
			continue
		}
		row, ok := agg[tx.BartenderID]
		if !ok {
			username := ""
			if staff, ok := s.staff[tx.BartenderID]; ok {
				username = staff.Username
			}
			row = &db.GetBarSalesByBartenderRow{
				BartenderID:       tx.BartenderID,
				BartenderUsername: username,
			}
			agg[tx.BartenderID] = row
		}
		row.Transactions++
		row.TotalBill += int64(tx.TotalBill)
		row.WalletDeducted += int64(tx.WalletDeducted)
		row.DeltaCollected += int64(tx.UpiDeltaPaid)
	}
	out := make([]db.GetBarSalesByBartenderRow, 0, len(agg))
	for _, row := range agg {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalBill > out[j].TotalBill })
	return out, nil
}

func (s *memStore) GetRecentGateEntries(_ context.Context, arg db.GetRecentGateEntriesParams) ([]db.GetRecentGateEntriesRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.GetRecentGateEntriesRow, 0)
	for _, ticket := range s.tickets {
		if ticket.EventID != arg.EventID || !ticket.IsCheckedIn {
			continue
		}
		booking, ok := s.bookings[ticket.BookingID]
		if !ok {
			continue
		}
		entry := db.GetRecentGateEntriesRow{
			TicketID:    ticket.ID,
			BarCode:     ticket.BarCode,
			CheckedInAt: ticket.CheckedInAt,
		}
		if cat, ok := s.categories[booking.CategoryID]; ok {
			entry.CategoryName = cat.Name
		}
		for _, guest := range s.guests {
			if guest.TicketID == ticket.ID && guest.IsPrimaryContact {
				entry.PrimaryGuest = pgtype.Text{String: guest.Name, Valid: true}
				break
			}
		}
		if ticket.CheckedInBy.Valid {
			if staff, ok := s.staff[ticket.CheckedInBy.Bytes]; ok {
				entry.GatekeeperUsername = pgtype.Text{String: staff.Username, Valid: true}
			}
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CheckedInAt.Time.After(out[j].CheckedInAt.Time)
	})
	if len(out) > int(arg.Limit) {
		out = out[:arg.Limit]
	}
	return out, nil
}

func (s *memStore) CreateHelpMessage(_ context.Context, arg db.CreateHelpMessageParams) (db.HelpMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := db.HelpMessage{
		ID:         uuid.New(),
		Name:       arg.Name,
		Phone:      arg.Phone,
		Message:    arg.Message,
		EventID:    arg.EventID,
		IsResolved: false,
		CreatedAt:  time.Now().UTC(),
	}
	s.helpMessages[msg.ID] = msg
	return msg, nil
}

func (s *memStore) ListHelpMessages(_ context.Context, arg db.ListHelpMessagesParams) ([]db.HelpMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []db.HelpMessage
	for _, m := range s.helpMessages {
		if arg.IsResolved.Valid && m.IsResolved != arg.IsResolved.Bool {
			continue
		}
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	start, end := pageBounds(len(list), arg.PageOffset, arg.PageLimit)
	return list[start:end], nil
}

func (s *memStore) ResolveHelpMessage(_ context.Context, arg db.ResolveHelpMessageParams) (db.HelpMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg, ok := s.helpMessages[arg.ID]
	if !ok || msg.IsResolved {
		return db.HelpMessage{}, pgx.ErrNoRows
	}
	msg.IsResolved = true
	msg.ResolvedBy = arg.ResolvedBy
	msg.ResolvedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	s.helpMessages[arg.ID] = msg
	return msg, nil
}

func pageBounds(total int, offset, limit int32) (int, int) {
	start := int(offset)
	if start < 0 {
		start = 0
	}
	if start >= total {
		return total, total
	}
	end := start + int(limit)
	if end > total || end <= start {
		end = total
	}
	return start, end
}

// --- Test Environment Harness ---

type testEnv struct {
	t           *testing.T
	cfg         *config.Config
	store       *memStore
	notifier    *recordingNotifier
	roleCache   *cache.StaffRoleCache
	qrSigner    *auth.QRSigner
	seatHub     *realtime.SeatHub
	seatingSvc  *seating.Service
	router      http.Handler
	adminID     uuid.UUID
	scannerID   uuid.UUID
	bartenderID uuid.UUID
	eventID     uuid.UUID
	coupleCatID uuid.UUID
	femaleCatID uuid.UUID
	maleCatID   uuid.UUID
	drinkID     uuid.UUID
	foodID      uuid.UUID
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	cfg := &config.Config{
		Env:                     "test",
		Port:                    "8080",
		AppBaseURL:              "https://venu.test",
		DBQueryTimeout:          3 * time.Second,
		AllowedOrigins:          []string{"https://venu.test"},
		CookieSecure:            false,
		StaffCacheTTL:           30 * time.Second,
		JWTAccessSecret:         "test-access-secret-32-bytes-minimum!!",
		JWTRefreshSecret:        "test-refresh-secret-32-bytes-minimum!",
		JWTIssuer:               "venu-api",
		JWTAudience:             "venu-staff",
		QRSigningSecret:         "test-qr-signing-secret-32-bytes-min!",
		AccessTokenTTL:          15 * time.Minute,
		RefreshTokenTTL:         24 * time.Hour,
		TicketQRTTL:             24 * time.Hour,
		UPIVPA:                  "venu.club@upi",
		UPIMerchantName:         "House Untold Nightclub",
		UPIQRImageURL:           "https://venu.test/static/upi-qr.png",
		First99FemaleLimit:      99,
		AuthRateLimitMax:        100,
		AuthRateLimitWindow:     time.Minute,
		CheckoutRateLimitMax:    100,
		CheckoutRateLimitWindow: time.Minute,
		ScanRateLimitMax:        100,
		ScanRateLimitWindow:     time.Minute,
		BarRateLimitMax:         100,
		BarRateLimitWindow:      time.Minute,
		RequestTimeout:          10 * time.Second,
	}

	ms := newMemStore()
	notifier := &recordingNotifier{}
	roleCache := cache.NewStaffRoleCache(cfg.StaffCacheTTL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tokenMgr := auth.NewTokenManager(
		cfg.JWTAccessSecret, cfg.JWTRefreshSecret,
		cfg.JWTAudience, cfg.JWTIssuer,
		cfg.AccessTokenTTL, cfg.RefreshTokenTTL,
	)
	qrSigner := auth.NewQRSigner(cfg.QRSigningSecret)
	repo := repositories.NewTicketingRepositoryWithQuerier(ms, ms.ping, ms.withTx, cfg.DBQueryTimeout)
	limiter := ratelimit.NewPostgresRateLimiter(ms, cfg.DBQueryTimeout, logger)

	ticketingSvc := services.NewTicketingService(cfg, repo, tokenMgr, qrSigner, roleCache, notifier, logger)
	seatHub := realtime.NewSeatHub()
	seatingSvc := seating.NewService(repo, seatHub, logger)
	ticketingSvc.SetSeatingService(seatingSvc)
	adminSvc := services.NewAdminService(repo, roleCache, notifier, cfg.AppBaseURL, logger)
	gateSvc := services.NewGateService(repo, qrSigner, logger)
	barSvc := services.NewBarService(repo, logger)
	seatHdlr := handlers.NewSeatingHandler(seatingSvc, seatHub, cfg.AllowedOrigins, logger)

	router := server.NewRouter(server.Deps{
		Cfg:       cfg,
		Logger:    logger,
		Handler:   handlers.NewTicketingHandler(cfg, ticketingSvc, tokenMgr, logger),
		AdminHdlr: handlers.NewAdminHandler(adminSvc, logger),
		GateHdlr:  handlers.NewGateHandler(gateSvc, logger),
		BarHdlr:   handlers.NewBarHandler(barSvc, logger),
		SeatHdlr:  seatHdlr,
		RBAC:      middleware.NewRBAC(tokenMgr, ms, roleCache, cfg.DBQueryTimeout, cfg.AllowedOrigins),
		Limiter:   limiter,
		AuditSink: repositories.NewAuditSink(repo),
	})

	env := &testEnv{
		t:          t,
		cfg:        cfg,
		store:      ms,
		notifier:   notifier,
		roleCache:  roleCache,
		qrSigner:   qrSigner,
		seatHub:    seatHub,
		seatingSvc: seatingSvc,
		router:     router,
	}
	env.seedFixtures()
	return env
}

func (e *testEnv) seedFixtures() {
	e.t.Helper()
	ctx := context.Background()

	seedStaff := func(username, password string, role db.StaffRole) uuid.UUID {
		hash, err := auth.HashPassword(password)
		if err != nil {
			e.t.Fatalf("hash seed staff %s: %v", username, err)
		}
		st, err := e.store.CreateStaff(ctx, db.CreateStaffParams{
			Username:     username,
			PasswordHash: hash,
			Role:         role,
		})
		if err != nil {
			e.t.Fatalf("create seed staff %s: %v", username, err)
		}
		return st.ID
	}

	e.adminID = seedStaff("admin_user", "AdminPass!123", db.StaffRoleADMIN)
	e.scannerID = seedStaff("gate_user", "GatePass!123", db.StaffRoleSCANNER)
	e.bartenderID = seedStaff("bar_user", "BarPass!123", db.StaffRoleBARTENDER)

	now := time.Now().UTC()
	ev, _ := e.store.CreateEvent(ctx, db.CreateEventParams{
		Title:    "Saturday Noir",
		StartsAt: now.Add(-1 * time.Hour),
		EndsAt:   now.Add(6 * time.Hour),
		Location: "The Velvet Room, Bengaluru",
		Status:   db.EventStatusONSALE,
	})
	e.eventID = ev.ID

	coupleCat, _ := e.store.CreateTicketCategory(ctx, db.CreateTicketCategoryParams{
		EventID:        ev.ID,
		Name:           "COUPLE",
		Category:       pgtype.Text{String: "MAIN_FLOOR", Valid: true},
		BasePrice:      3000,
		FnbCredit:      2000,
		EntryCount:     2,
		IsGenderStrict: true,
	})
	e.coupleCatID = coupleCat.ID

	femaleCat, _ := e.store.CreateTicketCategory(ctx, db.CreateTicketCategoryParams{
		EventID:        ev.ID,
		Name:           "STAG_FEMALE",
		BasePrice:      1500,
		FnbCredit:      1000,
		EntryCount:     1,
		IsGenderStrict: false,
	})
	e.femaleCatID = femaleCat.ID

	maleCat, _ := e.store.CreateTicketCategory(ctx, db.CreateTicketCategoryParams{
		EventID:        ev.ID,
		Name:           "STAG_MALE",
		BasePrice:      2000,
		FnbCredit:      1000,
		EntryCount:     1,
		IsGenderStrict: false,
	})
	e.maleCatID = maleCat.ID

	drink, _ := e.store.CreateMenuItem(ctx, db.CreateMenuItemParams{
		Name:      "Smoked Negroni",
		Category:  db.MenuCategoryDRINK,
		UnitPrice: 750,
	})
	e.drinkID = drink.ID

	food, _ := e.store.CreateMenuItem(ctx, db.CreateMenuItemParams{
		Name:      "Truffle Fries",
		Category:  db.MenuCategoryFOOD,
		UnitPrice: 500,
	})
	e.foodID = food.ID
}

func (e *testEnv) doRequest(method, path string, body any, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "10.0.0.15:54321"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if method == http.MethodPost && path == "/api/checkout/init" && req.Header.Get("Idempotency-Key") == "" {
		req.Header.Set("Idempotency-Key", uuid.NewString())
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) loginAs(username, password string) (string, []*http.Cookie) {
	e.t.Helper()
	rec := e.doRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"username": username,
		"password": password,
	}, nil)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("loginAs(%s) failed with status %d: %s", username, rec.Code, rec.Body.String())
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	decodeBody(e.t, rec, &payload)
	return payload.AccessToken, rec.Result().Cookies()
}

func bearerHeader(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode JSON (status %d): %v | raw=%s", rec.Code, err, rec.Body.String())
	}
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("got HTTP %d, want %d | body=%s", rec.Code, want, rec.Body.String())
	}
}

func (s *memStore) UpdateEvent(_ context.Context, arg db.UpdateEventParams) (db.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev, ok := s.events[arg.ID]
	if !ok || eventDeleted(ev) {
		return db.Event{}, pgx.ErrNoRows
	}
	if arg.Title.Valid {
		ev.Title = arg.Title.String
	}
	if arg.Location.Valid {
		ev.Location = arg.Location.String
	}
	if arg.StartsAt.Valid {
		ev.StartsAt = arg.StartsAt.Time
	}
	if arg.EndsAt.Valid {
		ev.EndsAt = arg.EndsAt.Time
	}
	if arg.VenueName.Valid {
		ev.VenueName = arg.VenueName.String
	}
	if arg.LocationArea.Valid {
		ev.LocationArea = arg.LocationArea.String
	}
	if arg.TransitInfo.Valid {
		ev.TransitInfo = arg.TransitInfo.String
	}
	if arg.AdvisoryNotice.Valid {
		ev.AdvisoryNotice = arg.AdvisoryNotice.String
	}
	if arg.Description.Valid {
		ev.Description = arg.Description.String
	}
	if arg.Status.Valid {
		ev.Status = arg.Status.EventStatus
	}
	s.events[arg.ID] = ev
	return ev, nil
}

func (s *memStore) UpdateTicketCategory(_ context.Context, arg db.UpdateTicketCategoryParams) (db.TicketCategory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cat, ok := s.categories[arg.ID]
	if !ok {
		return db.TicketCategory{}, pgx.ErrNoRows
	}
	if arg.Name.Valid {
		cat.Name = arg.Name.String
	}
	if arg.Category.Valid {
		cat.Category = arg.Category
	}
	if arg.BasePrice.Valid {
		cat.BasePrice = arg.BasePrice.Int32
	}
	if arg.FnbCredit.Valid {
		cat.FnbCredit = arg.FnbCredit.Int32
	}
	if arg.EntryCount.Valid {
		cat.EntryCount = arg.EntryCount.Int32
	}
	if arg.IsGenderStrict.Valid {
		cat.IsGenderStrict = arg.IsGenderStrict.Bool
	}
	s.categories[arg.ID] = cat
	return cat, nil
}

func (s *memStore) UpdateTicketCategoryPrice(_ context.Context, arg db.UpdateTicketCategoryPriceParams) (db.TicketCategory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cat, ok := s.categories[arg.ID]
	if !ok {
		return db.TicketCategory{}, pgx.ErrNoRows
	}
	cat.BasePrice = arg.BasePrice
	cat.FnbCredit = arg.FnbCredit
	s.categories[arg.ID] = cat
	return cat, nil
}

func (s *memStore) CreateArtist(_ context.Context, arg db.CreateArtistParams) (db.EventArtist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	artist := db.EventArtist{
		ID:          uuid.New(),
		EventID:     arg.EventID,
		Name:        arg.Name,
		Category:    arg.Category,
		Description: arg.Description,
		CreatedAt:   time.Now().UTC(),
	}
	s.artists[artist.ID] = artist
	return artist, nil
}

func (s *memStore) ListArtistsByEvent(_ context.Context, eventID uuid.UUID) ([]db.EventArtist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.EventArtist
	for _, a := range s.artists {
		if a.EventID == eventID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *memStore) UpdateArtist(_ context.Context, arg db.UpdateArtistParams) (db.EventArtist, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	artist, ok := s.artists[arg.ID]
	if !ok || artist.EventID != arg.EventID {
		return db.EventArtist{}, pgx.ErrNoRows
	}
	artist.Name = arg.Name
	artist.Category = arg.Category
	artist.Description = arg.Description
	s.artists[artist.ID] = artist
	return artist, nil
}

func (s *memStore) DeleteArtist(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.artists, id)
	return nil
}

func (s *memStore) UpdateBookingPaymentQRData(_ context.Context, arg db.UpdateBookingPaymentQRDataParams) (db.Booking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	booking, ok := s.bookings[arg.ID]
	if !ok {
		return db.Booking{}, pgx.ErrNoRows
	}
	booking.PaymentQrData = arg.PaymentQrData
	s.bookings[arg.ID] = booking
	return booking, nil
}

func (s *memStore) GetBookingIDByTicketID(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.tickets[id]
	if !ok {
		return uuid.Nil, pgx.ErrNoRows
	}
	return ticket.BookingID, nil
}

func (s *memStore) GetTicketBookingDetails(_ context.Context, id uuid.UUID) (db.GetTicketBookingDetailsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.tickets[id]
	if !ok {
		return db.GetTicketBookingDetailsRow{}, pgx.ErrNoRows
	}
	booking, ok := s.bookings[ticket.BookingID]
	if !ok {
		return db.GetTicketBookingDetailsRow{}, pgx.ErrNoRows
	}
	cat := s.categories[booking.CategoryID]
	section := cat.Name
	if cat.Category.Valid && cat.Category.String != "" {
		section = cat.Category.String
	}
	return db.GetTicketBookingDetailsRow{
		BookingID:                booking.ID,
		ContactPhone:             booking.ContactPhone,
		UpiTransactionID:         booking.UpiTransactionID,
		IsWhatsappSent:           booking.IsWhatsappSent,
		CoupleCount:              booking.CoupleCount,
		StagMaleCount:            booking.StagMaleCount,
		StagFemaleCount:          booking.StagFemaleCount,
		FreeTequilaEligibleCount: booking.FreeTequilaEligibleCount,
		FreeTequilaRedeemedCount: booking.FreeTequilaRedeemedCount,
		Section:                  section,
		BarCode:                  ticket.BarCode,
	}, nil
}

func (s *memStore) MarkBookingWhatsAppSent(_ context.Context, id uuid.UUID) (db.MarkBookingWhatsAppSentRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	booking, ok := s.bookings[id]
	if !ok {
		return db.MarkBookingWhatsAppSentRow{}, pgx.ErrNoRows
	}
	booking.IsWhatsappSent = true
	s.bookings[id] = booking
	return db.MarkBookingWhatsAppSentRow{ID: booking.ID, IsWhatsappSent: booking.IsWhatsappSent}, nil
}

func (s *memStore) SetBookingGroupCounts(_ context.Context, arg db.SetBookingGroupCountsParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	booking, ok := s.bookings[arg.ID]
	if !ok {
		return pgx.ErrNoRows
	}
	booking.CoupleCount = arg.CoupleCount
	booking.StagMaleCount = arg.StagMaleCount
	booking.StagFemaleCount = arg.StagFemaleCount
	booking.FreeTequilaEligibleCount = arg.FreeTequilaEligibleCount
	s.bookings[arg.ID] = booking
	return nil
}

func (s *memStore) SetBookingFreeTequilaEligible(_ context.Context, arg db.SetBookingFreeTequilaEligibleParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	booking, ok := s.bookings[arg.ID]
	if !ok {
		return pgx.ErrNoRows
	}
	booking.FreeTequilaEligibleCount = arg.FreeTequilaEligibleCount
	s.bookings[arg.ID] = booking
	return nil
}

func (s *memStore) GetBookingTequilaCountsByTicketID(_ context.Context, id uuid.UUID) (db.GetBookingTequilaCountsByTicketIDRow, error) {
	bookingID, err := s.GetBookingIDByTicketID(context.Background(), id)
	if err != nil {
		return db.GetBookingTequilaCountsByTicketIDRow{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	booking, ok := s.bookings[bookingID]
	if !ok {
		return db.GetBookingTequilaCountsByTicketIDRow{}, pgx.ErrNoRows
	}
	return db.GetBookingTequilaCountsByTicketIDRow{
		BookingID:                booking.ID,
		FreeTequilaEligibleCount: booking.FreeTequilaEligibleCount,
		FreeTequilaRedeemedCount: booking.FreeTequilaRedeemedCount,
	}, nil
}

func (s *memStore) IncrementBookingTequilaRedeemed(_ context.Context, arg db.IncrementBookingTequilaRedeemedParams) (db.IncrementBookingTequilaRedeemedRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	booking, ok := s.bookings[arg.ID]
	if !ok || booking.FreeTequilaRedeemedCount+arg.FreeTequilaRedeemedCount > booking.FreeTequilaEligibleCount {
		return db.IncrementBookingTequilaRedeemedRow{}, pgx.ErrNoRows
	}
	booking.FreeTequilaRedeemedCount += arg.FreeTequilaRedeemedCount
	s.bookings[arg.ID] = booking
	return db.IncrementBookingTequilaRedeemedRow{
		ID:                       booking.ID,
		FreeTequilaEligibleCount: booking.FreeTequilaEligibleCount,
		FreeTequilaRedeemedCount: booking.FreeTequilaRedeemedCount,
	}, nil
}

func (s *memStore) RedeemBookingTequilaShotsAtomic(_ context.Context, arg db.RedeemBookingTequilaShotsAtomicParams) (db.RedeemBookingTequilaShotsAtomicRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	booking, ok := s.bookings[arg.ID]
	if !ok || booking.FreeTequilaRedeemedCount+arg.FreeTequilaRedeemedCount > booking.FreeTequilaEligibleCount {
		return db.RedeemBookingTequilaShotsAtomicRow{}, pgx.ErrNoRows
	}
	booking.FreeTequilaRedeemedCount += arg.FreeTequilaRedeemedCount
	s.bookings[arg.ID] = booking
	return db.RedeemBookingTequilaShotsAtomicRow{
		ID:                       booking.ID,
		FreeTequilaEligibleCount: booking.FreeTequilaEligibleCount,
		FreeTequilaRedeemedCount: booking.FreeTequilaRedeemedCount,
	}, nil
}

func (s *memStore) GetEventFirst99Active(_ context.Context, id uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event, ok := s.events[id]
	if !ok || eventDeleted(event) {
		return false, pgx.ErrNoRows
	}
	return event.IsFirst99Active, nil
}

func (s *memStore) SetEventFirst99Active(_ context.Context, arg db.SetEventFirst99ActiveParams) (db.SetEventFirst99ActiveRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event, ok := s.events[arg.ID]
	if !ok || eventDeleted(event) {
		return db.SetEventFirst99ActiveRow{}, pgx.ErrNoRows
	}
	event.IsFirst99Active = arg.IsFirst99Active
	s.events[arg.ID] = event
	return db.SetEventFirst99ActiveRow{ID: event.ID, IsFirst99Active: event.IsFirst99Active}, nil
}

func (s *memStore) GetV2GroupStatsByEvent(_ context.Context, eventID uuid.UUID) (db.GetV2GroupStatsByEventRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out db.GetV2GroupStatsByEventRow
	for _, booking := range s.bookings {
		if booking.EventID != eventID || booking.Status != db.BookingStatusCONFIRMED {
			continue
		}
		out.TotalCouples += int64(booking.CoupleCount)
		out.TotalStagMale += int64(booking.StagMaleCount)
		out.TotalStagFemale += int64(booking.StagFemaleCount)
		out.TotalTequilaRedeemed += int64(booking.FreeTequilaRedeemedCount)
	}
	return out, nil
}

func (s *memStore) GetV2FinancialStatsByEvent(_ context.Context, eventID uuid.UUID) (db.GetV2FinancialStatsByEventRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out db.GetV2FinancialStatsByEventRow
	for _, ticket := range s.tickets {
		if ticket.EventID != eventID {
			continue
		}
		out.FnbCreditUsed += int64(ticket.InitialFoodBalance - ticket.FoodBalance)
	}
	for _, barTx := range s.barTxs {
		ticket, ok := s.tickets[barTx.TicketID]
		if !ok || ticket.EventID != eventID {
			continue
		}
		out.ExtraCashCollected += int64(barTx.CashDeltaPaid)
		out.ExtraUpiCollected += int64(barTx.UpiDeltaPaid)
	}
	return out, nil
}

var errSimulatedDBDown = errors.New("connection refused")
