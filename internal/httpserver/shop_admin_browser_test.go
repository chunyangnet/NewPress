package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/shop"
	"newpress.local/newpress/internal/virtualcurrency"
)

func TestShopAdminBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_SHOP_ADMIN_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated shop administration browser fixture")
	}
	shopFrontendBrowserFixture(t, directory, true)
}

func seedShopAdminBrowser(t *testing.T, service *shop.Service, user int64) map[string]string {
	t.Helper()
	orders := map[string]string{}
	for _, spec := range []struct{ name, product, variant string }{
		{"ship", "physical", ""}, {"prize", "raffle", ""}, {"review-refund", "physical", ""},
		{"reject-refund", "physical", ""}, {"refund", "physical", ""}, {"wallet", "points", ""},
		{"review-a", "digital", "personal"}, {"review-b", "digital", "studio"},
	} {
		catalog, err := service.Catalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		order, err := service.Create(t.Context(), shop.Purchase{QuoteInput: shop.QuoteInput{Product: spec.product, Variant: spec.variant, Quantity: 1}, UserID: user, Version: catalog.Version, RequestID: "admin-browser-order-" + spec.name, Accepted: true, Address: shop.Address{Name: "商城验收", Phone: "123456789", Region: "测试地区", Detail: "测试地址"}})
		if err != nil {
			t.Fatal(spec.name, err)
		}
		if spec.product == "points" {
			order, err = service.PayWallet(t.Context(), user, order.ID, 0)
		} else {
			order, err = service.MarkPaid(t.Context(), shop.PaymentReceipt{OrderID: order.ID, UserID: user, Index: 0, Reference: "admin-browser-paid-" + order.ID, Amount: order.Total, Currency: "CNY"})
		}
		if err != nil {
			t.Fatal(spec.name, err)
		}
		if spec.name == "review-refund" || spec.name == "reject-refund" || spec.name == "refund" || spec.name == "wallet" {
			if _, err = service.RequestRefund(t.Context(), user, order.ID, "验收退款申请"); err != nil {
				t.Fatal(err)
			}
		}
		if spec.name == "refund" {
			if _, err = service.ReviewRefund(t.Context(), order.ID, true, "批准渠道退款"); err != nil {
				t.Fatal(err)
			}
		}
		if spec.name == "review-a" || spec.name == "review-b" {
			if _, err = service.CreateReview(t.Context(), user, order.ID, "验收买家", shop.ReviewInput{Rating: 5, Content: "资源已收到，使用说明清楚。"}); err != nil {
				t.Fatal(err)
			}
		}
		orders[spec.name] = order.ID
	}
	return orders
}

