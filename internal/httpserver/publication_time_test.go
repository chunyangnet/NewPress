package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func TestParsePublishedLocalRejectsGapsAndRepeatedTimes(t *testing.T) {
	for _, test := range []struct {
		name, zone, raw, want, problem string
	}{
		{"new_york_spring_gap", "America/New_York", "2026-03-08T02:30", "", "不存在"},
		{"new_york_fall_repeat", "America/New_York", "2026-11-01T01:30", "", "出现两次"},
		{"new_york_before_spring_gap", "America/New_York", "2026-03-08T01:59", "2026-03-08T06:59:00Z", ""},
		{"new_york_after_spring_gap", "America/New_York", "2026-03-08T03:00", "2026-03-08T07:00:00Z", ""},
		{"new_york_before_fall_repeat", "America/New_York", "2026-11-01T00:30", "2026-11-01T04:30:00Z", ""},
		{"new_york_after_fall_repeat", "America/New_York", "2026-11-01T02:30", "2026-11-01T07:30:00Z", ""},
		{"lord_howe_half_hour_gap", "Australia/Lord_Howe", "2026-10-04T02:15", "", "不存在"},
		{"lord_howe_half_hour_repeat", "Australia/Lord_Howe", "2026-04-05T01:45", "", "出现两次"},
		{"lord_howe_after_gap", "Australia/Lord_Howe", "2026-10-04T02:30", "2026-10-03T15:30:00Z", ""},
		{"lord_howe_after_repeat", "Australia/Lord_Howe", "2026-04-05T02:00", "2026-04-04T15:30:00Z", ""},
		{"apia_missing_date", "Pacific/Apia", "2011-12-30T12:00", "", "不存在"},
		{"apia_after_date_line_jump", "Pacific/Apia", "2011-12-31T12:00", "2011-12-30T22:00:00Z", ""},
		{"utc", "UTC", "2026-03-08T02:30", "2026-03-08T02:30:00Z", ""},
		{"nil_location_uses_utc", "", "2026-03-08T02:30", "2026-03-08T02:30:00Z", ""},
		{"invalid_date", "UTC", "2026-02-30T12:00", "", "格式不正确"},
		{"year_zero", "UTC", "0000-01-01T00:00", "", "格式不正确"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var location *time.Location
			if test.zone != "" {
				location = publicationTestLocation(t, test.zone)
			}
			got, err := parsePublishedLocal(test.raw, location)
			if test.problem != "" {
				if err == nil || !strings.Contains(err.Error(), test.problem) || !got.IsZero() {
					t.Fatalf("parsePublishedLocal(%q, %q) = %v, %v; want rejection containing %q", test.raw, test.zone, got, err, test.problem)
				}
				return
			}
			want := publicationTestInstant(t, test.want)
			if err != nil || !got.Equal(want) {
				t.Fatalf("parsePublishedLocal(%q, %q) = %v, %v; want %v", test.raw, test.zone, got, err, want)
			}
		})
	}
}

