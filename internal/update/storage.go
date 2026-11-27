package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

// Readers may overlap progress publication. On Windows, even a short open
// handle can prevent replacement; serialize local access and retry the atomic
// rename for readers in the other serving/supervisor process.
var storageMu sync.RWMutex

const (
	maxUpdateRecordBytes   = 256 << 10
	maxGiteeTokenFileBytes = 514 // A 512-byte token plus CRLF.
)

func Directory(dataDir string) string {
	if configured := os.Getenv("NEWPRESS_UPDATE_DIR"); configured != "" {
		return configured
	}
	return filepath.Clean(dataDir) + "-updates"
}

// Updates and backup archives must live outside each mutable site tree.
func ValidateDirectory(root string, siteDirs ...string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	for _, dir := range siteDirs {
		if dir == "" {
			continue
		}
		other, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		a, b := absolute, other
		if runtime.GOOS == "windows" {
			a, b = strings.ToLower(a), strings.ToLower(b)
		}
		for _, pair := range [][2]string{{a, b}, {b, a}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))) {
				return errors.New("更新目录必须独立于数据、初始主题和插件目录")
			}
		}
	}
	return nil
}

func stringTrim(b []byte) string { return strings.TrimSpace(string(b)) }

func readJSON(filename string, target any) error {
	storageMu.RLock()
	defer storageMu.RUnlock()
	var err error
	for attempt := 0; attempt < 12; attempt++ {
		err = readJSONOnce(filename, target)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

func readJSONOnce(filename string, target any) error {
	f, _, err := packagefs.OpenRegularNoSymlink(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	// Validate the complete bounded document against the destination before
	// interpreting fields, retaining defaults for omitted optional fields.
	return packagefs.DecodeJSONInto(f, maxUpdateRecordBytes, "update record", target)
}

func writeJSON(filename string, value any) error {
	storageMu.Lock()
	defer storageMu.Unlock()
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// Include the newline in the same limit enforced by readers. Reject before
	// creating a temporary file or replacing the last readable record.
	if len(payload) >= maxUpdateRecordBytes {
		return fmt.Errorf("update record exceeds %d bytes", maxUpdateRecordBytes)
	}
	if err := packagefs.MkdirAllNoSymlink(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".update-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(payload, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for attempt := 0; attempt < 60; attempt++ {
		if err = packagefs.ValidateRegularFileTargetNoSymlink(filename); err != nil {
			return err
		}
		err = os.Rename(f.Name(), filename)
		if err == nil {
			return syncDirectory(filepath.Dir(filename))
		}
		if runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

func loadSource(root string) (Source, bool, error) {
	s := Source{Ref: "master", Manifest: "updates/stable.json"}
	if err := readJSON(filepath.Join(root, "source.json"), &s); errors.Is(err, os.ErrNotExist) {
		s = officialSource()
	} else if err != nil {
		return Source{}, false, errors.New("无法读取更新源配置")
	}
	envToken := false
	if token := os.Getenv("NEWPRESS_GITEE_TOKEN"); token != "" {
		s.Token = token
		envToken = true
	}
	if file := os.Getenv("NEWPRESS_GITEE_TOKEN_FILE"); file != "" {
		if envToken {
			return Source{}, false, errors.New("Gitee 令牌和令牌文件不能同时配置")
		}
		f, _, err := packagefs.OpenRegularNoSymlink(file)
		if err != nil {
			return Source{}, false, errors.New("无法读取 Gitee 令牌文件")
		}
		body, err := io.ReadAll(io.LimitReader(f, maxGiteeTokenFileBytes+1))
		f.Close()
		if err != nil {
			return Source{}, false, errors.New("无法读取 Gitee 令牌文件")
		}
		if len(body) > maxGiteeTokenFileBytes {
			return Source{}, false, fmt.Errorf("Gitee 令牌文件不能超过 %d 字节", maxGiteeTokenFileBytes)
		}
		s.Token = stringTrim(body)
		envToken = true
	}
	return s, envToken, nil
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := packagefs.OpenDirectoryNoSymlink(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func removeRecord(root, name string) error {
	err := os.Remove(filepath.Join(root, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(root)
}
