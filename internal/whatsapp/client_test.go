package whatsapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func testNotifier(t *testing.T, rt roundTripFunc) *CloudNotifier {
	t.Helper()
	n := NewCloudNotifier("wa-token", "1234567890", "v21.0", 1, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.httpClient.Transport = rt
	return n
}

func validMessage() TicketPassMessage {
	return TicketPassMessage{
		TicketID:       "ticket-1",
		RecipientPhone: "+91 98765 43210",
		PrimaryGuest:   "Aarav",
		EventTitle:     "Saturday Noir",
		EventLocation:  "Bengaluru",
		EventStartsAt:  time.Now().UTC(),
		CategoryName:   "COUPLE",
		TicketURL:      "https://venu.test/tickets/ticket-1",
		BarCode:        "K4829",
		EntryCount:     2,
		FoodBalance:    2000,
	}
}

func TestNewCloudNotifierDefaults(t *testing.T) {
	n := NewCloudNotifier(" token ", "1234567890", "bad-version", 0, 0, nil)
	if n.apiVersion != "v21.0" || n.httpClient.Timeout != 10*time.Second || cap(n.jobs) != 512 {
		t.Fatalf("unexpected notifier defaults: %+v", n)
	}
	if n.accessToken != "token" {
		t.Fatalf("token was not trimmed: %q", n.accessToken)
	}
	if err := n.CloseAndWait(context.Background()); err != nil {
		t.Fatalf("close failed: %v", err)
	}
}

func TestSanitizeE164Digits(t *testing.T) {
	for in, want := range map[string]string{
		"+91 98765 43210": "919876543210",
		"9876543210":      "919876543210",
		"999999999999":    "999999999999",
		"not-a-phone":     "",
	} {
		if got := sanitizeE164Digits(in); got != want {
			t.Fatalf("sanitizeE164Digits(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSendWithoutCredentialsSkips(t *testing.T) {
	n := testNotifier(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("no HTTP call expected without credentials")
		return nil, nil
	})
	n.accessToken = ""
	if err := n.sendTicketImageAndMessage(context.Background(), validMessage()); err != nil {
		t.Fatalf("skip path returned error: %v", err)
	}
	defer n.CloseAndWait(context.Background())
}

func TestSendWithBadPhoneRejected(t *testing.T) {
	n := testNotifier(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("no HTTP call expected for invalid phone")
		return nil, nil
	})
	msg := validMessage()
	msg.RecipientPhone = "123"
	if err := n.sendTicketImageAndMessage(context.Background(), msg); err == nil {
		t.Fatal("invalid phone must be rejected")
	}
	defer n.CloseAndWait(context.Background())
}

func TestSendTicketImageAndMessageSuccess(t *testing.T) {
	var calls []string
	n := testNotifier(t, func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer wa-token" {
			t.Fatalf("missing auth header: %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		bodyStr := string(body)
		if !strings.Contains(bodyStr, "K4829") || !strings.Contains(bodyStr, "Aarav") || !strings.Contains(bodyStr, "Saturday Noir") {
			t.Fatalf("unexpected message payload: %s", body)
		}
		return response(http.StatusOK, `{"message_id":"m1"}`), nil
	})
	defer n.CloseAndWait(context.Background())

	if err := n.sendTicketImageAndMessage(context.Background(), validMessage()); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if len(calls) != 1 || !strings.HasSuffix(calls[0], "/messages") {
		t.Fatalf("unexpected call sequence: %v", calls)
	}
}

func TestSendTicketImageFailures(t *testing.T) {
	tests := []struct {
		name string
		rt   roundTripFunc
	}{
		{"transport error", func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }},
		{"message status 400", func(*http.Request) (*http.Response, error) {
			return response(http.StatusBadRequest, "bad request"), nil
		}},
		{"message status 403", func(*http.Request) (*http.Response, error) { return response(http.StatusForbidden, "denied"), nil }},
		{"message status 500", func(*http.Request) (*http.Response, error) {
			return response(http.StatusInternalServerError, "server error"), nil
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := testNotifier(t, tc.rt)
			defer n.CloseAndWait(context.Background())
			if err := n.sendTicketImageAndMessage(context.Background(), validMessage()); err == nil {
				t.Fatalf("expected %s to fail", tc.name)
			}
		})
	}
}

func TestEnqueueAndCloseAndWait(t *testing.T) {
	n := testNotifier(t, func(*http.Request) (*http.Response, error) { return response(http.StatusOK, `{"id":"media-1"}`), nil })
	n.EnqueueTicketConfirmation(validMessage())
	if err := n.CloseAndWait(context.Background()); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	n.EnqueueTicketConfirmation(validMessage())
	if err := n.CloseAndWait(context.Background()); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
}

func TestCloseAndWaitRespectsContext(t *testing.T) {
	n := testNotifier(t, func(*http.Request) (*http.Response, error) {
		time.Sleep(300 * time.Millisecond)
		return response(http.StatusOK, `{"id":"media-1"}`), nil
	})
	n.EnqueueTicketConfirmation(validMessage())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.CloseAndWait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context error, got %v", err)
	}
	if err := n.CloseAndWait(context.Background()); err != nil {
		t.Fatalf("drain failed: %v", err)
	}
}
