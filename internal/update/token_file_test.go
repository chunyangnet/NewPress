package update

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGiteeTokenFileRejectsOversizedInput(t *testing.T) {
	t.Setenv("NEWPRESS_GITEE_TOKEN", "")
	const limit = 514
	token := strings.Repeat("t", 512)
	short := "file-token-fixture"
	for _, test := range []struct {
		name, body string
	}{
		{"hidden suffix after CRLF", token + "\r\n" + "hidden-suffix"},
		{"excess whitespace", token + "\r\n "},
		{"padded short token", short + strings.Repeat(" ", limit-len(short)) + "hidden-suffix"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := managerFixture(t)
			if err := writeJSON(filepath.Join(manager.root, "source.json"), testSource()); err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(t.TempDir(), "private-token.txt")
			if err := os.WriteFile(filename, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NEWPRESS_GITEE_TOKEN_FILE", filename)
			source, environment, err := loadSource(manager.root)
			if err == nil {
				t.Error("oversized token file was accepted after truncation")
			} else {
				if source != (Source{}) || environment {
					t.Error("failed token file read returned a usable source")
				}
				if strings.Contains(err.Error(), filename) || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), short) {
					t.Error("token file error disclosed credentials or the private path")
				}
			}
			if got, err := os.ReadFile(filename); err != nil || string(got) != test.body {
				t.Errorf("token file changed during reading: %v", err)
			}
		})
	}
}

func TestGiteeTokenFileAcceptsCompleteBoundaryValues(t *testing.T) {
	t.Setenv("NEWPRESS_GITEE_TOKEN", "")
	for _, test := range []struct {
		name, token, body string
	}{
		{"plain token", "file-token-fixture", "file-token-fixture"},
		{"LF", "file-token-fixture", "file-token-fixture\n"},
		{"CRLF", "file-token-fixture", "file-token-fixture\r\n"},
		{"surrounding whitespace", "file-token-fixture", " \tfile-token-fixture\r\n"},
		{"maximum token", strings.Repeat("t", 512), strings.Repeat("t", 512)},
		{"maximum token with LF", strings.Repeat("t", 512), strings.Repeat("t", 512) + "\n"},
		{"maximum token with CRLF", strings.Repeat("t", 512), strings.Repeat("t", 512) + "\r\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := managerFixture(t)
			filename := filepath.Join(t.TempDir(), "private-token.txt")
			if err := os.WriteFile(filename, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NEWPRESS_GITEE_TOKEN_FILE", filename)
			input := testSource()
			input.Token = ""
			if err := manager.Configure(input, false); err != nil {
				t.Fatal(err)
			}
			loaded, environment, err := loadSource(manager.root)
			if err != nil || !environment || loaded.Token != test.token || loaded.validate() != nil {
				t.Fatalf("valid token file failed to load: %v", err)
			}
			view, err := manager.Snapshot()
			if err != nil || !view.Configured || !view.TokenSet || !view.EnvironmentToken || view.Source.Token != "" {
				t.Fatalf("token file was not projected as a redacted environment credential: %v", err)
			}
			body, err := os.ReadFile(filepath.Join(manager.root, "source.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stored Source
			if err := json.Unmarshal(body, &stored); err != nil || stored.Token != "" || bytes.Contains(body, []byte(test.token)) {
				t.Error("environment file token was copied into saved source settings")
			}
			if got, err := os.ReadFile(filename); err != nil || string(got) != test.body {
				t.Errorf("valid token file changed: %v", err)
			}
		})
	}
}

func TestInvalidTokenFileBlocksUpdateOperations(t *testing.T) {
	t.Setenv("NEWPRESS_GITEE_TOKEN", "")
	t.Setenv("NEWPRESS_GITEE_TOKEN_FILE", "")
	for _, operation := range []string{"snapshot", "configure", "manual check"} {
		t.Run(operation, func(t *testing.T) {
			manager, hits := discoveryProbe(t)
			sourcePath := filepath.Join(manager.root, "source.json")
			original, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			// Its readable prefix looks exactly like a valid token to the old reader.
			body := testSource().Token + strings.Repeat(" ", 514-len(testSource().Token)) + "hidden-suffix"
			filename := filepath.Join(t.TempDir(), "private-token.txt")
			if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NEWPRESS_GITEE_TOKEN_FILE", filename)
			switch operation {
			case "snapshot":
				if _, err := manager.Snapshot(); err == nil {
					t.Error("snapshot accepted a truncated token file")
				}
			case "configure":
				next := testSource()
				next.Ref = "other-branch"
				next.Token = ""
				if err := manager.Configure(next, false); err == nil {
					t.Error("source settings were saved using a truncated token file")
				}
			case "manual check":
				if err := manager.Check(); err != nil {
					t.Fatal(err)
				}
				manager.wg.Wait()
				state, err := manager.state()
				if err != nil || state.Phase != "failed" {
					t.Errorf("manual check did not fail for the invalid token file: phase=%s, %v", state.Phase, err)
				}
				for _, log := range state.Logs {
					if strings.Contains(log.Message, testSource().Token) || strings.Contains(log.Message, filename) {
						t.Error("manual check log disclosed credentials or the private path")
					}
				}
			}
			if hits.Load() != 0 {
				t.Error("invalid token file was used for a network request")
			}
			if after, err := os.ReadFile(sourcePath); err != nil || !bytes.Equal(after, original) {
				t.Errorf("invalid token file changed saved source settings: %v", err)
			}
			if _, err := os.Stat(filepath.Join(manager.root, "available.json")); !os.IsNotExist(err) {
				t.Errorf("invalid token file produced an installable candidate: %v", err)
			}
			if after, err := os.ReadFile(filename); err != nil || string(after) != body {
				t.Errorf("invalid token file changed: %v", err)
			}
		})
	}
}

func TestGiteeTokenFileAndDirectTokenRemainExclusive(t *testing.T) {
	t.Setenv("NEWPRESS_GITEE_TOKEN", "direct-token-fixture")
	filename := filepath.Join(t.TempDir(), "private-token.txt")
	if err := os.WriteFile(filename, []byte("file-token-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWPRESS_GITEE_TOKEN_FILE", filename)
	if _, _, err := loadSource(t.TempDir()); err == nil {
		t.Error("direct token and token file were accepted together")
	}
}
