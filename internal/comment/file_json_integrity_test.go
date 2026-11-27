package comment

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func commentJSONFixture(t *testing.T) []byte {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "comments.json")
	store, err := NewFileStore(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CreateOnce(t.Context(), Submission{
		Key: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64),
		Input: CreateInput{PostID: 1, UserID: 7, AuthorName: "读者 🌐 �", AuthorEmail: "reader@example.test", ContentSource: "原文 🌐 �", UserAgent: "browser 🌐 �", Status: StatusApproved},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Edit(t.Context(), result.Receipt.CommentID, Mutation{ExpectedVersion: 1, Actor: "编辑 🌐 �"}, EditInput{AuthorName: "读者 🌐 �", AuthorEmail: "reader@example.test", ContentSource: "修订后 🌐 �"}); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func replaceCommentJSON(t *testing.T, payload []byte, from, to string) []byte {
	t.Helper()
	if !bytes.Contains(payload, []byte(from)) || from == to {
		t.Fatalf("JSON fixture lacks distinct replacement for %q", from)
	}
	return bytes.Replace(payload, []byte(from), []byte(to), 1)
}

func rewriteCommentSection(t *testing.T, payload []byte, section, from, to string) []byte {
	t.Helper()
	start := bytes.Index(payload, []byte(`"`+section+`": [`))
	if start < 0 {
		t.Fatalf("missing document section: %s", section)
	}
	result := append([]byte(nil), payload[:start]...)
	return append(result, replaceCommentJSON(t, payload[start:], from, to)...)
}

func assertCommentJSONRejected(t *testing.T, mode string, payload, valid []byte) {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "comments.json")
	input := filename
	if mode == "backup" {
		input += ".bak"
	} else if err := os.WriteFile(filename+".bak", valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(filename, true); err == nil {
		t.Error("invalid primary/backup accepted or silently replaced by seed/backup")
	}
	after, err := os.ReadFile(input)
	if err != nil || !bytes.Equal(after, payload) {
		t.Errorf("invalid document changed on rejected open: %v", err)
	}
	if mode == "backup" {
		if _, err := os.Stat(filename); !os.IsNotExist(err) {
			t.Errorf("rejected backup created primary file: %v", err)
		}
	} else {
		backup, err := os.ReadFile(filename + ".bak")
		if err != nil || !bytes.Equal(backup, valid) {
			t.Errorf("valid backup changed on rejected primary: %v", err)
		}
	}
}

func TestCommentFileRejectsLossyJSONUnicode(t *testing.T) {
	valid := commentJSONFixture(t)
	for _, mode := range []string{"primary", "backup"} {
		for name, malformed := range map[string]string{
			"invalid_byte": "\xff", "truncated_utf8": "\xe4\xb8", "encoded_surrogate": "\xed\xa0\x80",
			"high_surrogate": `\ud800`, "low_surrogate": `\udc00`, "unpaired_before_escape": `\ud800\u0041`,
		} {
			for _, target := range []string{"author", "email", "user_agent", "editor", "revision_actor"} {
				t.Run(mode+"/"+target+"/"+name, func(t *testing.T) {
					from, to := `"author_name": "读者 🌐 �"`, `"author_name": "reader`+malformed+`"`
					switch target {
					case "email":
						from, to = `"author_email": "reader@example.test"`, `"author_email": "reader`+malformed+`@example.test"`
					case "user_agent":
						from, to = `"user_agent": "browser 🌐 �"`, `"user_agent": "browser`+malformed+`"`
					case "editor":
						from, to = `"edited_by": "编辑 🌐 �"`, `"edited_by": "editor`+malformed+`"`
					case "revision_actor":
						from, to = `"actor": "编辑 🌐 �"`, `"actor": "editor`+malformed+`"`
					}
					assertCommentJSONRejected(t, mode, replaceCommentJSON(t, valid, from, to), valid)
				})
			}
		}
	}
}

func TestCommentFileRejectsConflictingJSONFields(t *testing.T) {
	valid := commentJSONFixture(t)
	for _, mode := range []string{"primary", "backup"} {
		for name, rewrite := range map[string]func([]byte) []byte{
			"schema_alias":         func(p []byte) []byte { return replaceCommentJSON(t, p, `"schema": 3`, `"schema": 1, "SCHEMA": 3`) },
			"escaped_schema_alias": func(p []byte) []byte { return replaceCommentJSON(t, p, `"schema": 3`, `"schema": 1, "\u0053CHEMA": 3`) },
			"items_alias":          func(p []byte) []byte { return replaceCommentJSON(t, p, `"items": [`, `"items": [], "ITEMS": [`) },
			"revisions_alias": func(p []byte) []byte {
				return replaceCommentJSON(t, p, `"revisions": [`, `"revisions": [], "REVISIONS": [`)
			},
			"activities_alias": func(p []byte) []byte {
				return replaceCommentJSON(t, p, `"activities": [`, `"activities": [], "ACTIVITIES": [`)
			},
			"submissions_alias": func(p []byte) []byte {
				return replaceCommentJSON(t, p, `"submissions": [`, `"submissions": [], "SUBMISSIONS": [`)
			},
			"comment_status": func(p []byte) []byte {
				return replaceCommentJSON(t, p, `"status": "approved"`, `"status": "spam", "STATUS": "approved"`)
			},
			"comment_owner": func(p []byte) []byte { return replaceCommentJSON(t, p, `"user_id": 7`, `"user_id": 8, "USER_ID": 7`) },
			"revision_actor": func(p []byte) []byte {
				return replaceCommentJSON(t, p, `"actor": "编辑 🌐 �"`, `"actor": "other", "ACTOR": "编辑 🌐 �"`)
			},
			"activity_status": func(p []byte) []byte {
				return rewriteCommentSection(t, p, "activities", `"status": "approved"`, `"status": "spam", "STATUS": "approved"`)
			},
			"receipt_post": func(p []byte) []byte {
				return rewriteCommentSection(t, p, "submissions", `"post_id": 1`, `"post_id": 2, "POST_ID": 1`)
			},
			"duplicate_key":     func(p []byte) []byte { return replaceCommentJSON(t, p, `"schema": 3`, `"schema": 1, "schema": 3`) },
			"escaped_duplicate": func(p []byte) []byte { return replaceCommentJSON(t, p, `"schema": 3`, `"schema": 1, "\u0073chema": 3`) },
			"unknown_field":     func(p []byte) []byte { return replaceCommentJSON(t, p, `"schema": 3`, `"unknown": 1, "schema": 3`) },
			"trailing_document": func(p []byte) []byte { return append(append([]byte(nil), p...), []byte(` {}`)...) },
		} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				assertCommentJSONRejected(t, mode, rewrite(valid), valid)
			})
		}
	}
}

