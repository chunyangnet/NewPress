package auth

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashSessionAndCSRF(t *testing.T) {
	password := "correct horse battery staple 2026"
	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if valid, err := VerifyPassword(encoded, password); err != nil || !valid {
		t.Fatalf("verify correct password = %v, %v", valid, err)
	}
	if valid, err := VerifyPassword(encoded, password+"!"); err != nil || valid {
		t.Fatalf("verify wrong password = %v, %v", valid, err)
	}

	manager, err := New(Config{
		Username: "admin", PasswordHash: encoded, AbsoluteTTL: time.Hour, IdleTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, principal, err := manager.Authenticate("127.0.0.1", "admin", password)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || principal.CSRFToken == "" || !manager.CheckCSRF(principal, principal.CSRFToken) {
		t.Fatal("session did not include a usable CSRF token")
	}
	if manager.CheckCSRF(principal, principal.CSRFToken+"x") {
		t.Fatal("accepted an invalid CSRF token")
	}
	loaded, ok := manager.ValidateSession(token)
	if !ok || loaded.Username != "admin" {
		t.Fatal("created session was not valid")
	}
	manager.RevokeSession(token)
	if _, ok := manager.ValidateSession(token); ok {
		t.Fatal("revoked session remained valid")
	}
}

func TestPersistentUsersBootstrapLoginCapabilityRefreshAndPasswordRevocation(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	bootstrapPassword := "Persistent owner password 2026"
	hash, err := HashPassword(bootstrapPassword)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewWithContext(ctx, Config{
		Username: "owner", PasswordHash: hash, Users: store,
		BootstrapEmail: "owner@example.test", BootstrapDisplayName: "站点所有者",
		AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !manager.Configured() || !manager.PersistentUsers() {
		t.Fatal("persistent identity manager was not configured")
	}
	token, owner, err := manager.AuthenticateContext(ctx, "owner-ip", "OWNER@EXAMPLE.TEST", bootstrapPassword)
	if err != nil {
		t.Fatal(err)
	}
	if owner.UserID != 1 || owner.Label() != "站点所有者" || owner.RoleName != "管理员" || !owner.Can(CapabilityUsersManage) || !owner.HasRole("administrator") {
		t.Fatalf("owner principal = %#v", owner)
	}
	if loaded, ok := manager.ValidateSessionContext(ctx, token); !ok || loaded.AccountVersion != 1 {
		t.Fatalf("persistent session = %#v, %v", loaded, ok)
	}

	editorPassword := "Editorial account password 2026"
	editor, err := manager.CreateUser(ctx, owner, UserInput{
		Username: "editor", Email: "editor@example.test", DisplayName: "内容编辑",
		Status: UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"editor"},
	}, editorPassword)
	if err != nil {
		t.Fatal(err)
	}
	editorToken, editorPrincipal, err := manager.AuthenticateContext(ctx, "editor-ip", editor.Email, editorPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !editorPrincipal.Can(CapabilityContentWrite) || editorPrincipal.Can(CapabilityUsersManage) || editorPrincipal.RoleName != "编辑" {
		t.Fatalf("editor principal = %#v", editorPrincipal)
	}
	editor, err = manager.UpdateUser(ctx, owner, editor.ID, editor.Version, UserInput{
		Username: editor.Username, Email: editor.Email, DisplayName: editor.DisplayName,
		Status: UserStatusSuspended, Locale: editor.Locale, RoleSlugs: []string{"editor"},
	})
	if err != nil || editor.Version != 2 {
		t.Fatalf("suspend editor = %#v, %v", editor, err)
	}
	if _, ok := manager.ValidateSessionContext(ctx, editorToken); ok {
		t.Fatal("suspended editor session remained valid")
	}

	ownerUser, err := manager.GetUser(ctx, owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateUserPassword(ctx, owner, owner.UserID, ownerUser.Version, "Rotated owner password 2026"); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.ValidateSessionContext(ctx, token); ok {
		t.Fatal("password rotation did not revoke existing owner sessions")
	}
	restarted, err := NewWithContext(ctx, Config{Users: store, AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.Configured() {
		t.Fatal("stored users did not survive authentication manager restart")
	}
	if _, principal, err := restarted.AuthenticateContext(ctx, "restart", "owner", "Rotated owner password 2026"); err != nil || principal.UserID != owner.UserID {
		t.Fatalf("restart login principal = %#v, err=%v", principal, err)
	}
}

func TestSessionInventoryMetadataScopeAndRevocation(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	ownerPassword := "Session owner password 2026"
	hash, err := HashPassword(ownerPassword)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewWithContext(ctx, Config{
		Username: "owner", PasswordHash: hash, Users: store,
		BootstrapEmail: "owner@example.test", BootstrapDisplayName: "会话管理员",
		AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, time.August, 11, 13, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return clock }
	ownerToken, owner, err := manager.AuthenticateWithMetadataContext(ctx, "192.0.2.10", "owner", ownerPassword, SessionMetadata{
		IPAddress: " 192.0.2.10 ", UserAgent: "Chrome on Windows",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	ownerOtherToken, _, err := manager.AuthenticateWithMetadataContext(ctx, "192.0.2.11", "owner", ownerPassword, SessionMetadata{
		IPAddress: "192.0.2.11", UserAgent: "Safari on macOS",
	})
	if err != nil {
		t.Fatal(err)
	}
	authorPassword := "Session author password 2026"
	author, err := manager.CreateUser(ctx, owner, UserInput{
		Username: "author", Email: "author@example.test", DisplayName: "设备作者",
		Status: UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"author"},
	}, authorPassword)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	authorToken, authorPrincipal, err := manager.AuthenticateWithMetadataContext(ctx, "198.51.100.20", author.Username, authorPassword, SessionMetadata{
		IPAddress: "198.51.100.20", UserAgent: "Firefox on Linux",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	authorOtherToken, _, err := manager.AuthenticateWithMetadataContext(ctx, "198.51.100.21", author.Username, authorPassword, SessionMetadata{
		IPAddress: "198.51.100.21", UserAgent: "Mobile Safari on iPhone",
	})
	if err != nil {
		t.Fatal(err)
	}

	all := manager.ListSessions(owner, ownerToken, true)
	if len(all) != 4 || !all[0].Current || all[0].IPAddress != "192.0.2.10" || all[0].UserAgent != "Chrome on Windows" {
		t.Fatalf("administrator session inventory = %#v", all)
	}
	authorSessions := manager.ListSessions(authorPrincipal, authorToken, true)
	if len(authorSessions) != 2 || !authorSessions[0].Current {
		t.Fatalf("author all-scope inventory escaped own boundary = %#v", authorSessions)
	}
	var ownerOtherID, authorOtherID string
	for _, item := range all {
		switch {
		case item.Username == owner.Username && !item.Current:
			ownerOtherID = item.ID
		case item.Username == author.Username && item.IPAddress == "198.51.100.21":
			authorOtherID = item.ID
		}
	}
	if ownerOtherID == "" || authorOtherID == "" {
		t.Fatalf("session IDs missing from inventory: %#v", all)
	}
	if _, err := manager.RevokeSessionID(authorPrincipal, authorToken, ownerOtherID); !errors.Is(err, ErrSessionForbidden) {
		t.Fatalf("author revoked administrator session: %v", err)
	}
	if _, err := manager.RevokeSessionID(authorPrincipal, authorToken, authorSessions[0].ID); !errors.Is(err, ErrCurrentSession) {
		t.Fatalf("current session revoke error = %v", err)
	}
	if revoked, err := manager.RevokeSessionID(authorPrincipal, authorToken, authorOtherID); err != nil || revoked.Username != author.Username {
		t.Fatalf("revoke own other session = %#v, %v", revoked, err)
	}
	if _, ok := manager.ValidateSessionContext(ctx, authorOtherToken); ok {
		t.Fatal("revoked author session remained valid")
	}
	clock = clock.Add(time.Minute)
	authorThirdToken, _, err := manager.AuthenticateWithMetadataContext(ctx, "198.51.100.22", author.Username, authorPassword, SessionMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := manager.RevokeOtherSessions(authorPrincipal, authorToken); err != nil || count != 1 {
		t.Fatalf("revoke other author sessions = %d, %v", count, err)
	}
	if _, ok := manager.ValidateSessionContext(ctx, authorThirdToken); ok {
		t.Fatal("bulk-revoked author session remained valid")
	}
	if _, ok := manager.ValidateSessionContext(ctx, ownerOtherToken); !ok {
		t.Fatal("author bulk revoke affected another identity")
	}
}

func TestAuthenticationRateLimit(t *testing.T) {
	encoded, err := HashPassword("a sufficiently long admin password")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(Config{Username: "admin", PasswordHash: encoded})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 6; attempt++ {
		if _, _, err := manager.Authenticate("198.51.100.10", "admin", "wrong password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d error = %v", attempt+1, err)
		}
	}
	if _, _, err := manager.Authenticate("198.51.100.10", "admin", "a sufficiently long admin password"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate-limited login error = %v", err)
	}
}

func TestAuthenticationStateIsBounded(t *testing.T) {
	password := "bounded authentication password 2026"
	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(Config{Username: "admin", PasswordHash: encoded, AbsoluteTTL: 48 * time.Hour, IdleTTL: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return clock }

	principal := Principal{Username: "admin", DisplayName: "admin", RoleName: "管理员", Capabilities: []string{CapabilityAll}}
	firstToken, _, err := manager.createSession(clock, principal, SessionMetadata{IPAddress: "198.51.100.1"}, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.ValidateSession(firstToken); !ok {
		t.Fatal("initial session was invalid before capacity eviction")
	}
	for index := 1; index <= maxSessionsPerIdentity; index++ {
		clock = clock.Add(time.Millisecond)
		if _, _, err := manager.createSession(clock, principal, SessionMetadata{IPAddress: "198.51.100." + strconv.Itoa((index%250)+1)}, encoded); err != nil {
			t.Fatal(err)
		}
	}
	if count := manager.ActiveSessionCount(); count != maxSessionsPerIdentity {
		t.Fatalf("identity sessions = %d, want %d", count, maxSessionsPerIdentity)
	}
	if _, ok := manager.ValidateSession(firstToken); ok {
		t.Fatal("oldest identity session was not evicted at capacity")
	}

	store := NewMemorySessionStore()
	manager.sessionStore = store
	for index := 0; index < maxActiveSessions; index++ {
		id := sessionDigest("global-session-" + strconv.Itoa(index))
		store.records[id] = SessionRecord{
			ID: id, UserID: int64(index + 1), Username: "user-" + strconv.Itoa(index),
			CreatedAt: clock, LastSeenAt: clock.Add(time.Duration(index) * time.Millisecond), ExpiresAt: clock.Add(48 * time.Hour),
			CredentialDigest: sessionDigest(""), CSRFToken: sessionDigest("csrf"),
		}
	}
	oldestGlobalKey := sessionDigest("global-session-0")
	clock = clock.Add(time.Hour)
	if _, _, err := manager.createSession(clock, Principal{UserID: maxActiveSessions + 1, Username: "new-user"}, SessionMetadata{}, ""); err != nil {
		t.Fatal(err)
	}
	globalCount := manager.ActiveSessionCount()
	_, oldestGlobalExists := store.records[oldestGlobalKey]
	if globalCount != maxActiveSessions || oldestGlobalExists {
		t.Fatalf("global sessions = count:%d oldest:%t", globalCount, oldestGlobalExists)
	}

	for index := 0; index <= maxLoginAttemptKeys; index++ {
		key := "attempt-" + strconv.Itoa(index)
		manager.recordLoginFailure(key, clock.Add(time.Duration(index)*time.Millisecond))
	}
	manager.mu.Lock()
	attemptCount := len(manager.attempts)
	_, oldestExists := manager.attempts["attempt-0"]
	_, newestExists := manager.attempts["attempt-"+strconv.Itoa(maxLoginAttemptKeys)]
	manager.mu.Unlock()
	if attemptCount != maxLoginAttemptKeys || oldestExists || !newestExists {
		t.Fatalf("attempt registry = count:%d oldest:%t newest:%t", attemptCount, oldestExists, newestExists)
	}
}

func TestRejectsUnsafePasswordHashParameters(t *testing.T) {
	_, err := New(Config{
		Username:     "admin",
		PasswordHash: "$argon2id$v=19$m=1048576,t=3,p=2$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
	})
	if err == nil {
		t.Fatal("accepted an excessive argon2 memory cost")
	}
}

func TestSessionAbsoluteAndIdleExpiration(t *testing.T) {
	password := "another sufficiently long password"
	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(Config{
		Username: "admin", PasswordHash: encoded, AbsoluteTTL: time.Hour, IdleTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return clock }

	idleToken, _, err := manager.Authenticate("idle", "admin", password)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(9 * time.Minute)
	if _, ok := manager.ValidateSession(idleToken); !ok {
		t.Fatal("session expired before the idle timeout")
	}
	if count := manager.ActiveSessionCount(); count != 1 {
		t.Fatalf("active session count before idle expiry = %d", count)
	}
	clock = clock.Add(10 * time.Minute)
	if count := manager.ActiveSessionCount(); count != 0 {
		t.Fatalf("active session count after idle expiry = %d", count)
	}
	if _, ok := manager.ValidateSession(idleToken); ok {
		t.Fatal("session remained valid at the idle timeout boundary")
	}

	clock = time.Date(2026, time.August, 11, 14, 0, 0, 0, time.UTC)
	absoluteToken, _, err := manager.Authenticate("absolute", "admin", password)
	if err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 6; step++ {
		clock = clock.Add(10*time.Minute - time.Second)
		if _, ok := manager.ValidateSession(absoluteToken); !ok {
			t.Fatalf("session expired before absolute timeout at step %d", step)
		}
	}
	clock = time.Date(2026, time.August, 11, 15, 0, 0, 0, time.UTC)
	if _, ok := manager.ValidateSession(absoluteToken); ok {
		t.Fatal("session remained valid at the absolute timeout boundary")
	}
}

func TestPasswordPolicyAndPHCParsingAreStrict(t *testing.T) {
	if _, err := HashPassword("中文中文中文中文"); err == nil {
		t.Fatal("accepted fewer than 12 Unicode characters")
	}
	if _, err := HashPassword(string([]byte{0xff, 0xfe}) + strings.Repeat("a", 12)); err == nil {
		t.Fatal("accepted invalid UTF-8 password")
	}
	encoded, err := HashPassword("valid password for parsing")
	if err != nil {
		t.Fatal(err)
	}
	for name, malformed := range map[string]string{
		"missing version prefix": strings.Replace(encoded, "$v=19$", "$19$", 1),
		"extra parameter":        strings.Replace(encoded, "$m=65536,t=3,p=2$", "$m=65536,t=3,p=2,x=1$", 1),
		"reordered parameter":    strings.Replace(encoded, "$m=65536,t=3,p=2$", "$t=3,m=65536,p=2$", 1),
		"empty parameter":        strings.Replace(encoded, "$m=65536,t=3,p=2$", "$m=,t=3,p=2$", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(Config{Username: "admin", PasswordHash: malformed}); err == nil {
				t.Fatal("accepted malformed PHC string")
			}
		})
	}
	if _, err := New(Config{
		Username: "admin", PasswordHash: encoded, AbsoluteTTL: time.Hour, IdleTTL: 2 * time.Hour,
	}); err == nil {
		t.Fatal("accepted idle timeout longer than absolute lifetime")
	}
}
