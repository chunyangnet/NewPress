package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/packagefs"
)

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "newpress.exe"
	}
	return "newpress"
}
func executable(root string, r Release, seed string) string {
	if r.ID == "" {
		return seed
	}
	return filepath.Join(root, "releases", r.ID, "bin", binaryName())
}
func releaseThemes(root string, r Release, fallback string) string {
	if r.ID == "" {
		return fallback
	}
	return filepath.Join(root, "releases", r.ID, "themes")
}

func hashFile(filename string) (string, error) {
	f, _, err := packagefs.OpenRegularNoSymlink(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Each release owns a complete theme snapshot, so a failed start never leaves
// the previous binary paired with partially replaced templates.
func copyThemes(ctx context.Context, source, target string) error {
	var size int64
	count := 0
	return filepath.WalkDir(source, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("主题快照不支持符号链接")
		}
		// Lock names are reserved inside the source tree, not in its root
		// basename. Never copy a live lock or accept a directory as a lock.
		if rel != "." && strings.EqualFold(entry.Name(), dirlock.FileName) {
			if !entry.Type().IsRegular() {
				return errors.New("主题锁路径必须是普通文件")
			}
			return nil
		}
		dest := filepath.Join(target, rel)
		if entry.IsDir() {
			return packagefs.MkdirAllNoSymlink(dest, 0700)
		}
		f, info, err := packagefs.OpenRegularNoSymlink(name)
		if err != nil {
			return err
		}
		defer f.Close()
		size += info.Size()
		count++
		if size > 512<<20 || count > 20000 {
			return errors.New("主题快照超过 512 MiB 或文件数量限制")
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		n, err := io.Copy(out, io.LimitReader(f, info.Size()+1))
		if err == nil && n != info.Size() {
			err = errors.New("主题文件在复制期间发生变化")
		}
		if err == nil {
			err = out.Sync()
		}
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

func stageArchive(ctx context.Context, archive, root, id, themes string) (Release, error) {
	stage := filepath.Join(root, "releases", id)
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		return Release{}, errors.New("更新目录已存在")
	}
	if err := packagefs.MkdirAllNoSymlink(stage, 0700); err != nil {
		return Release{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := copyThemes(ctx, themes, filepath.Join(stage, "themes")); err != nil {
		return Release{}, errors.New("无法建立主题快照，请检查权限、大小和符号链接")
	}
	zfile, z, err := packagefs.OpenZIP(archive, MaxPackageBytes, 20_000)
	if err != nil {
		return Release{}, errors.New("更新包不是有效的 ZIP")
	}
	defer zfile.Close()
	if len(z.File) > 20000 {
		return Release{}, errors.New("更新包文件数量超限")
	}
	seen := map[string]bool{}
	var total int64
	hasBinary := false
	for _, f := range z.File {
		if ctx.Err() != nil {
			return Release{}, ctx.Err()
		}
		name := strings.TrimSuffix(f.Name, "/")
		key := strings.ToLower(name)
		if !safePath(name) || seen[key] || f.NonUTF8 || f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return Release{}, errors.New("更新包包含不安全、重复或特殊文件路径")
		}
		seen[key] = true
		isBinary := name == "bin/"+binaryName()
		if !isBinary && name != "bin" {
			return Release{}, errors.New("核心包只能包含当前平台的程序，主题请通过扩展更新")
		}
		if f.FileInfo().IsDir() {
			if isBinary {
				return Release{}, errors.New("程序路径不能是目录")
			}
			if err := packagefs.ValidateZIPDirectory(f); err != nil {
				return Release{}, err
			}
			if err = packagefs.MkdirAllNoSymlink(filepath.Join(stage, filepath.FromSlash(name)), 0700); err != nil {
				return Release{}, err
			}
			continue
		}
		if name == "bin" {
			return Release{}, errors.New("更新包包含保留路径")
		}
		if f.UncompressedSize64 > 256<<20 {
			return Release{}, errors.New("更新包单文件大小超限")
		}
		total += int64(f.UncompressedSize64)
		if total > 512<<20 {
			return Release{}, errors.New("更新包解压大小超限")
		}
		target := filepath.Join(stage, filepath.FromSlash(name))
		if err = packagefs.MkdirAllNoSymlink(filepath.Dir(target), 0700); err != nil {
			return Release{}, err
		}
		if err = packagefs.ValidateRegularFileTargetNoSymlink(target); err != nil {
			return Release{}, err
		}
		input, err := f.Open()
		if err != nil {
			return Release{}, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			input.Close()
			return Release{}, err
		}
		n, copyErr := io.Copy(out, io.LimitReader(input, int64(f.UncompressedSize64)+1))
		input.Close()
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || n != int64(f.UncompressedSize64) {
			return Release{}, errors.New("解压更新包失败或文件校验错误")
		}
		if isBinary {
			hasBinary = true
			if err = os.Chmod(target, 0700); err != nil {
				return Release{}, err
			}
		}
	}
	if !hasBinary {
		return Release{}, errors.New("更新包缺少当前平台的 NewPress 程序")
	}
	r := Release{ID: id}
	r.BinaryHash, err = hashFile(executable(root, r, ""))
	keep = err == nil
	return r, err
}
