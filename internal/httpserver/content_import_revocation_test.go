package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contentimport"
)

type importRevocationRepository struct {
	content.Repository
	afterPreview func()
	applies      int
}

func (r *importRevocationRepository) PreviewImport(ctx context.Context, batch content.ImportBatch) (content.ImportPlan, error) {
	plan, err := r.Repository.PreviewImport(ctx, batch)
	if r.afterPreview != nil {
		r.afterPreview()
	}
	return plan, err
}

func (r *importRevocationRepository) ApplyImport(ctx context.Context, batch content.ImportBatch, revision string) (content.ImportPlan, error) {
	r.applies++
	return r.Repository.ApplyImport(ctx, batch, revision)
}

func TestContentImportRechecksAuthorityBeforeApply(t *testing.T) {
	for _, change := range []string{"permission", "logout", "allowed"} {
		t.Run(change, func(t *testing.T) {
			s, handler, manager, repo, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "import-session", "import-session@example.test", "Import actor", "administrator")
			input := contentimport.Request{Source: "revocation", Format: "markdown", State: "draft", Files: []contentimport.File{{Name: "article.md", Text: "---\ntitle: Import boundary\nslug: import-boundary\n---\nFixture content"}}}
			encode := func(token string) string {
				body, err := json.Marshal(map[string]any{"input": input, "token": token})
				if err != nil {
					t.Fatal(err)
				}
				return string(body)
			}
			const endpoint = "https://example.test/api/v1/admin/imports/"
			response := serveContentAPI(handler, cookie, http.MethodPost, endpoint+"preview", encode(""), authenticatedAPIHeaders(actor))
			var preview importResponse
			if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil || response.Code != http.StatusOK || preview.Token == "" {
				t.Fatalf("prepare preview: %d %s, %v", response.Code, response.Body.String(), err)
			}
			wrapped := &importRevocationRepository{Repository: s.repository, afterPreview: func() {
				switch change {
				case "permission":
					if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{"editor"}}); err != nil {
						t.Fatal(err)
					}
				case "logout":
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Fatal(err)
					}
				}
			}}
			s.repository = wrapped
			response = serveContentAPI(handler, cookie, http.MethodPost, endpoint+"apply", encode(preview.Token), authenticatedAPIHeaders(actor))
			posts, err := repo.ListAdmin(t.Context(), content.AdminQuery{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if change == "allowed" {
				if response.Code != http.StatusOK || posts.Total != 1 || wrapped.applies != 1 {
					t.Fatalf("authorized import failed: %d %s", response.Code, response.Body.String())
				}
				return
			}
			want := http.StatusForbidden
			if change == "logout" {
				want = http.StatusUnauthorized
			}
			if response.Code != want || posts.Total != 0 || wrapped.applies != 0 {
				t.Fatalf("revoked import reached commit: status=%d posts=%d applies=%d", response.Code, posts.Total, wrapped.applies)
			}
		})
	}
}