func TestParseContentInputPreservesExactUnchangedPublicationInstant(t *testing.T) {
	now := publicationTestInstant(t, "2026-11-14T12:00:00Z")
	for _, test := range []struct {
		name, zone, original, raw string
	}{
		{"ordinary_seconds", "Asia/Shanghai", "2026-02-03T04:05:59.123456789Z", "2026-02-03T12:05"},
		{"first_fold", "America/New_York", "2026-11-01T05:30:17.123456789Z", "2026-11-01T01:30"},
		{"second_fold", "America/New_York", "2026-11-01T06:30:43.987654321Z", "2026-11-01T01:30"},
		{"lord_howe_second_fold", "Australia/Lord_Howe", "2026-04-04T15:15:27.123456789Z", "2026-04-05T01:45"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := publicationTestInstant(t, test.original)
			form := publicationTestForm("", test.zone, test.raw)
			input, fields := parseContentInput(form, auth.Principal{Username: "editor"}, now, publicationTestLocation(t, test.zone), original)
			if len(fields) != 0 || !input.PublishedAt.Equal(original) {
				t.Fatalf("unchanged wall-minute lost original instant: got %s fields=%v want %s", input.PublishedAt.Format(time.RFC3339Nano), fields, original.Format(time.RFC3339Nano))
			}
		})
	}

	location := publicationTestLocation(t, "America/New_York")
	original := publicationTestInstant(t, "2026-11-01T06:30:43.987654321Z")
	form := publicationTestForm("", location.String(), "2026-11-01T01:31")
	if _, fields := parseContentInput(form, auth.Principal{}, now, location, original); !strings.Contains(fields["published_at"], "出现两次") {
		t.Fatalf("changing a fold minute must not reuse the original instant: %v", fields)
	}
	form.Set("published_at", "2026-11-01T01:30")
	if _, fields := parseContentInput(form, auth.Principal{}, now, location); !strings.Contains(fields["published_at"], "出现两次") {
		t.Fatalf("a repeated time without a trusted original was accepted: %v", fields)
	}
	for _, marker := range []string{"", "Asia/Shanghai"} {
		stale := cloneValues(form)
		stale.Set("published_timezone", marker)
		if _, fields := parseContentInput(stale, auth.Principal{}, now, location, original); !strings.Contains(fields["published_at"], "时区") {
			t.Fatalf("trusted unchanged instant bypassed timezone marker %q: %v", marker, fields)
		}
	}
	form.Set("published_at", "2026-11-01T02:30")
	input, fields := parseContentInput(form, auth.Principal{}, now, location, original)
	if len(fields) != 0 || !input.PublishedAt.Equal(publicationTestInstant(t, "2026-11-01T07:30:00Z")) {
		t.Fatalf("changed unique minute = %s fields=%v", input.PublishedAt.Format(time.RFC3339Nano), fields)
	}
}

func TestContentPublicationValidationRetainsRawHTMLInputAndReturnsRequestTimezone(t *testing.T) {
	s, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	setPublicationTestTimezone(t, s, "America/New_York")
	for _, test := range []struct {
		name, marker, raw, problem string
	}{
		{"missing_marker", "", "2026-02-03T12:34", "缺少时区来源"},
		{"missing_marker_empty_time", "", "", "缺少时区来源"},
		{"stale_marker", "Asia/Shanghai", " 2026-02-03T12:34 ", "站点时区已变化"},
		{"same_offset_different_zone", "Etc/GMT+5", "2026-02-03T12:34", "站点时区已变化"},
		{"spring_gap", "America/New_York", "2026-03-08T02:30", "不存在"},
		{"fall_repeat", "America/New_York", "2026-11-01T01:30", "出现两次"},
		{"invalid_raw", "America/New_York", `not-a-time" onfocus="alert(1)`, "格式不正确"},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := publicationTestForm(principal.CSRFToken, test.marker, test.raw)
			if test.marker == "" {
				form.Del("published_timezone")
			}
			response := serveContentForm(handler, cookie, "https://example.test/admin/content/new/", "https://example.test", form)
			body := response.Body.String()
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(body, test.problem) {
				t.Fatalf("HTML validation status=%d, missing diagnostic %q", response.Code, test.problem)
			}
			for name, want := range map[string]string{"published_at": test.raw, "published_timezone": "America/New_York", "published_source": "canonical"} {
				if got := publicationHTMLInput(t, body, name); got != want {
					t.Errorf("retained %s = %q, want %q", name, got, want)
				}
			}
			if strings.Contains(body, ` onfocus="alert(1)"`) {
				t.Fatal("invalid publication input escaped its HTML value attribute")
			}

			response = editorSubmit(handler, cookie, "/admin/content/new/", "timezone-validation-"+test.name, form, nil)
			assertPublicationJSONError(t, response, "validation_failed", "America/New_York", test.problem)
		})
	}
	posts, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 20})
	if err != nil || posts.Total != 0 {
		t.Fatalf("rejected publication inputs persisted: total=%d err=%v", posts.Total, err)
	}
}

