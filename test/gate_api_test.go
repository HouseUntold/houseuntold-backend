package test

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/services"
)

// Covers Routes 10–11:
//  10. POST /api/gate/validate
//  11. POST /api/gate/check-in
func TestGateValidateCheckInAndImmediateExpiry(t *testing.T) {
	env := newTestEnv(t)
	scannerToken, _ := env.loginAs("gate_user", "GatePass!123")
	barToken, _ := env.loginAs("bar_user", "BarPass!123")

	// Issue a confirmed ₹0 STAG_FEMALE pass to get a real signed Gate QR token
	rec := env.doRequest(http.MethodPost, "/api/checkout/init", services.InitCheckoutRequest{
		EventID:      env.eventID,
		CategoryID:   env.femaleCatID,
		ContactPhone: "+919876500001",
		ContactEmail: "meera@example.com",
		Guests:       []services.GuestInput{{Name: "Meera Nair", Age: 24, Gender: db.GenderTypeFEMALE, IsPrimaryContact: true}},
	}, nil)
	assertStatus(t, rec, http.StatusCreated)
	var pass services.InitCheckoutResponse
	decodeBody(t, rec, &pass)

	// Bartender cannot access gate endpoints -> 403
	assertStatus(t, env.doRequest(http.MethodPost, "/api/gate/validate", map[string]string{
		"qr_token": pass.GateQRToken,
	}, bearerHeader(barToken)), http.StatusForbidden)

	// Forged / tampered QR -> 401
	assertStatus(t, env.doRequest(http.MethodPost, "/api/gate/validate", map[string]string{
		"qr_token": pass.GateQRToken + "tampered",
	}, bearerHeader(scannerToken)), http.StatusUnauthorized)

	// Expired QR -> 400
	expiredQR := env.qrSigner.SignToken(pass.TicketID, env.eventID, time.Now().UTC().Add(-2*time.Hour), time.Now().UTC().Add(-1*time.Hour))
	assertStatus(t, env.doRequest(http.MethodPost, "/api/gate/validate", map[string]string{
		"qr_token": expiredQR,
	}, bearerHeader(scannerToken)), http.StatusBadRequest)

	// 10. POST /api/gate/validate -> 200 valid & does NOT mutate check-in state
	rec = env.doRequest(http.MethodPost, "/api/gate/validate", map[string]string{
		"qr_token": pass.GateQRToken,
	}, bearerHeader(scannerToken))
	assertStatus(t, rec, http.StatusOK)
	var valView services.GateTicketView
	decodeBody(t, rec, &valView)
	if !valView.Valid || valView.IsCheckedIn || valView.Reason != "ready_for_entry" {
		t.Fatalf("expected valid un-scanned ticket, got %+v", valView)
	}

	// 11. POST /api/gate/check-in -> 10 concurrent scans; strictly 1 wins (200), 9 rejected (409)
	var (
		wg        sync.WaitGroup
		wins      atomic.Int32
		conflicts atomic.Int32
	)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := env.doRequest(http.MethodPost, "/api/gate/check-in", map[string]string{
				"qr_token": pass.GateQRToken,
			}, bearerHeader(scannerToken))
			switch r.Code {
			case http.StatusOK:
				wins.Add(1)
			case http.StatusConflict:
				conflicts.Add(1)
			default:
				t.Errorf("unexpected concurrent check-in status %d: %s", r.Code, r.Body.String())
			}
		}()
	}
	wg.Wait()

	if wins.Load() != 1 || conflicts.Load() != 9 {
		t.Fatalf("expected 1 win & 9 conflicts under concurrent gate scans, got wins=%d conflicts=%d", wins.Load(), conflicts.Load())
	}

	// Verify GET /api/tickets/{uuid} immediately hides gate_qr_token while preserving 1D bar_code
	rec = env.doRequest(http.MethodGet, "/api/tickets/"+pass.TicketID.String(), nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var afterScan services.PublicTicketView
	decodeBody(t, rec, &afterScan)
	if !afterScan.IsCheckedIn || afterScan.GateQRToken != "" {
		t.Fatalf("gate_qr_token must expire and be omitted immediately after check-in: %+v", afterScan)
	}
	if afterScan.BarCode != pass.BarCode {
		t.Fatalf("1D bartender bar_code must remain active until event end: got %q want %q", afterScan.BarCode, pass.BarCode)
	}
}
