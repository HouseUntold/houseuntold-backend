package test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/services"
)

func confirmedInactiveFemaleTicket(t *testing.T, env *testEnv, adminToken, phone, email, name, upi string) services.PublicTicketView {
	t.Helper()
	assertStatus(t, env.doRequest(http.MethodPatch, "/api/admin/events/"+env.eventID.String()+"/first-99", map[string]bool{"is_first_99_active": false}, bearerHeader(adminToken)), http.StatusOK)

	rec := env.doRequest(http.MethodPost, "/api/checkout/init", services.InitCheckoutRequest{
		EventID:          env.eventID,
		CategoryID:       env.femaleCatID,
		ContactPhone:     phone,
		ContactEmail:     email,
		UPITransactionID: upi,
		Guests:           []services.GuestInput{{Name: name, Age: 25, Gender: db.GenderTypeFEMALE, IsPrimaryContact: true}},
	}, nil)
	assertStatus(t, rec, http.StatusCreated)
	var checkout services.InitCheckoutResponse
	decodeBody(t, rec, &checkout)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/admin/bookings/"+checkout.BookingID.String()+"/verify", map[string]string{"action": "APPROVE"}, bearerHeader(adminToken)), http.StatusOK)

	rec = env.doRequest(http.MethodGet, "/api/tickets/"+checkout.TicketID.String(), nil, nil)
	assertStatus(t, rec, http.StatusOK)
	var ticket services.PublicTicketView
	decodeBody(t, rec, &ticket)
	return ticket
}

