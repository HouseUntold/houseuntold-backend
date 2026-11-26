package seating

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/httpx"
	"github.com/venu-live/venu-api/internal/realtime"
	"github.com/venu-live/venu-api/internal/repositories"
)

type SeatConfigInput struct {
	Section     string `json:"section"`
	TableSize   int32  `json:"table_size"`
	TotalTables int32  `json:"total_tables"`
	TableTotal  int32  `json:"table_total"`
	FnbCredit   int32  `json:"fnb_credit"`
}

type SeatSelectionInput struct {
	Section   string `json:"section"`
	TableSize int32  `json:"table_size"`
}

type SeatInventoryView struct {
	Section          string    `json:"section"`
	TableSize        int32     `json:"table_size"`
	TotalTables      int32     `json:"total_tables"`
	ReservedTables   int32     `json:"reserved_tables"`
	AvailableTables  int32     `json:"available_tables"`
	TableTotal       int32     `json:"table_total"`
	FnbCredit        int32     `json:"fnb_credit"`
	TicketCategoryID uuid.UUID `json:"ticket_category_id,omitempty"`
}

type SeatView struct {
	Section     string `json:"section"`
	TableSize   int32  `json:"table_size"`
	TableNumber int32  `json:"table_number"`
	IsBooked    bool   `json:"is_booked"`
	TableTotal  int32  `json:"table_total"`
	FnbCredit   int32  `json:"fnb_credit"`
}

type SeatSnapshot struct {
	EventID     uuid.UUID           `json:"event_id"`
	Groups      []SeatInventoryView `json:"groups"`
	Tables      []SeatView          `json:"tables"`
	GeneratedAt time.Time           `json:"generated_at"`
}

type Service struct {
	repo   *repositories.TicketingRepository
	hub    *realtime.SeatHub
	logger *slog.Logger
}

func NewService(repo *repositories.TicketingRepository, hub *realtime.SeatHub, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{repo: repo, hub: hub, logger: logger}
}

func (s *Service) Snapshot(ctx context.Context, eventID uuid.UUID) (SeatSnapshot, error) {
	snapshot := SeatSnapshot{EventID: eventID, Groups: []SeatInventoryView{}, Tables: []SeatView{}, GeneratedAt: time.Now().UTC()}
	err := s.repo.Read(ctx, func(tctx context.Context, q db.Querier) error {
		if _, err := q.GetEventByID(tctx, eventID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewNotFound("event not found")
			}
			return err
		}
		groups, err := q.ListEventSeatInventory(tctx, eventID)
		if err != nil {
			return err
		}
		for _, row := range groups {
			view := SeatInventoryView{
				Section:         row.Section,
				TableSize:       row.TableSize,
				TotalTables:     row.TotalTables,
				ReservedTables:  row.ReservedTables,
				AvailableTables: row.AvailableTables,
			}
			if row.TicketCategoryID.Valid {
				view.TicketCategoryID = row.TicketCategoryID.Bytes
				if row.TableTotal.Valid { view.TableTotal = row.TableTotal.Int32 }
				if row.FnbCredit.Valid { view.FnbCredit = row.FnbCredit.Int32 }
			} else {
				view.TableTotal = defaultTableAmount(row.TableSize, row.Section)
				view.FnbCredit = view.TableTotal
			}
			snapshot.Groups = append(snapshot.Groups, view)
		}
		tables, err := q.ListEventSeats(tctx, eventID)
		if err != nil {
			return err
		}
		for _, row := range tables {
			view := SeatView{
				Section:     row.Section,
				TableSize:   row.TableSize,
				TableNumber: row.TableNumber,
				IsBooked:    row.IsBooked,
			}
			if row.TableTotal.Valid || row.FnbCredit.Valid {
				if row.TableTotal.Valid { view.TableTotal = row.TableTotal.Int32 }
				if row.FnbCredit.Valid { view.FnbCredit = row.FnbCredit.Int32 }
			} else {
				view.TableTotal = defaultTableAmount(row.TableSize, row.Section)
				view.FnbCredit = view.TableTotal
			}
			snapshot.Tables = append(snapshot.Tables, view)
		}
		return nil
	})
	if err != nil {
		return SeatSnapshot{}, fmt.Errorf("svc_seating_snapshot: %w", err)
	}
	return snapshot, nil
}

