package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/venu-live/venu-api/internal/db"
)

const (
	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
)

var (
	ErrInvalidToken     = errors.New("invalid or expired token")
	ErrUnexpectedClaims = errors.New("unexpected token claims")
)

// StaffClaims carries signed RBAC metadata for authenticated staff members.
type StaffClaims struct {
	StaffID      uuid.UUID    `json:"sid"`
	Username     string       `json:"username"`
	Role         db.StaffRole `json:"role"`
	TokenType    string       `json:"typ"`
	TokenVersion int32        `json:"ver"`
	jwt.RegisteredClaims
}

// JWTAuthenticator implements Authenticator using HMAC-SHA256.
type JWTAuthenticator struct {
	secret string
	aud    string
	iss    string
}

func NewJWTAuthenticator(secret, aud, iss string) *JWTAuthenticator {
	return &JWTAuthenticator{
		secret: secret,
		aud:    aud,
		iss:    iss,
	}
}

func (a *JWTAuthenticator) GenerateToken(claims jwt.Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(a.secret))
	if err != nil {
		return "", fmt.Errorf("jwt_sign: %w", err)
	}
	return tokenString, nil
}

func (a *JWTAuthenticator) ValidateToken(token string) (*jwt.Token, error) {
	return jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return []byte(a.secret), nil
	},
		jwt.WithExpirationRequired(),
		jwt.WithAudience(a.aud),
		jwt.WithIssuer(a.iss),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}),
	)
}

func (a *JWTAuthenticator) ParseStaffClaims(token string) (*StaffClaims, error) {
	claims := &StaffClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return []byte(a.secret), nil
	},
		jwt.WithExpirationRequired(),
		jwt.WithAudience(a.aud),
		jwt.WithIssuer(a.iss),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}),
	)
	if err != nil || !parsed.Valid {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	return claims, nil
}

// TokenManager issues and validates separate access and refresh JWTs for staff sessions.
type TokenManager struct {
	accessAuth  *JWTAuthenticator
	refreshAuth *JWTAuthenticator
	aud         string
	iss         string
	accessTTL   time.Duration
	refreshTTL  time.Duration
}

type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func NewTokenManager(
	accessSecret, refreshSecret, aud, iss string,
	accessTTL, refreshTTL time.Duration,
) *TokenManager {
	return &TokenManager{
		accessAuth:  NewJWTAuthenticator(accessSecret, aud, iss),
		refreshAuth: NewJWTAuthenticator(refreshSecret, aud, iss),
		aud:         aud,
		iss:         iss,
		accessTTL:   accessTTL,
		refreshTTL:  refreshTTL,
	}
}

func (m *TokenManager) IssueStaffTokens(staff db.StaffAccount) (TokenPair, error) {
	now := time.Now().UTC()
	accessExp := now.Add(m.accessTTL)
	refreshExp := now.Add(m.refreshTTL)

	accessClaims := StaffClaims{
		StaffID:      staff.ID,
		Username:     staff.Username,
		Role:         staff.Role,
		TokenType:    TokenTypeAccess,
		TokenVersion: staff.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   staff.ID.String(),
			Issuer:    m.iss,
			Audience:  jwt.ClaimStrings{m.aud},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(accessExp),
			ID:        uuid.NewString(),
		},
	}

	refreshClaims := StaffClaims{
		StaffID:      staff.ID,
		Username:     staff.Username,
		Role:         staff.Role,
		TokenType:    TokenTypeRefresh,
		TokenVersion: staff.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   staff.ID.String(),
			Issuer:    m.iss,
			Audience:  jwt.ClaimStrings{m.aud},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(refreshExp),
			ID:        uuid.NewString(),
		},
	}

	accessToken, err := m.accessAuth.GenerateToken(accessClaims)
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue_access_token: %w", err)
	}
	refreshToken, err := m.refreshAuth.GenerateToken(refreshClaims)
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue_refresh_token: %w", err)
	}

	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    accessExp,
	}, nil
}

func (m *TokenManager) VerifyAccessToken(raw string) (*StaffClaims, error) {
	claims, err := m.accessAuth.ParseStaffClaims(raw)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != TokenTypeAccess {
		return nil, ErrUnexpectedClaims
	}
	return claims, nil
}

func (m *TokenManager) VerifyRefreshToken(raw string) (*StaffClaims, error) {
	claims, err := m.refreshAuth.ParseStaffClaims(raw)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != TokenTypeRefresh {
		return nil, ErrUnexpectedClaims
	}
	return claims, nil
}
