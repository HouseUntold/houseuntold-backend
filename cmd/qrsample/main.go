package main

import (
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/venu-live/venu-api/internal/auth"
)

func generate(secret string, ticketID, eventID uuid.UUID, now time.Time) string {
	return auth.NewQRSigner(secret).SignToken(ticketID, eventID, now, now.Add(time.Hour))
}

func scan(secret, token string, now time.Time) (uuid.UUID, error) {
	payload, err := auth.NewQRSigner(secret).VerifyToken(token, now)
	if err != nil {
		return uuid.Nil, err
	}
	return payload.TicketID, nil
}

func flipLast(s string) string {
	replacement := byte('x')
	if s[len(s)-1] == replacement {
		replacement = 'y'
	}
	return s[:len(s)-1] + string(replacement)
}

func main() {
	secret := os.Getenv("QR_SIGNING_SECRET")
	if secret == "" {
		fmt.Println("QR_SIGNING_SECRET is required")
		os.Exit(2)
	}

	now := time.Now().UTC()
	ticketID, eventID := uuid.New(), uuid.New()
	token := generate(secret, ticketID, eventID, now)

	scanned, err := scan(secret, token, now.Add(time.Minute))
	if err != nil {
		fmt.Println("valid scan rejected:", err)
		os.Exit(1)
	}
	fmt.Println("valid scan ticket id:", scanned)

	tampered := flipLast(token)
	if _, err := scan(secret, tampered, now); err != nil {
		fmt.Println("tampered scan rejected:", err)
		return
	}
	fmt.Println("tampered scan accepted")
	os.Exit(1)
}
