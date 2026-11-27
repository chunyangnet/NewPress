package httpserver

import (
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/pluginshared"
)

func (s *Server) pluginSharedRecordInspect(w http.ResponseWriter, r *http.Request) {
	s.readSharedPluginMaintenance(w, r, false)
}
func (s *Server) pluginSharedQuarantines(w http.ResponseWriter, r *http.Request) {
	s.readSharedPluginMaintenance(w, r, true)
}
func (s *Server) readSharedPluginMaintenance(w http.ResponseWriter, r *http.Request, quarantines bool) {
	principal, ok := s.requireSharedReviewIdentity(w, r)
	if !ok {
		return
	}
	store, ok := s.pluginTasks.(pluginshared.Reader)
	if !ok {
		s.writeAPIError(w, r, 503, "plugins.shared_maintenance_unavailable", "当前存储不提供共享授权与内容回执维护", nil)
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"type": true, "plugin": true, "job": true, "limit": true, "offset": true})
	if !ok {
		return
	}
	q := pluginshared.Query{Type: values.Get("type"), Plugin: values.Get("plugin"), JobID: values.Get("job")}
	for key, target := range map[string]*int{"limit": &q.Limit, "offset": &q.Offset} {
		if text := values.Get(key); text != "" {
			value, err := strconv.Atoi(text)
			if err != nil || key == "limit" && value < 1 {
				s.pluginTaskError(w, r, pluginshared.ErrInvalid)
				return
			}
			*target = value
		}
	}
	q, err := pluginshared.NormalizeQuery(q)
	if err != nil {
		s.pluginTaskError(w, r, err)
		return
	}
	if allowed, _ := s.accessRate.allow("plugin-shared-maintenance-read:"+strconv.FormatInt(principal.UserID, 10), 30, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate", "查询过于频繁，请稍后再试", nil)
		return
	}
	var page any
	if quarantines {
		page, err = store.ListSharedQuarantines(r.Context(), q)
	} else {
		page, err = store.InspectSharedRecords(r.Context(), q)
	}
	if err != nil {
		s.pluginTaskError(w, r, err)
		return
	}
	current, allowed := s.recheckSharedReviewIdentity(w, r)
	if !allowed {
		return
	}
	if current.UserID != principal.UserID {
		s.writeAPIError(w, r, http.StatusConflict, "plugins.review_identity_changed", "登录账号已变化，请重新打开核对页面", nil)
		return
	}
	writeJSON(w, 200, page)
}
