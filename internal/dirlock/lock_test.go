package dirlock

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestExclusiveOwnershipAcrossProcesses(t *testing.T) {
	if directory := os.Getenv("NEWPRESS_TEST_LOCK_HELPER"); directory != "" {
		set, err := Acquire(directory)
		if errors.Is(err, ErrBusy) {
			os.Exit(23)
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = set.Close()
		return
	}
	directory := t.TempDir()
	set, err := Acquire(directory, directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	if second, err := Acquire(directory); !errors.Is(err, ErrBusy) {
		_ = second.Close()
		t.Fatalf("second owner = %v", err)
	}
	runChild := func() error {
		command := exec.Command(os.Args[0], "-test.run=^TestExclusiveOwnershipAcrossProcesses$")
		command.Env = append(os.Environ(), "NEWPRESS_TEST_LOCK_HELPER="+directory)
		return command.Run()
	}
	var exit *exec.ExitError
	if err := runChild(); !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("other process did not observe lock: %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runChild(); err != nil {
		t.Fatalf("closed owner retained lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, FileName)); err != nil {
		t.Fatalf("permanent lock file removed: %v", err)
	}
}

func TestProcessExitReleasesLockWithoutDeletingItsFile(t *testing.T) {
	if directory := os.Getenv("NEWPRESS_TEST_LOCK_CRASH_HELPER"); directory != "" {
		set, err := Acquire(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer set.Close()
		_, _ = os.Stdout.WriteString("locked\n")
		time.Sleep(time.Minute)
		return
	}
	directory := t.TempDir()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestProcessExitReleasesLockWithoutDeletingItsFile$")
	command.Env = append(os.Environ(), "NEWPRESS_TEST_LOCK_CRASH_HELPER="+directory)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("lock helper startup = %q, %v", line, err)
	}
	if set, err := Acquire(directory); !errors.Is(err, ErrBusy) {
		_ = set.Close()
		t.Fatalf("child did not retain ownership: %v", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	set, err := Acquire(directory)
	if err != nil {
		t.Fatalf("OS did not release terminated process lock: %v", err)
	}
	_ = set.Close()
}

func TestFailedSetReleasesEarlierLocks(t *testing.T) {
	root := t.TempDir()
	first, last := filepath.Join(root, "a"), filepath.Join(root, "z")
	owner, err := Acquire(last)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := Acquire(first, last); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected busy set: %v", err)
	}
	available, err := Acquire(first)
	if err != nil {
		t.Fatalf("failed set leaked first lock: %v", err)
	}
	_ = available.Close()
}
