package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestQRSignerVerifyToken_TableDriven(t *testing.T) {
	secret := "table-driven-qr-secret-32-bytes-long"
	signer := NewQRSigner(secret)
	otherSigner := NewQRSigner("different-qr-secret-32-bytes-long")
	ticketID := uuid.New()
	eventID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	iat := now.Add(-time.Minute)
	exp := now.Add(time.Hour)
	validToken := signer.SignToken(ticketID, eventID, iat, exp)

	replacement := byte('A')
	if validToken[len(validToken)-1] == replacement {
		replacement = 'B'
	}
	tampered := validToken[:len(validToken)-1] + string(replacement)

	tests := []struct {
		name     string
		signer   *QRSigner
		token    string
		now      time.Time
		wantErr  error
		wantData QRPayload
	}{
		{
			name:   "valid token",
			signer: signer,
			token:  validToken,
			now:    now,
			wantData: QRPayload{
				TicketID:  ticketID,
				EventID:   eventID,
				IssuedAt:  iat.UTC().Truncate(time.Second),
				ExpiresAt: exp.UTC().Truncate(time.Second),
			},
		},
		{
			name:    "expired token",
			signer:  signer,
			token:   signer.SignToken(ticketID, eventID, iat, now.Add(-time.Second)),
			now:     now,
			wantErr: ErrExpiredQRToken,
		},
		{
			name:    "tampered signature",
			signer:  signer,
			token:   tampered,
			now:     now,
			wantErr: ErrInvalidQRToken,
		},
		{
			name:    "wrong secret",
			signer:  signer,
			token:   otherSigner.SignToken(ticketID, eventID, iat, exp),
			now:     now,
			wantErr: ErrInvalidQRToken,
		},
		{
			name:    "malformed token",
			signer:  signer,
			token:   "not-a-token",
			now:     now,
			wantErr: ErrInvalidQRToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.signer.VerifyToken(tt.token, tt.now)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("VerifyToken error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && got != tt.wantData {
				t.Fatalf("VerifyToken payload = %+v, want %+v", got, tt.wantData)
			}
		})
	}
}
