package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/venu-live/venu-api/internal/auth"
)

const testSecret = "qr-sample-secret-32-bytes-minimum!!"

func TestQRScan_ValidReturnsTicketID(t *testing.T) {
	now := time.Now().UTC()
	ticketID, eventID := uuid.New(), uuid.New()
	token := generate(testSecret, ticketID, eventID, now)

	got, err := scan(testSecret, token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("valid QR scan failed: %v", err)
	}
	if got != ticketID {
		t.Fatalf("got ticket %s, want %s", got, ticketID)
	}
}

func TestQRScan_TamperedTokenRejected(t *testing.T) {
	now := time.Now().UTC()
	ticketID, eventID := uuid.New(), uuid.New()
	token := generate(testSecret, ticketID, eventID, now)

	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected token shape: %q", token)
	}
	tampered := parts[0] + "." + flipLast(parts[1])

	got, err := scan(testSecret, tampered, now)
	if !errors.Is(err, auth.ErrInvalidQRToken) {
		t.Fatalf("tampered QR must fail securely with ErrInvalidQRToken, got id=%s err=%v", got, err)
	}
	if got != uuid.Nil {
		t.Fatalf("tampered QR must not return a ticket id, got %s", got)
	}
}

func TestQRScan_WrongSecretRejected(t *testing.T) {
	now := time.Now().UTC()
	token := generate(testSecret, uuid.New(), uuid.New(), now)

	if _, err := scan("different-secret", token, now); !errors.Is(err, auth.ErrInvalidQRToken) {
		t.Fatalf("wrong secret must fail securely, got %v", err)
	}
}

func TestQRScan_ExpiredRejected(t *testing.T) {
	now := time.Now().UTC()
	token := generate(testSecret, uuid.New(), uuid.New(), now)

	if _, err := scan(testSecret, token, now.Add(2*time.Hour)); !errors.Is(err, auth.ErrExpiredQRToken) {
		t.Fatalf("expired QR must be rejected, got %v", err)
	}
}
