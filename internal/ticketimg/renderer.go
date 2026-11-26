package ticketimg

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"time"
)

// TicketImageData holds the confirmed pass metadata rendered into a WhatsApp/downloadable PNG pass.
type TicketImageData struct {
	TicketID      string
	EventTitle    string
	EventLocation string
	EventStartsAt time.Time
	CategoryName  string
	PrimaryGuest  string
	EntryCount    int32
	FoodBalance   int32
	BarCode       string // 5-char 1D linear bartender barcode (e.g. K4829)
	GateCheckedIn bool
}

// Code-39 ISO/IEC 16388 encoding table (9 elements: bar, space, bar, space, bar, space, bar, space, bar; 'w'=3x, 'n'=1x).
var code39Patterns = map[rune]string{
	'0': "nnnwwnwnn", '1': "wnnwnnnnw", '2': "nnwwnnnnw", '3': "wnwwnnnnn",
	'4': "nnnwwnnnw", '5': "wnnwwnnnn", '6': "nnwwwnnnn", '7': "nnnwnnwnw",
	'8': "wnnwnnwnn", '9': "nnwwnnwnn",
	'A': "wnnnnwnnw", 'B': "nnwnnwnnw", 'C': "wnwnnwnnn", 'D': "nnnnwwnnw",
	'E': "wnnnwwnnn", 'F': "nnwnwwnnn", 'G': "nnnnnwwnw", 'H': "wnnnnwwnn",
	'I': "nnwnnwwnn", 'J': "nnnnwwwnn", 'K': "wnnnnnnww", 'L': "nnwnnnnww",
	'M': "wnwnnnnwn", 'N': "nnnnwnnww", 'O': "wnnnwnnwn", 'P': "nnwnwnnwn",
	'Q': "nnnnnnwww", 'R': "wnnnnnwwn", 'S': "nnwnnnwwn", 'T': "nnnnwnwwn",
	'U': "wwnnnnnnw", 'V': "nwwnnnnnw", 'W': "wwwnnnnnn", 'X': "nwnnwnnnw",
	'Y': "wwnnwnnnn", 'Z': "nwwnwnnnn",
	'*': "nwnnwnwnn",
}

