package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
)

type resourceHistoryCountingRepository struct {
	content.Repository
	reads map[int64]int
}

func (r *resourceHistoryCountingRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	r.reads[id]++
	return r.Repository.GetByID(ctx, id)
}

func TestResourceHistoryDeduplicatesBeforeLimitingAndIncludesRecentUnlocks(t *testing.T) {
	f := newUnlockFixture(t, "points")
	actor := resourceActorID(f.reader.UserID)
	now := time.Now().UTC()
	if err := f.s.resources.Backend.Update(t.Context(), func(st *resources.State) error {
		for i := range 205 {
			resource := "repeat"
			if i == 0 {
				resource = "earlier"
			}
			st.Deliveries = append(st.Deliveries, resources.Delivery{Actor: actor, PostID: f.post.ID, Resource: resource, Variant: "complete", Link: "file", Request: fmt.Sprintf("download-%d", i), Title: resource, CreatedAt: now.Add(time.Duration(i-205) * time.Minute)})
		}
		st.Unlocks = append(st.Unlocks, resources.Unlock{Actor: actor, PostID: f.post.ID, Resource: "unlocked", Variant: "complete", Title: "unlocked", CreatedAt: now})
		st.Unlocks = append(st.Unlocks, resources.Unlock{Actor: resourceActorID(f.owner.UserID), PostID: f.post.ID, Resource: "other-user", Variant: "complete", Title: "other-user", CreatedAt: now.Add(time.Minute)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	counted := &resourceHistoryCountingRepository{Repository: f.repo, reads: map[int64]int{}}
	f.s.repository = counted
	response := serveContentAPI(f.h, f.cookie, "GET", "https://example.test/api/v1/social/downloads", "", nil)
	var result struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 {
		t.Fatal(response.Code, response.Body.String(), err)
	}
	if len(result.Items) != 3 || result.Items[0].Title != "unlocked" || result.Items[1].Title != "repeat" || result.Items[2].Title != "earlier" {
		t.Fatalf("history dropped unique or newer records: %+v", result.Items)
	}
	if counted.reads[f.post.ID] != 1 {
		t.Fatalf("one post required %d storage reads", counted.reads[f.post.ID])
	}
	if _, err := f.repo.ChangeStatus(t.Context(), f.post.ID, content.Mutation{ExpectedVersion: f.post.Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	response = serveContentAPI(f.h, f.cookie, "GET", "https://example.test/api/v1/social/downloads", "", nil)
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || len(result.Items) != 0 {
		t.Fatal("withdrawn resources remained visible across requests", response.Code, response.Body.String(), err)
	}
}

func TestResourceHistoryLimitsAfterMergingSources(t *testing.T) {
	f := newUnlockFixture(t, "points")
	actor := resourceActorID(f.reader.UserID)
	now := time.Now().UTC()
	if err := f.s.resources.Backend.Update(t.Context(), func(st *resources.State) error {
		for i := range 240 {
			id := fmt.Sprintf("resource-%03d", i)
			st.Deliveries = append(st.Deliveries, resources.Delivery{Actor: actor, PostID: f.post.ID, Resource: id, Variant: "complete", Link: "file", Request: id, Title: id, CreatedAt: now.Add(time.Duration(i-240) * time.Minute)})
		}
		st.Unlocks = append(st.Unlocks, resources.Unlock{Actor: actor, PostID: f.post.ID, Resource: "newest", Variant: "complete", Title: "newest", CreatedAt: now})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response := serveContentAPI(f.h, f.cookie, "GET", "https://example.test/api/v1/social/downloads", "", nil)
	var result struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 {
		t.Fatal(response.Code, response.Body.String(), err)
	}
	if len(result.Items) != 200 || result.Items[0].Title != "newest" || result.Items[199].Title != "resource-041" {
		t.Fatalf("incorrect merged page: %+v", result.Items)
	}
}

func TestResourceHistoryPageMatchesFullMergeAcrossUnorderedSources(t *testing.T) {
	rng := rand.New(rand.NewPCG(17, 31))
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	var page resourceHistoryPage
	wantByURL := map[string]resourceHistoryEntry{}
	for i := range 10000 {
		entry := resourceHistoryEntry{Title: fmt.Sprint(i), URL: fmt.Sprintf("/posts/%d#resource-file", rng.IntN(600)), CreatedAt: now.Add(time.Duration(rng.IntN(2000)) * time.Second)}
		old, found := wantByURL[entry.URL]
		if !found || entry.CreatedAt.After(old.CreatedAt) {
			wantByURL[entry.URL] = entry
		}
		if page.accepts(entry.CreatedAt) {
			page.add(entry)
		}
		if len(page.items) > resourceHistoryLimit || len(page.byURL) != len(page.items) {
			t.Fatal("unbounded or inconsistent history")
		}
	}
	want := make([]resourceHistoryEntry, 0, len(wantByURL))
	for _, entry := range wantByURL {
		want = append(want, entry)
	}
	sort.Slice(want, func(i, j int) bool {
		if want[i].CreatedAt.Equal(want[j].CreatedAt) {
			return want[i].URL < want[j].URL
		}
		return want[i].CreatedAt.After(want[j].CreatedAt)
	})
	want = want[:resourceHistoryLimit]
	got := page.result()
	for i := range got {
		got[i].index = 0
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("bounded merge differs from full newest-per-URL merge")
	}
}
