package commentorigin

import (
	"context"
	"errors"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
)

func TestDiagnosticsBeforeEnableAreCancelledByUninstall(t *testing.T) {
	s, m, _ := originFixture(t)
	if err := m.SetEnabled(plugin.CommentOrigin, false); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	s.lookup = func(ctx context.Context, _ Settings, _ string) (Location, error) {
		close(started)
		<-ctx.Done()
		return Location{}, ctx.Err()
	}
	go func() { _, err := s.Test(t.Context(), "8.8.8.8"); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("disabled installation could not run diagnostics")
	}
	if err := m.Uninstall(plugin.CommentOrigin); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("diagnostic was not cancelled: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("uninstall did not stop diagnostic request")
	}
}

func TestOldProviderErrorsDoNotSurviveSaveOrReinstall(t *testing.T) {
	s, m, _ := originFixture(t)
	s.lookup = func(context.Context, Settings, string) (Location, error) {
		return Location{}, errors.New("旧服务无法连接")
	}
	if err := s.Capture(1, "8.8.8.8", "Windows"); err != nil {
		t.Fatal(err)
	}
	s.process(t.Context(), <-s.queue)
	status, err := s.Status()
	if err != nil || status.LastError == "" {
		t.Fatalf("current error missing: %+v, %v", status, err)
	}
	config, _ := s.Settings()
	if _, err := s.Save(config, false); err != nil {
		t.Fatal(err)
	}
	status, err = s.Status()
	if err != nil || status.LastError != "" {
		t.Fatalf("stale config error: %+v, %v", status, err)
	}
	if err := m.Uninstall(plugin.CommentOrigin); err != nil {
		t.Fatal(err)
	}
	if err := m.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	status, err = s.Status()
	if err != nil || status.LastError != "" || status.Records != 0 {
		t.Fatalf("old installation error leaked: %+v, %v", status, err)
	}
}
