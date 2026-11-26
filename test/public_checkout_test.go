package test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/venu-live/venu-api/internal/auth"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/services"
	"github.com/venu-live/venu-api/internal/services/seating"
)

// Covers Routes 1–6:
//  1. GET  /api/health
//  2. GET  /api/events/{id}
//  3. POST /api/checkout/init
//  4. POST /api/checkout/{booking_id}/upi-reference
//  5. GET  /api/tickets/{uuid}
//  6. GET  /api/tickets/{uuid}/image
func TestPublicHealthAndEventsAPI(t *testing.T) {
	env := newTestEnv(t)

	// 1. GET /api/health -> 200 OK
	rec := env.doRequest(http.MethodGet, "/api/health", nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var health map[string]any
	decodeBody(t, rec, &health)
	if health["status"] != "ok" {
		t.Fatalf("expected status=ok, got %v", health)
	}

	// GET /api/health -> 503 when DB is unreachable
	env.store.setPingErr(errSimulatedDBDown)
	rec = env.doRequest(http.MethodGet, "/api/health", nil, nil)
	assertStatus(t, rec, http.StatusServiceUnavailable)
	env.store.setPingErr(nil)

	// 2. GET /api/events/{id} -> 200 with UPI VPA & live First-99 female pricing
	rec = env.doRequest(http.MethodGet, "/api/events/"+env.eventID.String(), nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var evView services.EventDetailsView
	decodeBody(t, rec, &evView)
	if evView.UPIVPA != "venu.club@upi" || len(evView.Tiers) != 3 {
		t.Fatalf("unexpected event view: vpa=%s tiers=%d", evView.UPIVPA, len(evView.Tiers))
	}

	foundFreeFemale := false
	for _, tier := range evView.Tiers {
		if tier.Name == "STAG_FEMALE" {
			if !tier.First99Applied || tier.EffectivePrice != 1 || tier.FreeSlotsRemaining != 99 {
				t.Fatalf("expected First-99 ₹1 pricing for STAG_FEMALE, got %+v", tier)
			}
			foundFreeFemale = true
		}
	}
	if !foundFreeFemale {
		t.Fatal("STAG_FEMALE tier missing from event response")
	}

	// Bad UUID -> 400; unknown UUID -> 404
	assertStatus(t, env.doRequest(http.MethodGet, "/api/events/not-a-uuid", nil, nil), http.StatusBadRequest)
	assertStatus(t, env.doRequest(http.MethodGet, "/api/events/"+uuid.NewString(), nil, nil), http.StatusNotFound)
}

func TestGuestCheckoutUPIAndTicketPassLifecycle(t *testing.T) {
	env := newTestEnv(t)

	// 3a. Strict couple rule rejection (2 males on COUPLE tier -> 400)
	badCouple := services.InitCheckoutRequest{
		EventID:      env.eventID,
		CategoryID:   env.coupleCatID,
		ContactPhone: "+919876543211",
		ContactEmail: "pair@example.com",
		Guests: []services.GuestInput{
			{Name: "Aarav", Age: 25, Gender: db.GenderTypeMALE, IsPrimaryContact: true},
			{Name: "Kabir", Age: 26, Gender: db.GenderTypeMALE},
		},
	}
	assertStatus(t, env.doRequest(http.MethodPost, "/api/checkout/init", badCouple, nil), http.StatusBadRequest)

	// 3b. Valid paid COUPLE checkout -> 201 PENDING with UPI QR instructions
	coupleReq := services.InitCheckoutRequest{
		EventID:      env.eventID,
		CategoryID:   env.coupleCatID,
		ContactPhone: "+919876543211",
		ContactEmail: "aarav@example.com",
		Guests: []services.GuestInput{
			{Name: "Aarav Mehta", Age: 25, Gender: db.GenderTypeMALE},
			{Name: "Riya Sharma", Age: 24, Gender: db.GenderTypeFEMALE, IsPrimaryContact: true},
		},
	}
	rec := env.doRequest(http.MethodPost, "/api/checkout/init", coupleReq, nil)
	assertStatus(t, rec, http.StatusCreated)
	var checkoutResp services.InitCheckoutResponse
	decodeBody(t, rec, &checkoutResp)
	if checkoutResp.Status != db.BookingStatusPENDING || checkoutResp.TotalAmount != 3000 || checkoutResp.UPIPayment == nil {
		t.Fatalf("expected PENDING paid booking with UPI instructions, got %+v", checkoutResp)
	}
	if checkoutResp.GateQRToken != "" || checkoutResp.BarCode != "" {
		t.Fatal("barcodes must NOT be exposed before admin verifies UPI payment")
	}
	if checkoutResp.AttendeeCount != 2 || checkoutResp.PrimaryGuestName != "Riya Sharma" {
		t.Fatalf("expected attendee_count=2 and primary_guest=Riya Sharma, got %+v", checkoutResp)
	}
	if !strings.HasPrefix(checkoutResp.PaymentQRData, "upi://pay?") || !strings.Contains(checkoutResp.PaymentQRData, "am=3000.00") {
		t.Fatalf("expected payment_qr_data with embedded amount 3000.00, got %q", checkoutResp.PaymentQRData)
	}

	// 5a. GET /api/tickets/{uuid} while PENDING must hide barcodes, but expose payment_qr_data with amount
	rec = env.doRequest(http.MethodGet, "/api/tickets/"+checkoutResp.TicketID.String(), nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var pendingTicket services.PublicTicketView
	decodeBody(t, rec, &pendingTicket)
	if pendingTicket.GateQRToken != "" || pendingTicket.BarCode != "" {
		t.Fatal("pending ticket lookup leaked barcodes")
	}
	if pendingTicket.AttendeeCount != 2 || pendingTicket.PrimaryGuestName != "Riya Sharma" {
		t.Fatalf("expected attendee_count=2 on ticket view, got %+v", pendingTicket)
	}
	if !strings.HasPrefix(pendingTicket.PaymentQRData, "upi://pay?") || !strings.Contains(pendingTicket.PaymentQRData, "am=3000.00") {
		t.Fatalf("expected payment_qr_data with embedded amount on pending ticket, got %q", pendingTicket.PaymentQRData)
	}

	// 6a. GET /api/tickets/{uuid}/image while PENDING -> 409 Conflict
	assertStatus(t, env.doRequest(http.MethodGet, "/api/tickets/"+checkoutResp.TicketID.String()+"/image", nil, nil), http.StatusConflict)

	// 4. POST /api/checkout/{booking_id}/upi-reference -> invalid UTR 400, valid UTR 200
	assertStatus(t, env.doRequest(http.MethodPost, "/api/checkout/"+checkoutResp.BookingID.String()+"/upi-reference", map[string]string{
		"upi_transaction_id": "bad",
		"contact_phone":      "+919876543211",
	}, nil), http.StatusBadRequest)

	rec = env.doRequest(http.MethodPost, "/api/checkout/"+checkoutResp.BookingID.String()+"/upi-reference", map[string]string{
		"upi_transaction_id": "431987654321",
		"contact_phone":      "+919876543211",
	}, nil)
	assertStatus(t, rec, http.StatusOK)

	// Duplicate UTR on another booking -> 409 Conflict
	rec2 := env.doRequest(http.MethodPost, "/api/checkout/init", services.InitCheckoutRequest{
		EventID:          env.eventID,
		CategoryID:       env.maleCatID,
		ContactPhone:     "+919999888877",
		ContactEmail:     "dup@example.com",
		UPITransactionID: "431987654321",
		Guests:           []services.GuestInput{{Name: "Vikram", Age: 27, Gender: db.GenderTypeMALE}},
	}, nil)
	assertStatus(t, rec2, http.StatusConflict)

	// 3c. First-99 STAG_FEMALE checkout -> 201 instant CONFIRMED + dual barcodes + WhatsApp queued
	femaleReq := services.InitCheckoutRequest{
		EventID:      env.eventID,
		CategoryID:   env.femaleCatID,
		ContactPhone: "+919111222333",
		ContactEmail: "ananya@example.com",
		Guests:       []services.GuestInput{{Name: "Ananya Roy", Age: 23, Gender: db.GenderTypeFEMALE, IsPrimaryContact: true}},
	}
	rec = env.doRequest(http.MethodPost, "/api/checkout/init", femaleReq, nil)
	assertStatus(t, rec, http.StatusCreated)
	var freeResp services.InitCheckoutResponse
	decodeBody(t, rec, &freeResp)
	if freeResp.Status != db.BookingStatusCONFIRMED || freeResp.TotalAmount != 1 || !freeResp.First99Applied {
		t.Fatalf("expected instant CONFIRMED First-99 pass, got %+v", freeResp)
	}
	if !auth.IsValidBarCode(freeResp.BarCode) || freeResp.GateQRToken == "" {
		t.Fatalf("expected valid 5-char 1D barcode and gate QR, got bar_code=%q qr=%q", freeResp.BarCode, freeResp.GateQRToken)
	}
	if len(env.notifier.Messages()) != 1 {
		t.Fatalf("expected 1 WhatsApp confirmation queued, got %d", len(env.notifier.Messages()))
	}

	// 5b. GET /api/tickets/{uuid} for CONFIRMED ticket exposes dual barcodes
	rec = env.doRequest(http.MethodGet, "/api/tickets/"+freeResp.TicketID.String(), nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var confirmedTicket services.PublicTicketView
	decodeBody(t, rec, &confirmedTicket)
	if confirmedTicket.BarCode != freeResp.BarCode || confirmedTicket.GateQRToken == "" {
		t.Fatalf("confirmed ticket missing barcodes: %+v", confirmedTicket)
	}
	if len(confirmedTicket.Guests) == 0 || confirmedTicket.Guests[0].AgeBand == "" || confirmedTicket.Guests[0].Gender != "REDACTED" {
		t.Fatalf("public ticket must mask exact age and gender: %+v", confirmedTicket.Guests)
	}

	// 6b. GET /api/tickets/{uuid}/image -> 200 valid PNG binary
	rec = env.doRequest(http.MethodGet, "/api/tickets/"+freeResp.TicketID.String()+"/image", nil, nil)
	assertStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("expected Content-Type image/png, got %q", ct)
	}
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if !bytes.HasPrefix(rec.Body.Bytes(), pngMagic) {
		t.Fatal("ticket image response did not begin with PNG magic bytes")
	}
}

func TestGroupTableCheckout(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	groupCat, err := env.store.CreateTicketCategory(ctx, db.CreateTicketCategoryParams{
		EventID:        env.eventID,
		Name:           "GROUP_A_5",
		Category:       pgtype.Text{String: "A", Valid: true},
		BasePrice:      5000,
		FnbCredit:      0,
		EntryCount:     5,
		IsGenderStrict: false,
	})
	if err != nil {
		t.Fatalf("create group category: %v", err)
	}
	if _, err := env.store.UpsertSeatingInventory(ctx, db.UpsertSeatingInventoryParams{
		EventID:          env.eventID,
		Section:          "A",
		TableSize:        5,
		TotalTables:      2,
		TicketCategoryID: pgtype.UUID{Bytes: groupCat.ID, Valid: true},
	}); err != nil {
		t.Fatalf("create seating inventory: %v", err)
	}

	guests := make([]services.GuestInput, 0, 5)
	for i := 0; i < 5; i++ {
		guests = append(guests, services.GuestInput{
			Name:             fmt.Sprintf("Guest %d", i+1),
			Age:              25,
			Gender:           db.GenderTypeMALE,
			IsPrimaryContact: i == 0,
		})
	}
	req := services.InitCheckoutRequest{
		EventID:       env.eventID,
		CategoryID:    groupCat.ID,
		ContactPhone:  "+919912345678",
		ContactEmail:  "group@example.com",
		SeatSelection: &seating.SeatSelectionInput{Section: "A", TableSize: 5},
		Guests:        guests,
	}

	rec := env.doRequest(http.MethodPost, "/api/checkout/init", req, nil)
	assertStatus(t, rec, http.StatusCreated)
	var resp services.InitCheckoutResponse
	decodeBody(t, rec, &resp)
	if resp.TotalAmount != 5000 || resp.EntryCount != 5 || resp.AttendeeCount != 5 {
		t.Fatalf("unexpected group checkout response: %+v", resp)
	}
}
