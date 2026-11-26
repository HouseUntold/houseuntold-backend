package repositories

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/venu-live/venu-api/internal/db"
)

func (r *TicketingRepository) GetTicketDetailsByQRDigest(ctx context.Context, digest string) (db.GetTicketDetailsByQRDigestRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.GetTicketDetailsByQRDigest(tctx, digest)
	if err != nil {
		return db.GetTicketDetailsByQRDigestRow{}, fmt.Errorf("repo_get_ticket_by_qr_digest: %w", err)
	}
	return row, nil
}

func (r *TicketingRepository) CheckInTicketAtomic(ctx context.Context, qrTokenDigest string, gatekeeperID uuid.UUID) (db.Ticket, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.CheckInTicketAtomic(tctx, db.CheckInTicketAtomicParams{
		QrTokenDigest: qrTokenDigest,
		CheckedInBy:   pgtype.UUID{Bytes: gatekeeperID, Valid: gatekeeperID != uuid.Nil},
	})
	if err != nil {
		return db.Ticket{}, fmt.Errorf("repo_check_in_ticket_atomic: %w", err)
	}
	return db.Ticket{
		ID:                 row.ID,
		BookingID:          row.BookingID,
		EventID:            row.EventID,
		QrTokenDigest:      row.QrTokenDigest,
		BarCode:            row.BarCode,
		FoodBalance:        row.FoodBalance,
		InitialFoodBalance: row.InitialFoodBalance,
		IsCheckedIn:        row.IsCheckedIn,
		CheckedInAt:        row.CheckedInAt,
		CheckedInBy:        pgtype.UUID{Bytes: gatekeeperID, Valid: gatekeeperID != uuid.Nil},
	}, nil
}
