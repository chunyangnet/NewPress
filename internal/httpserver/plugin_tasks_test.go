package httpserver

import (
	"newpress.local/newpress/internal/auth"
	"testing"
)

func TestPluginTaskUserAllowed(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		id           int64
		caps         []string
		want         bool
	}{
		{"administrator", auth.UserStatusActive, 1, []string{auth.CapabilityAll}, true},
		{"explicit", auth.UserStatusActive, 1, []string{auth.CapabilityAdminAccess, auth.CapabilityPluginsManage}, true},
		{"suspended", auth.UserStatusSuspended, 1, []string{auth.CapabilityAll}, false},
		{"system", auth.UserStatusSystem, 1, []string{auth.CapabilityAll}, false},
		{"missing identity", auth.UserStatusActive, 0, []string{auth.CapabilityAll}, false},
		{"no admin access", auth.UserStatusActive, 1, []string{auth.CapabilityPluginsManage}, false},
		{"revoked", auth.UserStatusActive, 1, []string{auth.CapabilityAdminAccess}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := auth.User{ID: tc.id, Status: tc.status, Roles: []auth.Role{{Capabilities: tc.caps}}}
			if got := pluginTaskUserAllowed(user); got != tc.want {
				t.Fatalf("allowed=%v want %v", got, tc.want)
			}
		})
	}
}