func TestContentAutosaveRequiresPublicationMetadata(t *testing.T) {
	s, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	setPublicationTestTimezone(t, s, "America/New_York")
	post, err := repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "Publication metadata", Slug: "publication-metadata", ContentSource: "Body", CommentStatus: content.CommentOpen})
	if err != nil {
		t.Fatal(err)
	}
	target := "https://example.test/admin/content/" + strconv.FormatInt(post.ID, 10) + "/autosave/"
	base := autosaveTestForm(principal.CSRFToken, post.Version, post.Title, post.Slug, "<p>Retained recovery point</p>")
	base.Set("published_timezone", "America/New_York")
	base.Set("published_at", "2026-02-03T12:34")
	if response := serveContentForm(handler, cookie, target, "https://example.test", base); response.Code != http.StatusOK {
		t.Fatalf("seed autosave = %d %s", response.Code, response.Body.String())
	}
	owner, _ := autosaveOwnerKey(principal)
	before, err := repository.GetAutosave(t.Context(), post.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"published_timezone", "published_source"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			form := cloneValues(base)
			form.Del(missing)
			response := serveContentForm(handler, cookie, target, "https://example.test", form)
			var result struct{ Error, Field string }
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusBadRequest || result.Error != "invalid_autosave_form" || result.Field != missing {
				t.Fatalf("missing metadata response = %d %s err=%v", response.Code, response.Body.String(), err)
			}
		})
	}
	for _, test := range []struct {
		name, marker, raw, source, problem string
	}{
		{"stale_marker", "Asia/Shanghai", "2026-02-03T12:34", "canonical", "站点时区已变化"},
		{"empty_marker", "", "2026-02-03T12:34", "canonical", "缺少时区来源"},
		{"gap", "America/New_York", "2026-03-08T02:30", "canonical", "不存在"},
		{"invalid_source", "America/New_York", "2026-02-03T12:34", "2026-11-01T06:30:00Z", "来源不正确"},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := cloneValues(base)
			form.Set("published_timezone", test.marker)
			form.Set("published_at", test.raw)
			form.Set("published_source", test.source)
			response := serveContentForm(handler, cookie, target, "https://example.test", form)
			assertPublicationJSONError(t, response, "autosave_validation_failed", "America/New_York", test.problem)
		})
	}
	after, err := repository.GetAutosave(t.Context(), post.ID, owner)
	if err != nil || !after.UpdatedAt.Equal(before.UpdatedAt) || !after.Snapshot.PublishedAt.Equal(before.Snapshot.PublishedAt) || after.Snapshot.ContentSource != before.Snapshot.ContentSource {
		t.Fatalf("rejected metadata replaced the recovery point: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestContentPublicationUsesAuthorizedAutosaveInstant(t *testing.T) {
	s, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	setPublicationTestTimezone(t, s, "America/New_York")
	canonicalTime := publicationTestInstant(t, "2026-11-01T05:30:17.123456789Z")
	recoveryTime := publicationTestInstant(t, "2026-11-01T06:30:43.987654321Z")
	post, err := repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "First fold", Slug: "trusted-publication-recovery", ContentSource: "Canonical", CommentStatus: content.CommentOpen, PublishedAt: canonicalTime})
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := autosaveOwnerKey(principal)
	snapshot := post
	snapshot.Title, snapshot.PublishedAt = "Second fold recovery", recoveryTime
	if _, err := repository.SaveAutosave(t.Context(), content.Autosave{PostID: post.ID, OwnerKey: owner, SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	path := "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/"
	for _, test := range []struct{ query, source string }{{"", "canonical"}, {"?recovery=autosave", "autosave"}} {
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path+test.query, cookie, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("editor render status=%d", response.Code)
		}
		for name, want := range map[string]string{"published_at": "2026-11-01T01:30", "published_timezone": "America/New_York", "published_source": test.source} {
			if got := publicationHTMLInput(t, response.Body.String(), name); got != want {
				t.Errorf("%s: %s = %q, want %q", test.query, name, got, want)
			}
		}
	}

	form := autosaveTestForm(principal.CSRFToken, post.Version, snapshot.Title, post.Slug, "<p>Recovered content</p>")
	form.Set("published_timezone", "America/New_York")
	form.Set("published_at", "2026-11-01T01:30")
	request := httptest.NewRequest(http.MethodPost, "https://example.test"+path, nil)
	for source, want := range map[string]time.Time{"canonical": canonicalTime, "autosave": recoveryTime} {
		form.Set("published_source", source)
		input, fields := s.parseEditorInput(request, form, principal, time.Now(), post)
		if len(fields) != 0 || !input.PublishedAt.Equal(want) {
			t.Fatalf("source %q = %s fields=%v, want %s", source, input.PublishedAt.Format(time.RFC3339Nano), fields, want.Format(time.RFC3339Nano))
		}
	}
	form.Set("published_source", "autosave")
	other := principal
	other.UserID, other.Username = principal.UserID+1000, "another-editor"
	if _, fields := s.parseEditorInput(request, form, other, time.Now(), post); !strings.Contains(fields["published_at"], "恢复点已不可用") {
		t.Fatalf("another editor borrowed an autosave instant: %v", fields)
	}

	autosaveURL := "https://example.test/admin/content/" + strconv.FormatInt(post.ID, 10) + "/autosave/"
	if response := serveContentForm(handler, cookie, autosaveURL, "https://example.test", form); response.Code != http.StatusOK {
		t.Fatalf("resave trusted recovery = %d %s", response.Code, response.Body.String())
	}
	recovered, err := repository.GetAutosave(t.Context(), post.ID, owner)
	if err != nil || !recovered.Snapshot.PublishedAt.Equal(recoveryTime) {
		t.Fatalf("autosave folded to the canonical instant: time=%s err=%v", recovered.Snapshot.PublishedAt.Format(time.RFC3339Nano), err)
	}
	forged := cloneValues(form)
	forged.Set("published_original", "2026-11-01T05:30:00Z")
	if response := serveContentForm(handler, cookie, autosaveURL, "https://example.test", forged); response.Code != http.StatusBadRequest {
		t.Fatalf("client-supplied original timestamp accepted: %d %s", response.Code, response.Body.String())
	}
	form.Set("action", "save")
	if response := serveContentForm(handler, cookie, "https://example.test"+path, "https://example.test", form); response.Code != http.StatusSeeOther {
		t.Fatalf("save trusted recovery = %d %s", response.Code, response.Body.String())
	}
	current, err := repository.GetByID(t.Context(), post.ID)
	if err != nil || current.Version != post.Version+1 || !current.PublishedAt.Equal(recoveryTime) {
		t.Fatalf("canonical did not preserve recovered fold and seconds: time=%s version=%d err=%v", current.PublishedAt.Format(time.RFC3339Nano), current.Version, err)
	}
	form.Set("version", strconv.FormatInt(current.Version, 10))
	response := editorSubmit(handler, cookie, path, "missing-trusted-recovery", form, nil)
	assertPublicationJSONError(t, response, "validation_failed", "America/New_York", "恢复点已不可用")
}