// RenderTicketPNG generates a light-mode editorial PNG ticket image with a scannable Code-39 1D linear
// bartender barcode and distinct gate/event metadata stub. Zero external dependencies or CGo.
func RenderTicketPNG(d TicketImageData) ([]byte, error) {
	const (
		width  = 960
		height = 480
	)

	img := image.NewRGBA(image.Rect(0, 0, width, height))

	bgWarm := color.RGBA{0xFA, 0xFA, 0xF7, 0xFF}      // #FAFAF7
	cardWhite := color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}   // #FFFFFF
	indigo := color.RGBA{0x2D, 0x2A, 0x6E, 0xFF}      // #2D2A6E
	ink := color.RGBA{0x0F, 0x14, 0x19, 0xFF}         // #0F1419
	muted := color.RGBA{0x5C, 0x65, 0x70, 0xFF}       // #5C6570
	border := color.RGBA{0xE7, 0xE5, 0xE0, 0xFF}      // #E7E5E0
	teal := color.RGBA{0x0F, 0x76, 0x6E, 0xFF}        // #0F766E
	accentLight := color.RGBA{0xEE, 0xEE, 0xF8, 0xFF} // soft indigo tint

	// Fill warm off-white backdrop
	draw.Draw(img, img.Bounds(), &image.Uniform{bgWarm}, image.Point{}, draw.Src)

	// Main ticket card surface
	cardRect := image.Rect(28, 28, width-28, height-28)
	fillRect(img, cardRect, cardWhite)
	strokeRect(img, cardRect, border, 2)

	// Top Deep Indigo editorial header bar
	fillRect(img, image.Rect(28, 28, width-28, 86), indigo)
	drawText(img, 56, 48, "VENU  OFFICIAL ENTRY PASS", cardWhite, 3)

	// Subtle vertical perforation divider between main pass and gate stub
	stubX := 650
	for y := 96; y < height-36; y += 14 {
		fillRect(img, image.Rect(stubX, y, stubX+2, y+8), border)
	}

	// Left section: Event details + Guest info + 1D Bartender Barcode
	drawText(img, 56, 112, truncateUpper(d.EventTitle, 28), ink, 3)
	drawText(img, 56, 148, "DATE: "+strings.ToUpper(d.EventStartsAt.Format("02 JAN 2006  03:04 PM")), muted, 2)
	drawText(img, 56, 174, "VENUE: "+truncateUpper(d.EventLocation, 36), muted, 2)

	fillRect(img, image.Rect(56, 206, stubX-28, 276), accentLight)
	drawText(img, 72, 220, fmt.Sprintf("GUEST: %s   ADMITS: %d", truncateUpper(d.PrimaryGuest, 18), d.EntryCount), ink, 2)
	drawText(img, 72, 246, fmt.Sprintf("TIER: %s   F&B CREDIT: INR %d", truncateUpper(d.CategoryName, 14), d.FoodBalance), teal, 2)

	// 1D Linear Barcode section (Bartender POS — Multi-use until event ends)
	drawText(img, 56, 294, "BARTENDER 1D BARCODE (MULTI-USE F&B WALLET)", muted, 2)
	drawCode39Barcode(img, 56, 322, 540, 86, strings.ToUpper(strings.TrimSpace(d.BarCode)), ink)
	drawText(img, 248, 418, strings.ToUpper(strings.TrimSpace(d.BarCode)), ink, 3)

	// Right section: Gate Stub (Single-use QR verification reference)
	drawText(img, stubX+28, 112, "GATE ENTRY STUB", indigo, 2)
	if d.GateCheckedIn {
		drawText(img, stubX+28, 144, "STATUS: SCANNED / USED", muted, 2)
	} else {
		drawText(img, stubX+28, 144, "STATUS: VALID SINGLE SCAN", teal, 2)
	}
	drawText(img, stubX+28, 180, "PASS ID:", muted, 2)
	shortID := strings.ToUpper(d.TicketID)
	if len(shortID) > 13 {
		shortID = shortID[:13]
	}
	drawText(img, stubX+28, 204, shortID, ink, 2)

	// Visual gate stamp box
	stampRect := image.Rect(stubX+28, 246, width-56, 408)
	fillRect(img, stampRect, bgWarm)
	strokeRect(img, stampRect, indigo, 2)
	drawText(img, stubX+56, 278, "SHOW LIVE QR AT", indigo, 2)
	drawText(img, stubX+56, 304, "VENUE GATE LINK", indigo, 2)
	drawText(img, stubX+56, 344, fmt.Sprintf("ADMIT %d GUEST(S)", d.EntryCount), ink, 2)
	drawText(img, stubX+56, 372, "EXPIRES ON SCAN", muted, 2)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("ticketimg_png_encode: %w", err)
	}
	return buf.Bytes(), nil
}

// drawCode39Barcode renders a crisp ISO/IEC 16388 Code-39 1D linear barcode wrapped with '*' start/stop markers.
func drawCode39Barcode(img *image.RGBA, x, y, maxWidth, height int, value string, barColor color.Color) {
	if value == "" {
		return
	}
	encoded := "*" + value + "*"

	// Each Code-39 character has 3 wide (3u) + 6 narrow (1u) = 15 units, plus 1 unit inter-character gap.
	totalUnits := len(encoded)*16 - 1
	if totalUnits <= 0 {
		return
	}
	unitPx := maxWidth / totalUnits
	if unitPx < 2 {
		unitPx = 2
	}
	actualWidth := totalUnits * unitPx
	cursorX := x + (maxWidth-actualWidth)/2
	if cursorX < x {
		cursorX = x
	}

	for ci, r := range encoded {
		pat, ok := code39Patterns[r]
		if !ok {
			continue
		}
		for pi, elem := range pat {
			w := unitPx
			if elem == 'w' {
				w = unitPx * 3
			}
			// Even indices are bars; odd indices are spaces.
			if pi%2 == 0 {
				fillRect(img, image.Rect(cursorX, y, cursorX+w, y+height), barColor)
			}
			cursorX += w
		}
		if ci < len(encoded)-1 {
			cursorX += unitPx // Inter-character narrow space
		}
	}
}

