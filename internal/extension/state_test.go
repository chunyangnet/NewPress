package extension

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func transactionFixture(t *testing.T) (string, *Transaction) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sample"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample", "content"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	tx, err := Begin(root, "sample")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tx.Stage, "content"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, tx
}

func TestRecoveryAtEveryDirectorySwitchBoundary(t *testing.T) {
	for _, phase := range []string{"prepared", "detached", "replaced", "committed"} {
		t.Run(phase, func(t *testing.T) {
			root, tx := transactionFixture(t)
			target := filepath.Join(root, "sample")
			if phase != "prepared" {
				if err := os.Rename(target, filepath.Join(tx.dir, "old")); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "replaced" || phase == "committed" {
				if err := os.Rename(tx.Stage, target); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "committed" {
				if err := WriteJSON(filepath.Join(tx.dir, "committed.json"), journal{1, "sample"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := Recover(root); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(target, "content"))
			want := "old"
			if phase == "committed" {
				want = "new"
			}
			if err != nil || string(got) != want {
				t.Fatalf("got %q, %v; want %q", got, err, want)
			}
			if _, err := os.Stat(tx.dir); !os.IsNotExist(err) {
				t.Fatal("transaction left behind")
			}
			if err := Recover(root); err != nil {
				t.Fatal("recovery is not idempotent", err)
			}
		})
	}
}

func TestMalformedRecoveryDoesNotDeleteTheOriginal(t *testing.T) {
	root, tx := transactionFixture(t)
	if err := os.Rename(filepath.Join(root, "sample"), filepath.Join(tx.dir, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tx.dir, "transaction.json"), []byte(`{"schema":1,"slug":"../escape"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Recover(root); err == nil {
		t.Fatal("corrupt recovery metadata accepted")
	}
	old, err := os.ReadFile(filepath.Join(tx.dir, "old", "content"))
	if err != nil || string(old) != "old" {
		t.Fatal("recovery deleted original", err)
	}
}

func TestCommitAndRollbackSnapshotIntegrity(t *testing.T) {
	root, tx := transactionFixture(t)
	old := filepath.Join(root, "sample")
	if err := CopyTree(t.Context(), old, filepath.Join(tx.Stage, RollbackDir)); err != nil {
		t.Fatal(err)
	}
	if err := Record(old, tx.Stage, "1.0.0", "1.1.0", "update", ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRollback(old); err != nil {
		t.Fatal(err)
	}
	state, err := State(old, "1.1.0")
	if err != nil || state.Previous != "1.0.0" || len(state.History) != 1 {
		t.Fatal(state, err)
	}
	if err := os.WriteFile(filepath.Join(old, RollbackDir, "content"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRollback(old); err == nil {
		t.Fatal("tampered rollback snapshot accepted")
	}
}

func TestCancelledSnapshotLeavesSourceUntouched(t *testing.T) {
	root, tx := transactionFixture(t)
	defer tx.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := CopyTree(ctx, filepath.Join(root, "sample"), filepath.Join(tx.Stage, RollbackDir)); err == nil {
		t.Fatal("cancelled copy succeeded")
	}
	old, err := os.ReadFile(filepath.Join(root, "sample", "content"))
	if err != nil || string(old) != "old" {
		t.Fatal("source changed", err)
	}
}

func TestTransactionsRejectLinks(t *testing.T) {
	root, tx := transactionFixture(t)
	defer tx.Close()
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tx.Stage, "linked")); err != nil {
		t.Skip("symlink unavailable:", err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("linked update file accepted")
	}
	got, err := os.ReadFile(filepath.Join(root, "sample", "content"))
	if err != nil || string(got) != "old" {
		t.Fatal("unsafe stage replaced original")
	}
}
