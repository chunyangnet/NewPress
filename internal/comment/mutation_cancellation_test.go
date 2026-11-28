package comment

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"newpress.local/newpress/internal/audit"
)

func commentCancelledMutation(ctx context.Context, s *FileStore, c Comment, operation string) (bool, error) {
	m := Mutation{ExpectedVersion: c.Version, Actor: "moderator"}
	var value Comment
	var err error
	switch operation {
	case "create":
		value, err = s.Create(ctx, CreateInput{PostID: c.PostID, AuthorName: "New", AuthorEmail: "new@example.test", ContentSource: "New body", Status: StatusApproved})
	case "create-once":
		v, err := s.CreateOnce(ctx, submissionFixture("cancel-mutation"))
		return v == (SubmissionResult{}), err
	case "edit":
		value, err = s.Edit(ctx, c.ID, m, EditInput{AuthorName: c.AuthorName, AuthorEmail: c.AuthorEmail, ContentSource: "Cancelled edit"})
	case "restore":
		value, err = s.RestoreRevision(ctx, c.ID, 1, m)
	case "reply":
		value, err = s.Reply(ctx, ReplyInput{ParentID: c.ID, ExpectedParentVersion: c.Version, UserID: 7, AuthorName: "Staff", AuthorEmail: "staff@example.test", ContentSource: "Cancelled reply", Actor: m.Actor})
	case "status":
		value, err = s.ChangeStatus(ctx, c.ID, m, StatusSpam)
	case "bulk":
		v, err := s.BulkChangeStatus(ctx, BulkStatusChange{Items: []BulkMutation{{ID: c.ID, ExpectedVersion: c.Version}}, Status: StatusSpam, Actor: m.Actor})
		return v == nil, err
	case "delete":
		return true, s.Delete(ctx, c.ID, m)
	case "ack":
		return true, s.AckActivity(ctx, s.activities[0].Key)
	}
	return value == (Comment{}), err
}

func TestCommentMutationCancellation(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, operation := range []string{"create", "create-once", "edit", "restore", "reply", "status", "bulk", "delete", "ack"} {
			for _, boundary := range []string{"entry", "lock", "prepared", "encoded"} {
				t.Run(map[bool]string{false: "memory", true: "file"}[disk]+"/"+operation+"/"+boundary, func(t *testing.T) {
					if !disk && boundary == "encoded" {
						t.Skip("memory store has no encoding boundary")
					}
					path, mode := "", "volatile"
					if disk {
						mode = "file"
					}
					s, path, c := unicodeCommentFixture(t, mode)
					var ledger *audit.Ledger
					if disk {
						var err error
						ledger, err = audit.Open(filepath.Join(filepath.Dir(path), "mutation-audit.jsonl"))
						if err != nil {
							t.Fatal(err)
						}
						s.audit = ledger
						// Establish a matching audited baseline before attempting cancellation.
						c, err = s.ChangeStatus(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version, Actor: "moderator"}, c.Status)
						if err != nil {
							t.Fatal(err)
						}
					}
					before := commentWriteState(t, s)
					var document []byte
					var records []audit.Record
					if disk {
						var err error
						document, err = os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						records = ledger.Records()
					}
					parent, cancel := context.WithCancel(t.Context())
					defer cancel()
					var ctx context.Context = parent
					switch boundary {
					case "entry":
						cancel()
					case "lock":
						observed := &stateObservedContext{Context: parent, entered: make(chan struct{})}
						ctx = observed
						s.mu.Lock()
						go func() {
							select {
							case <-observed.entered:
							case <-time.After(time.Second):
							}
							cancel()
							s.mu.Unlock()
						}()
					case "prepared":
						at := 3
						ctx = &commentReadCancelContext{Context: parent, cancel: cancel, at: at}
					case "encoded":
						at := 6
						if operation == "ack" {
							at = 4
						}
						ctx = &commentReadCancelContext{Context: parent, cancel: cancel, at: at}
					}
					empty, err := commentCancelledMutation(ctx, s, c, operation)
					if !errors.Is(err, context.Canceled) || !empty {
						t.Errorf("cancelled mutation returned %v, empty=%v", err, empty)
					}
					if !bytes.Equal(before, commentWriteState(t, s)) {
						t.Error("cancelled mutation changed state, revisions, events or receipts")
					}
					if disk {
						after, err := os.ReadFile(path)
						if err != nil || !bytes.Equal(document, after) {
							t.Error("cancelled mutation changed disk", err)
						}
						if !reflect.DeepEqual(records, ledger.Records()) {
							t.Error("cancelled mutation appended audit records")
						}
						reopened, err := NewFileStore(path, false, ledger)
						if err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(before, commentWriteState(t, reopened)) {
							t.Error("cancelled mutation changed reopened state")
						}
					}
				})
			}
		}
	}
}

// Cancel immediately after observing a successful final commit check. This
// models the unavoidable window after that check without timing disk writes.
type commentCancelAfterGate struct {
	context.Context
	cancel    context.CancelFunc
	at, calls int
}

func (c *commentCancelAfterGate) Err() error {
	err := c.Context.Err()
	c.calls++
	if c.calls == c.at {
		c.cancel()
	}
	return err
}

func TestCommentMutationCommittedCancellation(t *testing.T) {
	for _, operation := range []string{"create-once", "edit", "reply", "bulk", "delete", "ack"} {
		for _, disk := range []bool{false, true} {
			t.Run(operation+"/"+map[bool]string{false: "memory", true: "file"}[disk], func(t *testing.T) {
				mode := "volatile"
				if disk {
					mode = "file"
				}
				s, path, c := unicodeCommentFixture(t, mode)
				before := commentWriteState(t, s)
				parent, cancel := context.WithCancel(t.Context())
				defer cancel()
				at := 5
				if disk {
					at = 6
				}
				if operation == "ack" {
					at = 3
					if disk {
						at = 4
					}
				}
				ctx := &commentCancelAfterGate{Context: parent, cancel: cancel, at: at}
				_, err := commentCancelledMutation(ctx, s, c, operation)
				if err != nil || !errors.Is(parent.Err(), context.Canceled) {
					t.Fatal("commit gate did not preserve result", ctx.calls, err, parent.Err())
				}
				after := commentWriteState(t, s)
				if bytes.Equal(before, after) {
					t.Fatal("successful commit was discarded")
				}
				if disk {
					reopened, err := NewFileStore(path, false)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(after, commentWriteState(t, reopened)) {
						t.Fatal("committed memory and disk diverged")
					}
				}
				if operation == "create-once" {
					receipt, err := s.CreateOnce(t.Context(), submissionFixture("cancel-mutation"))
					if err != nil || !receipt.Replayed || !bytes.Equal(after, commentWriteState(t, s)) {
						t.Fatal("committed receipt duplicated a comment", receipt, err)
					}
				}
			})
		}
	}
}
