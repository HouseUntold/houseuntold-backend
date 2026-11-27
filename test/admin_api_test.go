package test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/services"
)

// Covers Routes 18–25:
//  18. POST   /api/admin/staff
//  19. PATCH  /api/admin/staff/{id}/password
//  20. DELETE /api/admin/staff/{id}
//  21. GET    /api/admin/bookings
//  22. POST   /api/admin/bookings/{id}/verify
//  23. POST   /api/admin/events
//  24. POST   /api/admin/events/{id}/tiers
//  25. GET    /api/admin/stats/{event_id}
func TestAdminStaffLifecycleAndInstantCacheRevocation(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	// 18. POST /api/admin/staff -> 201 creates a new SCANNER
	rec := env.doRequest(http.MethodPost, "/api/admin/staff", services.CreateStaffRequest{
		Username: "gate_shift_2",
		Password: "ShiftPassword!1",
		Role:     db.StaffRoleSCANNER,
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusCreated)
	var created services.StaffAccountView
	decodeBody(t, rec, &created)
	if created.Role != db.StaffRoleSCANNER || !created.IsActive {
		t.Fatalf("unexpected created staff: %+v", created)
	}

	// Log in as new staff (populates StaffRoleCache) and verify token works
	staffToken, _ := env.loginAs("gate_shift_2", "ShiftPassword!1")
	if _, hit := env.roleCache.Get(created.ID); !hit {
		t.Fatal("expected new staff to be cached on login")
	}
	assertStatus(t, env.doRequest(http.MethodPost, "/api/gate/validate", map[string]string{
		"qr_token": "not-a-real-qr",
	}, bearerHeader(staffToken)), http.StatusUnauthorized) // reached GateService (not 403/401 auth)

	// GET /api/admin/staff -> 401 unauthenticated, 403 non-admin, 200 admin
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/staff", nil, nil), http.StatusUnauthorized)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/staff", nil, bearerHeader(staffToken)), http.StatusForbidden)
	recList := env.doRequest(http.MethodGet, "/api/admin/staff", nil, bearerHeader(adminToken))
	assertStatus(t, recList, http.StatusOK)
	var staffList []services.StaffAccountView
	decodeBody(t, recList, &staffList)
	if len(staffList) < 2 {
		t.Fatalf("expected at least 2 staff members, got %d", len(staffList))
	}

	// 19. PATCH /api/admin/staff/{id}/password -> 200 rotates password + evicts cache
	rec = env.doRequest(http.MethodPatch, "/api/admin/staff/"+created.ID.String()+"/password", map[string]string{
		"password": "RotatedPassword!2",
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	if _, hit := env.roleCache.Get(created.ID); hit {
		t.Fatal("expected StaffRoleCache eviction on password update")
	}
	// Old password fails; new password works
	assertStatus(t, env.doRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"username": "gate_shift_2",
		"password": "ShiftPassword!1",
	}, nil), http.StatusUnauthorized)
	staffToken, _ = env.loginAs("gate_shift_2", "RotatedPassword!2")

	// 20a. Admin cannot self-deactivate -> 400
	assertStatus(t, env.doRequest(http.MethodDelete, "/api/admin/staff/"+env.adminID.String(), nil, bearerHeader(adminToken)), http.StatusBadRequest)

	// 20b. DELETE /api/admin/staff/{id} -> 200 deactivates + evicts cache -> existing JWT immediately rejected with 401
	rec = env.doRequest(http.MethodDelete, "/api/admin/staff/"+created.ID.String(), nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)

	assertStatus(t, env.doRequest(http.MethodPost, "/api/gate/validate", map[string]string{
		"qr_token": "anything",
	}, bearerHeader(staffToken)), http.StatusUnauthorized)

	// 20c. Cannot deactivate the last admin
	// First create another admin
	recAdmin2 := env.doRequest(http.MethodPost, "/api/admin/staff", services.CreateStaffRequest{
		Username: "admin_two",
		Password: "AdminPassword!2",
		Role:     db.StaffRoleADMIN,
	}, bearerHeader(adminToken))
	assertStatus(t, recAdmin2, http.StatusCreated)
	var admin2 services.StaffAccountView
	decodeBody(t, recAdmin2, &admin2)

	// Deactivate admin2 (allowed since admin1 still active)
	assertStatus(t, env.doRequest(http.MethodDelete, "/api/admin/staff/"+admin2.ID.String(), nil, bearerHeader(adminToken)), http.StatusOK)

	// Now try to deactivate admin2 again -> 404 (already inactive)
	assertStatus(t, env.doRequest(http.MethodDelete, "/api/admin/staff/"+admin2.ID.String(), nil, bearerHeader(adminToken)), http.StatusNotFound)
}

