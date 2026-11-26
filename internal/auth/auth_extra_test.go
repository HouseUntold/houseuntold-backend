package auth

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/venu-live/venu-api/internal/db"
)

func TestQRSignerMalformedTokens(t *testing.T) {
	signer := NewQRSigner("secret")
	now := time.Now().UTC()

	for _, token := range []string{
		"",
		"only-one-part",
		"a.b.c",
		".sig",
		"payload.",
		"!!!.c2ln",
		strings.ReplaceAll(signer.SignToken(uuid.New(), uuid.New(), now, now.Add(time.Hour)), ".", "-"),
	} {
		if _, err := signer.VerifyToken(token, now); !errors.Is(err, ErrInvalidQRToken) {
			t.Fatalf("token %q must be rejected as invalid, got %v", token, err)
		}
	}
}

func TestQRSignerRejectsTamperedPayloadFields(t *testing.T) {
	signer := NewQRSigner("secret")
	now := time.Now().UTC()
	ticketID, eventID := uuid.New(), uuid.New()
	exp := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	encoded := func(raw string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(raw))
	}

	badPayloads := []string{
		encoded("v2|" + ticketID.String() + "|" + eventID.String() + "|0|" + exp),
		encoded("v1|not-a-uuid|" + eventID.String() + "|0|" + exp),
		encoded("v1|" + ticketID.String() + "|not-a-uuid|0|" + exp),
		encoded("v1|" + ticketID.String() + "|" + eventID.String() + "|x|" + exp),
		encoded("v1|" + ticketID.String() + "|" + eventID.String() + "|0|x"),
	}
	for _, payload := range badPayloads {
		mac := signer.sign(payload)
		if _, err := signer.VerifyToken(payload+"."+mac, now); !errors.Is(err, ErrInvalidQRToken) {
			t.Fatalf("signed but malformed payload must be rejected: %q got %v", payload, err)
		}
	}
}

func TestQRSignerComputeDigest(t *testing.T) {
	first := NewQRSigner("secret")
	second := NewQRSigner("secret")
	other := NewQRSigner("different-secret")
	id := uuid.New()

	if first.ComputeDigest(id) != second.ComputeDigest(id) {
		t.Fatal("digest must be deterministic for the same secret")
	}
	if first.ComputeDigest(id) == other.ComputeDigest(id) {
		t.Fatal("digest must differ across secrets")
	}
	if first.ComputeDigest(id) == first.ComputeDigest(uuid.New()) {
		t.Fatal("digest must be ticket-specific")
	}
	if !strings.HasPrefix(first.ComputeDigest(id), "v1:") || len(first.ComputeDigest(id)) != 67 {
		t.Fatalf("unexpected digest format: %q", first.ComputeDigest(id))
	}
}

func TestQRSignerExpiryBoundary(t *testing.T) {
	signer := NewQRSigner("secret")
	now := time.Now().UTC().Truncate(time.Second)
	exp := now.Add(time.Hour)
	token := signer.SignToken(uuid.New(), uuid.New(), now, exp)

	payload, err := signer.VerifyToken(token, exp)
	if err != nil || payload.ExpiresAt != exp {
		t.Fatalf("token must be valid exactly at exp: %v %+v", err, payload)
	}
	if _, err := signer.VerifyToken(token, exp.Add(time.Second)); !errors.Is(err, ErrExpiredQRToken) {
		t.Fatalf("token must be expired after exp, got %v", err)
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	for _, hash := range []string{
		"",
		"plain",
		"$argon2id$v=19$m=10,t=1,p=1$!!!!$!!!!",
		"$argon2i$v=19$m=10,t=1,p=1$c2FsdA$aGFzaA",
	} {
		if ok, err := VerifyPassword("password", hash); ok || err == nil {
			t.Fatalf("malformed hash %q must fail: ok=%v err=%v", hash, ok, err)
		}
	}
}

func TestHashPasswordRejectsShortPassword(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password must be rejected")
	}
}

