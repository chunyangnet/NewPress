package community

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCircleSnapshotDoesNotAliasNestedState(t *testing.T) {
	store, err := NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	owner, member := Actor{ID: 1, Admin: true}, Actor{ID: 2}
	version, sequence := int64(0), 0
	apply := func(actor Actor, command Command) Result {
		t.Helper()
		sequence++
		command.Key, command.Version = fmt.Sprintf("snapshot-command-%016d", sequence), version
		result, err := store.Apply(t.Context(), "snapshot", actor, command, now)
		if err != nil {
			t.Fatal(command.Action, err)
		}
		version = result.Version
		return result
	}
	apply(owner, Command{Action: "create", Name: "Snapshot", JoinPolicy: "open", Visibility: "members", OwnerID: owner.ID})
	apply(member, Command{Action: "join"})
	files, err := NewFiles("")
	if err != nil {
		t.Fatal(err)
	}
	attachments := make([]string, 2)
	for i := range attachments {
		file, err := files.Stage(t.Context(), "snapshot", fmt.Sprintf("snapshot-file-%016d", i), member.ID, "note.txt", strings.NewReader(fmt.Sprint("note ", i)))
		if err != nil {
			t.Fatal(err)
		}
		attachments[i] = apply(member, Command{Action: "add_attachment", Attachment: &file}).AttachmentID
	}
	topic := apply(member, Command{Action: "topic", Title: "Question", Body: "@reader a topic", Kind: "question", Attachments: attachments[:1]})
	apply(member, Command{Action: "reply", TopicID: topic.TopicID, Body: "@reader a reply", Attachments: attachments[1:]})
	apply(member, Command{Action: "report", TopicID: topic.TopicID, Category: "spam", Reason: "Check this topic"})
	want, err := store.Get(t.Context(), "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	got.Members[0].Role = "moderator"
	got.Topics[0].Title = "changed"
	got.Topics[0].Attachments[0] = "changed"
	got.Topics[0].Replies[0].Body = "changed"
	got.Topics[0].Replies[0].Attachments[0] = "changed"
	got.Receipts[0].Result.Version = 999
	got.Attachments[0].Name = "changed"
	got.Reports[0].Reason = "changed"
	got.Changes[0].Action = "changed"
	mentions, recipients := false, false
	for i := range got.Activities {
		if len(got.Activities[i].Mentions) > 0 {
			got.Activities[i].Mentions[0] = "changed"
			mentions = true
		}
		if len(got.Activities[i].Recipients) > 0 {
			got.Activities[i].Recipients[0] = 999
			recipients = true
		}
	}
	if !mentions || !recipients {
		t.Fatal("fixture must include nested activity mentions and recipients")
	}
	again, err := store.Get(t.Context(), "snapshot")
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatal("mutating a snapshot changed stored community state", err)
	}
}

func TestCircleFailedEditPreservesStoredSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "community.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := Actor{ID: 1, Admin: true}
	now := time.Now().UTC()
	var version int64
	for i, command := range []Command{
		{Action: "create", Name: "Snapshot", JoinPolicy: "open", Visibility: "public", OwnerID: owner.ID},
		{Action: "topic", Title: "Original title", Body: "Original body", Kind: "question"},
		{Action: "reply", TopicID: 1, Body: "Original answer"},
		{Action: "accept", TopicID: 1, ReplyID: 1},
	} {
		command.Key, command.Version = fmt.Sprintf("snapshot-setup-%016d", i), version
		result, err := store.Apply(t.Context(), "snapshot", owner, command, now)
		if err != nil {
			t.Fatal(command.Action, err)
		}
		version = result.Version
	}
	want, err := store.Get(t.Context(), "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	for i, command := range []Command{
		{Action: "edit_topic", TopicID: 1, Title: "Changed title", Body: "Changed body", Format: "invalid"},
		{Action: "edit_reply", TopicID: 1, ReplyID: 1, Body: "Changed answer", Format: "invalid"},
	} {
		command.Key, command.Version = fmt.Sprintf("snapshot-rejected-%016d", i), version
		if _, err := store.Apply(t.Context(), "snapshot", owner, command, now.Add(time.Minute)); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid format must reject the edited candidate", err)
		}
		got, err := store.Get(t.Context(), "snapshot")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("rejected edit changed stored state", command.Action, err)
		}
	}
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(t.Context(), "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	before, beforeErr := json.Marshal(want)
	after, afterErr := json.Marshal(got)
	if beforeErr != nil || afterErr != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected edit changed persisted state", beforeErr, afterErr)
	}
}

func BenchmarkCircleSnapshotRead(b *testing.B) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	c := Circle{Slug: "snapshot", Name: "Snapshot", JoinPolicy: "open", Visibility: "public", OwnerID: 1,
		Version: 1, CreatedAt: now, UpdatedAt: now, NextTopic: 501, NextReply: 2501,
		Members: []Member{}, Receipts: []Receipt{}}
	body := strings.TrimSpace(strings.Repeat("discussion text ", 40))
	for i := range 500 {
		topic := Topic{ID: int64(i + 1), AuthorID: 1, Title: fmt.Sprintf("Topic %d", i), Body: body, Kind: "discussion", CreatedAt: now}
		for j := range 5 {
			topic.Replies = append(topic.Replies, Reply{ID: int64(i*5 + j + 1), AuthorID: 1, Body: body, CreatedAt: now})
		}
		c.Topics = append(c.Topics, topic)
	}
	if err := Validate(c); err != nil {
		b.Fatal(err)
	}
	encoded, err := json.Marshal(c)
	if err != nil || len(encoded) > MaxCircleBytes {
		b.Fatal("fixture exceeds real circle capacity", len(encoded), err)
	}
	store, err := NewFileStore("")
	if err != nil {
		b.Fatal(err)
	}
	store.circles[c.Slug] = c
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		got, err := store.Get(context.Background(), c.Slug)
		if err != nil || len(got.Topics) != 500 {
			b.Fatal(err)
		}
	}
}
