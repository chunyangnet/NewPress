package questions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestQuestionLifecycle(t *testing.T) {
	s := Empty()
	now := time.Now().UTC()
	seq := 0
	apply := func(actor Actor, c Command) Result {
		t.Helper()
		seq++
		c.Key = fmt.Sprintf("command-%08d", seq)
		id := int64(1)
		if c.Action == "create" {
			id = 0
		} else {
			c.Version = s.Items[0].Version
		}
		next, r, e := Apply(s, id, actor, c, now)
		if e != nil {
			t.Fatal(c.Action, e)
		}
		s = next
		return r
	}
	owner, reader, admin := Actor{ID: 1}, Actor{ID: 2}, Actor{ID: 3, Admin: true}
	apply(owner, Command{Action: "create", Title: "如何测试全站问答？", Body: "不加入任何圈子。", Tags: []string{"测试"}})
	answer := apply(reader, Command{Action: "answer", Body: "使用独立问答入口。"})
	apply(owner, Command{Action: "note", AnswerID: answer.AnswerID, Body: "可以补充细节吗？"})
	apply(owner, Command{Action: "accept", AnswerID: answer.AnswerID})
	if s.Items[0].AcceptedID != 1 {
		t.Fatal("not accepted")
	}
	apply(reader, Command{Action: "edit_answer", AnswerID: 1, Body: "通过顶部去提问按钮进入。"})
	if s.Items[0].AcceptedID != 0 {
		t.Fatal("editing must clear acceptance")
	}
	apply(owner, Command{Action: "accept", AnswerID: 1})
	apply(admin, Command{Action: "hide_answer", AnswerID: 1, Enabled: true})
	if s.Items[0].AcceptedID != 0 {
		t.Fatal("hidden answer remains accepted")
	}
	apply(admin, Command{Action: "hide_answer", AnswerID: 1, Enabled: false})
	apply(reader, Command{Action: "report", Body: "需要核实内容", AnswerID: 1})
	apply(admin, Command{Action: "resolve_report", ReportID: 1})
	if !s.Items[0].Reports[0].Resolved {
		t.Fatal("report not resolved")
	}
	apply(admin, Command{Action: "lock", Enabled: true})
	_, _, err := Apply(s, 1, reader, Command{Action: "answer", Key: "locked-answer", Version: s.Items[0].Version, Body: "不应发布"}, now)
	if !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	if _, err = Decode(b); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionsAndReplay(t *testing.T) {
	now := time.Now()
	c := Command{Action: "create", Key: "create-replay", Title: "Question", Body: "Details"}
	s, r, e := Apply(Empty(), 0, Actor{ID: 1}, c, now)
	if e != nil {
		t.Fatal(e)
	}
	replay, rr, e := Apply(s, 0, Actor{ID: 1}, c, now)
	if e != nil || !rr.Replay || len(replay.Items) != 1 || rr.ID != r.ID {
		t.Fatal(rr, e)
	}
	c.Body = "Changed"
	if _, _, e = Apply(s, 0, Actor{ID: 1}, c, now); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		actor Actor
		cmd   Command
		want  error
	}{
		{Actor{}, Command{Action: "answer", Version: 1, Body: "text"}, ErrForbidden},
		{Actor{ID: 2}, Command{Action: "edit", Version: 1, Title: "x", Body: "y"}, ErrForbidden},
		{Actor{ID: 2}, Command{Action: "accept", Version: 1}, ErrForbidden},
		{Actor{ID: 2}, Command{Action: "hide", Version: 1, Enabled: true}, ErrForbidden},
		{Actor{ID: 2}, Command{Action: "answer", Version: 0, Body: "text"}, ErrConflict},
	} {
		tc.cmd.Key = "permission-check"
		_, _, err := Apply(s, 1, tc.actor, tc.cmd, now)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", tc.cmd.Action, err)
		}
	}
}

func TestFilePersistenceAndOutbox(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "questions.json")
	store, e := NewFileStore(path)
	if e != nil {
		t.Fatal(e)
	}
	_, e = store.Apply(ctx, 0, Actor{ID: 1}, Command{Action: "create", Key: "persist-create", Title: "Question", Body: "Body"}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	_, e = store.Apply(ctx, 1, Actor{ID: 2}, Command{Action: "answer", Key: "persist-answer", Version: 1, Body: "Answer"}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	reopened, e := NewFileStore(path)
	if e != nil {
		t.Fatal(e)
	}
	state, e := reopened.Snapshot(ctx)
	if e != nil || len(state.Items[0].Answers) != 1 || len(state.Events) != 1 {
		t.Fatal(state, e)
	}
	if e = reopened.AckActivity(ctx, state.Events[0].Key); e != nil {
		t.Fatal(e)
	}
	state, e = reopened.Snapshot(ctx)
	if e != nil || len(state.Events) != 0 {
		t.Fatal(e)
	}
	if e = store.Ready(ctx); e == nil {
		t.Fatal("external replacement not detected")
	}
}