func TestEditorSubmissionCommittedRetrySurvivesTimezoneChange(t *testing.T) {
	s, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	form := publicationTestForm(principal.CSRFToken, "Asia/Shanghai", "2026-02-03T12:34")
	first := editorSubmit(handler, cookie, "/admin/content/new/", "timezone-committed-retry", form, nil)
	var saved struct {
		ID, Version int64
		Saved       bool
		Replayed    bool
	}
	if err := json.Unmarshal(first.Body.Bytes(), &saved); err != nil || first.Code != http.StatusOK || !saved.Saved || saved.Replayed {
		t.Fatalf("initial save = %d %s err=%v", first.Code, first.Body.String(), err)
	}
	setPublicationTestTimezone(t, s, "America/New_York")
	retry := editorSubmit(handler, cookie, "/admin/content/new/", "timezone-committed-retry", form, nil)
	var replay struct {
		ID, Version int64
		Saved       bool
		Replayed    bool
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &replay); err != nil || retry.Code != http.StatusOK || !replay.Saved || !replay.Replayed || replay.ID != saved.ID || replay.Version != saved.Version {
		t.Fatalf("committed retry after timezone change = %d %s err=%v", retry.Code, retry.Body.String(), err)
	}
	fresh := editorSubmit(handler, cookie, "/admin/content/new/", "timezone-uncommitted-retry", form, nil)
	assertPublicationJSONError(t, fresh, "validation_failed", "America/New_York", "站点时区已变化")
	changed := cloneValues(form)
	changed.Set("published_timezone", "America/New_York")
	if response := editorSubmit(handler, cookie, "/admin/content/new/", "timezone-committed-retry", changed, nil); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "submission_conflict") {
		t.Fatalf("changing a committed retry payload was accepted: %d %s", response.Code, response.Body.String())
	}
	post, err := repository.GetByID(t.Context(), saved.ID)
	if err != nil || post.Version != 1 || !post.PublishedAt.Equal(publicationTestInstant(t, "2026-02-03T04:34:00Z")) {
		t.Fatalf("retry changed committed publication: time=%s version=%d err=%v", post.PublishedAt.Format(time.RFC3339Nano), post.Version, err)
	}
	revisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("retry added revisions: count=%d err=%v", len(revisions), err)
	}
	posts, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 20})
	if err != nil || posts.Total != 1 {
		t.Fatalf("retry created another post: total=%d err=%v", posts.Total, err)
	}
}

