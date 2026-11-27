package content

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func autosaveJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestAutosaveIncompleteEditorialFieldsSurviveReopen(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, field := range []string{"empty title", "spaced title", "empty slug", "unfinished slug", "source whitespace", "empty excerpt", "spaced excerpt", "empty author", "empty comments", "unscheduled future"} {
			t.Run(mode+"/"+field, func(t *testing.T) {
				r, _, reopen := unicodeWriteRepository(t, mode)
				post, err := r.Create(t.Context(), PostInput{Title: "正式标题", Slug: "recovery", ContentSource: "<p>正文</p>"})
				if err != nil {
					t.Fatal(err)
				}
				revisions, err := r.ListRevisions(t.Context(), post.ID, 10)
				if err != nil {
					t.Fatal(err)
				}
				snapshot := clonePost(post)
				wantHTML := "<p>正文</p>"
				switch field {
				case "empty title":
					snapshot.Title = ""
				case "spaced title":
					snapshot.Title = "  编辑中的标题  "
				case "empty slug":
					snapshot.Slug = ""
				case "unfinished slug":
					snapshot.Slug = "Not Finished Yet"
				case "source whitespace":
					snapshot.ContentSource = "\r\n  第一段\r\n第二行  \r\n"
					wantHTML = "<p>第一段<br>第二行</p>"
				case "empty excerpt":
					snapshot.Excerpt = ""
				case "spaced excerpt":
					snapshot.Excerpt = "  未完成的摘要\n"
				case "empty author":
					snapshot.AuthorName = ""
				case "empty comments":
					snapshot.CommentStatus = ""
				case "unscheduled future":
					snapshot.Status, snapshot.PublishedAt = StatusFuture, time.Time{}
				}
				input := Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)}
				saved, err := r.SaveAutosave(t.Context(), input)
				if err != nil {
					t.Fatalf("incomplete recovery rejected: %v", err)
				}
				want := saved
				want.Snapshot = snapshot
				want.Snapshot.ContentHTML = wantHTML
				if !bytes.Equal(autosaveJSON(t, saved), autosaveJSON(t, want)) {
					t.Error("save changed editorial fields or trusted a stale HTML projection")
				}
				for _, loaded := range []Repository{r, reopen()} {
					got, err := loaded.GetAutosave(t.Context(), post.ID, input.OwnerKey)
					if err != nil || !bytes.Equal(autosaveJSON(t, got), autosaveJSON(t, want)) {
						t.Errorf("recovery did not round trip unchanged: %v", err)
					}
					canonical, err := loaded.GetByID(t.Context(), post.ID)
					if err != nil || !bytes.Equal(autosaveJSON(t, canonical), autosaveJSON(t, post)) {
						t.Errorf("recovery changed canonical content: %v", err)
					}
					gotRevisions, err := loaded.ListRevisions(t.Context(), post.ID, 10)
					if err != nil || !bytes.Equal(autosaveJSON(t, gotRevisions), autosaveJSON(t, revisions)) {
						t.Errorf("recovery changed revision history: %v", err)
					}
				}
			})
		}
	}
}

func TestAutosaveDerivesHTMLWithoutChangingEditorSource(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, source := range []struct{ name, input, html string }{
			{"HTML", `<p onclick="bad()">恢复正文</p><script>bad()</script>`, "<p>恢复正文</p>"},
			{"plain text", "a & b\r\n第二行", "<p>a &amp; b<br>第二行</p>"},
			{"blank", "", ""},
		} {
			t.Run(mode+"/"+source.name, func(t *testing.T) {
				r, _, reopen := unicodeWriteRepository(t, mode)
				post, err := r.Create(t.Context(), PostInput{Title: "正式标题", ContentSource: "<p>原正文</p>"})
				if err != nil {
					t.Fatal(err)
				}
				post.ContentSource, post.ContentHTML = source.input, `<img src="x" onerror="bad()"><script>bad()</script>`
				// Empty excerpt is a legitimate recovery field even with a body.
				post.Excerpt = ""
				saved, err := r.SaveAutosave(t.Context(), Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				if saved.Snapshot.ContentHTML != source.html || saved.Snapshot.ContentSource != source.input || saved.Snapshot.Excerpt != "" {
					t.Error("save accepted injected HTML or changed the draft source/excerpt")
				}
				for _, loaded := range []Repository{r, reopen()} {
					got, err := loaded.GetAutosave(t.Context(), post.ID, "user:1")
					if err != nil || got.Snapshot.ContentHTML != source.html || got.Snapshot.ContentSource != source.input || got.Snapshot.Excerpt != "" {
						t.Errorf("unsafe or lossy recovery after load: %v", err)
					}
				}
			})
		}
	}
}

