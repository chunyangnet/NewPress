package objectstorage

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
)

func recordReadFixture(tb testing.TB, count int) *Service {
	tb.Helper()
	manager := plugin.NewManager(tb.TempDir())
	if err := manager.InstallBundled(plugin.ObjectStorage); err != nil {
		tb.Fatal(err)
	}
	library, err := media.NewManager("")
	if err != nil {
		tb.Fatal(err)
	}
	s := New(manager, library)
	if err := s.load(false); err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(s.Close)
	when := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	statuses := []string{"pending", "syncing", "synced", "failed", "orphaned", "removed"}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("record-%08d", i)
		status := statuses[i%len(statuses)]
		r := Record{ID: id, ItemID: int64(i + 1), ProfileID: fmt.Sprintf("profile-%d", i%2), Filename: fmt.Sprintf("Photo %d.png", i), RemoteKey: "assets/" + id + ".png", ObjectKey: "objects/" + id + ".png", Size: int64(i + 1), SHA256: strings.Repeat("a", 64), Status: status, Operation: "upload", Updated: when.Add(time.Duration(i) * time.Second)}
		if i%9 == 0 {
			r.Next = when.Add(24 * time.Hour)
		}
		s.records[id] = r
	}
	return s
}

func TestStorageStatusKeepsNewestRecordsAndFullCounts(t *testing.T) {
	for _, count := range []int{0, 17, 100, 401} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := recordReadFixture(t, count)
			all := make([]Record, 0, count)
			counts, profiles := map[string]int{}, map[string]map[string]int{}
			profileBytes := map[string]int64{}
			var total, synced int64
			for id, r := range s.records {
				// Many equal timestamps exercise the existing ID tie breaker.
				r.Updated = r.Updated.Truncate(10 * time.Second)
				s.records[id] = r
				all = append(all, r)
				counts[r.Status]++
				if profiles[r.ProfileID] == nil {
					profiles[r.ProfileID] = map[string]int{}
				}
				profiles[r.ProfileID][r.Status]++
				if r.Status != "removed" {
					total += r.Size
				}
				if r.Status == "synced" {
					synced += r.Size
					profileBytes[r.ProfileID] += r.Size
				}
			}
			sort.Slice(all, func(i, j int) bool {
				if all[i].Updated.Equal(all[j].Updated) {
					return all[i].ID < all[j].ID
				}
				return all[i].Updated.After(all[j].Updated)
			})
			status, err := s.Status()
			if err != nil || !reflect.DeepEqual(status.Records, all[:min(100, len(all))]) || !reflect.DeepEqual(status.Counts, counts) || !reflect.DeepEqual(status.ProfileCounts, profiles) || !reflect.DeepEqual(status.ProfileBytes, profileBytes) || status.TotalBytes != total || status.SyncedBytes != synced {
				t.Fatalf("status aggregation or newest selection changed: %v", err)
			}
			if len(status.Records) > 0 {
				id := status.Records[0].ID
				status.Records[0].Filename = "caller mutation"
				if s.records[id].Filename == "caller mutation" {
					t.Fatal("status exposed internal records")
				}
			}
		})
	}
}

func TestStorageRecordPaginationKeepsFiltersAndOrdering(t *testing.T) {
	s := recordReadFixture(t, 350)
	for _, tc := range []struct {
		profile, status, query string
		page                   int
	}{
		{"", "", "", -1}, {"", "", "", 2}, {"", "", "", 999}, {"", "", "", math.MaxInt}, {"profile-1", "", " PHOTO 1 ", 1}, {"", "failed", "", 2}, {"", "", "absent", 1},
	} {
		all := []Record{}
		for _, r := range s.records {
			if (tc.profile == "" || r.ProfileID == tc.profile) && (tc.status == "" || r.Status == tc.status) && strings.Contains(strings.ToLower(r.Filename+" "+r.RemoteKey), strings.ToLower(strings.TrimSpace(tc.query))) {
				all = append(all, r)
			}
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].Updated.Equal(all[j].Updated) {
				return all[i].ID < all[j].ID
			}
			return all[i].Updated.After(all[j].Updated)
		})
		pages := max(1, (len(all)+99)/100)
		page := min(max(1, tc.page), pages)
		start := (page - 1) * 100
		got, err := s.Records(tc.profile, tc.status, tc.query, tc.page)
		if err != nil || got.Total != len(all) || got.Pages != pages || got.Page != page || !reflect.DeepEqual(got.Records, all[start:min(start+100, len(all))]) {
			t.Fatalf("pagination changed: case=%+v err=%v got=%+v", tc, err, got)
		}
	}
}

func TestStorageReadyJobsKeepsEligibilityAndOrder(t *testing.T) {
	s := recordReadFixture(t, 500)
	now := time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)
	want := []Record{}
	for _, r := range s.records {
		if (r.Status == "pending" || r.Status == "syncing") && !r.Next.After(now) {
			want = append(want, r)
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Updated.Before(want[j].Updated) })
	for _, limit := range []int{0, 1, 2, 4, 128} {
		got := s.readyJobs(now, limit)
		if !reflect.DeepEqual(got, want[:min(limit, len(want))]) {
			t.Fatalf("ready job selection changed for limit %d", limit)
		}
		if len(got) > 0 {
			got[0].Status = "caller mutation"
			if s.records[got[0].ID].Status == "caller mutation" {
				t.Fatal("selection exposed mutable record")
			}
		}
	}
}

func TestStorageReadyJobsBreaksTimestampTiesByID(t *testing.T) {
	s := recordReadFixture(t, 500)
	when := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	want := []string{}
	for id, r := range s.records {
		r.Updated, r.Next = when, time.Time{}
		s.records[id] = r
		if r.Status == "pending" || r.Status == "syncing" {
			want = append(want, id)
		}
	}
	sort.Strings(want)
	for i := 0; i < 5; i++ {
		got := s.readyJobs(when, 4)
		if len(got) != 4 {
			t.Fatal("wrong tie selection length")
		}
		for i, r := range got {
			if r.ID != want[i] {
				t.Fatal("same-time selection was nondeterministic", r.ID, want[i])
			}
		}
	}
}

func BenchmarkStorageRecordReads(b *testing.B) {
	for _, count := range []int{1000, 50000} {
		for _, operation := range []string{"status", "first-page", "search-page", "ready-jobs"} {
			b.Run(fmt.Sprintf("%d/%s", count, operation), func(b *testing.B) {
				s := recordReadFixture(b, count)
				now := time.Date(2026, 11, 28, 20, 0, 0, 0, time.UTC)
				b.ReportAllocs()
				for b.Loop() {
					switch operation {
					case "status":
						if _, err := s.Status(); err != nil {
							b.Fatal(err)
						}
					case "ready-jobs":
						s.readyJobs(now, 4)
					default:
						query := ""
						if operation == "search-page" {
							query = "PHOTO 123"
						}
						if _, err := s.Records("", "", query, 1); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}
