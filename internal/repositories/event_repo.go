package repositories

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/venu-live/venu-api/internal/db"
)

func (r *TicketingRepository) GetEventByID(ctx context.Context, id uuid.UUID) (db.Event, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	event, err := r.queries.GetEventByID(tctx, id)
	if err != nil {
		return db.Event{}, fmt.Errorf("repo_get_event_by_id: %w", err)
	}
	return event, nil
}

func (r *TicketingRepository) CreateEvent(ctx context.Context, arg db.CreateEventParams) (db.Event, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	event, err := r.queries.CreateEvent(tctx, arg)
	if err != nil {
		return db.Event{}, fmt.Errorf("repo_create_event: %w", err)
	}
	return event, nil
}

func (r *TicketingRepository) ListEventCategories(ctx context.Context, eventID uuid.UUID) ([]db.TicketCategory, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	cats, err := r.queries.ListEventCategories(tctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("repo_list_event_categories: %w", err)
	}
	return cats, nil
}

func (r *TicketingRepository) GetCategoryByID(ctx context.Context, id uuid.UUID) (db.TicketCategory, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	cat, err := r.queries.GetCategoryByID(tctx, id)
	if err != nil {
		return db.TicketCategory{}, fmt.Errorf("repo_get_category_by_id: %w", err)
	}
	return cat, nil
}

func (r *TicketingRepository) CreateTicketCategory(ctx context.Context, arg db.CreateTicketCategoryParams) (db.TicketCategory, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	cat, err := r.queries.CreateTicketCategory(tctx, arg)
	if err != nil {
		return db.TicketCategory{}, fmt.Errorf("repo_create_ticket_category: %w", err)
	}
	return cat, nil
}

func (r *TicketingRepository) CountConfirmedFemaleStagByEvent(ctx context.Context, eventID uuid.UUID) (int64, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	count, err := r.queries.CountConfirmedFemaleStagByEvent(tctx, eventID)
	if err != nil {
		return 0, fmt.Errorf("repo_count_confirmed_female_stag: %w", err)
	}
	return count, nil
}

func (r *TicketingRepository) GetEventBookingStats(ctx context.Context, eventID uuid.UUID) (db.GetEventBookingStatsRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.GetEventBookingStats(tctx, eventID)
	if err != nil {
		return db.GetEventBookingStatsRow{}, fmt.Errorf("repo_get_event_booking_stats: %w", err)
	}
	return row, nil
}

func (r *TicketingRepository) GetEventBarRevenueStats(ctx context.Context, eventID uuid.UUID) (db.GetEventBarRevenueStatsRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.GetEventBarRevenueStats(tctx, eventID)
	if err != nil {
		return db.GetEventBarRevenueStatsRow{}, fmt.Errorf("repo_get_event_bar_revenue_stats: %w", err)
	}
	return row, nil
}

func (r *TicketingRepository) GetEventGenderStats(ctx context.Context, eventID uuid.UUID) (db.GetEventGenderStatsRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.GetEventGenderStats(tctx, eventID)
	if err != nil {
		return db.GetEventGenderStatsRow{}, fmt.Errorf("repo_get_event_gender_stats: %w", err)
	}
	return row, nil
}

func (r *TicketingRepository) ListAdminEvents(ctx context.Context) ([]db.ListAdminEventsRow, error) {
	var rows []db.ListAdminEventsRow
	err := r.Read(ctx, func(tctx context.Context, q db.Querier) (err error) {
		rows, err = q.ListAdminEvents(tctx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("repo_list_admin_events: %w", err)
	}
	return rows, nil
}

func (r *TicketingRepository) GetTopBarItemsByEvent(ctx context.Context, arg db.GetTopBarItemsByEventParams) ([]db.GetTopBarItemsByEventRow, error) {
	var rows []db.GetTopBarItemsByEventRow
	err := r.Read(ctx, func(tctx context.Context, q db.Querier) (err error) {
		rows, err = q.GetTopBarItemsByEvent(tctx, arg)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("repo_get_top_bar_items: %w", err)
	}
	return rows, nil
}

func (r *TicketingRepository) GetBarSalesByBartender(ctx context.Context, eventID uuid.UUID) ([]db.GetBarSalesByBartenderRow, error) {
	var rows []db.GetBarSalesByBartenderRow
	err := r.Read(ctx, func(tctx context.Context, q db.Querier) (err error) {
		rows, err = q.GetBarSalesByBartender(tctx, eventID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("repo_get_bar_sales_by_bartender: %w", err)
	}
	return rows, nil
}

func (r *TicketingRepository) GetRecentGateEntries(ctx context.Context, arg db.GetRecentGateEntriesParams) ([]db.GetRecentGateEntriesRow, error) {
	var rows []db.GetRecentGateEntriesRow
	err := r.Read(ctx, func(tctx context.Context, q db.Querier) (err error) {
		rows, err = q.GetRecentGateEntries(tctx, arg)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("repo_get_recent_gate_entries: %w", err)
	}
	return rows, nil
}

func (r *TicketingRepository) SoftDeleteEvent(ctx context.Context, id uuid.UUID) (db.Event, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	ev, err := r.queries.SoftDeleteEvent(tctx, id)
	if err != nil {
		return db.Event{}, fmt.Errorf("repo_soft_delete_event: %w", err)
	}
	return ev, nil
}

func (r *TicketingRepository) UpdateEvent(ctx context.Context, arg db.UpdateEventParams) (db.Event, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	ev, err := r.queries.UpdateEvent(tctx, arg)
	if err != nil {
		return db.Event{}, fmt.Errorf("repo_update_event: %w", err)
	}
	return ev, nil
}

func (r *TicketingRepository) UpdateTicketCategory(ctx context.Context, arg db.UpdateTicketCategoryParams) (db.TicketCategory, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	cat, err := r.queries.UpdateTicketCategory(tctx, arg)
	if err != nil {
		return db.TicketCategory{}, fmt.Errorf("repo_update_ticket_category: %w", err)
	}
	return cat, nil
}

func (r *TicketingRepository) UpdateTicketCategoryPrice(ctx context.Context, arg db.UpdateTicketCategoryPriceParams) (db.TicketCategory, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	cat, err := r.queries.UpdateTicketCategoryPrice(tctx, arg)
	if err != nil {
		return db.TicketCategory{}, fmt.Errorf("repo_update_ticket_category_price: %w", err)
	}
	return cat, nil
}

func (r *TicketingRepository) CreateArtist(ctx context.Context, arg db.CreateArtistParams) (db.EventArtist, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	artist, err := r.queries.CreateArtist(tctx, arg)
	if err != nil {
		return db.EventArtist{}, fmt.Errorf("repo_create_artist: %w", err)
	}
	return artist, nil
}

func (r *TicketingRepository) ListArtistsByEvent(ctx context.Context, eventID uuid.UUID) ([]db.EventArtist, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	artists, err := r.queries.ListArtistsByEvent(tctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("repo_list_artists_by_event: %w", err)
	}
	return artists, nil
}

func (r *TicketingRepository) UpdateArtist(ctx context.Context, arg db.UpdateArtistParams) (db.EventArtist, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	artist, err := r.queries.UpdateArtist(tctx, arg)
	if err != nil {
		return db.EventArtist{}, fmt.Errorf("repo_update_artist: %w", err)
	}
	return artist, nil
}

func (r *TicketingRepository) DeleteArtist(ctx context.Context, id uuid.UUID) error {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()
	if err := r.queries.DeleteArtist(tctx, id); err != nil {
		return fmt.Errorf("repo_delete_artist: %w", err)
	}
	return nil
}