func TestAutosaveExpiryFollowsMonotonicTimestampBeforeMutation(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, rollback := range []bool{false, true} {
			name := mode + "/same clock"
			if rollback {
				name = mode + "/clock rollback"
			}
			t.Run(name, func(t *testing.T) {
				r, state, reopen := unicodeWriteRepository(t, mode)
				now := time.Now().UTC().Add(time.Hour)
				clock := func() time.Time { return now }
				switch repository := r.(type) {
				case *MemoryRepository:
					repository.now = clock
				case *FileRepository:
					repository.state.now = clock
				}
				post, err := r.Create(t.Context(), PostInput{Title: "保留恢复点"})
				if err != nil {
					t.Fatal(err)
				}
				input := Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: now.Add(time.Hour)}
				original, err := r.SaveAutosave(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				before := state()
				input.ExpiresAt = now.Add(time.Nanosecond)
				if rollback {
					now = now.Add(-time.Minute)
					input.ExpiresAt = now.Add(time.Second)
				}
				input.Snapshot.Title = "不能覆盖恢复点"
				if _, err := r.SaveAutosave(t.Context(), input); FieldErrors(err)["expires_at"] == "" {
					t.Errorf("expiry at or before effective updated_at accepted: %v", err)
				}
				if !bytes.Equal(before, state()) {
					t.Error("invalid expiry changed persisted state")
				}
				got, err := reopen().GetAutosave(t.Context(), post.ID, input.OwnerKey)
				if err != nil || !bytes.Equal(autosaveJSON(t, got), autosaveJSON(t, original)) {
					t.Errorf("invalid expiry replaced original recovery: %v", err)
				}
				input.ExpiresAt = original.UpdatedAt.Add(time.Hour)
				next, err := r.SaveAutosave(t.Context(), input)
				if err != nil || !next.UpdatedAt.After(original.UpdatedAt) || !next.ExpiresAt.After(next.UpdatedAt) {
					t.Errorf("valid update lost monotonic timestamp: %v", err)
				}
			})
		}
	}
}

func TestFileAutosaveRecoveryStillChecksIdentityAndCanonicalContent(t *testing.T) {
	for _, field := range []string{"post ID", "source version", "snapshot version", "author", "owner", "timestamp", "expiry", "duplicate", "canonical title", "revision title"} {
		t.Run(field, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "content.json")
			r, err := NewFileRepository(filename, false)
			if err != nil {
				t.Fatal(err)
			}
			post, err := r.Create(t.Context(), PostInput{Title: "正式标题", AuthorID: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.SaveAutosave(t.Context(), Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			var document contentDocument
			if err := json.Unmarshal(body, &document); err != nil {
				t.Fatal(err)
			}
			autosave := &document.Autosaves[0]
			switch field {
			case "post ID":
				autosave.Snapshot.ID++
			case "source version":
				autosave.SourceVersion++
			case "snapshot version":
				autosave.Snapshot.Version++
			case "author":
				autosave.Snapshot.AuthorID++
			case "owner":
				autosave.OwnerKey = "user:01"
			case "timestamp":
				autosave.UpdatedAt = time.Time{}
			case "expiry":
				autosave.ExpiresAt = autosave.UpdatedAt
			case "duplicate":
				document.Autosaves = append(document.Autosaves, *autosave)
			case "canonical title":
				document.Posts[0].Title = ""
			case "revision title":
				document.Revisions[0].Snapshot.Title = ""
			}
			invalid := autosaveJSON(t, document)
			if err := os.WriteFile(filename, invalid, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileRepository(filename, false); err == nil {
				t.Error("invalid identity or canonical facts accepted")
			}
			after, err := os.ReadFile(filename)
			if err != nil || !bytes.Equal(after, invalid) {
				t.Error("failed load modified the damaged source file")
			}
		})
	}
}

func TestFileAutosaveLoadRebuildsEditedProjection(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name := "primary"
		if rollback {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "content.json")
			r, err := NewFileRepository(filename, false)
			if err != nil {
				t.Fatal(err)
			}
			post, err := r.Create(t.Context(), PostInput{Title: "原稿", ContentSource: "<p>原正文</p>"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.SaveAutosave(t.Context(), Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			var document contentDocument
			if err := json.Unmarshal(payload, &document); err != nil {
				t.Fatal(err)
			}
			snapshot := &document.Autosaves[0].Snapshot
			snapshot.Title, snapshot.Excerpt = "", ""
			snapshot.ContentSource = "  <p>待恢复正文</p><script>bad()</script>  "
			snapshot.ContentHTML = `<script>injected()</script>`
			edited := autosaveJSON(t, document)
			if err := os.WriteFile(filename, edited, 0600); err != nil {
				t.Fatal(err)
			}
			if rollback {
				if err := os.Rename(filename, filename+".bak"); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := NewFileRepository(filename, false)
			if err != nil {
				t.Fatalf("readable draft rejected: %v", err)
			}
			got, err := loaded.GetAutosave(t.Context(), post.ID, "user:1")
			if err != nil || got.Snapshot.Title != "" || got.Snapshot.Excerpt != "" || got.Snapshot.ContentSource != snapshot.ContentSource || got.Snapshot.ContentHTML != "<p>待恢复正文</p>" {
				t.Fatalf("unsafe or changed recovery: %#v, %v", got.Snapshot, err)
			}
			after, err := os.ReadFile(filename)
			if err != nil || !bytes.Equal(after, edited) {
				t.Error("loading a recovery changed its original persisted bytes")
			}
		})
	}
}