func fillRect(img *image.RGBA, r image.Rectangle, c color.Color) {
	draw.Draw(img, r, &image.Uniform{c}, image.Point{}, draw.Src)
}

func strokeRect(img *image.RGBA, r image.Rectangle, c color.Color, thickness int) {
	fillRect(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+thickness), c)
	fillRect(img, image.Rect(r.Min.X, r.Max.Y-thickness, r.Max.X, r.Max.Y), c)
	fillRect(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+thickness, r.Max.Y), c)
	fillRect(img, image.Rect(r.Max.X-thickness, r.Min.Y, r.Max.X, r.Max.Y), c)
}

func truncateUpper(s string, maxLen int) string {
	runes := []rune(strings.ToUpper(strings.TrimSpace(s)))
	if maxLen <= 1 || len(runes) <= maxLen {
		return string(runes)
	}
	return string(runes[:maxLen-1]) + "."
}

// Compact 5x7 uppercase bitmap font for clean zero-dependency ticket typography.
var font5x7 = map[rune][7]uint8{
	'A': {0x0E, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'B': {0x1E, 0x11, 0x11, 0x1E, 0x11, 0x11, 0x1E},
	'C': {0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E},
	'D': {0x1C, 0x12, 0x11, 0x11, 0x11, 0x12, 0x1C},
	'E': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F},
	'F': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10},
	'G': {0x0E, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0E},
	'H': {0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'I': {0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'J': {0x07, 0x02, 0x02, 0x02, 0x12, 0x12, 0x0C},
	'K': {0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11},
	'L': {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F},
	'M': {0x11, 0x1B, 0x15, 0x15, 0x11, 0x11, 0x11},
	'N': {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
	'O': {0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'P': {0x1E, 0x11, 0x11, 0x1E, 0x10, 0x10, 0x10},
	'Q': {0x0E, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0D},
	'R': {0x1E, 0x11, 0x11, 0x1E, 0x14, 0x12, 0x11},
	'S': {0x0F, 0x10, 0x10, 0x0E, 0x01, 0x01, 0x1E},
	'T': {0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'U': {0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'V': {0x11, 0x11, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'W': {0x11, 0x11, 0x11, 0x15, 0x15, 0x1B, 0x11},
	'X': {0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11},
	'Y': {0x11, 0x11, 0x0A, 0x04, 0x04, 0x04, 0x04},
	'Z': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1F},
	'0': {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
	'1': {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'2': {0x0E, 0x11, 0x01, 0x06, 0x08, 0x10, 0x1F},
	'3': {0x1E, 0x01, 0x01, 0x0E, 0x01, 0x01, 0x1E},
	'4': {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
	'5': {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
	'6': {0x0E, 0x10, 0x1E, 0x11, 0x11, 0x11, 0x0E},
	'7': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8': {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
	'9': {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C},
	':': {0x00, 0x04, 0x04, 0x00, 0x04, 0x04, 0x00},
	'-': {0x00, 0x00, 0x00, 0x1F, 0x00, 0x00, 0x00},
	'/': {0x01, 0x02, 0x02, 0x04, 0x08, 0x08, 0x10},
	'.': {0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x04},
	'&': {0x0C, 0x12, 0x14, 0x08, 0x15, 0x12, 0x0D},
	'(': {0x02, 0x04, 0x08, 0x08, 0x08, 0x04, 0x02},
	')': {0x08, 0x04, 0x02, 0x02, 0x02, 0x04, 0x08},
	' ': {0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
}

func drawText(img *image.RGBA, x, y int, text string, c color.Color, scale int) {
	if scale < 1 {
		scale = 1
	}
	cursorX := x
	for _, r := range strings.ToUpper(text) {
		glyph, ok := font5x7[r]
		if !ok {
			glyph = font5x7[' ']
		}
		for row := 0; row < 7; row++ {
			bits := glyph[row]
			for col := 0; col < 5; col++ {
				if (bits & (1 << (4 - col))) != 0 {
					px := cursorX + col*scale
					py := y + row*scale
					fillRect(img, image.Rect(px, py, px+scale, py+scale), c)
				}
			}
		}
		cursorX += 6 * scale
	}
}
