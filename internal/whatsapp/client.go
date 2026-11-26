package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const metaGraphBaseURL = "https://graph.facebook.com"

var (
	validAPIVersionRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+$`)
	validDigitsRE     = regexp.MustCompile(`^[0-9]{6,20}$`)
)

// TicketPassMessage carries confirmed pass details rendered into a ticket PNG and sent via WhatsApp Cloud API.
type TicketPassMessage struct {
	TicketID       string
	RecipientPhone string
	PrimaryGuest   string
	EventTitle     string
	EventLocation  string
	EventStartsAt  time.Time
	CategoryName   string
	TicketURL      string
	BarCode        string // 5-char 1D linear bartender barcode (e.g. K4829)
	EntryCount     int32
	FoodBalance    int32
}

// Notifier dispatches confirmed ticket image + message notifications asynchronously and drains cleanly on shutdown.
type Notifier interface {
	EnqueueTicketConfirmation(msg TicketPassMessage)
	CloseAndWait(ctx context.Context) error
}

// CloudNotifier uploads the rendered PNG ticket pass to Meta's Media API and sends a WhatsApp image+caption message.
type CloudNotifier struct {
	accessToken   string
	phoneNumberID string
	apiVersion    string
	httpClient    *http.Client
	logger        *slog.Logger

	jobs      chan TicketPassMessage
	wg        sync.WaitGroup
	closeOnce sync.Once
	closed    bool
	mu        sync.RWMutex
}

func NewCloudNotifier(
	accessToken, phoneNumberID, apiVersion string,
	workers int,
	httpTimeout time.Duration,
	logger *slog.Logger,
) *CloudNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	if workers <= 0 {
		workers = 4
	}
	if httpTimeout <= 0 {
		httpTimeout = 10 * time.Second
	}
	if !validAPIVersionRE.MatchString(apiVersion) {
		apiVersion = "v21.0"
	}

	n := &CloudNotifier{
		accessToken:   strings.TrimSpace(accessToken),
		phoneNumberID: strings.TrimSpace(phoneNumberID),
		apiVersion:    apiVersion,
		httpClient:    &http.Client{Timeout: httpTimeout},
		logger:        logger,
		jobs:          make(chan TicketPassMessage, 512),
	}

	for i := 0; i < workers; i++ {
		n.wg.Add(1)
		go n.worker()
	}
	return n
}

func (n *CloudNotifier) EnqueueTicketConfirmation(msg TicketPassMessage) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.closed {
		return
	}
	select {
	case n.jobs <- msg:
	default:
		n.logger.Warn("whatsapp notification queue full; dropping message", "ticket_id", msg.TicketID)
	}
}

func (n *CloudNotifier) CloseAndWait(ctx context.Context) error {
	n.closeOnce.Do(func() {
		n.mu.Lock()
		n.closed = true
		close(n.jobs)
		n.mu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		n.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *CloudNotifier) worker() {
	defer n.wg.Done()
	for msg := range n.jobs {
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), n.httpClient.Timeout)
			lastErr = n.sendTicketImageAndMessage(ctx, msg)
			cancel()
			if lastErr == nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
		}
		if lastErr != nil {
			n.logger.Error("whatsapp ticket image dispatch failed after retries",
				"ticket_id", msg.TicketID,
				"error", lastErr,
			)
		}
	}
}

type waTextMessageRequest struct {
	MessagingProduct string        `json:"messaging_product"`
	RecipientType    string        `json:"recipient_type"`
	To               string        `json:"to"`
	Type             string        `json:"type"`
	Text             waTextPayload `json:"text"`
}

type waTextPayload struct {
	PreviewURL bool   `json:"preview_url"`
	Body       string `json:"body"`
}

func (n *CloudNotifier) sendTicketImageAndMessage(ctx context.Context, msg TicketPassMessage) error {
	if n.accessToken == "" || !validDigitsRE.MatchString(n.phoneNumberID) {
		n.logger.Info("whatsapp cloud api credentials not configured; skipping outbound message",
			"ticket_id", msg.TicketID,
			"bar_code", msg.BarCode,
		)
		return nil
	}

	toPhone := sanitizeE164Digits(msg.RecipientPhone)
	if !validDigitsRE.MatchString(toPhone) {
		return errors.New("whatsapp: invalid recipient phone number")
	}

	// Dispatch structured text message with all ticket pass data (no image rendering/storage needed).
	bodyText := fmt.Sprintf(
		"🎟️ *Your HouseUntold Pass is Confirmed!*\n\n"+
			"👤 *Booked By:* %s\n"+
			"👥 *Attendees:* %d\n"+
			"🎉 *Event:* %s\n"+
			"📅 *Date & Time:* %s\n"+
			"📍 *Venue:* %s\n"+
			"🎫 *Category:* %s\n"+
			"💰 *F&B Wallet Balance:* ₹%d\n"+
			"🍸 *Bartender Barcode:* *%s* (Show at counter to redeem)\n\n"+
			"🔗 *View your live Ticket & Gate QR Code:*\n%s\n\n"+
			"ℹ️ *Note:*\n"+
			"• Gate Entry QR is single-use and expires immediately after scanning.\n"+
			"• Bartender barcode remains active until event ends or balance is fully spent.",
		msg.PrimaryGuest,
		msg.EntryCount,
		msg.EventTitle,
		msg.EventStartsAt.Format("02 Jan 2006, 03:04 PM"),
		msg.EventLocation,
		msg.CategoryName,
		msg.FoodBalance,
		msg.BarCode,
		msg.TicketURL,
	)

	payload := waTextMessageRequest{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               toPhone,
		Type:             "text",
		Text: waTextPayload{
			PreviewURL: true,
			Body:       bodyText,
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("whatsapp_marshal: %w", err)
	}

	endpoint := fmt.Sprintf("%s/%s/%s/messages", metaGraphBaseURL, n.apiVersion, n.phoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("whatsapp_new_request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+n.accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp_http_do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("whatsapp_api_status_%d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	n.logger.Info("whatsapp ticket confirmation message sent", "ticket_id", msg.TicketID)
	return nil
}

func sanitizeE164Digits(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if len(digits) == 10 {
		return "91" + digits
	}
	return digits
}
