package community

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCommunityStorageContract(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint("disk=", disk), func(t *testing.T) {
			path := ""
			if disk {
				path = filepath.Join(t.TempDir(), "community.json")
			}
			s, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			admin := Actor{ID: 1, Admin: true}
			owner := Actor{ID: 2}
			member := Actor{ID: 3}
			other := Actor{ID: 4}
			now := time.Now().UTC()
			seq := 0
			run := func(a Actor, cmd Command) (Result, error) {
				t.Helper()
				seq++
				cmd.Key = fmt.Sprintf("request-%016d", seq)
				if c, e := s.Get(t.Context(), "reading"); e == nil {
					cmd.Version = c.Version
				}
				return s.Apply(t.Context(), "reading", a, cmd, now)
			}
			must := func(a Actor, cmd Command) Result {
				t.Helper()
				r, e := run(a, cmd)
				if e != nil {
					t.Fatal(cmd.Action, e)
				}
				return r
			}
			create := Command{Action: "create", Name: "阅读会", Rules: "先读规则", JoinPolicy: "approval", Visibility: "members", OwnerID: 2}
			if _, e := run(member, create); !errors.Is(e, ErrForbidden) {
				t.Fatal("unprivileged create", e)
			}
			must(admin, create)
			c, _ := s.Get(t.Context(), "reading")
			if CanRead(c, other) || CanPost(c, other) {
				t.Fatal("private permission")
			}
			must(member, Command{Action: "join"})
			c, _ = s.Get(t.Context(), "reading")
			if Role(c, member) != "pending" || CanRead(c, member) {
				t.Fatal("pending is a member")
			}
			if _, e := run(member, Command{Action: "member", UserID: 3, Role: "moderator"}); !errors.Is(e, ErrForbidden) {
				t.Fatal("self elevation", e)
			}
			must(owner, Command{Action: "member", UserID: 3, Role: "member"})
			must(other, Command{Action: "join"})
			must(owner, Command{Action: "member", UserID: 4, Role: "moderator"})
			if _, e := run(other, Command{Action: "settings", Name: "Changed", JoinPolicy: "open", Visibility: "public"}); !errors.Is(e, ErrForbidden) {
				t.Fatal("moderator changed rules", e)
			}
			topic := must(member, Command{Action: "topic", Kind: "question", Title: "如何开始？", Body: "<script>only text</script>"})
			reply := must(other, Command{Action: "reply", TopicID: topic.TopicID, Body: "从一篇开始"})
			if _, e := run(Actor{ID: 5}, Command{Action: "accept", TopicID: topic.TopicID, ReplyID: reply.ReplyID}); !errors.Is(e, ErrForbidden) {
				t.Fatal("outsider accepts", e)
			}
			must(member, Command{Action: "accept", TopicID: topic.TopicID, ReplyID: reply.ReplyID})
			yes := true
			must(other, Command{Action: "pin", TopicID: topic.TopicID, Enabled: &yes})
			must(other, Command{Action: "feature", TopicID: topic.TopicID, Enabled: &yes})
			must(other, Command{Action: "hide_reply", TopicID: topic.TopicID, ReplyID: reply.ReplyID, Enabled: &yes})
			c, _ = s.Get(t.Context(), "reading")
			if c.Topics[0].AcceptedID != 0 {
				t.Fatal("hidden answer remains accepted")
			}
			if _, e := run(member, Command{Action: "accept", TopicID: topic.TopicID, ReplyID: reply.ReplyID}); !errors.Is(e, ErrNotFound) {
				t.Fatal("accepted hidden answer", e)
			}
			second := must(member, Command{Action: "topic", Kind: "question", Title: "另一题", Body: "另一个问题"})
			if _, e := run(member, Command{Action: "accept", TopicID: second.TopicID, ReplyID: reply.ReplyID}); !errors.Is(e, ErrNotFound) {
				t.Fatal("cross-topic answer", e)
			}
			must(other, Command{Action: "lock", TopicID: topic.TopicID, Enabled: &yes})
			if _, e := run(member, Command{Action: "reply", TopicID: topic.TopicID, Body: "不应进入"}); !errors.Is(e, ErrForbidden) {
				t.Fatal("locked reply", e)
			}
			must(owner, Command{Action: "member", UserID: 3, Role: "banned"})
			if _, e := run(member, Command{Action: "join"}); !errors.Is(e, ErrForbidden) {
				t.Fatal("ban evaded", e)
			}
			if _, e := run(member, Command{Action: "leave"}); !errors.Is(e, ErrForbidden) {
				t.Fatal("ban erased", e)
			}
			if _, e := run(owner, Command{Action: "leave"}); !errors.Is(e, ErrForbidden) {
				t.Fatal("owner abandoned", e)
			}
			must(owner, Command{Action: "transfer", UserID: 4})
			must(owner, Command{Action: "leave"})
			c, _ = s.Get(t.Context(), "reading")
			if c.OwnerID != 4 || Role(c, owner) != "" {
				t.Fatal("transfer", c)
			}
			directory, _ := s.List(t.Context())
			if len(directory) != 1 || len(directory[0].Topics) != 0 || len(directory[0].Receipts) != 0 {
				t.Fatal("directory carries bodies")
			}
			// Concurrent writes using a shared version have one winner.
			base := c.Version
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for i := range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, e := s.Apply(t.Context(), "reading", other, Command{Action: "topic", Key: fmt.Sprintf("concurrent-%016d", i), Version: base, Kind: "discussion", Title: "并发", Body: "同一版本"}, now)
					errs <- e
				}()
			}
			wg.Wait()
			close(errs)
			wins := 0
			for e := range errs {
				if e == nil {
					wins++
				} else if !errors.Is(e, ErrConflict) {
					t.Fatal(e)
				}
			}
			if wins != 1 {
				t.Fatal("concurrent winners", wins)
			}
			c, _ = s.Get(t.Context(), "reading")
			cmd := Command{Action: "reply", Key: "retry-one-exact-key", Version: c.Version, TopicID: second.TopicID, Body: "只保存一次"}
			first, e := s.Apply(t.Context(), "reading", other, cmd, now)
			if e != nil {
				t.Fatal(e)
			}
			if disk {
				s, e = NewFileStore(path)
				if e != nil {
					t.Fatal(e)
				}
			}
			repeated, e := s.Apply(t.Context(), "reading", other, cmd, now)
			if e != nil || !repeated.Replay || repeated.ReplyID != first.ReplyID {
				t.Fatal("restart replay", repeated, e)
			}
			cmd.Body = "tamper"
			if _, e = s.Apply(t.Context(), "reading", other, cmd, now); !errors.Is(e, ErrConflict) {
				t.Fatal("key reused", e)
			}
			if disk {
				before, _ := s.Get(t.Context(), "reading")
				b, _ := os.ReadFile(path)
				if e = os.WriteFile(path, append(b, ' '), 0600); e != nil {
					t.Fatal(e)
				}
				if _, e = run(other, Command{Action: "join"}); !errors.Is(e, ErrChanged) {
					t.Fatal("external changes overwritten", e)
				}
				after, _ := s.Get(t.Context(), "reading")
				x, _ := json.Marshal(before)
				y, _ := json.Marshal(after)
				if string(x) != string(y) {
					t.Fatal("failed file mutation changed memory")
				}
			}
		})
	}
}
func TestCommunityDecodeAndFailure(t *testing.T) {
	for _, b := range []string{`{"schema":1,"schema":1,"circles":[]}`, `{"schema":2,"circles":[]}`, `{"schema":1,"circles":[],"unknown":true}`} {
		path := filepath.Join(t.TempDir(), "community.json")
		os.WriteFile(path, []byte(b), 0600)
		if _, e := NewFileStore(path); e == nil {
			t.Fatal("invalid accepted", b)
		}
	}
	path := filepath.Join(t.TempDir(), "blocked", "community.json")
	s, e := NewFileStore(path)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Dir(path), []byte("file blocks directory"), 0600)
	_, e = s.Apply(t.Context(), "fail", Actor{1, true}, Command{Action: "create", Key: "failed-write-request", Name: "Failure", JoinPolicy: "open", Visibility: "public", OwnerID: 1}, time.Now())
	if e == nil {
		t.Fatal("write must fail")
	}
	all, _ := s.List(t.Context())
	if len(all) != 0 {
		t.Fatal("failure leaked memory")
	}
}
