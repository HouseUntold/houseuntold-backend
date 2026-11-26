package repositories

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/venu-live/venu-api/internal/db"
)

func (r *TicketingRepository) GetTicketDetailsByID(ctx context.Context, id uuid.UUID) (db.GetTicketDetailsByIDRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.GetTicketDetailsByID(tctx, id)
	if err != nil {
		return db.GetTicketDetailsByIDRow{}, fmt.Errorf("repo_get_ticket_details_by_id: %w", err)
	}
	return row, nil
}

func (r *TicketingRepository) ListGuestsByTicketID(ctx context.Context, ticketID uuid.UUID) ([]db.TicketGuest, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	guests, err := r.queries.ListGuestsByTicketID(tctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("repo_list_guests_by_ticket_id: %w", err)
	}
	return db.TicketGuestsFromListRows(guests), nil
}

func (r *TicketingRepository) SubmitBookingUPITransactionID(ctx context.Context, arg db.SubmitBookingUPITransactionIDParams) (db.Booking, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.SubmitBookingUPITransactionID(tctx, arg)
	if err != nil {
		return db.Booking{}, fmt.Errorf("repo_submit_booking_upi_txn: %w", err)
	}
	return db.Booking{
		ID:               row.ID,
		EventID:          row.EventID,
		CategoryID:       row.CategoryID,
		ContactPhone:     row.ContactPhone,
		ContactEmail:     row.ContactEmail,
		TotalAmount:      row.TotalAmount,
		UpiTransactionID: row.UpiTransactionID,
		Status:           row.Status,
		VerifiedBy:       row.VerifiedBy,
		VerifiedAt:       row.VerifiedAt,
		RejectionReason:  row.RejectionReason,
		IdempotencyKey:   row.IdempotencyKey,
		CreatedAt:        row.CreatedAt,
	}, nil
}

func (r *TicketingRepository) ListBookingsForVerification(ctx context.Context, arg db.ListBookingsForVerificationParams) ([]db.ListBookingsForVerificationRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	rows, err := r.queries.ListBookingsForVerification(tctx, arg)
	if err != nil {
		return nil, fmt.Errorf("repo_list_bookings_for_verification: %w", err)
	}
	return rows, nil
}

func (r *TicketingRepository) SearchConfirmedTickets(ctx context.Context, arg db.SearchConfirmedTicketsParams) ([]db.SearchConfirmedTicketsRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	rows, err := r.queries.SearchConfirmedTickets(tctx, arg)
	if err != nil {
		return nil, fmt.Errorf("repo_search_confirmed_tickets: %w", err)
	}
	return rows, nil
}

func (r *TicketingRepository) GetBookingByIDForUpdate(ctx context.Context, id uuid.UUID) (db.Booking, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.GetBookingByIDForUpdate(tctx, id)
	if err != nil {
		return db.Booking{}, fmt.Errorf("repo_get_booking_by_id_for_update: %w", err)
	}
	return db.Booking{
		ID:               row.ID,
		EventID:          row.EventID,
		CategoryID:       row.CategoryID,
		ContactPhone:     row.ContactPhone,
		ContactEmail:     row.ContactEmail,
		TotalAmount:      row.TotalAmount,
		UpiTransactionID: row.UpiTransactionID,
		Status:           row.Status,
		VerifiedBy:       row.VerifiedBy,
		VerifiedAt:       row.VerifiedAt,
		RejectionReason:  row.RejectionReason,
		IdempotencyKey:   row.IdempotencyKey,
		CreatedAt:        row.CreatedAt,
	}, nil
}

func (r *TicketingRepository) GetSeatReservationByBookingID(ctx context.Context, bookingID uuid.UUID) (db.EventSeatReservation, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	res, err := r.queries.GetSeatReservationByBookingID(tctx, bookingID)
	if err != nil {
		return db.EventSeatReservation{}, fmt.Errorf("repo_get_seat_reservation: %w", err)
	}
	return res, nil
}
