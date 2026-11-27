package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/venu-live/venu-api/internal/db"
)

type CheckoutLeadAttempt struct {
	IdempotencyKey   string
	EventID          uuid.UUID
	CategoryID       uuid.UUID
	ContactPhone     string
	WhatsAppNumber   string
	ContactEmail     string
	PrimaryName      string
	PrimaryAge       int32
	Gender           string
	SelectionJSON    any
	UPITransactionID string
}

func (r *TicketingRepository) UpsertCheckoutLeadAttempt(ctx context.Context, a CheckoutLeadAttempt) error {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	b, err := json.Marshal(a.SelectionJSON)
	if err != nil {
		return fmt.Errorf("repo_checkout_lead_marshal: %w", err)
	}
	var age pgtype.Int4
	if a.PrimaryAge > 0 {
		age = pgtype.Int4{Int32: a.PrimaryAge, Valid: true}
	}
	err = r.queries.UpsertCheckoutLeadAttempt(tctx, db.UpsertCheckoutLeadAttemptParams{
		IdempotencyKey:   a.IdempotencyKey,
		EventID:          pgtype.UUID{Bytes: a.EventID, Valid: a.EventID != uuid.Nil},
		CategoryID:       pgtype.UUID{Bytes: a.CategoryID, Valid: a.CategoryID != uuid.Nil},
		ContactPhone:     a.ContactPhone,
		WhatsappNumber:   pgtype.Text{String: a.WhatsAppNumber, Valid: a.WhatsAppNumber != ""},
		ContactEmail:     pgtype.Text{String: a.ContactEmail, Valid: a.ContactEmail != ""},
		PrimaryName:      pgtype.Text{String: a.PrimaryName, Valid: a.PrimaryName != ""},
		PrimaryAge:       age,
		Gender:           pgtype.Text{String: a.Gender, Valid: a.Gender != ""},
		SelectionJson:    b,
		UpiTransactionID: pgtype.Text{String: a.UPITransactionID, Valid: a.UPITransactionID != ""},
	})
	if err != nil {
		return fmt.Errorf("repo_upsert_checkout_lead: %w", err)
	}
	return nil
}

func (r *TicketingRepository) MarkCheckoutLeadResult(ctx context.Context, idempotencyKey string, bookingID *uuid.UUID, status, utr, errMsg string) error {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	var bid pgtype.UUID
	if bookingID != nil && *bookingID != uuid.Nil {
		bid = pgtype.UUID{Bytes: *bookingID, Valid: true}
	}
	err := r.queries.MarkCheckoutLeadResult(tctx, db.MarkCheckoutLeadResultParams{
		IdempotencyKey:   idempotencyKey,
		BookingID:        bid,
		Status:           status,
		UpiTransactionID: utr,
		ErrorMessage:     pgtype.Text{String: errMsg, Valid: errMsg != ""},
	})
	if err != nil {
		return fmt.Errorf("repo_mark_checkout_lead: %w", err)
	}
	return nil
}

func (r *TicketingRepository) MarkCheckoutLeadUTR(ctx context.Context, bookingID uuid.UUID, utr string) error {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	if err := r.queries.MarkCheckoutLeadUTR(tctx, db.MarkCheckoutLeadUTRParams{
		BookingID:        pgtype.UUID{Bytes: bookingID, Valid: true},
		UpiTransactionID: pgtype.Text{String: utr, Valid: utr != ""},
	}); err != nil {
		return fmt.Errorf("repo_mark_checkout_lead_utr: %w", err)
	}
	return nil
}

func (r *TicketingRepository) ListCheckoutLeads(ctx context.Context, includeCompleted bool, eventID uuid.UUID, query string, limit int32, cursorUpdatedAt *time.Time, cursorID uuid.UUID) ([]db.ListCheckoutLeadsRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	var curTime time.Time
	if cursorUpdatedAt != nil {
		curTime = cursorUpdatedAt.UTC()
	}
	rows, err := r.queries.ListCheckoutLeads(tctx, db.ListCheckoutLeadsParams{
		IncludeCompleted: includeCompleted,
		EventID:          eventID,
		Query:            strings.TrimSpace(query),
		CursorUpdatedAt:  curTime,
		CursorID:         cursorID,
		PageLimit:        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("repo_list_checkout_leads: %w", err)
	}
	return rows, nil
}