// Covers Routes 12–17:
//  12. GET    /api/counter/menu
//  13. POST   /api/counter/menu
//  14. DELETE /api/counter/menu/{id}
//  15. GET    /api/counter/search
//  16. POST   /api/counter/redeem
//  17. GET    /api/counter/tickets/{bar_code}/history
func TestBartenderMenuCRUDAndTraceableRedemption(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")
	barToken, _ := env.loginAs("bar_user", "BarPass!123")
	scannerToken, _ := env.loginAs("gate_user", "GatePass!123")

	// Scanner cannot access bartender routes -> 403
	assertStatus(t, env.doRequest(http.MethodGet, "/api/counter/menu", nil, bearerHeader(scannerToken)), http.StatusForbidden)

	// 12. GET /api/counter/menu -> 200 seeded 2 items
	rec := env.doRequest(http.MethodGet, "/api/counter/menu", nil, bearerHeader(barToken))
	assertStatus(t, rec, http.StatusOK)
	var menuResp struct {
		Items []db.MenuItem `json:"items"`
	}
	decodeBody(t, rec, &menuResp)
	if len(menuResp.Items) != 2 {
		t.Fatalf("expected 2 seeded menu items, got %d", len(menuResp.Items))
	}

	// 13. POST /api/counter/menu -> bartender forbidden (403), admin creates new drink (201)
	assertStatus(t, env.doRequest(http.MethodPost, "/api/counter/menu", services.CreateMenuItemRequest{
		Name:      "Espresso Martini",
		Category:  db.MenuCategoryDRINK,
		UnitPrice: 600,
	}, bearerHeader(barToken)), http.StatusForbidden)

	rec = env.doRequest(http.MethodPost, "/api/counter/menu", services.CreateMenuItemRequest{
		Name:      "Espresso Martini",
		Category:  db.MenuCategoryDRINK,
		UnitPrice: 600,
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusCreated)
	var createdItem db.MenuItem
	decodeBody(t, rec, &createdItem)
	if createdItem.UnitPrice != 600 || !createdItem.IsActive {
		t.Fatalf("unexpected created menu item: %+v", createdItem)
	}

	assertStatus(t, env.doRequest(http.MethodPut, "/api/counter/menu/"+createdItem.ID.String(), services.UpdateMenuItemRequest{
		Name: "Espresso Martini", Category: db.MenuCategoryDRINK, UnitPrice: 650, ServingLabel: "200 ml",
	}, bearerHeader(barToken)), http.StatusForbidden)

	rec = env.doRequest(http.MethodPut, "/api/counter/menu/"+createdItem.ID.String(), services.UpdateMenuItemRequest{
		Name: "Espresso Martini", Category: db.MenuCategoryDRINK, UnitPrice: 650, ServingLabel: "200 ml",
	}, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	var updatedItem db.MenuItem
	decodeBody(t, rec, &updatedItem)
	if updatedItem.UnitPrice != 650 || updatedItem.ServingLabel != "200 ml" {
		t.Fatalf("unexpected updated menu item: %+v", updatedItem)
	}

	// 14. DELETE /api/counter/menu/{id} -> bartender forbidden (403), admin soft-deletes (200), second delete (404)
	assertStatus(t, env.doRequest(http.MethodDelete, "/api/counter/menu/"+createdItem.ID.String(), nil, bearerHeader(barToken)), http.StatusForbidden)

	rec = env.doRequest(http.MethodDelete, "/api/counter/menu/"+createdItem.ID.String(), nil, bearerHeader(adminToken))
	assertStatus(t, rec, http.StatusOK)
	assertStatus(t, env.doRequest(http.MethodDelete, "/api/counter/menu/"+createdItem.ID.String(), nil, bearerHeader(adminToken)), http.StatusNotFound)

	pass := confirmedInactiveFemaleTicket(t, env, adminToken, "+919800001122", "tara@example.com", "Tara Sen", "990011223344")

	// 15. GET /api/counter/search -> bad format 400, valid 5-char 1D code 200
	assertStatus(t, env.doRequest(http.MethodGet, "/api/counter/search?bar_code=BAD", nil, bearerHeader(barToken)), http.StatusBadRequest)

	rec = env.doRequest(http.MethodGet, "/api/counter/search?bar_code="+pass.BarCode, nil, bearerHeader(barToken))
	assertStatus(t, rec, http.StatusOK)
	var searchView services.BarTicketSearchView
	decodeBody(t, rec, &searchView)
	if searchView.FoodBalance != 1000 || searchView.WalletExpired || len(searchView.Guests) != 1 {
		t.Fatalf("unexpected bar search view: %+v", searchView)
	}
	guestID := searchView.Guests[0].ID

	// Ordering a deleted menu item -> 400
	assertStatus(t, env.doRequest(http.MethodPost, "/api/counter/redeem", services.RedeemItemsRequest{
		BarCode: pass.BarCode,
		Items:   []services.RedeemLineItemInput{{MenuItemID: createdItem.ID, Quantity: 1}},
	}, bearerHeader(barToken)), http.StatusBadRequest)

	// 16a. POST /api/counter/redeem (2x Smoked Negroni = 1500 INR against 1000 wallet -> 1000 deducted, 500 delta_to_collect + free shot)
	idemHdr := map[string]string{
		"Authorization":   "Bearer " + barToken,
		"Idempotency-Key": "bar-order-001",
	}
	redeemReq := services.RedeemItemsRequest{
		BarCode:               pass.BarCode,
		Items:                 []services.RedeemLineItemInput{{MenuItemID: env.drinkID, Quantity: 2}},
		RedeemFreeShotGuestID: &guestID,
	}
	rec = env.doRequest(http.MethodPost, "/api/counter/redeem", redeemReq, idemHdr)
	assertStatus(t, rec, http.StatusOK)
	var redeemResp services.RedeemItemsResponse
	decodeBody(t, rec, &redeemResp)
	if redeemResp.TotalBill != 1500 || redeemResp.WalletDeducted != 1000 || redeemResp.DeltaToCollect != 500 || redeemResp.RemainingBalance != 0 || !redeemResp.FreeShotRedeemed {
		t.Fatalf("unexpected settlement math: %+v", redeemResp)
	}

	// 16b. Idempotent retry with identical Idempotency-Key returns original receipt without double-charging
	rec = env.doRequest(http.MethodPost, "/api/counter/redeem", redeemReq, idemHdr)
	assertStatus(t, rec, http.StatusOK)
	var replayResp services.RedeemItemsResponse
	decodeBody(t, rec, &replayResp)
	if !replayResp.IdempotentReplay || replayResp.TransactionID != redeemResp.TransactionID {
		t.Fatalf("expected idempotent replay of tx %s, got %+v", redeemResp.TransactionID, replayResp)
	}

	// 16c. Free shot replay on new transaction -> 409 Conflict
	assertStatus(t, env.doRequest(http.MethodPost, "/api/counter/redeem", services.RedeemItemsRequest{
		BarCode:               pass.BarCode,
		RedeemFreeShotGuestID: &guestID,
	}, bearerHeader(barToken)), http.StatusConflict)

	// 17. GET /api/counter/tickets/{bar_code}/history -> full before/after balance & line-item traceability
	rec = env.doRequest(http.MethodGet, "/api/counter/tickets/"+pass.BarCode+"/history?limit=10", nil, bearerHeader(barToken))
	assertStatus(t, rec, http.StatusOK)
	var histResp struct {
		BarCode      string                               `json:"bar_code"`
		Transactions []services.BarTransactionHistoryView `json:"transactions"`
	}
	decodeBody(t, rec, &histResp)
	if len(histResp.Transactions) != 1 || histResp.Transactions[0].BalanceBefore != 1000 || histResp.Transactions[0].BalanceAfter != 0 || len(histResp.Transactions[0].Items) != 1 {
		t.Fatalf("unexpected bar history audit trail: %+v", histResp)
	}

	// 16d. After event.ends_at passes, multi-use 1D bartender barcode expires automatically -> 409
	env.store.mu.Lock()
	ev := env.store.events[env.eventID]
	ev.EndsAt = time.Now().UTC().Add(-1 * time.Minute)
	env.store.events[env.eventID] = ev
	env.store.mu.Unlock()

	assertStatus(t, env.doRequest(http.MethodPost, "/api/counter/redeem", services.RedeemItemsRequest{
		BarCode: pass.BarCode,
		Items:   []services.RedeemLineItemInput{{MenuItemID: env.foodID, Quantity: 1}},
	}, bearerHeader(barToken)), http.StatusConflict)
}

func TestBartenderConcurrentWalletRedemptionsNeverGoNegative(t *testing.T) {
	env := newTestEnv(t)
	adminToken, _ := env.loginAs("admin_user", "AdminPass!123")
	barToken, _ := env.loginAs("bar_user", "BarPass!123")

	pass := confirmedInactiveFemaleTicket(t, env, adminToken, "+919800009999", "concurrent@example.com", "Neha Rao", "887766554433")

	// Fire 5 concurrent orders of 500 INR (Truffle Fries) -> total bill 2500 across 5 txns:
	// Strictly 1000 total deducted from wallet, 1500 total delta_to_collect, 0 remaining balance!
	var (
		wg          sync.WaitGroup
		mu          sync.Mutex
		totalWallet int32
		totalDelta  int32
	)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r := env.doRequest(http.MethodPost, "/api/counter/redeem", services.RedeemItemsRequest{
				BarCode: pass.BarCode,
				Items:   []services.RedeemLineItemInput{{MenuItemID: env.foodID, Quantity: 1}},
			}, map[string]string{
				"Authorization":   "Bearer " + barToken,
				"Idempotency-Key": fmt.Sprintf("concurrent-order-%d", idx),
			})
			if r.Code != http.StatusOK {
				t.Errorf("concurrent redeem %d failed with %d: %s", idx, r.Code, r.Body.String())
				return
			}
			var out services.RedeemItemsResponse
			decodeBody(t, r, &out)
			mu.Lock()
			totalWallet += out.WalletDeducted
			totalDelta += out.DeltaToCollect
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if totalWallet != 1000 || totalDelta != 1500 {
		t.Fatalf("wallet conservation violated: totalWallet=%d (want 1000), totalDelta=%d (want 1500)", totalWallet, totalDelta)
	}
}
