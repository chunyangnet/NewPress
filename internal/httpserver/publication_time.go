package httpserver

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

const publishedLocalLayout = "2006-01-02T15:04"

// parsePublishedLocal enumerates the offsets actually in effect near this civil
// date. ZoneBounds handles half-hour changes and date-line jumps as well as DST;
// ParseInLocation alone silently chooses one side of a gap or repeated hour.
func parsePublishedLocal(raw string, location *time.Location) (time.Time, error) {
	wall, err := time.Parse(publishedLocalLayout, raw)
	if err != nil || wall.Year() < 1 {
		return time.Time{}, errors.New("发布时间格式不正确")
	}
	if location == nil {
		location = time.UTC
	}
	offsets := map[int]bool{}
	end := wall.Add(48 * time.Hour)
	for cursor := wall.Add(-48 * time.Hour); !cursor.After(end); {
		local := cursor.In(location)
		_, offset := local.Zone()
		offsets[offset] = true
		_, next := local.ZoneBounds()
		if next.IsZero() || !next.After(cursor) {
			break
		}
		cursor = next
	}
	var found time.Time
	matches := 0
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second).In(location)
		if candidate.Format(publishedLocalLayout) == raw {
			found = candidate
			matches++
		}
	}
	if matches == 0 {
		return time.Time{}, errors.New("此站点时区中不存在这个本地时间（夏令时跳时或日期跳变），请选择其他时间")
	}
	if matches > 1 {
		return time.Time{}, errors.New("此站点时区中这个本地时间出现两次（时钟回拨），请选择重复时段以外的时间")
	}
	return found, nil
}

// Existing instants are sourced from an authorized canonical post or the same
// editor's server recovery point, never from a client-supplied RFC3339 value.
func (s *Server) parseEditorInput(request *http.Request, form url.Values, principal auth.Principal, now time.Time, canonical content.Post) (content.PostInput, map[string]string) {
	original := canonical.PublishedAt
	missingRecovery := false
	if form.Get("published_source") == "autosave" {
		if recovery, ok := s.loadEditorAutosave(request, canonical, principal); ok {
			original = recovery.Snapshot.PublishedAt
		} else {
			missingRecovery = true
		}
	}
	input, fields := parseContentInput(form, principal, now, s.siteLocation(request.Context()), original)
	if source := form.Get("published_source"); source != "" && source != "canonical" && source != "autosave" {
		fields["published_at"] = "发布时间来源不正确，请重新打开编辑器核对"
	}
	if missingRecovery {
		fields["published_at"] = "原自动保存恢复点已不可用，请重新核对发布时间"
	}
	return input, fields
}
