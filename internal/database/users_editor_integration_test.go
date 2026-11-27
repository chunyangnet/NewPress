package database

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/progression"
)

func TestPostgreSQLUserAccountEditor(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	pool, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepository(pool)
	if err = users.EnsureRoles(t.Context(), auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Postgres user editor password 2026")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("account-editor-%d", time.Now().UnixNano())
	in := auth.UserInput{Username: name, Email: name + "@example.test", RoleSlugs: []string{"author", "subscriber"}, Account: &auth.UserAccountInput{Bio: "创建简介", EmailVerified: true}}
	u, err := users.CreateUser(t.Context(), auth.UserMutation{Actor: "test"}, in, hash)
	if err != nil {
		t.Fatal(err)
	}
	if u.Bio != "创建简介" || u.EmailVerifiedAt == nil {
		t.Fatal("incomplete create", u)
	}
	in.Account.Bio, in.Account.AvatarMediaID = "修改简介", 123
	in.Account.Phone, in.Account.PhoneVerified = "+8613800999000", true
	in.Account.EmailVerified, in.Status = false, auth.UserStatusSuspended
	u, err = users.UpdateUser(t.Context(), u.ID, auth.UserMutation{Actor: "test", ExpectedVersion: u.Version}, in)
	if err != nil {
		t.Fatal(err)
	}
	if u.Bio != "修改简介" || u.AvatarMediaID != 123 || u.PhoneVerifiedAt == nil || u.EmailVerifiedAt != nil || len(u.Roles) != 2 || u.Status != auth.UserStatusSuspended {
		t.Fatal("incomplete update", u)
	}
	in.Account.Phone, in.Account.PhoneVerified, in.Account.AvatarMediaID = "", false, 0
	u, err = users.UpdateUser(t.Context(), u.ID, auth.UserMutation{Actor: "test", ExpectedVersion: u.Version}, in)
	if err != nil || u.Phone != "" || u.PhoneVerifiedAt != nil || u.AvatarMediaID != 0 {
		t.Fatal("clear fields", u, err)
	}
	store := NewProgressionStore(pool)
	cfg, err := store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Membership.Enabled = true
	cfg, err = store.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	member := progression.MembershipInput{ActorID: u.ID, UserID: u.ID, ExpectedVersion: cfg.Version, Action: "set", Plan: cfg.Membership.Plans[0].Key, Since: now, Until: now.AddDate(0, 1, 0), RequestID: name, Reason: "数据库编辑验证"}
	result, err := store.ManageMembership(t.Context(), member)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.ManageMembership(t.Context(), member)
	if err != nil || !replay.Replayed || result.Entry.ID != replay.Entry.ID {
		t.Fatal("membership replay", replay, err)
	}
	snap, err := store.Snapshot(t.Context(), u.ID, 0)
	if err != nil || !snap.Account.Membership.Until.Equal(member.Until) || len(snap.Entries) != 1 {
		t.Fatal("membership roundtrip", snap, err)
	}
}
