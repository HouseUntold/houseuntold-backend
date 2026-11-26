package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/services"
)

func BenchmarkPublicHealth(b *testing.B) {
	t := &testing.T{}
	env := newTestEnv(t)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			req.RemoteAddr = "10.0.0.1:12345"
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("health check failed: %d", rec.Code)
			}
		}
	})
}

func BenchmarkPublicEventDetails(b *testing.B) {
	t := &testing.T{}
	env := newTestEnv(t)
	path := "/api/events/" + env.eventID.String()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "10.0.0.2:12345"
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("get event details failed: %d", rec.Code)
			}
		}
	})
}

func BenchmarkGateQRValidation(b *testing.B) {
	t := &testing.T{}
	env := newTestEnv(t)
	scannerToken, _ := env.loginAs("gate_user", "GatePass!123")

	// Create confirmed ticket to obtain valid HMAC QR
	rec := env.doRequest(http.MethodPost, "/api/checkout/init", services.InitCheckoutRequest{
		EventID:      env.eventID,
		CategoryID:   env.femaleCatID,
		ContactPhone: "+919876540099",
		ContactEmail: "bench_qr@example.com",
		Guests:       []services.GuestInput{{Name: "Bench Guest", Age: 23, Gender: db.GenderTypeFEMALE, IsPrimaryContact: true}},
	}, nil)
	var pass services.InitCheckoutResponse
	decodeBody(t, rec, &pass)

	payloadBytes, _ := json.Marshal(map[string]string{
		"qr_token": pass.GateQRToken,
	})

	var counter atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := counter.Add(1)
			req := httptest.NewRequest(http.MethodPost, "/api/gate/validate", bytes.NewReader(payloadBytes))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+scannerToken)
			req.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:12345", (id>>16)&0xFF, (id>>8)&0xFF, id&0xFF)
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK && rec.Code != http.StatusTooManyRequests {
				b.Fatalf("gate validate failed: %d (%s)", rec.Code, rec.Body.String())
			}
		}
	})
}

func BenchmarkGuestCheckoutWithPaymentQR(b *testing.B) {
	t := &testing.T{}
	env := newTestEnv(t)
	var counter atomic.Int64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := counter.Add(1)
			body, _ := json.Marshal(services.InitCheckoutRequest{
				EventID:      env.eventID,
				CategoryID:   env.coupleCatID,
				ContactPhone: fmt.Sprintf("+919%09d", id%1000000000),
				ContactEmail: fmt.Sprintf("patron_%d_%d@example.com", time.Now().UnixNano(), id),
				Guests: []services.GuestInput{
					{Name: "Patron M", Age: 25, Gender: db.GenderTypeMALE},
					{Name: "Patron F", Age: 24, Gender: db.GenderTypeFEMALE, IsPrimaryContact: true},
				},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/checkout/init", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", fmt.Sprintf("bench-idem-%d-%d", time.Now().UnixNano(), id))
			req.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:12345", (id>>16)&0xFF, (id>>8)&0xFF, id&0xFF)
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated && rec.Code != http.StatusTooManyRequests && rec.Code != http.StatusConflict {
				b.Fatalf("checkout init failed: %d (%s)", rec.Code, rec.Body.String())
			}
		}
	})
}