func TestTokenManagerRejectsWrongSecretAndClaims(t *testing.T) {
	staff := db.StaffAccount{ID: uuid.New(), Username: "a", Role: db.StaffRoleADMIN, IsActive: true}
	issuer := NewTokenManager("access-secret", "refresh-secret", "aud", "iss", time.Minute, time.Hour)
	other := NewTokenManager("different", "different", "aud", "iss", time.Minute, time.Hour)
	pair, err := issuer.IssueStaffTokens(staff)
	if err != nil {
		t.Fatalf("issue failed: %v", err)
	}

	if _, err := other.VerifyAccessToken(pair.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong secret must be rejected, got %v", err)
	}
	if _, err := other.VerifyRefreshToken(pair.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong refresh secret must be rejected, got %v", err)
	}
	now := time.Now().UTC()
	wrongTypeClaims := StaffClaims{
		StaffID:   staff.ID,
		Role:      staff.Role,
		TokenType: TokenTypeRefresh,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{"aud"},
			Issuer:    "iss",
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	wrongTypeToken, err := issuer.accessAuth.GenerateToken(wrongTypeClaims)
	if err != nil {
		t.Fatalf("generate wrong-type token: %v", err)
	}
	if _, err := issuer.VerifyAccessToken(wrongTypeToken); !errors.Is(err, ErrUnexpectedClaims) {
		t.Fatalf("refresh-typed access token must be rejected, got %v", err)
	}
	if _, err := issuer.VerifyRefreshToken(pair.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("access token as refresh must be rejected, got %v", err)
	}
}

func TestStaffClaimsCarryTokenVersion(t *testing.T) {
	staff := db.StaffAccount{ID: uuid.New(), Role: db.StaffRoleADMIN, IsActive: true, TokenVersion: 7}
	mgr := NewTokenManager("access-secret", "refresh-secret", "aud", "iss", time.Minute, time.Hour)

	pair, err := mgr.IssueStaffTokens(staff)
	if err != nil {
		t.Fatalf("issue failed: %v", err)
	}
	accessClaims, err := mgr.VerifyAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("verify access: %v", err)
	}
	refreshClaims, err := mgr.VerifyRefreshToken(pair.RefreshToken)
	if err != nil {
		t.Fatalf("verify refresh: %v", err)
	}
	if accessClaims.TokenVersion != staff.TokenVersion || refreshClaims.TokenVersion != staff.TokenVersion {
		t.Fatalf("token version not propagated: access=%d refresh=%d want=%d", accessClaims.TokenVersion, refreshClaims.TokenVersion, staff.TokenVersion)
	}
}

func TestTokenManagerRejectsExpiredToken(t *testing.T) {
	staff := db.StaffAccount{ID: uuid.New(), Role: db.StaffRoleSCANNER, IsActive: true}
	mgr := NewTokenManager("access-secret", "refresh-secret", "aud", "iss", time.Millisecond, time.Hour)
	pair, err := mgr.IssueStaffTokens(staff)
	if err != nil {
		t.Fatalf("issue failed: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := mgr.VerifyAccessToken(pair.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired access token must be rejected, got %v", err)
	}
}

func TestJWTAuthenticatorAuxiliaryPaths(t *testing.T) {
	a := NewJWTAuthenticator("secret", "aud", "iss")
	claims := &StaffClaims{}
	if _, err := a.GenerateToken(claims); err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	token, err := a.GenerateToken(claims)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if _, err := a.ValidateToken(token); err == nil {
		t.Fatal("token without expiry must be rejected")
	}
	if _, err := a.ParseStaffClaims("bad"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("bad token must map to ErrInvalidToken, got %v", err)
	}
}

func TestUPIValidationEdgeCases(t *testing.T) {
	for _, id := range []string{"432198765432", "1 2 3 4 5 6 7 8 9 8 7 6", "  908765432109  "} {
		if !IsValidUPITransactionID(id) {
			t.Fatalf("expected %q to be valid", id)
		}
	}
	for _, id := range []string{"1234567", strings.Repeat("1", 12), "123456789012", "987654321098", "ABCDEFGHIJKL", "12345 67890A1"} {
		if IsValidUPITransactionID(id) {
			t.Fatalf("expected %q to be invalid", id)
		}
	}
}

func TestBuildUPIIntentURIEncoding(t *testing.T) {
	uri := BuildUPIIntentURI(" venu@icici ", "House Untold & Co", "Booking #1", 500)
	if !strings.HasPrefix(uri, "upi://pay?") || !strings.Contains(uri, "pa=venu%40icici") || !strings.Contains(uri, "tn=Booking+%231") {
		t.Fatalf("unexpected UPI URI: %s", uri)
	}
}
