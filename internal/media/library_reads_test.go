package media

import (
	"bytes"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func mediaReadFixture(tb testing.TB, count int) *Manager {
	tb.Helper()
	manager, err := NewManager("")
	if err != nil {
		tb.Fatal(err)
	}
	when := time.Date(2026, time.November, 28, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= count; i++ {
		id := int64(i)
		manager.items[id] = Item{ID: id, Version: 1, Kind: KindImage, SizeBytes: 1024, Title: fmt.Sprintf("Image %d", i), Filename: fmt.Sprintf("image-%d.png", i), MIMEType: "image/png", UploadSource: "media-library", CreatedAt: when.Add(time.Duration(i/2) * time.Minute), UpdatedAt: when, Caption: "Fixture caption for searchable media", UploadedBy: "editor"}
	}
	manager.nextID = int64(count + 1)
	manager.updatedAt = when
	return manager
}

func TestMediaListKeepsOrderingFiltersAndCounts(t *testing.T) {
	manager := mediaReadFixture(t, 6)
	item := manager.items[2]
	item.Kind, item.MIMEType, item.Filename = KindDocument, "application/pdf", "report.pdf"
	item.Title = "跨字段标题"
	item.Image.Variants[0].Native.SizeBytes = 17
	item.Image.Variants[0].WebP.SizeBytes = 23
	manager.items[2] = item
	item = manager.items[4]
	item.TrashedAt = item.CreatedAt
	manager.items[4] = item
	for _, tc := range []struct {
		name                     string
		query                    Query
		ids                      []int64
		total, images, documents int
		bytes                    int64
	}{
		{"default", Query{}, []int64{6, 5, 3, 2, 1}, 5, 4, 1, 5*1024 + 40},
		{"all", Query{Status: StatusAll}, []int64{6, 5, 4, 3, 2, 1}, 6, 5, 1, 6*1024 + 40},
		{"trash", Query{Status: StatusTrash}, []int64{4}, 1, 1, 0, 1024},
		{"page", Query{Offset: 1, Limit: 2}, []int64{5, 3}, 5, 4, 1, 5*1024 + 40},
		{"past-end", Query{Offset: 99}, []int64{}, 5, 4, 1, 5*1024 + 40},
		{"kind", Query{Kind: KindDocument}, []int64{2}, 1, 4, 1, 5*1024 + 40},
		{"case-insensitive", Query{Search: "iMaGe 3"}, []int64{3}, 1, 4, 1, 5*1024 + 40},
		{"cross-field", Query{Search: "REPORT.PDF 跨字段"}, []int64{2}, 1, 4, 1, 5*1024 + 40},
		{"no-match", Query{Search: "missing"}, []int64{}, 0, 4, 1, 5*1024 + 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := manager.List(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]int64, 0, len(result.Items))
			for _, item := range result.Items {
				ids = append(ids, item.ID)
			}
			if !reflect.DeepEqual(ids, tc.ids) || result.Total != tc.total || result.TotalBytes != tc.bytes || result.KindCounts[KindImage] != tc.images || result.KindCounts[KindDocument] != tc.documents || result.StatusCounts[StatusActive] != 5 || result.StatusCounts[StatusTrash] != 1 {
				t.Fatalf("list semantics changed: ids=%v result=%+v", ids, result)
			}
			if len(result.Items) > 0 {
				first := result.Items[0]
				result.Items[0].Title = "Caller changed"
				current, _ := manager.Get(first.ID)
				if current.Title != first.Title {
					t.Fatal("list exposed stored items")
				}
			}
		})
	}
}

