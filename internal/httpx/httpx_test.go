package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, http.StatusAccepted, Envelope{"ok": true}); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}
	if rec.Code != http.StatusAccepted || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("unexpected response: %d %v", rec.Code, rec.Header())
	}
	var body map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body["ok"] {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestReadJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"venu"}`))
	var dst payload
	if err := ReadJSON(rec, req, &dst); err != nil || dst.Name != "venu" {
		t.Fatalf("valid payload rejected: %v %+v", err, dst)
	}

	tests := []struct {
		name string
		body string
	}{
		{"invalid json", `{"name":`},
		{"unknown field", `{"name":"venu","extra":1}`},
		{"two objects", `{"name":"a"}{"name":"b"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			if err := ReadJSON(httptest.NewRecorder(), r, &payload{}); err == nil {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
		})
	}
}

func TestReadJSONRejectsOversizedBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"`+strings.Repeat("x", MaxJSONBodyBytes+1)+`"}`))
	if err := ReadJSON(httptest.NewRecorder(), r, &struct{}{}); err == nil {
		t.Fatal("expected oversized body to be rejected")
	}
}

func TestHandleErrorMapping(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"bad request", NewBadRequest("bad", errors.New("cause")), http.StatusBadRequest},
		{"unauthorized", NewUnauthorized("no", nil), http.StatusUnauthorized},
		{"forbidden", NewForbidden("no"), http.StatusForbidden},
		{"not found", NewNotFound("missing"), http.StatusNotFound},
		{"conflict", NewConflict("dup", nil), http.StatusConflict},
		{"wrapped app error", errors.Join(errors.New("outer"), NewNotFound("missing")), http.StatusNotFound},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout},
		{"no rows", pgx.ErrNoRows, http.StatusNotFound},
		{"unique violation", &pgconn.PgError{Code: "23505"}, http.StatusConflict},
		{"fk violation", &pgconn.PgError{Code: "23503"}, http.StatusBadRequest},
		{"check violation", &pgconn.PgError{Code: "23514"}, http.StatusBadRequest},
		{"unknown pg error", &pgconn.PgError{Code: "42601"}, http.StatusInternalServerError},
		{"generic", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			HandleError(rec, req, logger, tc.err)
			if rec.Code != tc.want {
				t.Fatalf("got %d want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
			var body ErrorPayload
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
				t.Fatalf("error contract violated: %s", rec.Body.String())
			}
		})
	}
}

func TestHandleErrorUsesDefaultLogger(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleError(rec, httptest.NewRequest(http.MethodGet, "/", nil), nil, errors.New("boom"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status %d", rec.Code)
	}
}

func TestAppErrorUnwrapAndDetails(t *testing.T) {
	cause := errors.New("cause")
	err := &AppError{Status: http.StatusBadRequest, Message: "bad", Err: cause, Details: map[string]any{"field": "name"}}
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "cause") || err.Unwrap() != cause {
		t.Fatalf("AppError lost cause: %v", err)
	}
	if err.Details["field"] != "name" {
		t.Fatalf("unexpected details: %v", err.Details)
	}
}