func TestAdminHelpMessageSubmitListAndResolve(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	// 1. Guest submits help message
	rec := env.doRequest(http.MethodPost, "/api/help", services.CreateHelpRequest{
		Name:    "Rahul Verma",
		Phone:   "+919876543211",
		Message: "Need help changing attendee name on ticket",
	}, nil)
	assertStatus(t, rec, http.StatusCreated)
	var helpResp services.CreateHelpResponse
	decodeBody(t, rec, &helpResp)
	if helpResp.ID == [16]byte{} || helpResp.Message == "" {
		t.Fatalf("unexpected help response: %+v", helpResp)
	}

	// 2. Admin lists unresolved help messages -> sees 1 message
	rec = env.doRequest(http.MethodGet, "/api/admin/help?resolved=false", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var helpList struct {
		Messages []db.HelpMessage `json:"messages"`
		Count    int              `json:"count"`
	}
	decodeBody(t, rec, &helpList)
	if helpList.Count != 1 || helpList.Messages[0].Name != "Rahul Verma" || helpList.Messages[0].IsResolved {
		t.Fatalf("unexpected admin help list: %+v", helpList)
	}

	// 3. Admin resolves help message
	rec = env.doRequest(http.MethodPost, "/api/admin/help/"+helpResp.ID.String()+"/resolve", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var resolved db.HelpMessage
	decodeBody(t, rec, &resolved)
	if !resolved.IsResolved {
		t.Fatalf("expected message to be resolved, got %+v", resolved)
	}

	// 4. Listing unresolved help messages -> now 0
	rec = env.doRequest(http.MethodGet, "/api/admin/help?resolved=false", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	decodeBody(t, rec, &helpList)
	if helpList.Count != 0 {
		t.Fatalf("expected 0 unresolved help messages, got %d", helpList.Count)
	}
}

func TestAdminUPIVerificationEventsTiersAndStats(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")
	barToken, _ := env.loginAs("bar_user", "BarPass!123")

	// 23. POST /api/admin/events -> invalid time window 400, valid 201
	now := time.Now().UTC()
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/events", services.CreateEventRequest{
		Title:    "Bad Window",
		StartsAt: now.Add(2 * time.Hour),
		EndsAt:   now.Add(1 * time.Hour),
		Location: "Club",
		Status:   db.EventStatusONSALE,
	}, bearerHeader(adminToken)), http.StatusBadRequest)

	rec := env.doRequest(http.MethodPost, "/api/admin/events", services.CreateEventRequest{
		Title:    "Sunday Rooftop",
		StartsAt: now.Add(-30 * time.Minute),
		EndsAt:   now.Add(5 * time.Hour),
		Location: "Skyline Deck, Bengaluru",
		Status:   db.EventStatusONSALE,
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusCreated)
	var newEv db.Event
	decodeBody(t, rec, &newEv)

	// 24. POST /api/admin/events/{id}/tiers -> bad entry_count 400, valid COUPLE 201
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/events/"+newEv.ID.String()+"/tiers", services.CreateTicketTierRequest{
		Name:       "GROUP",
		BasePrice:  10000,
		FnbCredit:  8000,
		EntryCount: 7,
	}, bearerHeader(adminToken)), http.StatusBadRequest)

	rec = env.doRequest(http.MethodPost, "/api/admin/events/"+newEv.ID.String()+"/tiers", services.CreateTicketTierRequest{
		Name:       "COUPLE",
		Category:   "VIP",
		BasePrice:  4000,
		FnbCredit:  2500,
		EntryCount: 2,
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusCreated)
	var tier db.TicketCategory
	decodeBody(t, rec, &tier)
	if !tier.IsGenderStrict {
		t.Fatal("COUPLE tier must automatically enforce is_gender_strict=true")
	}

	// Guest initiates paid booking WITHOUT UTR first
	rec = env.doRequest(http.MethodPost, "/api/checkout/init", services.InitCheckoutRequest{
		EventID:      newEv.ID,
		CategoryID:   tier.ID,
		ContactPhone: "+919876540000",
		ContactEmail: "couple@example.com",
		Guests: []services.GuestInput{
			{Name: "Rohan Das", Age: 27, Gender: db.GenderTypeMALE},
			{Name: "Diya Kapoor", Age: 25, Gender: db.GenderTypeFEMALE, IsPrimaryContact: true},
		},
	}, nil)
	assertStatus(t, rec, http.StatusCreated)
	var booking services.InitCheckoutResponse
	decodeBody(t, rec, &booking)

	// 22a. Admin cannot APPROVE paid booking before patron submits UPI UTR -> 400
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/bookings/"+booking.BookingID.String()+"/verify", services.VerifyBookingRequest{
		Action: "APPROVE",
	}, bearerHeader(adminToken)), http.StatusBadRequest)

	// Patron submits UTR
	assertStatus(t, env.doRequest(http.MethodPost, "/api/checkout/"+booking.BookingID.String()+"/upi-reference", map[string]string{
		"upi_transaction_id": "556677889911",
		"contact_phone":      "+919876540000",
	}, nil), http.StatusOK)

	// 21. GET /api/admin/bookings -> Admin sees both UTR and exact expected_amount
	rec = env.doRequest(http.MethodGet, "/api/admin/bookings?status=PENDING&event_id="+newEv.ID.String(), nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var queue struct {
		Bookings []services.BookingVerificationItem `json:"bookings"`
		Count    int                                `json:"count"`
	}
	decodeBody(t, rec, &queue)
	if queue.Count != 1 || queue.Bookings[0].UPITransactionID != "556677889911" || queue.Bookings[0].ExpectedAmount != 4000 {
		t.Fatalf("unexpected verification queue: %+v", queue)
	}

	// 22b. Admin APPROVES booking -> CONFIRMED + WhatsApp PNG pass queued with primary guest "Diya Kapoor"
	rec = env.doRequest(http.MethodPost, "/api/admin/bookings/"+booking.BookingID.String()+"/verify", services.VerifyBookingRequest{
		Action: "APPROVE",
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var verified services.VerifyBookingResponse
	decodeBody(t, rec, &verified)
	if verified.Status != db.BookingStatusCONFIRMED || !verified.WhatsAppQueued {
		t.Fatalf("expected CONFIRMED + whatsapp_queued=true, got %+v", verified)
	}

	msgs := env.notifier.Messages()
	if len(msgs) != 1 || msgs[0].PrimaryGuest != "Diya Kapoor" {
		t.Fatalf("expected WhatsApp confirmation addressed to primary guest Diya Kapoor, got %+v", msgs)
	}

	for _, query := range []string{"Diya", "+919876540000", "556677889911"} {
		rec = env.doRequest(http.MethodGet, "/api/admin/tickets/search?q="+url.QueryEscape(query), nil, bearerHeader(adminToken))
		assertStatus(t, rec, http.StatusOK)
		var found struct {
			Tickets []services.PublicTicketView `json:"tickets"`
			Count   int                         `json:"count"`
		}
		decodeBody(t, rec, &found)
		if found.Count != 1 || len(found.Tickets) != 1 || found.Tickets[0].BookingID != booking.BookingID {
			t.Fatalf("unexpected ticket search result for %q: %+v", query, found)
		}
		if found.Tickets[0].GateQRToken == "" || found.Tickets[0].BarCode == "" {
			t.Fatalf("ticket search must return a signed gate QR and barcode: %+v", found.Tickets[0])
		}
		if found.Tickets[0].ContactPhone != "+919876540000" || found.Tickets[0].UPITransactionID != "556677889911" || found.Tickets[0].Section == "" {
			t.Fatalf("ticket search must return contact, UTR and section for CSV export: %+v", found.Tickets[0])
		}
	}

	rec = env.doRequest(http.MethodGet, "/api/admin/tickets/search?q=Diya&limit=1", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var limited struct {
		Tickets []services.PublicTicketView `json:"tickets"`
		Count   int                         `json:"count"`
	}
	decodeBody(t, rec, &limited)
	if limited.Count != 1 || len(limited.Tickets) != 1 {
		t.Fatalf("expected ticket search limit=1 to return one pass, got %+v", limited)
	}

	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/tickets/search?limit=0", nil, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/tickets/search?q=Diya", nil, bearerHeader(barToken)), http.StatusForbidden)

	// 22c. Double-verifying an already CONFIRMED booking -> 409
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/bookings/"+booking.BookingID.String()+"/verify", services.VerifyBookingRequest{
		Action: "REJECT",
	}, bearerHeader(adminToken)), http.StatusConflict)

	// 22d. Explicit REJECT on a second pending booking -> FAILED + no extra WhatsApp queued
	recRejInit := env.doRequest(http.MethodPost, "/api/checkout/init", services.InitCheckoutRequest{
		EventID:          newEv.ID,
		CategoryID:       tier.ID,
		ContactPhone:     "+919876540009",
		ContactEmail:     "reject@example.com",
		UPITransactionID: "665544332211",
		Guests: []services.GuestInput{
			{Name: "Guest A", Age: 25, Gender: db.GenderTypeMALE, IsPrimaryContact: true},
			{Name: "Guest B", Age: 25, Gender: db.GenderTypeFEMALE},
		},
	}, nil)
	assertStatus(t, recRejInit, http.StatusCreated)
	var rejBooking services.InitCheckoutResponse
	decodeBody(t, recRejInit, &rejBooking)

	recRej := env.doRequest(http.MethodPost, "/api/admin/bookings/"+rejBooking.BookingID.String()+"/verify", services.VerifyBookingRequest{
		Action:          "REJECT",
		RejectionReason: "UTR not found in bank statement",
	}, bearerHeader(adminToken))
	assertStatus(t, recRej, http.StatusOK)
	var rejResp services.VerifyBookingResponse
	decodeBody(t, recRej, &rejResp)
	if rejResp.Status != db.BookingStatusFAILED || rejResp.WhatsAppQueued {
		t.Fatalf("expected FAILED booking with whatsapp_queued=false, got %+v", rejResp)
	}

	// Redeem 1 drink against the newly approved couple ticket so stats show bar activity
	barCode := queue.Bookings[0].BarCode
	env.checkInTicketByBarCode(barCode)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/counter/redeem", services.RedeemItemsRequest{
		BarCode: barCode,
		Items:   []services.RedeemLineItemInput{{MenuItemID: env.drinkID, Quantity: 2}}, // 1500 INR from configured credit
	}, bearerHeader(barToken)), http.StatusOK)

	// 25. GET /api/admin/stats/{event_id} -> aggregates revenue, F&B, and gender breakdown
	rec = env.doRequest(http.MethodGet, "/api/admin/stats/"+newEv.ID.String(), nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var stats services.EventStatsView
	decodeBody(t, rec, &stats)
	if stats.ConfirmedBookings != 1 || stats.TicketRevenue != 4000 || stats.TotalFnbAllocated != 2500 || stats.TotalFnbRedeemed != 1500 || stats.TotalFnbRemaining != 1000 || stats.MaleGuests != 1 || stats.FemaleGuests != 1 {
		t.Fatalf("unexpected event stats: %+v", stats)
	}
}