func publicationTestForm(csrf, zone, raw string) url.Values {
	return url.Values{
		"csrf_token": {csrf}, "action": {"save"}, "type": {content.TypePost}, "status": {content.StatusDraft},
		"title": {"Publication timezone input"}, "slug": {"publication-timezone-input"}, "content_source": {"<p>Unchanged editor input</p>"},
		"comment_status": {content.CommentOpen}, "published_at": {raw}, "published_timezone": {zone}, "published_source": {"canonical"},
	}
}

func publicationTestLocation(t *testing.T, zone string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func publicationTestInstant(t *testing.T, raw string) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func setPublicationTestTimezone(t *testing.T, s *Server, zone string) {
	t.Helper()
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.Identity.Timezone = zone
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) > 0 {
		t.Fatalf("set timezone %q: fields=%v err=%v", zone, fields, err)
	}
}

func publicationHTMLInput(t *testing.T, body, name string) string {
	t.Helper()
	tokens := html.NewTokenizer(strings.NewReader(body))
	for tokenType := tokens.Next(); tokenType != html.ErrorToken; tokenType = tokens.Next() {
		if tokenType != html.StartTagToken && tokenType != html.SelfClosingTagToken {
			continue
		}
		token := tokens.Token()
		if token.Data != "input" {
			continue
		}
		var inputName, value string
		for _, attribute := range token.Attr {
			switch attribute.Key {
			case "name":
				inputName = attribute.Val
			case "value":
				value = attribute.Val
			}
		}
		if inputName == name {
			return value
		}
	}
	t.Fatalf("HTML input %q is missing", name)
	return ""
}

func assertPublicationJSONError(t *testing.T, response *httptest.ResponseRecorder, code, zone, problem string) {
	t.Helper()
	var result struct {
		Error             string            `json:"error"`
		Fields            map[string]string `json:"fields"`
		PublishedTimezone string            `json:"published_timezone"`
		Saved             bool              `json:"saved"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusUnprocessableEntity || result.Error != code || result.Saved || result.PublishedTimezone != zone || !strings.Contains(result.Fields["published_at"], problem) {
		t.Fatalf("publication validation = %d %s err=%v; want %s, timezone=%s, problem=%q", response.Code, response.Body.String(), err, code, zone, problem)
	}
}
