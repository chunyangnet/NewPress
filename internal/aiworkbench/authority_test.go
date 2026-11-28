package aiworkbench

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestReportAuthorityPreservesState(t *testing.T) {
	for _, operation := range []string{"save", "delete"} {
		for _, durable := range []bool{false, true} {
			for _, denyAt := range []int{1, 2} {
				t.Run(operation+"/disk="+strconv.FormatBool(durable)+"/check="+strconv.Itoa(denyAt), func(t *testing.T) {
					now := time.Now()
					path := ""
					if durable {
						path = filepath.Join(t.TempDir(), "reports.json")
					}
					s, err := NewStore(path)
					if err != nil {
						t.Fatal(err)
					}
					original, candidate := testReport(1, now), testReport(1, now)
					if err := s.Save(original, now); err != nil {
						t.Fatal(err)
					}
					before := s.List(1, now)
					var disk []byte
					if durable {
						disk, err = os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
					}
					denied, checks := errors.New("authority revoked"), 0
					check := func() error {
						checks++
						if checks == denyAt {
							return denied
						}
						return nil
					}
					perform := func(check func() error) error {
						if operation == "save" {
							return s.SaveChecked(candidate, now, check)
						}
						return s.DeleteChecked(1, original.ID, now, check)
					}
					if err := perform(check); !errors.Is(err, denied) || !reflect.DeepEqual(before, s.List(1, now)) {
						t.Fatal("denied report mutation changed state", err, checks)
					}
					if durable {
						after, err := os.ReadFile(path)
						if err != nil || string(after) != string(disk) {
							t.Fatal("denied mutation changed disk", err)
						}
					}
					if err := perform(func() error { return nil }); err != nil {
						t.Fatal("authorized retry failed", err)
					}
					if durable {
						reopened, err := NewStore(path)
						if err != nil {
							t.Fatal(err)
						}
						got, _ := json.Marshal(reopened.List(1, now))
						want, _ := json.Marshal(s.List(1, now))
						if string(got) != string(want) {
							t.Fatal("authorized retry was not durable", err)
						}
					}
				})
			}
		}
	}
}

func TestPreviewAuthorityPreservesTokens(t *testing.T) {
	for _, operation := range []string{"put", "take"} {
		for _, denyAt := range []int{1, 2} {
			t.Run(operation+"/check="+strconv.Itoa(denyAt), func(t *testing.T) {
				now := time.Now()
				var s Previews
				original, err := s.Put(1, Preview{Task: "summary", Context: "original"}, now)
				if err != nil {
					t.Fatal(err)
				}
				denied, checks := errors.New("authority revoked"), 0
				check := func() error {
					checks++
					if checks == denyAt {
						return denied
					}
					return nil
				}
				var result Preview
				if operation == "put" {
					result, err = s.PutChecked(1, Preview{Task: "summary", Context: "replacement"}, now, check)
				} else {
					result, err = s.TakeChecked(1, original.Token, now, check)
				}
				if !errors.Is(err, denied) || result.Token != "" || result.Context != "" || len(s.items) != 1 {
					t.Fatal("denied preview mutated/returned data", err, result, len(s.items))
				}
				got, err := s.TakeChecked(1, original.Token, now, func() error { return nil })
				if err != nil || got != original {
					t.Fatal("denied request invalidated previous token", err)
				}
				if _, err := s.Take(1, original.Token, now); !errors.Is(err, ErrPreview) {
					t.Fatal("authorized retry token replayed", err)
				}
			})
		}
	}
}

func TestWorkbenchAuthorityAfterLockWait(t *testing.T) {
	for _, operation := range []string{"save", "delete", "put", "take"} {
		t.Run(operation, func(t *testing.T) {
			now := time.Now()
			s, _ := NewStore("")
			report := testReport(1, now)
			if err := s.Save(report, now); err != nil {
				t.Fatal(err)
			}
			var previews Previews
			preview, err := previews.Put(1, Preview{Task: "summary", Context: "original"}, now)
			if err != nil {
				t.Fatal(err)
			}
			var revoked atomic.Bool
			denied := errors.New("revoked during lock wait")
			check := func() error {
				if revoked.Load() {
					return denied
				}
				return nil
			}
			s.mu.Lock()
			previews.mu.Lock()
			entered, done := make(chan struct{}), make(chan error, 1)
			go func() {
				close(entered)
				var err error
				switch operation {
				case "save":
					err = s.SaveChecked(testReport(1, now), now, check)
				case "delete":
					err = s.DeleteChecked(1, report.ID, now, check)
				case "put":
					_, err = previews.PutChecked(1, Preview{Task: "summary", Context: "replacement"}, now, check)
				case "take":
					_, err = previews.TakeChecked(1, preview.Token, now, check)
				}
				done <- err
			}()
			<-entered
			revoked.Store(true)
			s.mu.Unlock()
			previews.mu.Unlock()
			select {
			case err := <-done:
				if !errors.Is(err, denied) {
					t.Fatal("stale authority used after lock", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("checked operation did not return")
			}
			if len(s.List(1, now)) != 1 {
				t.Fatal("report changed")
			}
			if got, err := previews.Take(1, preview.Token, now); err != nil || got != preview {
				t.Fatal("preview changed", err)
			}
		})
	}
}
