package audit

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLedgerApplyPersistsValidatedHashChain(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "audit.jsonl")
	factPath := filepath.Join(directory, "users.json")
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("{\"schema\":1}\n")
	event := Event{
		Action: "user.created", ObjectType: "user", ObjectID: "1", Actor: "administrator",
		Metadata: map[string]any{"roles": []string{"administrator"}, "version": int64(1)},
	}
	if err := ledger.Apply("users", event, payload, func() error {
		return os.WriteFile(factPath, payload, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	records := ledger.Records()
	if len(records) != 2 || records[0].Phase != "prepare" || records[1].Phase != "commit" {
		t.Fatalf("records = %#v", records)
	}
	if records[0].Sequence != 1 || records[1].Sequence != 2 || records[1].PreviousHash != records[0].Hash {
		t.Fatalf("chain is not sequential: %#v", records)
	}
	if records[0].FactHash != Digest(payload) || records[0].Action != event.Action || records[0].Actor != event.Actor {
		t.Fatalf("unexpected prepare record: %#v", records[0])
	}
	if got := records[0].Metadata["version"]; got == nil || got != float64(1) {
		t.Fatalf("normalized metadata = %#v", records[0].Metadata)
	}
	if err := ledger.Ready(); err != nil {
		t.Fatalf("ready after commit: %v", err)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatalf("reload valid ledger: %v", err)
	}
	if got := reloaded.Records(); len(got) != 2 || got[1].Hash != records[1].Hash {
		t.Fatalf("reloaded records = %#v", got)
	}
	if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode = %v, err=%v", info.Mode().Perm(), err)
	}
}

func TestLedgerRecoveryCommitsMatchingFactAndAbortsMissingFact(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		writeFact bool
		wantPhase string
	}{
		{name: "matching fact", writeFact: true, wantPhase: "commit"},
		{name: "fact not replaced", writeFact: false, wantPhase: "abort"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			ledgerPath := filepath.Join(directory, "audit.jsonl")
			factPath := filepath.Join(directory, "comments.json")
			ledger, err := Open(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			payload := []byte("candidate fact\n")
			if _, err := ledger.Prepare("comments", Event{
				Action: "comment.status.approved", ObjectType: "comment", ObjectID: "7", Actor: "moderator",
				Metadata: map[string]any{"from": "pending", "to": "approved", "version": 2},
			}, Digest(payload)); err != nil {
				t.Fatal(err)
			}
			if test.writeFact {
				if err := os.WriteFile(factPath, payload, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			reloaded, err := Open(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := reloaded.ResolvePaths(map[string]string{"comments": factPath}); err != nil {
				t.Fatal(err)
			}
			records := reloaded.Records()
			if len(records) != 2 || records[1].Phase != test.wantPhase {
				t.Fatalf("records = %#v", records)
			}
			if err := reloaded.Ready(); err != nil {
				t.Fatalf("ledger remains pending: %v", err)
			}
		})
	}
}

func TestLedgerFactAndCommitFailuresRemainPending(t *testing.T) {
	t.Parallel()
	t.Run("fact write failure", func(t *testing.T) {
		directory := t.TempDir()
		ledger, err := Open(filepath.Join(directory, "audit.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		sentinel := errors.New("fact failed")
		err = ledger.Apply("navigation", testEvent(), []byte("candidate"), func() error { return sentinel })
		var applyErr *ApplyError
		if !errors.As(err, &applyErr) || applyErr.Phase != "fact" || !errors.Is(err, sentinel) {
			t.Fatalf("apply error = %v", err)
		}
		if !errors.Is(ledger.Ready(), ErrPending) || !ledger.Pending("navigation") {
			t.Fatalf("fact failure did not retain pending transaction: %v", ledger.Ready())
		}
	})

	t.Run("commit append failure", func(t *testing.T) {
		directory := t.TempDir()
		ledgerPath := filepath.Join(directory, "audit.jsonl")
		factPath := filepath.Join(directory, "navigation.json")
		ledger, err := Open(ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("committed fact")
		err = ledger.Apply("navigation", testEvent(), payload, func() error {
			if err := os.WriteFile(factPath, payload, 0o600); err != nil {
				return err
			}
			return os.Rename(ledgerPath, ledgerPath+".saved")
		})
		var applyErr *ApplyError
		if !errors.As(err, &applyErr) || applyErr.Phase != "commit" || !errors.Is(err, ErrFactCommitted) {
			t.Fatalf("apply error = %v", err)
		}
		if !errors.Is(ledger.Ready(), ErrFactCommitted) {
			t.Fatalf("ledger fault = %v", ledger.Ready())
		}
		if err := os.Rename(ledgerPath+".saved", ledgerPath); err != nil {
			t.Fatal(err)
		}
		reloaded, err := Open(ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := reloaded.ResolvePaths(map[string]string{"navigation": factPath}); err != nil {
			t.Fatal(err)
		}
		if records := reloaded.Records(); len(records) != 2 || records[1].Phase != "commit" {
			t.Fatalf("recovered records = %#v", records)
		}
	})
}

func TestLedgerRejectsSensitiveMetadataBeforePrepare(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"email", "author_email", "content_source", "password_hash", "session_token", "ip_address", "user_agent"} {
		t.Run(key, func(t *testing.T) {
			ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			event := testEvent()
			event.Metadata = map[string]any{key: "must-not-appear"}
			if _, err := ledger.Prepare("navigation", event, Digest([]byte("fact"))); err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Fatalf("sensitive metadata error = %v", err)
			}
			if records := ledger.Records(); len(records) != 0 {
				t.Fatalf("sensitive event reached ledger: %#v", records)
			}
		})
	}
}

func TestLedgerRejectsTamperTruncationAndDuplicateSequence(t *testing.T) {
	t.Parallel()
	create := func(t *testing.T) (string, []byte) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		ledger, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := ledger.Apply("navigation", testEvent(), []byte("fact"), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return path, payload
	}

	t.Run("tamper", func(t *testing.T) {
		path, payload := create(t)
		payload = []byte(strings.Replace(string(payload), "operator", "intruder", 1))
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil || !errors.Is(err, ErrCorrupt) {
			t.Fatalf("tamper error = %v", err)
		}
	})

	t.Run("truncated record", func(t *testing.T) {
		path, payload := create(t)
		if err := os.WriteFile(path, payload[:len(payload)-1], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil || !errors.Is(err, ErrCorrupt) {
			t.Fatalf("truncation error = %v", err)
		}
	})

	t.Run("duplicate sequence", func(t *testing.T) {
		path, payload := create(t)
		firstEnd := strings.IndexByte(string(payload), '\n') + 1
		payload = append(payload, payload[:firstEnd]...)
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil || !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "sequence") {
			t.Fatalf("duplicate sequence error = %v", err)
		}
	})
}

func TestLedgerRejectsSymlinkFileAndParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Creating symlinks requires a platform policy or developer mode. Run the
		// same assertions when this environment permits it, otherwise skip.
	}
	t.Run("file", func(t *testing.T) {
		directory := t.TempDir()
		realPath := filepath.Join(directory, "real.jsonl")
		if err := os.WriteFile(realPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		linkPath := filepath.Join(directory, "audit.jsonl")
		if err := os.Symlink(realPath, linkPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := Open(linkPath); err == nil {
			t.Fatal("accepted symlink ledger")
		}
	})
	t.Run("parent", func(t *testing.T) {
		directory := t.TempDir()
		realDirectory := filepath.Join(directory, "real")
		if err := os.Mkdir(realDirectory, 0o750); err != nil {
			t.Fatal(err)
		}
		linkDirectory := filepath.Join(directory, "linked")
		if err := os.Symlink(realDirectory, linkDirectory); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := Open(filepath.Join(linkDirectory, "audit.jsonl")); err == nil {
			t.Fatal("accepted symlink ledger parent")
		}
	})
}

func TestLedgerSerializesConcurrentFacts(t *testing.T) {
	ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan string, 2)
	release := make(chan struct{})
	var wait sync.WaitGroup
	errorsByFact := make(chan error, 2)
	for _, factKey := range []string{"users", "comments"} {
		factKey := factKey
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := ledger.Apply(factKey, Event{
				Action: factKey + ".updated", ObjectType: "storage_fact", ObjectID: factKey, Actor: "operator",
			}, []byte(factKey), func() error {
				entered <- factKey
				<-release
				return nil
			})
			errorsByFact <- err
		}()
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first fact writer did not start")
	}
	select {
	case second := <-entered:
		t.Fatalf("second fact writer entered before first commit: %s", second)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wait.Wait()
	close(errorsByFact)
	for err := range errorsByFact {
		if err != nil {
			t.Fatal(err)
		}
	}
	if records := ledger.Records(); len(records) != 4 {
		t.Fatalf("records = %#v", records)
	}
}

func TestLedgerReadyWaitsForActiveApply(t *testing.T) {
	ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	applyResult := make(chan error, 1)
	go func() {
		applyResult <- ledger.Apply("users", testEvent(), []byte("candidate"), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fact writer did not start")
	}
	readyResult := make(chan error, 1)
	go func() { readyResult <- ledger.Ready() }()
	select {
	case err := <-readyResult:
		t.Fatalf("readiness returned during active apply: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-applyResult; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readyResult:
		if err != nil {
			t.Fatalf("readiness after commit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness did not resume after commit")
	}
}

func TestLedgerResolvePathsWaitsForActiveApply(t *testing.T) {
	directory := t.TempDir()
	factPath := filepath.Join(directory, "users.json")
	if err := os.WriteFile(factPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger, err := Open(filepath.Join(directory, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.EnsureBaselinePath("users", factPath); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	applyResult := make(chan error, 1)
	go func() {
		payload := []byte("new")
		_, applyErr := ledger.ApplyFile("users", testEvent(), payload, factPath, func() error {
			close(entered)
			<-release
			return os.WriteFile(factPath, payload, 0o600)
		})
		applyResult <- applyErr
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fact writer did not start")
	}
	resolveResult := make(chan error, 1)
	go func() { resolveResult <- ledger.ResolvePaths(map[string]string{"users": factPath}) }()
	select {
	case err := <-resolveResult:
		t.Fatalf("recovery raced with active apply: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-applyResult; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-resolveResult:
		if err != nil {
			t.Fatalf("recovery after commit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery did not resume after commit")
	}
}

func TestLedgerBaselineWaitsForActiveApply(t *testing.T) {
	directory := t.TempDir()
	factPath := filepath.Join(directory, "comments.json")
	if err := os.WriteFile(factPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger, err := Open(filepath.Join(directory, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	applyResult := make(chan error, 1)
	go func() {
		payload := []byte("new")
		_, applyErr := ledger.ApplyFile("comments", testEvent(), payload, factPath, func() error {
			close(entered)
			<-release
			return os.WriteFile(factPath, payload, 0o600)
		})
		applyResult <- applyErr
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fact writer did not start")
	}
	baselineResult := make(chan error, 1)
	go func() { baselineResult <- ledger.EnsureBaselinePath("comments", factPath) }()
	select {
	case err := <-baselineResult:
		t.Fatalf("baseline raced with active apply: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-applyResult; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-baselineResult:
		if err != nil {
			t.Fatalf("baseline after commit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("baseline did not resume after commit")
	}
	if records := ledger.Records(); len(records) != 2 {
		t.Fatalf("baseline duplicated committed fact: %#v", records)
	}
}

func TestLedgerRejectsOversizedAndInvalidUTF8Records(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		payload := append([]byte(strings.Repeat("x", maxLineBytes+1)), '\n')
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil || !errors.Is(err, ErrCorrupt) {
			t.Fatalf("oversized record error = %v", err)
		}
	})
	t.Run("invalid UTF-8", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		if err := os.WriteFile(path, []byte{'{', 0xff, '}', '\n'}, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil || !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "UTF-8") {
			t.Fatalf("invalid UTF-8 error = %v", err)
		}
	})
}

func TestLedgerRejectsReplacementAfterOpen(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "audit.jsonl")
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	original := path + ".original"
	if err := os.Rename(path, original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err = ledger.Apply("users", Event{
		Action: "user.updated", ObjectType: "user", ObjectID: "1", Actor: "operator",
	}, []byte("fact"), func() error { return nil })
	if err == nil || !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("replacement error = %v", err)
	}
	if info, statErr := os.Stat(path); statErr != nil || info.Size() != 0 {
		t.Fatalf("replacement ledger was appended: size=%d, err=%v", info.Size(), statErr)
	}
}

func testEvent() Event {
	return Event{
		Action: "navigation.menu.update", ObjectType: "navigation_menu", ObjectID: "3", Actor: "operator",
		Metadata: map[string]any{"from_version": int64(1), "version": int64(2), "locations": []string{"primary"}},
	}
}
