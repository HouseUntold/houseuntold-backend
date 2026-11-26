package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	qrTokenVersion  = "v1"
	barCodeAlphaSet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	barCodeDigitSet = "0123456789"
	barCodeCharset  = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

var (
	ErrInvalidQRToken = errors.New("invalid or forged QR token")
	ErrExpiredQRToken = errors.New("QR token has expired")

	// Bartender 5-character bar code format: 5 uppercase alphanumeric characters (e.g. A7X9P or K4829).
	barCodeFormatRE = regexp.MustCompile(`^[A-Z0-9]{5}$`)
)

// QRPayload is the verified claim set extracted from a signed single-use gate QR code.
type QRPayload struct {
	TicketID  uuid.UUID
	EventID   uuid.UUID
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// QRSigner produces and verifies HMAC-SHA256 signed gate QR tokens and database digests.
type QRSigner struct {
	secret []byte
}

func NewQRSigner(secret string) *QRSigner {
	return &QRSigner{secret: []byte(secret)}
}

// ComputeDigest returns a deterministic versioned HMAC-SHA256 hex digest of the ticket ID.
// Only this digest is stored in tickets.qr_token_digest — never the plaintext QR token.
func (s *QRSigner) ComputeDigest(ticketID uuid.UUID) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("venu:ticket:digest:"))
	mac.Write([]byte(ticketID.String()))
	return qrTokenVersion + ":" + hex.EncodeToString(mac.Sum(nil))
}

// SignToken creates a tamper-proof compact token: base64url(payload).base64url(hmac_sha256).
func (s *QRSigner) SignToken(ticketID, eventID uuid.UUID, iat, exp time.Time) string {
	rawPayload := fmt.Sprintf("%s|%s|%s|%d|%d",
		qrTokenVersion,
		ticketID.String(),
		eventID.String(),
		iat.UTC().Unix(),
		exp.UTC().Unix(),
	)
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(rawPayload))
	sig := s.sign(encodedPayload)
	return encodedPayload + "." + sig
}

// VerifyToken checks the HMAC signature in constant time and validates expiration.
func (s *QRSigner) VerifyToken(token string, now time.Time) (QRPayload, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return QRPayload{}, ErrInvalidQRToken
	}

	expectedSig := s.sign(parts[0])
	if !hmac.Equal([]byte(parts[1]), []byte(expectedSig)) {
		return QRPayload{}, ErrInvalidQRToken
	}

	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return QRPayload{}, ErrInvalidQRToken
	}

	fields := strings.Split(string(decoded), "|")
	if len(fields) != 5 || fields[0] != qrTokenVersion {
		return QRPayload{}, ErrInvalidQRToken
	}

	ticketID, err := uuid.Parse(fields[1])
	if err != nil {
		return QRPayload{}, ErrInvalidQRToken
	}
	eventID, err := uuid.Parse(fields[2])
	if err != nil {
		return QRPayload{}, ErrInvalidQRToken
	}
	iatUnix, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return QRPayload{}, ErrInvalidQRToken
	}
	expUnix, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil {
		return QRPayload{}, ErrInvalidQRToken
	}

	expTime := time.Unix(expUnix, 0).UTC()
	if now.UTC().After(expTime) {
		return QRPayload{}, ErrExpiredQRToken
	}

	return QRPayload{
		TicketID:  ticketID,
		EventID:   eventID,
		IssuedAt:  time.Unix(iatUnix, 0).UTC(),
		ExpiresAt: expTime,
	}, nil
}

func (s *QRSigner) sign(encodedPayload string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("venu:ticket:qr:"))
	mac.Write([]byte(encodedPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// GenerateBarCode creates a cryptographically random 5-character bar code value (e.g., "A7X9P").
func GenerateBarCode() (string, error) {
	var b strings.Builder
	b.Grow(5)

	max := big.NewInt(int64(len(barCodeCharset)))
	for i := 0; i < 5; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate_bar_code: %w", err)
		}
		b.WriteByte(barCodeCharset[idx.Int64()])
	}
	return b.String(), nil
}

// IsValidBarCode validates the strict 5-character bartender 1D barcode format ([A-Z][0-9]{4}).
func IsValidBarCode(code string) bool {
	return barCodeFormatRE.MatchString(strings.ToUpper(strings.TrimSpace(code)))
}

// BuildUPIIntentURI builds a standard NPCI UPI deep-link URI suitable for QR display on checkout.
func BuildUPIIntentURI(vpa, merchantName, note string, amountINR int32) string {
	params := url.Values{}
	params.Set("pa", strings.TrimSpace(vpa))
	params.Set("pn", strings.TrimSpace(merchantName))
	params.Set("am", fmt.Sprintf("%d.00", amountINR))
	params.Set("cu", "INR")
	params.Set("tn", strings.TrimSpace(note))
	return "upi://pay?" + params.Encode()
}