func TestMediaSnapshotReflectsLifecycle(t *testing.T) {
	for _, storage := range []string{"memory", "file"} {
		t.Run(storage, func(t *testing.T) {
			root := ""
			if storage == "file" {
				root = t.TempDir()
			}
			manager, err := NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			previous := manager.Snapshot()
			check := func(count, active, trash int, bytes int64) {
				t.Helper()
				current := manager.Snapshot()
				if current.Revision == "" || current.Revision == previous.Revision || current.Count != count || current.ActiveCount != active || current.TrashCount != trash || current.KindCounts[KindImage] != count || current.TotalBytes != bytes {
					t.Fatalf("stale snapshot after mutation: %+v, previous=%+v", current, previous)
				}
				previous = current
				current.KindCounts[KindImage] = 999
				if manager.Snapshot().KindCounts[KindImage] != count {
					t.Fatal("snapshot exposes shared counts")
				}
				if storage == "file" {
					doc, err := loadDocument(manager.indexPath)
					if err != nil || doc.Revision != previous.Revision {
						t.Fatalf("snapshot differs from persisted revision: %v", err)
					}
				}
			}
			item, err := manager.Upload(t.Context(), bytes.NewReader(testPNG(t, 4, 3)), UploadInput{Filename: "lifecycle.png"}, 4096)
			if err != nil {
				t.Fatal(err)
			}
			check(1, 1, 0, item.SizeBytes)
			item, err = manager.Update(item.ID, item.Version, UpdateInput{Title: "Updated title"})
			if err != nil {
				t.Fatal(err)
			}
			check(1, 1, 0, item.SizeBytes)
			item, err = manager.ProcessImage(t.Context(), item.ID, item.Version, ImageInput{50, 50})
			if err != nil {
				t.Fatal(err)
			}
			total := item.SizeBytes
			for _, variant := range item.Image.Variants {
				total += variant.Native.SizeBytes + variant.WebP.SizeBytes
			}
			check(1, 1, 0, total)
			item, err = manager.Trash(item.ID, item.Version)
			if err != nil {
				t.Fatal(err)
			}
			check(1, 0, 1, total)
			item, err = manager.Restore(t.Context(), item.ID, item.Version)
			if err != nil {
				t.Fatal(err)
			}
			check(1, 1, 0, total)
			item, err = manager.Trash(item.ID, item.Version)
			if err != nil {
				t.Fatal(err)
			}
			check(1, 0, 1, total)
			if err := manager.Delete(item.ID, item.Version); err != nil {
				t.Fatal(err)
			}
			check(0, 0, 0, 0)
		})
	}
}

func TestMediaSnapshotAfterFailedCommit(t *testing.T) {
	for _, action := range []string{"upload", "update", "image", "trash", "restore", "delete"} {
		t.Run(action, func(t *testing.T) {
			manager, err := NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			item, err := manager.Upload(t.Context(), bytes.NewReader(testPNG(t, 4, 3)), UploadInput{Filename: "before.png"}, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if action == "restore" || action == "delete" {
				item, err = manager.Trash(item.ID, item.Version)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := manager.Snapshot()
			manager.indexPath = t.TempDir()
			switch action {
			case "upload":
				_, err = manager.Upload(t.Context(), bytes.NewReader(testPNG(t, 5, 4)), UploadInput{Filename: "new.png"}, 4096)
			case "update":
				_, err = manager.Update(item.ID, item.Version, UpdateInput{Title: "Not committed"})
			case "image":
				_, err = manager.ProcessImage(t.Context(), item.ID, item.Version, ImageInput{50, 50})
			case "trash":
				_, err = manager.Trash(item.ID, item.Version)
			case "restore":
				_, err = manager.Restore(t.Context(), item.ID, item.Version)
			case "delete":
				err = manager.Delete(item.ID, item.Version)
			}
			if err == nil || !reflect.DeepEqual(manager.Snapshot(), before) {
				t.Fatalf("failed commit changed the snapshot: %v before=%+v after=%+v", err, before, manager.Snapshot())
			}
		})
	}
}

func TestMediaSnapshotConcurrentReadsAndWrites(t *testing.T) {
	manager := mediaReadFixture(t, 20)
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 50; i++ {
				snapshot := manager.Snapshot()
				if snapshot.Count != 20 || snapshot.ActiveCount != 20 || snapshot.TotalBytes != 20*1024 || snapshot.KindCounts[KindImage] != 20 || snapshot.Revision == "" {
					t.Errorf("inconsistent concurrent snapshot: %+v", snapshot)
				}
				snapshot.KindCounts[KindImage] = -1
				list, err := manager.List(Query{Limit: 5})
				if err != nil || len(list.Items) != 5 || list.Total != 20 {
					t.Errorf("concurrent list: %+v %v", list, err)
				}
			}
		}()
	}
	for i := int64(1); i <= 50; i++ {
		if _, err := manager.Update(1, i, UpdateInput{Title: fmt.Sprintf("Update %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	workers.Wait()
}

func BenchmarkMediaLibraryReads(b *testing.B) {
	for _, count := range []int{500, 5000} {
		for _, operation := range []string{"snapshot", "snapshot-after-update", "first-page", "selective-search"} {
			b.Run(fmt.Sprintf("%d/%s", count, operation), func(b *testing.B) {
				manager := mediaReadFixture(b, count)
				manager.Snapshot()
				version := int64(1)
				b.ReportAllocs()
				for b.Loop() {
					switch operation {
					case "snapshot":
						manager.Snapshot()
					case "snapshot-after-update":
						if _, err := manager.Update(1, version, UpdateInput{Title: "Updated title"}); err != nil {
							b.Fatal(err)
						}
						version++
						manager.Snapshot()
					default:
						query := Query{Limit: 24}
						if operation == "selective-search" {
							query.Search = "IMAGE 123"
						}
						if _, err := manager.List(query); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}
