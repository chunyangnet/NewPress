package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/shop"
)

// An opt-in browser fixture with isolated users, inventory and currency files.
func TestShopRefundBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_SHOP_REFUND_BROWSER_FIXTURE")
	if dir == "" {
		t.Skip("isolated shop refund browser QA")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f := newShopRefundHTTPFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		cookie := *f.cookie
		cookie.Secure = false
		http.SetCookie(w, &cookie)
		http.Redirect(w, r, "/admin/shop/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /fixture/wallet-enable", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := f.ledger.Settings(r.Context())
		if err == nil {
			cfg.Enabled = true
			_, err = f.ledger.UpdateSettings(r.Context(), cfg.Version, cfg)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /fixture/state", func(w http.ResponseWriter, r *http.Request) {
		balance, err := f.ledger.Balance(r.Context(), f.owner.UserID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		orders, err := f.service.Orders(r.Context(), 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for i := range orders {
			orders[i] = shop.PublicOrder(orders[i])
		}
		writeJSON(w, 200, map[string]any{"balance": balance, "orders": orders})
	})
	mux.Handle("/", f.handler)
	host := httptest.NewServer(mux)
	defer host.Close()
	f.server.config.PublicURL = host.URL
	body, err := json.Marshal(map[string]any{"url": host.URL, "login": host.URL + "/fixture/login", "orders": f.orders})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "fixture.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-ticker.C:
			if _, err = os.Stat(filepath.Join(dir, "stop")); err == nil {
				return
			}
		}
	}
}