func TestCommentFileJSONCompatibilityAcrossReopen(t *testing.T) {
	valid := commentJSONFixture(t)
	for _, mode := range []string{"primary", "backup"} {
		for _, name := range []string{"unicode", "escaped_unicode", "single_case_aliases", "legacy_schema", "schema_two"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				var expected fileDocument
				if err := json.Unmarshal(valid, &expected); err != nil {
					t.Fatal(err)
				}
				payload := append([]byte(nil), valid...)
				switch name {
				case "escaped_unicode":
					payload = bytes.ReplaceAll(payload, []byte("🌐"), []byte(`\ud83c\udf10`))
					payload = bytes.ReplaceAll(payload, []byte("�"), []byte(`\ufffd`))
				case "single_case_aliases":
					for _, key := range []string{"schema", "items", "revisions", "activities", "submissions", "author_name", "actor", "post_id"} {
						payload = bytes.ReplaceAll(payload, []byte(`"`+key+`":`), []byte(`"`+strings.ToUpper(key)+`":`))
					}
				case "legacy_schema", "schema_two":
					expected.Schema = 1
					expected.Submissions = nil
					if name == "schema_two" {
						expected.Schema = 2
					} else {
						expected.Revisions = nil
					}
					var err error
					payload, err = json.Marshal(expected)
					if err != nil {
						t.Fatal(err)
					}
				}
				filename := filepath.Join(t.TempDir(), "comments.json")
				input := filename
				if mode == "backup" {
					input += ".bak"
				}
				if err := os.WriteFile(input, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				loaded, err := loadFileDocument(filename)
				if err != nil || !reflect.DeepEqual(loaded, expected) {
					t.Fatalf("valid JSON changed during decoding: %v", err)
				}
				store, err := NewFileStore(filename, true)
				if name == "legacy_schema" || name == "schema_two" {
					if err == nil {
						t.Fatal("retired schema accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				item, err := store.Get(t.Context(), expected.Items[0].ID)
				if err != nil || item != expected.Items[0] {
					t.Fatalf("valid comment changed while opening: %v", err)
				}
				before := commentWriteState(t, store)
				if _, err := store.ChangeStatus(t.Context(), item.ID, Mutation{ExpectedVersion: item.Version, Actor: "编辑 🌐 �"}, StatusPending); err != nil {
					t.Fatal(err)
				}
				after := commentWriteState(t, store)
				if bytes.Equal(before, after) {
					t.Fatal("valid update did not persist")
				}
				reopened, err := NewFileStore(filename, false)
				if err != nil || !bytes.Equal(after, commentWriteState(t, reopened)) {
					t.Fatalf("Unicode, revisions, activity or receipts changed after save/reopen: %v", err)
				}
			})
		}
	}
}
