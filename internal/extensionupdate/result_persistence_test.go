package extensionupdate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
)

func TestTaskCompletionPersistenceAndRecovery(t *testing.T) {
	for _, kind := range []string{"plugin", "theme"} {
		for _, outcome := range []string{"succeeded", "failed", "cancelled"} {
			for _, writable := range []bool{false, true} {
				storage := "unwritable"
				if writable {
					storage = "writable"
				}
				t.Run(kind+"/"+outcome+"/"+storage, func(t *testing.T) {
					s, market, request := serviceFixture(t)
					if kind == "theme" {
						request = Request{Kind: kind, Action: "check", Channel: "stable"}
					}
					started := make(chan context.Context, 1)
					release := make(chan struct{})
					var releaseOnce sync.Once
					releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
					t.Cleanup(func() {
						releaseWorker()
						s.Close()
					})
					market.work = func(ctx context.Context, progress extension.Progress) error {
						progress.Report("downloading", 100, 100)
						started <- ctx
						<-release
						switch outcome {
						case "failed":
							return errors.New("fixture operation failed")
						case "cancelled":
							return ctx.Err()
						default:
							return nil
						}
					}
					job, err := s.Start(request)
					if err != nil {
						t.Fatal(err)
					}
					var running context.Context
					select {
					case running = <-started:
					case <-time.After(3 * time.Second):
						t.Fatal("task did not start")
					}
					if outcome == "cancelled" {
						if err := s.Cancel(kind, job.ID); err != nil {
							t.Fatal(err)
						}
						if !errors.Is(running.Err(), context.Canceled) {
							t.Fatal("cancellation did not reach the worker")
						}
					} else if running.Err() != nil {
						t.Fatal("task stopped before the final persistence failure", running.Err())
					}
					before, err := os.ReadFile(s.stateFile)
					if err != nil {
						t.Fatal(err)
					}
					if !writable {
						// Preserve the last durable state and make only the final
						// result write fail, without relying on OS permission bits.
						if err := os.Rename(s.stateFile, s.stateFile+".saved"); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(s.stateFile, 0700); err != nil {
							t.Fatal(err)
						}
					}
					releaseWorker()
					final := awaitJob(t, s, kind)
					s.wg.Wait()
					if final.ID != job.ID || final.Kind != kind || final.Action != request.Action || final.Slug != request.Slug || final.Target != request.Version {
						t.Fatal("completion lost task identity", final)
					}
					if writable {
						stored, err := extension.ReadJSON[Job](s.stateFile, 16<<10)
						if err != nil || stored != final || final.Phase != outcome {
							t.Fatalf("normal result changed: memory=%+v disk=%+v err=%v", final, stored, err)
						}
						return
					}
					if final.Phase != "failed" || !strings.Contains(final.Message, "结果无法保存") || !strings.Contains(final.Message, "核对当前版本") {
						t.Errorf("final write failure was hidden from the task reader: %+v", final)
					}
					if strings.Contains(final.Message, s.stateFile) || strings.Contains(final.Message, "保持原状") || strings.Contains(final.Message, "已中止更新") {
						t.Error("persistence error exposed a path or claimed the operation was undone", final.Message)
					}
					saved, err := os.ReadFile(s.stateFile + ".saved")
					if err != nil || !bytes.Equal(saved, before) {
						t.Fatal("failed final write changed the preserved durable state", err)
					}
					if err := os.Remove(s.stateFile); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(s.stateFile+".saved", s.stateFile); err != nil {
						t.Fatal(err)
					}
					// A later, explicit check can persist after storage recovers.
					// It must not inherit the previous task's storage error.
					market.work = func(context.Context, extension.Progress) error { return nil }
					if _, err := s.Start(Request{Kind: kind, Action: "check", Channel: "stable"}); err != nil {
						t.Fatal(err)
					}
					checked := awaitJob(t, s, kind)
					s.wg.Wait()
					if checked.Phase != "succeeded" || checked.ID == job.ID {
						t.Fatal("explicit check did not recover after storage was repaired", checked)
					}
					// Restore the interrupted receipt in this temporary fixture
					// to also exercise the independent process-restart path.
					if err := os.WriteFile(s.stateFile, before, 0600); err != nil {
						t.Fatal(err)
					}
					restarted, err := New(s.stateFile, market, s.plugins, s.themes)
					if err != nil {
						t.Fatal(err)
					}
					defer restarted.Close()
					if restored := restarted.Job(kind); restored.ID != job.ID || restored.Phase != "interrupted" {
						t.Fatal("restart lost the original unconfirmed task", restored)
					}
				})
			}
		}
	}
}
