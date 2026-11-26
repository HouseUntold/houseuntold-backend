package ticketimg

import (
	"bytes"
	"testing"
	"time"
)

func TestRenderTicketPNG_ProducesValidPNG(t *testing.T) {
	pngBytes, err := RenderTicketPNG(TicketImageData{
		TicketID:      "550e8400-e29b-41d4-a716-446655440000",
		EventTitle:    "Solstice Underground",
		EventLocation: "Bandra Terrace, Mumbai",
		EventStartsAt: time.Date(2026, 12, 31, 21, 0, 0, 0, time.UTC),
		CategoryName:  "COUPLE",
		PrimaryGuest:  "Aarav Sharma",
		EntryCount:    2,
		FoodBalance:   3000,
		BarCode:       "K4829",
		GateCheckedIn: false,
	})
	if err != nil {
		t.Fatalf("RenderTicketPNG failed: %v", err)
	}
	pngMagic := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if len(pngBytes) < 1000 || !bytes.HasPrefix(pngBytes, pngMagic) {
		t.Fatalf("expected valid PNG output, got %d bytes", len(pngBytes))
	}
}
