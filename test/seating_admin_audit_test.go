package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/services"
	"github.com/venu-live/venu-api/internal/services/seating"
)

func configureSeating(t *testing.T, env *testEnv, token string, rows ...seating.SeatConfigInput) seating.SeatSnapshot {
	t.Helper()
	rec := env.doRequest(http.MethodPut, "/api/admin/events/"+env.eventID.String()+"/seating", map[string]any{"rows": rows}, bearerHeader(token))
	assertStatus(t, rec, http.StatusOK)
	var snapshot seating.SeatSnapshot
	decodeBody(t, rec, &snapshot)
	return snapshot
}

func groupGuests(n int) []services.GuestInput {
	guests := make([]services.GuestInput, n)
	for i := range guests {
		guests[i] = services.GuestInput{
			Name:             fmt.Sprintf("Group Guest %d", i+1),
			Age:              25,
			Gender:           db.GenderTypeOTHER,
			IsPrimaryContact: i == 0,
		}
	}
	return guests
}

func groupCheckoutRequest(categoryID, eventID uuid.UUID, phone, email string, tableSize int32) services.InitCheckoutRequest {
	return services.InitCheckoutRequest{
		EventID:       eventID,
		CategoryID:    categoryID,
		ContactPhone:  phone,
		ContactEmail:  email,
		SeatSelection: &seating.SeatSelectionInput{Section: "A", TableSize: tableSize},
		Guests:        groupGuests(int(tableSize)),
	}
}

func waitForAuditLogs(t *testing.T, env *testEnv, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		env.store.mu.Lock()
		got := len(env.store.auditLogs)
		env.store.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	env.store.mu.Lock()
	got := len(env.store.auditLogs)
	env.store.mu.Unlock()
	t.Fatalf("audit logs = %d, want >= %d", got, want)
}

