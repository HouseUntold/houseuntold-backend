package repositories

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/venu-live/venu-api/internal/db"
)

func (r *TicketingRepository) ListActiveMenuItems(ctx context.Context) ([]db.MenuItem, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	items, err := r.queries.ListActiveMenuItems(tctx)
	if err != nil {
		return nil, fmt.Errorf("repo_list_active_menu_items: %w", err)
	}
	return items, nil
}

func (r *TicketingRepository) CreateMenuItem(ctx context.Context, arg db.CreateMenuItemParams) (db.MenuItem, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	item, err := r.queries.CreateMenuItem(tctx, arg)
	if err != nil {
		return db.MenuItem{}, fmt.Errorf("repo_create_menu_item: %w", err)
	}
	return item, nil
}

func (r *TicketingRepository) UpdateMenuItem(ctx context.Context, arg db.UpdateMenuItemParams) (db.MenuItem, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	item, err := r.queries.UpdateMenuItem(tctx, arg)
	if err != nil {
		return db.MenuItem{}, fmt.Errorf("repo_update_menu_item: %w", err)
	}
	return item, nil
}

func (r *TicketingRepository) DeactivateMenuItem(ctx context.Context, id uuid.UUID) (db.MenuItem, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	item, err := r.queries.DeactivateMenuItem(tctx, id)
	if err != nil {
		return db.MenuItem{}, fmt.Errorf("repo_deactivate_menu_item: %w", err)
	}
	return item, nil
}

func (r *TicketingRepository) GetActiveMenuItemsByIDs(ctx context.Context, ids []uuid.UUID) ([]db.MenuItem, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	items, err := r.queries.GetActiveMenuItemsByIDs(tctx, ids)
	if err != nil {
		return nil, fmt.Errorf("repo_get_active_menu_items_by_ids: %w", err)
	}
	return items, nil
}

func (r *TicketingRepository) GetTicketDetailsByBarCode(ctx context.Context, barCode string) (db.GetTicketDetailsByBarCodeRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.GetTicketDetailsByBarCode(tctx, barCode)
	if err != nil {
		return db.GetTicketDetailsByBarCodeRow{}, fmt.Errorf("repo_get_ticket_by_bar_code: %w", err)
	}
	return row, nil
}

func (r *TicketingRepository) ListBarTransactionsByTicketID(ctx context.Context, ticketID uuid.UUID, limit int32) ([]db.ListBarTransactionsByTicketIDRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	rows, err := r.queries.ListBarTransactionsByTicketID(tctx, db.ListBarTransactionsByTicketIDParams{
		TicketID: ticketID,
		Limit:    limit,
	})
	if err != nil {
		return nil, fmt.Errorf("repo_list_bar_transactions: %w", err)
	}
	return rows, nil
}

func (r *TicketingRepository) ListLineItemsByTransactionIDs(ctx context.Context, txIDs []uuid.UUID) ([]db.ListLineItemsByTransactionIDsRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	rows, err := r.queries.ListLineItemsByTransactionIDs(tctx, txIDs)
	if err != nil {
		return nil, fmt.Errorf("repo_list_line_items: %w", err)
	}
	return rows, nil
}

func (r *TicketingRepository) GetBarTransactionByIdempotencyKey(ctx context.Context, key string) (db.BarTransaction, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.GetBarTransactionByIdempotencyKey(tctx, pgtype.Text{String: key, Valid: true})
	if err != nil {
		return db.BarTransaction{}, fmt.Errorf("repo_get_bar_tx_by_idempotency_key: %w", err)
	}
	return db.BarTransaction{
		ID:                 row.ID,
		TicketID:           row.TicketID,
		BartenderID:        row.BartenderID,
		IdempotencyKey:     row.IdempotencyKey,
		TotalBill:          row.TotalBill,
		WalletDeducted:     row.WalletDeducted,
		UpiDeltaPaid:       row.UpiDeltaPaid,
		BalanceBefore:      row.BalanceBefore,
		BalanceAfter:       row.BalanceAfter,
		CreatedAt:          row.CreatedAt,
		DeltaPaymentMethod: row.DeltaPaymentMethod,
		DeltaCollectedAt:   row.DeltaCollectedAt,
	}, nil
}
