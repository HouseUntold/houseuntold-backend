package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/config"
	"github.com/venu-live/venu-api/internal/db"
)

type manifest struct {
	AdminID            string `json:"admin_id"`
	ScannerID          string `json:"scanner_id"`
	BartenderID        string `json:"bartender_id"`
	AdminToken         string `json:"admin_token"`
	AdminRefreshToken  string `json:"admin_refresh_token"`
	ScannerToken       string `json:"scanner_token"`
	BartenderToken     string `json:"bartender_token"`
	EventID            string `json:"event_id"`
	EndedEventID       string `json:"ended_event_id"`
	CoupleCategoryID   string `json:"couple_category_id"`
	FemaleCategoryID   string `json:"female_category_id"`
	PublicTicketID     string `json:"public_ticket_id"`
	GateTicketID       string `json:"gate_ticket_id"`
	GateQRToken        string `json:"gate_qr_token"`
	GateBarCode        string `json:"gate_bar_code"`
	BarPartialTicketID string `json:"bar_partial_ticket_id"`
	BarPartialBarCode  string `json:"bar_partial_bar_code"`
	BarConcurrentID    string `json:"bar_concurrent_ticket_id"`
	BarConcurrentCode  string `json:"bar_concurrent_bar_code"`
	BeerItemID         string `json:"beer_item_id"`
	BurgerItemID       string `json:"burger_item_id"`
	CocktailItemID     string `json:"cocktail_item_id"`
	PremiumItemID      string `json:"premium_item_id"`
	TotalEvents        int    `json:"total_events"`
	TotalMenuItems     int    `json:"total_menu_items"`
	TotalTickets       int    `json:"total_tickets"`
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()

	m, err := seed(ctx, pool, cfg)
	if err != nil {
		fatal(err)
	}

	path := os.Getenv("SEED_MANIFEST")
	if path == "" {
		path = ".loadtest/seed.json"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatal(err)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		fatal(err)
	}
	fmt.Printf("seed complete: events=%d menu_items=%d tickets=%d manifest=%s\n",
		m.TotalEvents, m.TotalMenuItems, m.TotalTickets, path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func stable(key string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("venu-loadtest:"+key))
}

func seed(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) (manifest, error) {
	var m manifest
	if _, err := pool.Exec(ctx, `TRUNCATE TABLE
		transaction_line_items, bar_transactions, ticket_guests, tickets,
		bookings, rate_limits, menu_items, ticket_categories, events,
		staff_accounts RESTART IDENTITY CASCADE`); err != nil {
		return m, fmt.Errorf("truncate: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	tokenMgr := auth.NewTokenManager(
		cfg.JWTAccessSecret, cfg.JWTRefreshSecret,
		cfg.JWTAudience, cfg.JWTIssuer,
		cfg.AccessTokenTTL, cfg.RefreshTokenTTL,
	)
	qrSigner := auth.NewQRSigner(cfg.QRSigningSecret)

	adminID := stable("staff:admin")
	scannerID := stable("staff:scanner")
	bartenderID := stable("staff:bartender")
	staffRows := make([][]any, 0, 3)
	staffAccounts := make([]db.StaffAccount, 0, 3)
	for _, s := range []struct {
		id       uuid.UUID
		username string
		password string
		role     string
	}{
		{adminID, "load_admin", "AdminPass!123", "ADMIN"},
		{scannerID, "load_scanner", "ScannerPass!123", "SCANNER"},
		{bartenderID, "load_bartender", "BartenderPass!123", "BARTENDER"},
	} {
		hash, err := auth.HashPassword(s.password)
		if err != nil {
			return m, fmt.Errorf("hash %s: %w", s.username, err)
		}
		staffRows = append(staffRows, []any{s.id, s.username, hash, s.role, true, now})
		staffAccounts = append(staffAccounts, db.StaffAccount{
			ID: s.id, Username: s.username, PasswordHash: hash,
			Role: db.StaffRole(s.role), IsActive: true, CreatedAt: now,
		})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"staff_accounts"},
		[]string{"id", "username", "password_hash", "role", "is_active", "created_at"},
		pgx.CopyFromRows(staffRows)); err != nil {
		return m, fmt.Errorf("copy staff: %w", err)
	}
	for i := range staffAccounts {
		pair, err := tokenMgr.IssueStaffTokens(staffAccounts[i])
		if err != nil {
			return m, fmt.Errorf("issue token %s: %w", staffAccounts[i].Username, err)
		}
		switch staffAccounts[i].Username {
		case "load_admin":
			m.AdminID, m.AdminToken = staffAccounts[i].ID.String(), pair.AccessToken
			m.AdminRefreshToken = pair.RefreshToken
		case "load_scanner":
			m.ScannerID, m.ScannerToken = staffAccounts[i].ID.String(), pair.AccessToken
		case "load_bartender":
			m.BartenderID, m.BartenderToken = staffAccounts[i].ID.String(), pair.AccessToken
		}
	}

	const eventCount = 120
	eventIDs := make([]uuid.UUID, eventCount)
	eventRows := make([][]any, 0, eventCount)
	for i := 0; i < eventCount; i++ {
		eventIDs[i] = stable(fmt.Sprintf("event:%d", i))
		starts := now.Add(time.Duration(i) * 6 * time.Hour)
		ends := starts.Add(10 * time.Hour)
		status := "ON_SALE"
		switch {
		case i == 0:
			starts, ends = now.Add(-time.Hour), now.Add(12*time.Hour)
		case i >= 80 && i < 110:
			starts = now.Add(-time.Duration(i-79) * 24 * time.Hour)
			ends = starts.Add(6 * time.Hour)
			status = "ENDED"
		case i >= 110:
			status = "DRAFT"
			if i%2 == 0 {
				status = "SOLD_OUT"
			}
		}
		eventRows = append(eventRows, []any{
			eventIDs[i], fmt.Sprintf("Load Test Event %03d", i),
			starts, ends, fmt.Sprintf("Sector %d, Bengaluru", i), status, now,
		})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"events"},
		[]string{"id", "title", "starts_at", "ends_at", "location", "status", "created_at"},
		pgx.CopyFromRows(eventRows)); err != nil {
		return m, fmt.Errorf("copy events: %w", err)
	}
	m.EventID = eventIDs[0].String()
	m.EndedEventID = eventIDs[80].String()
	m.TotalEvents = eventCount

	coupleCatIDs := make(map[uuid.UUID]uuid.UUID, eventCount)
	femaleCatIDs := make(map[uuid.UUID]uuid.UUID, eventCount)
	groupCatIDs := make(map[string]uuid.UUID, eventCount*12)
	catRows := make([][]any, 0, eventCount*15)
	for _, eventID := range eventIDs {
		for _, cat := range []struct {
			name   string
			price  int32
			credit int32
			entry  int32
			strict bool
		}{
			{"COUPLE", 3000, 2000, 2, true},
			{"STAG_FEMALE", 1500, 1000, 1, false},
			{"STAG_MALE", 2000, 1000, 1, false},
		} {
			catID := stable("category:" + eventID.String() + ":" + cat.name)
			switch cat.name {
			case "COUPLE":
				coupleCatIDs[eventID] = catID
			case "STAG_FEMALE":
				femaleCatIDs[eventID] = catID
			}
			catRows = append(catRows, []any{
				catID, eventID, cat.name, "MAIN_FLOOR",
				cat.price, cat.credit, cat.entry, cat.strict,
			})
		}
		for _, section := range []string{"A", "B", "C"} {
			for _, size := range []int32{5, 8, 10, 15} {
				name := fmt.Sprintf("GROUP_%s_%d", section, size)
				price := map[string]map[int32]int32{
					"A": {5: 5000, 8: 4000, 10: 3500, 15: 2300},
					"B": {5: 4000, 8: 3200, 10: 2800, 15: 1900},
					"C": {5: 3200, 8: 2600, 10: 2200, 15: 1500},
				}[section][size]
				catID := stable("category:" + eventID.String() + ":" + name)
				groupCatIDs[eventID.String()+":"+section+":"+fmt.Sprint(size)] = catID
				catRows = append(catRows, []any{catID, eventID, name, section, price, 0, size, false})
			}
		}
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"ticket_categories"},
		[]string{"id", "event_id", "name", "category", "base_price", "fnb_credit", "entry_count", "is_gender_strict"},
		pgx.CopyFromRows(catRows)); err != nil {
		return m, fmt.Errorf("copy categories: %w", err)
	}

	seatRows := make([][]any, 0, eventCount*12)
	for _, eventID := range eventIDs {
		for _, section := range []string{"A", "B", "C"} {
			for _, size := range []int32{5, 8, 10, 15} {
				catID := groupCatIDs[eventID.String()+":"+section+":"+fmt.Sprint(size)]
				seatRows = append(seatRows, []any{eventID, section, size, 10, catID, now})
			}
		}
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"event_seating_inventory"},
		[]string{"event_id", "section", "table_size", "total_tables", "ticket_category_id", "updated_at"},
		pgx.CopyFromRows(seatRows)); err != nil {
		return m, fmt.Errorf("copy seating inventory: %w", err)
	}
	m.CoupleCategoryID = coupleCatIDs[eventIDs[0]].String()
	m.FemaleCategoryID = femaleCatIDs[eventIDs[0]].String()

	const menuCount = 120
	special := []struct {
		id    string
		name  string
		cat   string
		price int32
	}{
		{"beer", "Seed Beer 250", "DRINK", 250},
		{"burger", "Seed Burger 500", "FOOD", 500},
		{"cocktail", "Seed Cocktail 500", "DRINK", 500},
		{"premium", "Seed Premium Bottle 1500", "DRINK", 1500},
	}
	menuIDs := make([]uuid.UUID, menuCount)
	menuRows := make([][]any, 0, menuCount)
	for i := 0; i < menuCount; i++ {
		if i < len(special) {
			menuIDs[i] = stable("menu:" + special[i].id)
			menuRows = append(menuRows, []any{
				menuIDs[i], special[i].name, special[i].cat, special[i].price, true,
			})
			continue
		}
		menuIDs[i] = stable(fmt.Sprintf("menu:%d", i))
		category := "DRINK"
		if i%2 == 0 {
			category = "FOOD"
		}
		price := int32(100 + (i*137)%4901)
		menuRows = append(menuRows, []any{
			menuIDs[i], fmt.Sprintf("Seed Menu Item %03d", i), category, price, true,
		})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"menu_items"},
		[]string{"id", "name", "category", "unit_price", "is_active"},
		pgx.CopyFromRows(menuRows)); err != nil {
		return m, fmt.Errorf("copy menu: %w", err)
	}
	m.BeerItemID = menuIDs[0].String()
	m.BurgerItemID = menuIDs[1].String()
	m.CocktailItemID = menuIDs[2].String()
	m.PremiumItemID = menuIDs[3].String()
	m.TotalMenuItems = menuCount

	const totalTickets = 10000
	const bulkTickets = totalTickets - 3
	bookingRows := make([][]any, 0, totalTickets)
	ticketRows := make([][]any, 0, totalTickets)
	guestRows := make([][]any, 0, totalTickets*2)

	addTicket := func(label string, i int, eventID, catID uuid.UUID, barCode string, balance int32) (uuid.UUID, uuid.UUID) {
		bookingID := stable("booking:" + label)
		ticketID := stable("ticket:" + label)
		utr := fmt.Sprintf("LOADSEED%06d", i)
		if label != "bulk" {
			utr = "LOADDEDICATED" + label
		}
		bookingRows = append(bookingRows, []any{
			bookingID, eventID, catID,
			fmt.Sprintf("+9198%08d", i), fmt.Sprintf("seed%d@example.test", i),
			int32(3000), utr, "CONFIRMED", adminID, now, nil, now,
		})
		ticketRows = append(ticketRows, []any{
			ticketID, bookingID, eventID, qrSigner.ComputeDigest(ticketID),
			barCode, balance, balance, false, nil,
		})
		guestRows = append(guestRows, []any{
			stable("guest:" + label + ":m"), ticketID, "Seed Guest M " + label,
			int32(25), "MALE", true, false,
		})
		guestRows = append(guestRows, []any{
			stable("guest:" + label + ":f"), ticketID, "Seed Guest F " + label,
			int32(24), "FEMALE", false, false,
		})
		return ticketID, bookingID
	}

	for i := 0; i < bulkTickets; i++ {
		eventID := eventIDs[i%100]
		addTicket(fmt.Sprintf("bulk:%d", i), i, eventID, coupleCatIDs[eventID],
			fmt.Sprintf("A%04d", i), 2000)
	}

	dedicated := []struct {
		label string
		i     int
		code  string
		bal   int32
	}{
		{"gate", 9997, "Z0001", 2000},
		{"bar_partial", 9998, "Z0002", 3000},
		{"bar_concurrent", 9999, "Z0003", 3000},
	}
	dedicatedIDs := make(map[string]uuid.UUID, len(dedicated))
	for _, d := range dedicated {
		id, _ := addTicket("dedicated:"+d.label, d.i, eventIDs[0], coupleCatIDs[eventIDs[0]], d.code, d.bal)
		dedicatedIDs[d.label] = id
	}

	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"bookings"},
		[]string{
			"id", "event_id", "category_id", "contact_phone", "contact_email",
			"total_amount", "upi_transaction_id", "status", "verified_by",
			"verified_at", "rejection_reason", "created_at",
		}, pgx.CopyFromRows(bookingRows)); err != nil {
		return m, fmt.Errorf("copy bookings: %w", err)
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"tickets"},
		[]string{
			"id", "booking_id", "event_id", "qr_token_digest", "bar_code",
			"food_balance", "initial_food_balance", "is_checked_in", "checked_in_at",
		}, pgx.CopyFromRows(ticketRows)); err != nil {
		return m, fmt.Errorf("copy tickets: %w", err)
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"ticket_guests"},
		[]string{
			"id", "ticket_id", "name", "age", "gender",
			"is_primary_contact", "free_shot_redeemed",
		}, pgx.CopyFromRows(guestRows)); err != nil {
		return m, fmt.Errorf("copy guests: %w", err)
	}

	gateID := dedicatedIDs["gate"]
	m.GateTicketID = gateID.String()
	m.GateBarCode = "Z0001"
	m.GateQRToken = qrSigner.SignToken(gateID, eventIDs[0], now.Add(-time.Minute), now.Add(24*time.Hour))
	m.PublicTicketID = gateID.String()
	m.BarPartialTicketID = dedicatedIDs["bar_partial"].String()
	m.BarPartialBarCode = "Z0002"
	m.BarConcurrentID = dedicatedIDs["bar_concurrent"].String()
	m.BarConcurrentCode = "Z0003"
	m.TotalTickets = totalTickets

	if _, err := pool.Exec(ctx, `DELETE FROM rate_limits`); err != nil {
		return m, fmt.Errorf("clear rate limits: %w", err)
	}
	return m, nil
}
