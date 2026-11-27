package httpserver

import (
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/pluginjobs"
)

// HTTP exposes safe inspection only. Isolation and repair require the offline
// CLI, site directory locks and an explicit declaration that all workers stop.
func (s *Server) pluginTaskRecordInspect(w http.ResponseWriter, r *http.Request) {
	s.readPluginMaintenance(w, r, false)
}

func (s *Server) pluginTaskQuarantines(w http.ResponseWriter, r *http.Request) {
	s.readPluginMaintenance(w, r, true)
}

func (s *Server) readPluginMaintenance(w http.ResponseWriter, r *http.Request, quarantines bool) {
	principal, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if readOnly || principal.UserID < 1 {
		s.writeAPIError(w, r, 403, "plugins.authentication", "请使用持久管理员账号登录后查看维护记录", nil)
		return
	}
	store, ok := s.pluginTasks.(pluginjobs.MaintenanceReader)
	if !ok {
		s.pluginTaskError(w, r, nil)
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"type": true, "limit": true, "offset": true})
	if !ok {
		return
	}
	query := pluginjobs.DiagnosticQuery{Type: pluginjobs.RecordType(values.Get("type"))}
	for key, target := range map[string]*int{"limit": &query.Limit, "offset": &query.Offset} {
		if text := values.Get(key); text != "" {
			value, err := strconv.Atoi(text)
			if err != nil || key == "limit" && value < 1 {
				s.pluginTaskError(w, r, pluginjobs.ErrInvalid)
				return
			}
			*target = value
		}
	}
	query, err := pluginjobs.NormalizeDiagnosticQuery(query)
	if err != nil {
		s.pluginTaskError(w, r, err)
		return
	}
	if allowed, _ := s.accessRate.allow("plugin-maintenance-read:"+strconv.FormatInt(principal.UserID, 10), 30, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "plugins.rate", "查询过于频繁，请稍后再试", nil)
		return
	}
	if quarantines {
		page, err := store.ListQuarantines(r.Context(), query)
		if err != nil {
			s.pluginTaskError(w, r, err)
			return
		}
		writeJSON(w, 200, page)
	} else {
		page, err := store.InspectRecords(r.Context(), query)
		if err != nil {
			s.pluginTaskError(w, r, err)
			return
		}
		writeJSON(w, 200, page)
	}
}
