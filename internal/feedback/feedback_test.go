package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFeedbackWorkflowAndPersistence(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			path := ""
			if disk {
				path = filepath.Join(t.TempDir(), "feedback.json")
			}
			s, e := NewFileStore(path)
			if e != nil {
				t.Fatal(e)
			}
			ctx := t.Context()
			now := time.Now().UTC()
			admin := Actor{ID: 1, Admin: true}
			reader := Actor{ID: 2, Roles: []string{"subscriber"}}
			other := Actor{ID: 3, Roles: []string{"subscriber"}}
			editor := Actor{ID: 4, Roles: []string{"editor"}}
			seq := 0
			apply := func(a Actor, c Command, want error) Result {
				t.Helper()
				seq++
				c.Key = fmt.Sprintf("request-%016d", seq)
				if b, e := s.Get(ctx, "ideas"); e == nil {
					c.Version = b.Version
				}
				result, e := s.Apply(ctx, "ideas", a, c, now)
				if !errors.Is(e, want) {
					t.Fatalf("%s: got %v want %v", c.Action, e, want)
				}
				return result
			}
			config := Defaults()
			config.Moderation = true
			apply(reader, Command{Action: "create", Settings: &config}, ErrForbidden)
			apply(admin, Command{Action: "create", Settings: &config}, nil)
			id := apply(reader, Command{Action: "submit", Title: "改善搜索体验", Body: "搜索时希望可以筛选不同分类的内容。", Kind: "suggestion", Category: "使用体验"}, nil).ItemID
			b, _ := s.Get(ctx, "ideas")
			if CanSee(b, other, b.Items[0]) || CanSee(b, Actor{}, b.Items[0]) || !CanSee(b, reader, b.Items[0]) {
				t.Fatal("pending privacy")
			}
			yes, no := true, false
			apply(other, Command{Action: "vote", ItemID: id, Enabled: &yes}, ErrNotFound)
			apply(reader, Command{Action: "visibility", ItemID: id, Visibility: "public"}, ErrForbidden)
			apply(editor, Command{Action: "visibility", ItemID: id, Visibility: "public"}, nil)
			apply(reader, Command{Action: "vote", ItemID: id, Enabled: &yes}, nil)
			apply(reader, Command{Action: "vote", ItemID: id, Enabled: &yes}, nil)
			b, _ = s.Get(ctx, "ideas")
			if len(b.Items[0].Votes) != 1 {
				t.Fatal("duplicate vote")
			}
			apply(reader, Command{Action: "vote", ItemID: id, Enabled: &no}, nil)
			b, _ = s.Get(ctx, "ideas")
			if len(b.Items[0].Votes) != 0 {
				t.Fatal("vote withdrawal")
			}
			apply(reader, Command{Action: "reply", ItemID: id, Body: "冒充官方", Official: true}, ErrForbidden)
			reply := apply(editor, Command{Action: "reply", ItemID: id, Body: "已经加入本周的改进计划。", Official: true}, nil)
			apply(editor, Command{Action: "hide_reply", ItemID: id, ReplyID: reply.ReplyID, Enabled: &yes}, nil)
			apply(editor, Command{Action: "status", ItemID: id, Status: "planned", Body: "评估完成"}, nil)
			apply(reader, Command{Action: "edit", ItemID: id, Title: "改过的标题", Body: "编辑后的描述内容。", Kind: "suggestion"}, ErrForbidden)
			apply(editor, Command{Action: "lock", ItemID: id, Enabled: &yes}, nil)
			apply(reader, Command{Action: "reply", ItemID: id, Body: "尝试追加"}, ErrForbidden)
			apply(editor, Command{Action: "reply", ItemID: id, Body: "锁定后官方仍可回复", Official: true}, nil)
			privateID := apply(reader, Command{Action: "submit", Title: "私密问题反馈", Body: "这里只应显示给作者与管理者。", Kind: "issue", Private: true}, nil).ItemID
			apply(editor, Command{Action: "visibility", ItemID: privateID, Visibility: "public"}, nil)
			b, _ = s.Get(ctx, "ideas")
			if !b.Items[1].Private || CanSee(b, other, b.Items[1]) {
				t.Fatal("moderation published private content")
			}
			config.Voting = false
			config.Comments = false
			config.OfficialReplies = false
			config.PrivateSubmissions = false
			apply(admin, Command{Action: "configure", Settings: &config}, nil)
			apply(editor, Command{Action: "reply", ItemID: id, Body: "官方开关关闭", Official: true}, ErrForbidden)
			apply(reader, Command{Action: "submit", Title: "新的私密反馈", Body: "这项功能现在已经关闭。", Kind: "feedback", Private: true}, ErrForbidden)
			b, _ = s.Get(ctx, "ideas")
			if len(b.Items[0].History) != 2 || b.Items[0].Status != "planned" || !b.Items[0].Replies[0].Hidden {
				t.Fatal("lost workflow state")
			}
			if disk {
				s, e = NewFileStore(path)
				if e != nil {
					t.Fatal(e)
				}
				restored, _ := s.Get(ctx, "ideas")
				if restored.Version != b.Version || len(restored.Items) != 2 || restored.Items[0].Replies[1].Body != "锁定后官方仍可回复" {
					t.Fatal("restart lost facts")
				}
			}
			// Returned projections never share writable slices with stored facts.
			headers, _ := s.List(ctx)
			headers[0].Settings.Permissions[0].Read = false
			fresh, _ := s.Get(ctx, "ideas")
			if !fresh.Settings.Permissions[0].Read {
				t.Fatal("header alias mutated facts")
			}
			config.Enabled = false
			apply(admin, Command{Action: "configure", Settings: &config}, nil)
			b, _ = s.Get(ctx, "ideas")
			if Can(b, editor, "read") || !Can(b, admin, "read") {
				t.Fatal("disabled board")
			}
		})
	}
}

func TestFeedbackConcurrencyReceiptsAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.json")
	s, _ := NewFileStore(path)
	ctx := t.Context()
	now := time.Now().UTC()
	a := Actor{ID: 1, Admin: true}
	c := Defaults()
	_, e := s.Apply(ctx, "ideas", a, Command{Action: "create", Key: "create-request-key", Settings: &c}, now)
	if e != nil {
		t.Fatal(e)
	}
	reader := Actor{ID: 2, Roles: []string{"subscriber"}}
	cmd := Command{Action: "submit", Version: 1, Key: "durable-submit-key", Title: "重试不会重复创建", Body: "请保留完全相同的请求标识。", Kind: "feedback"}
	first, e := s.Apply(ctx, "ideas", reader, cmd, now)
	if e != nil {
		t.Fatal(e)
	}
	s, e = NewFileStore(path)
	if e != nil {
		t.Fatal(e)
	}
	replayed, e := s.Apply(ctx, "ideas", reader, cmd, now)
	if e != nil || !replayed.Replay || replayed.ItemID != first.ItemID {
		t.Fatal("durable receipt", e, replayed)
	}
	cmd.Title = "不同的内容"
	if _, e = s.Apply(ctx, "ideas", reader, cmd, now); !errors.Is(e, ErrConflict) {
		t.Fatal("key payload mismatch", e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			yes := true
			_, err := s.Apply(ctx, "ideas", Actor{ID: int64(100 + i), Roles: []string{"subscriber"}}, Command{Action: "vote", Key: fmt.Sprintf("concurrent-vote-%08d", i), Version: 1, ItemID: first.ItemID, Enabled: &yes}, now)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	b, _ := s.Get(ctx, "ideas")
	if len(b.Items) != 1 || len(b.Items[0].Votes) != 24 {
		t.Fatal("lost concurrent writes")
	}
	if _, e = s.Apply(ctx, "ideas", a, Command{Action: "status", Key: "stale-admin-update", Version: 1, ItemID: first.ItemID, Status: "done"}, now); !errors.Is(e, ErrConflict) {
		t.Fatal("stale update", e)
	}
	c.Permissions = []Grant{{Role: "guest", Read: true}}
	_, e = s.Apply(ctx, "ideas", a, Command{Action: "configure", Key: "revoke-reader-role", Version: b.Version, Settings: &c}, now)
	if e != nil {
		t.Fatal(e)
	}
	cmd.Title = "重试不会重复创建"
	if _, e = s.Apply(ctx, "ideas", reader, cmd, now); !errors.Is(e, ErrForbidden) {
		t.Fatal("replay after revocation", e)
	}
}

func TestFeedbackInvalidFactsAndAtomicFailure(t *testing.T) {
	c := Defaults()
	if !ValidSettings(c) {
		t.Fatal("defaults invalid")
	}
	c.Permissions[0].Submit = true
	if ValidSettings(c) {
		t.Fatal("anonymous writes enabled")
	}
	c = Defaults()
	c.Permissions[1].Read = false
	if ValidSettings(c) {
		t.Fatal("unreadable write permission")
	}
	path := filepath.Join(t.TempDir(), "feedback.json")
	s, _ := NewFileStore(path)
	a := Actor{ID: 1, Admin: true}
	c = Defaults()
	now := time.Now().UTC()
	_, e := s.Apply(t.Context(), "ideas", a, Command{Action: "create", Key: "create-board-key-1", Settings: &c}, now)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := s.Get(t.Context(), "ideas")
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e = s.Apply(cancelled, "ideas", a, Command{Action: "submit", Key: "cancelled-request", Version: b.Version, Title: "不能保存的反馈", Body: "请求在提交前已取消。", Kind: "issue"}, now); e == nil {
		t.Fatal("cancelled write")
	}
	if _, e = s.Apply(t.Context(), "ideas", a, Command{Action: "vote", Key: "confused-command", Version: b.Version, ItemID: 1, Settings: &c}, now); !errors.Is(e, ErrInvalid) {
		t.Fatal("irrelevant command field", e)
	}
	bytes, _ := os.ReadFile(path)
	if e = os.WriteFile(path, append(bytes, ' '), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Apply(t.Context(), "ideas", a, Command{Action: "configure", Key: "external-conflict", Version: b.Version, Settings: &c}, now); !errors.Is(e, ErrChanged) {
		t.Fatal("external overwrite", e)
	}
	b.Items = []Item{{ID: 1}}
	raw, _ := json.Marshal(b)
	if _, e = DecodeBoard(raw); e == nil {
		t.Fatal("invalid persisted item")
	}
	for _, raw := range []string{`{"schema":1,"schema":1,"boards":[]}`, `{"schema":99,"boards":[]}`, `{"schema":1,"boards":[],"extra":true}`} {
		if e = os.WriteFile(path, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = NewFileStore(path); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