func (s *Service) Configure(ctx context.Context, eventID uuid.UUID, rows []SeatConfigInput) (SeatSnapshot, error) {
	if len(rows) == 0 {
		return SeatSnapshot{}, httpx.NewBadRequest("at least one seating row is required", nil)
	}
	seen := make(map[string]struct{}, len(rows))
	for i := range rows {
		if err := validateSeatConfig(&rows[i]); err != nil {
			return SeatSnapshot{}, err
		}
		key := fmt.Sprintf("%s-%d", rows[i].Section, rows[i].TableSize)
		if _, ok := seen[key]; ok {
			return SeatSnapshot{}, httpx.NewBadRequest("duplicate section/table_size row", nil)
		}
		seen[key] = struct{}{}
	}

	err := s.repo.WithTx(ctx, func(tctx context.Context, q db.Querier) error {
		if _, err := q.GetEventByIDForUpdate(tctx, eventID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewNotFound("event not found")
			}
			return err
		}
		for _, row := range rows {
			var reserved int32
			_, lookupErr := q.GetSeatingInventoryForUpdate(tctx, db.GetSeatingInventoryForUpdateParams{
				EventID:   eventID,
				Section:   row.Section,
				TableSize: row.TableSize,
			})
			if lookupErr == nil {
				existing, err := q.ListEventSeatInventory(tctx, eventID)
				if err != nil {
					return err
				}
				for _, item := range existing {
					if item.Section == row.Section && item.TableSize == row.TableSize {
						reserved = item.ReservedTables
						break
					}
				}
				if row.TotalTables < reserved {
					return httpx.NewConflict(fmt.Sprintf("%s-%d has %d reserved tables", row.Section, row.TableSize, reserved), nil)
				}
			} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
				return lookupErr
			}

			categoryText := pgtype.Text{String: row.Section, Valid: true}
			category, err := q.GetGroupTicketCategory(tctx, db.GetGroupTicketCategoryParams{
				EventID:    eventID,
				Category:   categoryText,
				EntryCount: row.TableSize,
			})
			if err == nil {
				category, err = q.UpdateGroupTicketCategory(tctx, db.UpdateGroupTicketCategoryParams{
					ID:        category.ID,
					BasePrice: row.TableTotal,
					FnbCredit: row.FnbCredit,
				})
			} else if errors.Is(err, pgx.ErrNoRows) && (row.TotalTables > 0 || row.TableTotal > 0 || row.FnbCredit > 0) {
				category, err = q.CreateTicketCategory(tctx, db.CreateTicketCategoryParams{
					EventID:        eventID,
					Name:           fmt.Sprintf("GROUP_%s_%d", row.Section, row.TableSize),
					Category:       categoryText,
					BasePrice:      row.TableTotal,
					FnbCredit:      row.FnbCredit,
					EntryCount:     row.TableSize,
					IsGenderStrict: false,
				})
			} else if errors.Is(err, pgx.ErrNoRows) {
				err = nil
			}
			if err != nil {
				return err
			}
			ticketCategoryID := pgtype.UUID{}
			if category.ID != uuid.Nil {
				ticketCategoryID = pgtype.UUID{Bytes: category.ID, Valid: true}
			}
			_, err = q.UpsertSeatingInventory(tctx, db.UpsertSeatingInventoryParams{
				EventID:          eventID,
				Section:          row.Section,
				TableSize:        row.TableSize,
				TotalTables:      row.TotalTables,
				TicketCategoryID: ticketCategoryID,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return SeatSnapshot{}, fmt.Errorf("svc_seating_configure: %w", err)
	}
	snapshot, err := s.Snapshot(ctx, eventID)
	if err != nil {
		return SeatSnapshot{}, err
	}
	s.Broadcast(eventID, snapshot)
	return snapshot, nil
}

func (s *Service) Acquire(ctx context.Context, q db.Querier, eventID uuid.UUID, selection SeatSelectionInput, bookingID, ticketID uuid.UUID) (db.EventSeatReservation, error) {
	selection.Section = strings.ToUpper(strings.TrimSpace(selection.Section))
	if selection.Section != "A" && selection.Section != "B" && selection.Section != "C" {
		return db.EventSeatReservation{}, httpx.NewBadRequest("section must be A, B, or C", nil)
	}
	if selection.TableSize != 5 && selection.TableSize != 8 && selection.TableSize != 10 && selection.TableSize != 15 {
		return db.EventSeatReservation{}, httpx.NewBadRequest("table_size must be 5, 8, 10, or 15", nil)
	}
	reservation, err := q.AcquireGroupSeatReservation(ctx, db.AcquireGroupSeatReservationParams{
		EventID:   eventID,
		Section:   selection.Section,
		TableSize: selection.TableSize,
		BookingID: bookingID,
		TicketID:  pgtype.UUID{Bytes: ticketID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.EventSeatReservation{}, httpx.NewConflict("no seats available in selected section", nil)
	}
	if err != nil {
		return db.EventSeatReservation{}, fmt.Errorf("svc_seating_acquire: %w", err)
	}
	return reservation, nil
}

func (s *Service) Broadcast(eventID uuid.UUID, snapshot SeatSnapshot) {
	if s.hub == nil {
		return
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		s.logger.Warn("seat_broadcast_marshal", "event_id", eventID, "error", err)
		return
	}
	s.hub.Broadcast(eventID, payload)
}

func (s *Service) BroadcastLatest(ctx context.Context, eventID uuid.UUID) {
	snapshot, err := s.Snapshot(ctx, eventID)
	if err != nil {
		s.logger.Warn("seat_broadcast_snapshot", "event_id", eventID, "error", err)
		return
	}
	s.Broadcast(eventID, snapshot)
}

func validateSeatConfig(row *SeatConfigInput) error {
	row.Section = strings.ToUpper(strings.TrimSpace(row.Section))
	if row.Section != "A" && row.Section != "B" && row.Section != "C" {
		return httpx.NewBadRequest("section must be A, B, or C", nil)
	}
	if row.TableSize != 5 && row.TableSize != 8 && row.TableSize != 10 && row.TableSize != 15 {
		return httpx.NewBadRequest("table_size must be 5, 8, 10, or 15", nil)
	}
	if row.TotalTables < 0 || row.TotalTables > 10000 {
		return httpx.NewBadRequest("total_tables must be between 0 and 10000", nil)
	}
	if row.TableTotal < 0 || row.TableTotal > 1000000 {
		return httpx.NewBadRequest("table amount must be between 0 and 1000000", nil)
	}
	if row.FnbCredit < 0 || row.FnbCredit > 1000000 {
		return httpx.NewBadRequest("fnb_credit must be between 0 and 1000000", nil)
	}
	if row.TotalTables > 0 && row.TableTotal < 1 {
		return httpx.NewBadRequest("table amount must be between 1 and 1000000 when total_tables > 0", nil)
	}
	return nil
}

func defaultTableAmount(size int32, section string) int32 {
	matrix := map[int32]map[string]int32{
		5:  {"A": 14999, "B": 18999, "C": 21999},
		8:  {"A": 19999, "B": 22999, "C": 24999},
		10: {"A": 24999, "B": 27999, "C": 29999},
		15: {"A": 31999, "B": 32999, "C": 34999},
	}
	if bySection, ok := matrix[size]; ok {
		if amount, ok := bySection[strings.ToUpper(strings.TrimSpace(section))]; ok { return amount }
	}
	return 0
}