func TestAdminSoftDeleteArtistSyncAndAggregateStats(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")
	scannerToken, _ := env.loginAs("gate_user", "GatePass!123")
	barToken, _ := env.loginAs("bar_user", "BarPass!123")
	now := time.Now().UTC()

	createdArtists := []services.ArtistInput{{Name: "Opening DJ", Category: "DJ", Description: "Warms up the crowd"}}
	rec := env.doRequest(http.MethodPost, "/api/admin/events", services.CreateEventRequest{
		Title:    "Synced Artists Gala",
		StartsAt: now.Add(-30 * time.Minute),
		EndsAt:   now.Add(5 * time.Hour),
		Location: "Rooftop, Bengaluru",
		Status:   db.EventStatusONSALE,
		Artists:  &createdArtists,
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusCreated)
	var ev db.Event
	decodeBody(t, rec, &ev)

	rec = env.doRequest(http.MethodGet, "/api/events/"+ev.ID.String(), nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var initialDetails services.EventDetailsView
	decodeBody(t, rec, &initialDetails)
	if len(initialDetails.Artists) != 1 || initialDetails.Artists[0].Name != "Opening DJ" {
		t.Fatalf("expected artist from create request, got %+v", initialDetails.Artists)
	}
	createdArtistID := initialDetails.Artists[0].ID

	rec = env.doRequest(http.MethodPost, "/api/admin/events/"+ev.ID.String()+"/tiers", services.CreateTicketTierRequest{
		Name:       "FREE",
		Category:   "MAIN_FLOOR",
		BasePrice:  0,
		FnbCredit:  500,
		EntryCount: 1,
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusCreated)
	var tier db.TicketCategory
	decodeBody(t, rec, &tier)

	rec = env.doRequest(http.MethodPut, "/api/admin/events/"+ev.ID.String(), map[string]any{
		"artists": []map[string]any{
			{"id": createdArtistID.String(), "name": "Headliner DJ", "category": "DJ", "description": "Main act"},
			{"name": "Closing DJ", "category": "DJ", "description": "Last set"},
		},
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)

	rec = env.doRequest(http.MethodGet, "/api/events/"+ev.ID.String(), nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var details services.EventDetailsView
	decodeBody(t, rec, &details)
	artistNames := map[string]bool{}
	for _, item := range details.Artists {
		artistNames[item.Name] = true
	}
	if len(details.Artists) != 2 || !artistNames["Headliner DJ"] || !artistNames["Closing DJ"] {
		t.Fatalf("unexpected synced artists: %+v", details.Artists)
	}

	rec = env.doRequest(http.MethodPut, "/api/admin/events/"+ev.ID.String(), map[string]any{
		"artists": []any{},
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	rec = env.doRequest(http.MethodGet, "/api/events/"+ev.ID.String(), nil, nil)
	assertStatus(t, rec, http.StatusOK)
	details = services.EventDetailsView{}
	decodeBody(t, rec, &details)
	if len(details.Artists) != 0 {
		t.Fatalf("expected artists to be removed, got %+v", details.Artists)
	}

	rec = env.doRequest(http.MethodPost, "/api/checkout/init", services.InitCheckoutRequest{
		EventID:      ev.ID,
		CategoryID:   tier.ID,
		ContactPhone: "+919876540777",
		ContactEmail: "free@example.com",
		Guests:       []services.GuestInput{{Name: "Anya Rao", Age: 26, Gender: db.GenderTypeFEMALE, IsPrimaryContact: true}},
	}, nil)
	assertStatus(t, rec, http.StatusCreated)
	var pass services.InitCheckoutResponse
	decodeBody(t, rec, &pass)
	if pass.Status != db.BookingStatusCONFIRMED {
		t.Fatalf("expected free booking to be confirmed, got %s", pass.Status)
	}
	assertStatus(t, env.doRequest(http.MethodPost, "/api/gate/check-in", map[string]string{"qr_token": pass.GateQRToken}, bearerHeader(scannerToken)), http.StatusOK)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/counter/redeem", services.RedeemItemsRequest{
		BarCode: pass.BarCode,
		Items:   []services.RedeemLineItemInput{{MenuItemID: env.drinkID, Quantity: 1}},
	}, map[string]string{
		"Authorization":   "Bearer " + barToken,
		"Idempotency-Key": "aggregate-stats-order-001",
	}), http.StatusOK)

	rec = env.doRequest(http.MethodGet, "/api/admin/stats", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var stats services.AdminStatsView
	decodeBody(t, rec, &stats)
	if stats.ActiveEvents != 2 || len(stats.PerEventPerformance) != 2 {
		t.Fatalf("unexpected active event aggregation: active=%d per_event=%d", stats.ActiveEvents, len(stats.PerEventPerformance))
	}
	if stats.ConfirmedBookings != 1 || stats.TotalTickets != 1 || stats.CheckedInTickets != 1 || stats.TotalGuests != 1 || stats.FnbRedeemed != 500 || stats.BarTransactions != 1 {
		t.Fatalf("unexpected aggregate KPIs: %+v", stats)
	}
	if len(stats.TopBarItems) != 1 || stats.TopBarItems[0].ItemName != "Smoked Negroni" || len(stats.RecentGateEntries) != 1 || stats.RecentGateEntries[0].TicketID != pass.TicketID {
		t.Fatalf("unexpected aggregate arrays: top_bar=%+v gate=%+v", stats.TopBarItems, stats.RecentGateEntries)
	}

	assertStatus(t, env.doRequest(http.MethodDelete, "/api/admin/events/"+ev.ID.String(), nil, bearerHeader(adminToken)), http.StatusOK)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/events/"+ev.ID.String(), nil, nil), http.StatusNotFound)

	rec = env.doRequest(http.MethodGet, "/api/admin/events", nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var adminEvents struct {
		Events []services.AdminEventView `json:"events"`
		Count  int                       `json:"count"`
	}
	decodeBody(t, rec, &adminEvents)
	if adminEvents.Count != 1 || len(adminEvents.Events) != 1 || adminEvents.Events[0].ID != env.eventID {
		t.Fatalf("deleted event still visible in admin list: %+v", adminEvents)
	}

	rec = env.doRequest(http.MethodGet, "/api/events?status=ALL&limit=50", nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var publicEvents struct {
		Events []services.PublicEventView `json:"events"`
		Count  int                        `json:"count"`
	}
	decodeBody(t, rec, &publicEvents)
	if publicEvents.Count != 1 || len(publicEvents.Events) != 1 || publicEvents.Events[0].ID != env.eventID {
		t.Fatalf("deleted event still visible in public list: %+v", publicEvents)
	}
}

func TestAdminUpdateTicketTier(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	rec := env.doRequest(http.MethodPatch, "/api/admin/tiers/"+env.maleCatID.String(), map[string]any{
		"name":             "group_a",
		"category":         "a",
		"base_price":       2500,
		"fnb_credit":       900,
		"entry_count":      5,
		"is_gender_strict": true,
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var tier db.TicketCategory
	decodeBody(t, rec, &tier)
	if tier.Name != "GROUP_A" || !tier.Category.Valid || tier.Category.String != "A" || tier.BasePrice != 2500 || tier.FnbCredit != 900 || tier.EntryCount != 5 || !tier.IsGenderStrict {
		t.Fatalf("unexpected updated tier: %+v", tier)
	}

	assertStatus(t, env.doRequest(http.MethodPatch, "/api/admin/tiers/"+env.maleCatID.String(), map[string]any{
		"category": "VIP",
	}, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPatch, "/api/admin/tiers/"+env.maleCatID.String(), map[string]any{
		"entry_count": 3,
	}, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPatch, "/api/admin/tiers/"+env.maleCatID.String(), map[string]any{
		"base_price": -1,
	}, bearerHeader(adminToken)), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodPatch, "/api/admin/tiers/00000000-0000-0000-0000-000000000000", map[string]any{
		"base_price": 100,
	}, bearerHeader(adminToken)), http.StatusNotFound)
}

func TestAdminWhatsAppDeckCursorPagination(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")

	for i := 0; i < 12; i++ {
		rec := env.doRequest(http.MethodPost, "/api/checkout/init", services.InitCheckoutRequest{
			EventID:      env.eventID,
			CategoryID:   env.femaleCatID,
			ContactPhone: fmt.Sprintf("+91990000%04d", i),
			ContactEmail: fmt.Sprintf("wa-cursor-%d@example.com", i),
			Guests: []services.GuestInput{{
				Name:             fmt.Sprintf("Cursor Guest %d", i),
				Age:              25,
				Gender:           db.GenderTypeFEMALE,
				IsPrimaryContact: true,
			}},
		}, nil)
		assertStatus(t, rec, http.StatusCreated)
	}

	type waPage struct {
		Tickets    []services.PublicTicketView `json:"tickets"`
		Count      int                         `json:"count"`
		NextCursor string                      `json:"next_cursor"`
		HasMore    bool                        `json:"has_more"`
	}
	fetch := func(cursor string) waPage {
		path := "/api/admin/tickets/search?wa_pending=true&limit=10"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := env.doRequest(http.MethodGet, path, nil, bearerHeader(adminToken))
		assertStatus(t, rec, http.StatusOK)
		var page waPage
		decodeBody(t, rec, &page)
		return page
	}

	page := fetch("")
	if page.Count != 10 || len(page.Tickets) != 10 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("unexpected first WA page: count=%d has_more=%v cursor=%q", page.Count, page.HasMore, page.NextCursor)
	}
	firstID := page.Tickets[0].ID
	secondID := page.Tickets[1].ID

	assertStatus(t, env.doRequest(http.MethodPatch, "/api/admin/bookings/"+firstID.String()+"/wa-sent", nil, bearerHeader(adminToken)), http.StatusOK)
	refetched := fetch("")
	if refetched.Count != 10 || refetched.Tickets[0].ID != secondID {
		t.Fatalf("marking WA sent did not reveal next ticket: got %s, want %s", refetched.Tickets[0].ID, secondID)
	}

	lastPage := fetch(page.NextCursor)
	if lastPage.Count != 2 || lastPage.HasMore {
		t.Fatalf("unexpected last WA page: count=%d has_more=%v", lastPage.Count, lastPage.HasMore)
	}
	assertStatus(t, env.doRequest(http.MethodGet, "/api/admin/tickets/search?cursor=not-a-cursor", nil, bearerHeader(adminToken)), http.StatusBadRequest)
}