func TestPublicEventsAndSeatingSnapshots(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	_, err := env.store.CreateEvent(context.Background(), db.CreateEventParams{
		Title:    "Hidden Draft",
		StartsAt: time.Now().UTC().Add(24 * time.Hour),
		EndsAt:   time.Now().UTC().Add(26 * time.Hour),
		Location: "Backstage",
		Status:   db.EventStatusDRAFT,
	})
	if err != nil {
		t.Fatalf("seed draft event: %v", err)
	}

	snapshot := configureSeating(t, env, adminToken,
		seating.SeatConfigInput{Section: "A", TableSize: 10, TotalTables: 1, TableTotal: 2500, FnbCredit: 1000},
		seating.SeatConfigInput{Section: "B", TableSize: 15, TotalTables: 2, TableTotal: 3000, FnbCredit: 1500},
	)
	if len(snapshot.Groups) != 2 || len(snapshot.Tables) != 3 {
		t.Fatalf("unexpected configured seating snapshot: %+v", snapshot)
	}

	rec := env.doRequest(http.MethodGet, "/api/events?status=ALL&limit=50", nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var list struct {
		Events []services.PublicEventView `json:"events"`
		Count  int                        `json:"count"`
	}
	decodeBody(t, rec, &list)
	if list.Count != 1 || len(list.Events) != 1 || list.Events[0].ID != env.eventID {
		t.Fatalf("public events must exclude drafts: count=%d events=%+v", list.Count, list.Events)
	}

	rec = env.doRequest(http.MethodGet, "/api/events/"+env.eventID.String()+"/seats", nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var seats seating.SeatSnapshot
	decodeBody(t, rec, &seats)
	if len(seats.Groups) != 2 || seats.Groups[0].TableTotal != 2500 || seats.Groups[1].AvailableTables != 2 {
		t.Fatalf("unexpected public seat inventory: %+v", seats.Groups)
	}

	rec = env.doRequest(http.MethodGet, "/api/events/"+env.eventID.String(), nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var details services.EventDetailsView
	decodeBody(t, rec, &details)
	if len(details.Seats) != 2 || len(details.Tiers) != 5 {
		t.Fatalf("event details missing seat/tier inventory: seats=%d tiers=%d", len(details.Seats), len(details.Tiers))
	}
}

func TestGroupCheckoutReservesTableWithTableTotalPricing(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	snapshot := configureSeating(t, env, adminToken, seating.SeatConfigInput{
		Section:     "A",
		TableSize:   10,
		TotalTables: 1,
		TableTotal:  2500,
		FnbCredit:   1000,
	})
	categoryID := snapshot.Groups[0].TicketCategoryID
	if categoryID == uuid.Nil {
		t.Fatalf("group seating did not create ticket category: %+v", snapshot.Groups[0])
	}

	rec := env.doRequest(http.MethodPost, "/api/checkout/init", groupCheckoutRequest(categoryID, env.eventID, "+919800000001", "group1@example.com", 10), nil)
	assertStatus(t, rec, http.StatusCreated)
	var pass services.InitCheckoutResponse
	decodeBody(t, rec, &pass)
	if pass.TotalAmount != 2500 || pass.FnbCredit != 1000 || pass.SeatSection != "A" || pass.SeatTableSize != 10 || pass.SeatTableNumber != 1 {
		t.Fatalf("unexpected group checkout response: %+v", pass)
	}

	rec = env.doRequest(http.MethodGet, "/api/events/"+env.eventID.String()+"/seats", nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var seats seating.SeatSnapshot
	decodeBody(t, rec, &seats)
	if len(seats.Groups) != 1 || seats.Groups[0].ReservedTables != 1 || seats.Groups[0].AvailableTables != 0 {
		t.Fatalf("group table was not reserved: %+v", seats.Groups)
	}

	rec = env.doRequest(http.MethodPost, "/api/checkout/init", groupCheckoutRequest(categoryID, env.eventID, "+919800000002", "group2@example.com", 10), nil)
	assertStatus(t, rec, http.StatusConflict)
}

func TestSeatWebsocketBroadcastsConfigAndReservation(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	httpServer := httptest.NewServer(env.router)
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/events/" + env.eventID.String() + "/seats/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skip("skipping websocket live network dial: socket dial restricted in sandbox")
		}
		t.Fatalf("dial seat websocket: %v", err)
	}
	defer conn.Close()

	readSnapshot := func() seating.SeatSnapshot {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatalf("set websocket deadline: %v", err)
		}
		var snapshot seating.SeatSnapshot
		if err := conn.ReadJSON(&snapshot); err != nil {
			t.Fatalf("read websocket snapshot: %v", err)
		}
		return snapshot
	}

	initial := readSnapshot()
	if len(initial.Groups) != 0 {
		t.Fatalf("unexpected initial seat snapshot: %+v", initial.Groups)
	}

	snapshot := configureSeating(t, env, adminToken, seating.SeatConfigInput{
		Section:     "A",
		TableSize:   10,
		TotalTables: 1,
		TableTotal:  2500,
		FnbCredit:   1000,
	})
	configured := readSnapshot()
	if len(configured.Groups) != 1 || configured.Groups[0].AvailableTables != 1 {
		t.Fatalf("expected config broadcast, got %+v", configured.Groups)
	}

	rec := env.doRequest(http.MethodPost, "/api/checkout/init", groupCheckoutRequest(snapshot.Groups[0].TicketCategoryID, env.eventID, "+919800000003", "group3@example.com", 10), nil)
	assertStatus(t, rec, http.StatusCreated)
	reserved := readSnapshot()
	if len(reserved.Groups) != 1 || reserved.Groups[0].ReservedTables != 1 || reserved.Groups[0].AvailableTables != 0 {
		t.Fatalf("expected reservation broadcast, got %+v", reserved.Groups)
	}
}

func TestBartenderDeltaCollectionMetadata(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")
	barToken, _ := env.loginAs("bar_user", "BarPass!123")

	pass := confirmedInactiveFemaleTicket(t, env, adminToken, "+919800000010", "delta@example.com", "Anya Rao", "998877665544")

	req := services.RedeemItemsRequest{
		BarCode:            pass.BarCode,
		Items:              []services.RedeemLineItemInput{{MenuItemID: env.drinkID, Quantity: 2}},
		DeltaPaymentMethod: "CASH",
		DeltaCollected:     true,
	}
	headers := map[string]string{
		"Authorization":   "Bearer " + barToken,
		"Idempotency-Key": "delta-order-001",
	}
	rec := env.doRequest(http.MethodPost, "/api/counter/redeem", req, headers)
	assertStatus(t, rec, http.StatusOK)
	var redeem services.RedeemItemsResponse
	decodeBody(t, rec, &redeem)
	if redeem.DeltaToCollect != 500 || redeem.DeltaPaymentMethod != "CASH" || !redeem.DeltaCollected || redeem.IdempotentReplay {
		t.Fatalf("unexpected delta redemption: %+v", redeem)
	}

	rec = env.doRequest(http.MethodPost, "/api/counter/redeem", req, headers)
	assertStatus(t, rec, http.StatusOK)
	var replay services.RedeemItemsResponse
	decodeBody(t, rec, &replay)
	if !replay.IdempotentReplay || replay.DeltaPaymentMethod != "CASH" || !replay.DeltaCollected {
		t.Fatalf("idempotent replay lost delta metadata: %+v", replay)
	}
}

func TestAdminEventsAuditLogsAndExtendedStats(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")
	scannerToken, _ := env.loginAs("gate_user", "GatePass!123")
	barToken, _ := env.loginAs("bar_user", "BarPass!123")

	rec := env.doRequest(http.MethodPost, "/api/checkout/init", services.InitCheckoutRequest{
		EventID:      env.eventID,
		CategoryID:   env.femaleCatID,
		ContactPhone: "+919800000020",
		ContactEmail: "analytics@example.com",
		Guests:       []services.GuestInput{{Name: "Ira Kapoor", Age: 26, Gender: db.GenderTypeFEMALE, IsPrimaryContact: true}},
	}, nil)
	assertStatus(t, rec, http.StatusCreated)
	var pass services.InitCheckoutResponse
	decodeBody(t, rec, &pass)

	rec = env.doRequest(http.MethodPost, "/api/gate/check-in", map[string]string{"qr_token": pass.GateQRToken}, bearerHeader(scannerToken))
	assertStatus(t, rec, http.StatusOK)
	env.store.mu.Lock()
	checkedInBy := env.store.tickets[pass.TicketID].CheckedInBy
	env.store.mu.Unlock()
	if !checkedInBy.Valid || checkedInBy.Bytes != env.scannerID {
		t.Fatalf("check-in did not persist gatekeeper id: %+v", checkedInBy)
	}

	rec = env.doRequest(http.MethodPost, "/api/counter/redeem", services.RedeemItemsRequest{
		BarCode: pass.BarCode,
		Items:   []services.RedeemLineItemInput{{MenuItemID: env.drinkID, Quantity: 1}},
	}, map[string]string{
		"Authorization":   "Bearer " + barToken,
		"Idempotency-Key": "analytics-order-001",
	})
	assertStatus(t, rec, http.StatusOK)

	rec = env.doRequest(http.MethodGet, "/api/admin/events", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var events struct {
		Events []services.AdminEventView `json:"events"`
		Count  int                       `json:"count"`
	}
	decodeBody(t, rec, &events)
	if events.Count != 1 || len(events.Events) != 1 || events.Events[0].ID != env.eventID {
		t.Fatalf("unexpected admin events: count=%d events=%+v", events.Count, events.Events)
	}

	waitForAuditLogs(t, env, 5)
	rec = env.doRequest(http.MethodGet, "/api/admin/audit-logs?path=/api/admin/events&limit=20", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var logs struct {
		Logs  []services.AuditLogView `json:"logs"`
		Count int                     `json:"count"`
	}
	decodeBody(t, rec, &logs)
	if logs.Count == 0 {
		t.Fatal("expected audit logs for /api/admin/events")
	}
	found := false
	for _, log := range logs.Logs {
		if log.Route == "/api/admin/events" && log.ActorID != nil && *log.ActorID == env.adminID && log.ActorRole == "ADMIN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit log missing admin actor/route metadata: %+v", logs.Logs)
	}

	rec = env.doRequest(http.MethodGet, "/api/admin/stats/"+env.eventID.String(), nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var stats services.EventStatsView
	decodeBody(t, rec, &stats)
	if stats.BarTransactions != 1 || stats.TotalBarBill != 750 || len(stats.TopBarItems) != 1 || len(stats.BarSalesByBartender) != 1 {
		t.Fatalf("unexpected bar analytics: %+v", stats)
	}
	if stats.TopBarItems[0].ItemName != "Smoked Negroni" || stats.TopBarItems[0].Quantity != 1 {
		t.Fatalf("unexpected top bar item: %+v", stats.TopBarItems[0])
	}
	if stats.BarSalesByBartender[0].BartenderUsername != "bar_user" {
		t.Fatalf("unexpected bartender sales: %+v", stats.BarSalesByBartender[0])
	}
	if len(stats.RecentGateEntries) != 1 || stats.RecentGateEntries[0].TicketID != pass.TicketID || stats.RecentGateEntries[0].GatekeeperUsername != "gate_user" {
		t.Fatalf("unexpected recent gate entries: %+v", stats.RecentGateEntries)
	}
}
