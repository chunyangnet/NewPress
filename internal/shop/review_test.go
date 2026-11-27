package shop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestReviewsVerifiedPurchaseModerationPersistenceAndPrivacy(t *testing.T) {
	f := newFixture(t, item("digital", "virtual", "direct"), item("physical", "physical", "direct"))
	o := f.create(t, "digital", 7, nil)
	in := ReviewInput{Rating: 4, Content: "交付及时，说明完整。"}
	if _, err := f.s.CreateReview(t.Context(), 8, o.ID, "买家昵称", in); !errors.Is(err, ErrNotFound) {
		t.Fatal("other buyer", err)
	}
	if _, err := f.s.CreateReview(t.Context(), 7, o.ID, "买家昵称", in); !errors.Is(err, ErrReviewEligibility) {
		t.Fatal("unpaid", err)
	}
	o = f.pay(t, o, 0)
	for _, bad := range []ReviewInput{{Rating: 0, Content: "评价"}, {Rating: 6, Content: "评价"}, {Rating: 5, Content: "  "}, {Rating: 5, Content: strings.Repeat("字", 2001)}, {Rating: 5, Content: "<script>"}} {
		if _, err := f.s.CreateReview(t.Context(), 7, o.ID, "买家昵称", bad); !errors.Is(err, ErrReviewInvalid) {
			t.Fatal("validation", err)
		}
	}
	r, err := f.s.CreateReview(t.Context(), 7, o.ID, "买家昵称", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CreateReview(t.Context(), 7, o.ID, "买家昵称", in); !errors.Is(err, ErrReviewExists) {
		t.Fatal("duplicate", err)
	}
	page, err := f.s.ProductReviews(t.Context(), ReviewQuery{Product: "digital", Page: 1})
	if err != nil || page.Summary.Count != 1 || page.Summary.Average != 4 || page.Summary.Positive != 1 || page.Summary.Distribution[3] != 1 {
		t.Fatal(page, err)
	}
	data, _ := json.Marshal(page)
	for _, secret := range []string{o.ID, "user_id", "moderator_id", "买家昵称", "hidden_reason", "private/download"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("public leak", secret, string(data))
		}
	}
	if page.Reviews[0].Author != "买***称" {
		t.Fatal(page.Reviews[0])
	}
	if _, _, _, err = f.s.OrderReview(t.Context(), 8, o.ID); err == nil {
		t.Fatal("ownership")
	}
	admin, err := f.s.ModerateReview(t.Context(), 1, r.ID, ReviewModeration{Version: 1, Reply: "感谢反馈，使用问题可以联系商家。"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ModerateReview(t.Context(), 1, r.ID, ReviewModeration{Version: 1, Reply: "旧回复"}); !errors.Is(err, ErrReviewConflict) {
		t.Fatal("stale overwrite", err)
	}
	if _, err = f.s.ModerateReview(t.Context(), 1, r.ID, ReviewModeration{Version: admin.Version, Hidden: true}); !errors.Is(err, ErrReviewInvalid) {
		t.Fatal("missing reason", err)
	}
	admin, err = f.s.ModerateReview(t.Context(), 1, r.ID, ReviewModeration{Version: admin.Version, Hidden: true, Reason: "包含个人信息", Reply: admin.Reply})
	if err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	page, err = f.s.ProductReviews(t.Context(), ReviewQuery{Product: "digital", Page: 1})
	if err != nil || page.Total != 0 || page.Summary.Count != 0 || page.Summary.Average != 0 {
		t.Fatal(page, err)
	}
	mine, eligible, note, err := f.s.OrderReview(t.Context(), 7, o.ID)
	if err != nil || mine == nil || eligible || !strings.Contains(note, "个人信息") {
		t.Fatal(mine, eligible, note, err)
	}
	admin, err = f.s.ModerateReview(t.Context(), 1, r.ID, ReviewModeration{Version: admin.Version, Reply: "已处理"})
	if err != nil {
		t.Fatal(err)
	}
	page, _ = f.s.ProductReviews(t.Context(), ReviewQuery{Product: "digital", Page: 1, WithReply: true})
	if page.Total != 1 || page.Reviews[0].Reply != "已处理" {
		t.Fatal(page)
	}
	stats, _ := f.s.ProductStatistics(t.Context())
	if stats["digital"].Sold != 1 || stats["digital"].Count != 1 {
		t.Fatal(stats)
	}
	// A later refund must remain possible and must not erase a buyer's experience.
	if _, err = f.s.RequestRefund(t.Context(), 7, o.ID, "退款原因"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ReviewRefund(t.Context(), o.ID, true, "同意退款"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ReconcileRefund(t.Context(), Refund{OrderID: o.ID, Reference: "refund-proof", Note: "已退款"}); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	p := f.create(t, "physical", 7, nil)
	p = f.pay(t, p, 0)
	if _, err = f.s.CreateReview(t.Context(), 7, p.ID, "买家", in); !errors.Is(err, ErrReviewEligibility) {
		t.Fatal("before delivery", err)
	}
	p, err = f.s.Ship(t.Context(), Shipment{OrderID: p.ID, Carrier: "快递", Tracking: "TEST-001"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CreateReview(t.Context(), 7, p.ID, "买家", in); !errors.Is(err, ErrReviewEligibility) {
		t.Fatal("before receipt", err)
	}
	if _, err = f.s.ConfirmReceipt(t.Context(), 7, p.ID); err != nil {
		t.Fatal(err)
	}
	in.Anonymous = true
	r, err = f.s.CreateReview(t.Context(), 7, p.ID, "买家", in)
	if err != nil || r.Author != "匿名买家" {
		t.Fatal(r, err)
	}
}

func TestReviewsConcurrentDeduplicationPaginationAndFilters(t *testing.T) {
	f := newFixture(t, item("digital", "virtual", "direct"))
	o := f.create(t, "digital", 1, nil)
	o = f.pay(t, o, 0)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.s.CreateReview(t.Context(), 1, o.ID, "买家", ReviewInput{Rating: 5, Content: "评价"})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrReviewExists) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
	for i := 2; i <= 22; i++ {
		o = f.create(t, "digital", int64(i), nil)
		o = f.pay(t, o, 0)
		if _, err := f.s.CreateReview(t.Context(), int64(i), o.ID, "买家", ReviewInput{Rating: i%5 + 1, Content: fmt.Sprintf("评价 %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.s.ProductReviews(t.Context(), ReviewQuery{Product: "digital", Page: 1})
	if err != nil || page.Total != 22 || len(page.Reviews) != 20 {
		t.Fatal(page, err)
	}
	second, _ := f.s.ProductReviews(t.Context(), ReviewQuery{Product: "digital", Page: 2})
	if len(second.Reviews) != 2 {
		t.Fatal(second)
	}
	seen := map[string]bool{}
	for _, r := range append(page.Reviews, second.Reviews...) {
		if seen[r.ID] {
			t.Fatal("unstable page", r.ID)
		}
		seen[r.ID] = true
	}
	filtered, _ := f.s.ProductReviews(t.Context(), ReviewQuery{Product: "digital", Page: 1, Rating: 1})
	for _, r := range filtered.Reviews {
		if r.Rating != 1 {
			t.Fatal(r)
		}
	}
	if filtered.Summary.Count != 22 {
		t.Fatal("summary filtered", filtered)
	}
	if _, err := f.s.ProductReviews(t.Context(), ReviewQuery{Product: "digital", Page: 0}); !errors.Is(err, ErrReviewInvalid) {
		t.Fatal(err)
	}
	if _, err := f.s.ProductReviews(t.Context(), ReviewQuery{Product: "missing", Page: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	c, _ := f.s.Catalog(t.Context())
	c.Products[0].Enabled = false
	if _, err := f.s.SaveCatalog(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ProductReviews(t.Context(), ReviewQuery{Product: "digital", Page: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatal("disabled product", err)
	}
}

func TestReviewsNormalizeLegacyState(t *testing.T) {
	st := InitialState()
	st.Reviews = nil
	NormalizeState(&st)
	if st.Reviews == nil || ValidateState(st) != nil {
		t.Fatal("legacy state")
	}
}
