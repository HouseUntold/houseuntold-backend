package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/venu-live/venu-api/internal/db"
)

func TestArgon2idPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("VenuStaff!2026")
	if err != nil {
		t.Fatalf("hash failed: %v", err)
	}
	ok, err := VerifyPassword("VenuStaff!2026", hash)
	if err != nil || !ok {
		t.Fatalf("expected password to verify, ok=%v err=%v", ok, err)
	}
	bad, err := VerifyPassword("WrongPassword", hash)
	if err != nil || bad {
		t.Fatalf("expected wrong password to fail verification")
	}
}

func TestTokenManager_AccessRefreshSeparation(t *testing.T) {
	mgr := NewTokenManager("access-secret-123", "refresh-secret-456", "venu-staff", "venu-api", 15*time.Minute, 24*time.Hour)
	staff := db.StaffAccount{
		ID:       uuid.New(),
		Username: "gate_01",
		Role:     db.StaffRoleSCANNER,
		IsActive: true,
	}

	pair, err := mgr.IssueStaffTokens(staff)
	if err != nil {
		t.Fatalf("issue tokens failed: %v", err)
	}

	claims, err := mgr.VerifyAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("verify access failed: %v", err)
	}
	if claims.StaffID != staff.ID || claims.Role != db.StaffRoleSCANNER {
		t.Fatalf("unexpected access claims: %+v", claims)
	}

	if _, err := mgr.VerifyAccessToken(pair.RefreshToken); err == nil {
		t.Fatalf("expected refresh token to be rejected as access token")
	}

	refClaims, err := mgr.VerifyRefreshToken(pair.RefreshToken)
	if err != nil || refClaims.StaffID != staff.ID {
		t.Fatalf("verify refresh failed: %v", err)
	}
}

func TestQRSigner_SignVerifyAndTamperRejection(t *testing.T) {
	signer := NewQRSigner("super-secret-qr-key")
	ticketID := uuid.New()
	eventID := uuid.New()
	now := time.Now().UTC()

	token := signer.SignToken(ticketID, eventID, now, now.Add(6*time.Hour))
	payload, err := signer.VerifyToken(token, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("expected valid QR token, got %v", err)
	}
	if payload.TicketID != ticketID || payload.EventID != eventID {
		t.Fatalf("payload mismatch: %+v", payload)
	}

	if _, err := signer.VerifyToken(token+"x", now); err == nil {
		t.Fatalf("expected tampered QR token to be rejected")
	}
	if _, err := signer.VerifyToken(token, now.Add(7*time.Hour)); err == nil {
		t.Fatalf("expected expired QR token to be rejected")
	}
}

func TestGenerateAndValidateBarCode1D(t *testing.T) {
	for i := 0; i < 50; i++ {
		code, err := GenerateBarCode()
		if err != nil {
			t.Fatalf("GenerateBarCode failed: %v", err)
		}
		if len(code) != 5 || !IsValidBarCode(code) {
			t.Fatalf("invalid 1D barcode format generated: %q", code)
		}
	}

	if IsValidBarCode("A123") || IsValidBarCode("A12345") || IsValidBarCode("A7X-P") || IsValidBarCode("   ") {
		t.Fatalf("expected malformed 1D barcodes to be rejected")
	}
	if !IsValidBarCode("k4829") {
		t.Fatalf("expected lowercase input to normalize and validate")
	}
}

func TestUPIHelpers(t *testing.T) {
	if !IsValidUPITransactionID("432198765432") {
		t.Fatalf("expected 12-digit UTR to be valid")
	}
	if IsValidUPITransactionID("short") || IsValidUPITransactionID("bad space id") {
		t.Fatalf("expected invalid UPI txn ID to be rejected")
	}
	uri := BuildUPIIntentURI("venu@icici", "House Untold Live", "Booking 1234", 1999)
	if !strings.HasPrefix(uri, "upi://pay?") || !strings.Contains(uri, "am=1999.00") {
		t.Fatalf("unexpected UPI intent URI: %s", uri)
	}
}
