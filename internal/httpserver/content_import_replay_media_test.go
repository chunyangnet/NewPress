package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contentimport"
	"newpress.local/newpress/internal/media"
)

func TestContentImportRetainsExistingSourceWithUnavailableMedia(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	input := contentimport.Request{Source: "replay", Format: "markdown", State: "draft", Files: []contentimport.File{{Name: "old.md", Text: "---\nslug: old\n---\nOriginal body"}}}
	call := func(action, token string, want int) importResponse {
		t.Helper()
		body, err := json.Marshal(map[string]any{"input": input, "token": token})
		if err != nil {
			t.Fatal(err)
		}
		response := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/admin/imports/"+action, string(body), authenticatedAPIHeaders(owner))
		if response.Code != want {
			t.Fatalf("%s returned %d, want %d: %s", action, response.Code, want, response.Body.String())
		}
		var result importResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := call("preview", "", http.StatusOK)
	applied := call("apply", first.Token, http.StatusOK)
	old, err := s.repository.Update(t.Context(), applied.Plan.Items[0].PostID, content.Mutation{ExpectedVersion: applied.Plan.Items[0].Version}, content.PostInput{
		Title: "Manual edit", Slug: "renamed", AuthorID: owner.UserID, ContentSource: "<p>Saved edit</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 4, 3)), media.UploadInput{Filename: "unused.png"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.mediaUsage.Trash(t.Context(), item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	input.Files[0].Text += "\n\n![Changed source](" + item.PublicURL + ")"
	input.Files = append(input.Files, contentimport.File{Name: "new.md", Text: "---\nslug: new\n---\n![New image](" + item.PublicURL + ")"})
	rejected := call("preview", "", http.StatusUnprocessableEntity)
	if rejected.Token != "" {
		t.Fatal("invalid new media received an apply token")
	}
	input.Files[1].Text = "---\nslug: new\n---\nNew body"
	preview := call("preview", "", http.StatusOK)
	if preview.Token == "" || preview.Plan.New != 1 || preview.Plan.Existing != 1 || preview.Plan.Items[0].Action != "changed" {
		t.Fatalf("wrong mixed-batch preview: %+v", preview.Plan)
	}
	applied = call("apply", preview.Token, http.StatusOK)
	if !applied.Applied || applied.Plan.Items[0].Action != "changed" || applied.Plan.Items[1].Action != "created" {
		t.Fatalf("wrong apply result: %+v", applied.Plan)
	}
	replay := call("apply", preview.Token, http.StatusOK)
	if !replay.Applied || replay.Plan.New != 0 || replay.Plan.Existing != 2 {
		t.Fatalf("committed import could not be confirmed: %+v", replay.Plan)
	}
	retained, err := s.repository.GetByID(t.Context(), old.ID)
	if err != nil || retained.Version != old.Version || retained.Title != old.Title || retained.ContentHTML != old.ContentHTML || retained.AuthorID != old.AuthorID {
		t.Fatalf("reimport overwrote manual edits: %+v, %v", retained, err)
	}
	posts, err := s.repository.ListAdmin(t.Context(), content.AdminQuery{})
	if err != nil || posts.Total != 2 {
		t.Fatalf("replay created duplicate or partial content: %+v, %v", posts, err)
	}
}