// Browser mutations obtain real service receipts from disposable clones. The
// page's stores, including wallet debits and credits, remain unchanged.
func registerShopAdminBrowserResponses(t *testing.T, mux *http.ServeMux, server *Server, service *shop.Service, ledgerPath string, user int64) {
	mux.HandleFunc("POST /fixture/admin-response", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Operation      string                     `json:"operation"`
			ID             string                     `json:"id"`
			Ticket         string                     `json:"ticket"`
			Body           json.RawMessage            `json:"body"`
			Catalog        *shop.Catalog              `json:"catalog"`
			Coupons        []shop.Coupon              `json:"coupons"`
			CouponVersion  int64                      `json:"coupon_version"`
			Orders         map[string]json.RawMessage `json:"orders"`
			Reviews        map[string]shop.Review     `json:"reviews"`
			WalletDisabled bool                       `json:"wallet_disabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&input); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		state, err := service.Backend.Read(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Break every shared map/slice before changing the clone.
		encoded, _ := json.Marshal(state)
		if err = json.Unmarshal(encoded, &state); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if input.Catalog != nil {
			state.Catalog = *input.Catalog
		}
		if input.Coupons != nil {
			state.Coupons, state.CouponVersion = input.Coupons, input.CouponVersion
		}
		for id, raw := range input.Orders {
			original, ok := state.Orders[id]
			if !ok {
				http.Error(w, "unknown order", 400)
				return
			}
			updated := original
			if err = json.Unmarshal(raw, &updated); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			updated.Snapshot, updated.Reservations, updated.RequestID, updated.Fingerprint = original.Snapshot, original.Reservations, original.RequestID, original.Fingerprint
			state.Orders[id] = updated
		}
		for id, review := range input.Reviews {
			state.Reviews[id] = review
		}
		backend, err := shop.NewFileBackend("")
		if err == nil {
			err = backend.Update(r.Context(), func(next *shop.State) error { *next = state; return nil })
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		ledgerBytes, err := os.ReadFile(ledgerPath)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		clonePath := filepath.Join(t.TempDir(), "currency.json")
		if err = os.WriteFile(clonePath, ledgerBytes, 0600); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		ledger, err := virtualcurrency.NewFileStore(clonePath)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if input.WalletDisabled {
			cfg, e := ledger.Settings(r.Context())
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			cfg.Enabled = false
			if _, e = ledger.UpdateSettings(r.Context(), cfg.Version, cfg); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
		}
		clone := *service
		clone.Backend, clone.Currencies = backend, virtualcurrency.NewMemoryCatalog(ledger)
		decode := func(value any) error {
			decoder := json.NewDecoder(bytes.NewReader(input.Body))
			decoder.DisallowUnknownFields()
			return decoder.Decode(value)
		}
		var result any
		switch input.Operation {
		case "catalog":
			var desired shop.Catalog
			err = decode(&desired)
			if err == nil {
				for i := range desired.Products {
					p := &desired.Products[i]
					p.DescriptionAuthorID = 0
					for _, old := range state.Catalog.Products {
						if old.Key == p.Key {
							p.DescriptionAuthorID = old.DescriptionAuthorID
						}
					}
					if p.DescriptionAuthorID == 0 && p.DescriptionSource != "" {
						p.DescriptionAuthorID = user
					}
				}
				var catalog shop.Catalog
				catalog, err = clone.SaveCatalog(r.Context(), desired)
				result = map[string]any{"catalog": catalog}
			}
		case "coupons":
			var desired struct {
				Version int64         `json:"version"`
				Coupons []shop.Coupon `json:"coupons"`
			}
			err = decode(&desired)
			if err == nil {
				var version int64
				version, err = clone.SaveCoupons(r.Context(), desired.Version, desired.Coupons)
				current, readErr := backend.Read(r.Context())
				if err == nil {
					err = readErr
				}
				result = map[string]any{"version": version, "coupons": current.Coupons}
			}
		case "review":
			var desired shop.ReviewModeration
			err = decode(&desired)
			if err == nil {
				var review shop.Review
				review, err = clone.ModerateReview(r.Context(), user, input.ID, desired)
				result = map[string]any{"review": review}
			}
		case "ship", "refund", "refund-review", "deliver":
			var order shop.Order
			switch input.Operation {
			case "ship":
				var desired struct {
					Carrier  string `json:"carrier"`
					Tracking string `json:"tracking"`
				}
				err = decode(&desired)
				if err == nil {
					order, err = clone.Ship(r.Context(), shop.Shipment{OrderID: input.ID, Carrier: desired.Carrier, Tracking: desired.Tracking})
				}
			case "refund":
				var desired struct {
					Reference string `json:"reference"`
					Note      string `json:"note"`
				}
				err = decode(&desired)
				if err == nil {
					order, err = clone.ReconcileRefund(r.Context(), shop.Refund{OrderID: input.ID, Reference: desired.Reference, Note: desired.Note})
				}
			case "refund-review":
				var desired struct {
					Approved bool   `json:"approved"`
					Note     string `json:"note"`
				}
				err = decode(&desired)
				if err == nil {
					order, err = clone.ReviewRefund(r.Context(), input.ID, desired.Approved, desired.Note)
				}
			case "deliver":
				var desired struct {
					Content string `json:"content"`
				}
				err = decode(&desired)
				if err == nil {
					order, err = clone.DeliverPrize(r.Context(), input.ID, input.Ticket, desired.Content)
				}
			}
			result = map[string]any{"order": shop.PublicOrder(order)}
		case "advance":
			err = clone.Advance(r.Context())
			result = map[string]bool{"ok": true}
		default:
			err = fmt.Errorf("unknown fixture operation")
		}
		status := 200
		if err != nil {
			status = 422
			result = map[string]any{"error": map[string]string{"message": err.Error()}}
		}
		final, readErr := backend.Read(r.Context())
		if readErr != nil {
			http.Error(w, readErr.Error(), 500)
			return
		}
		public := map[string]shop.Order{}
		for id, order := range final.Orders {
			public[id] = shop.PublicOrder(order)
		}
		writeJSON(w, 200, map[string]any{"status": status, "body": result, "orders": public, "catalog": final.Catalog, "coupons": final.Coupons, "coupon_version": final.CouponVersion, "reviews": final.Reviews})
	})
}
