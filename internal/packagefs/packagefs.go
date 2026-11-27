package packagefs

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

type Limits struct {
	MaxArchiveBytes     int64
	MaxFiles            int
	MaxFileBytes        int64
	MaxTotal            int64
	MaxCompressionRatio uint64
}

func DefaultLimits() Limits {
	return Limits{MaxArchiveBytes: 160 << 20, MaxFiles: 2_000, MaxFileBytes: 32 << 20, MaxTotal: 128 << 20, MaxCompressionRatio: 1_000}
}

func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("invalid package slug %q", slug)
	}
	return nil
}

// ValidateRelativePath accepts only portable, canonical slash-separated paths
// that resolve beneath a package root.
func ValidateRelativePath(name string) error {
	canonical, err := validateArchivePath(name)
	if err != nil {
		return err
	}
	if canonical != name {
		return fmt.Errorf("package path must be canonical: %s", name)
	}
	return nil
}

// LstatNoSymlink verifies every component of path with Lstat and rejects
// symbolic links in the path. It is intended for trusted package/runtime roots
// where following a user-replaced directory or file would escape the boundary.
func LstatNoSymlink(path string) (os.FileInfo, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	components := make([]string, 0, 8)
	for current := absolute; ; current = filepath.Dir(current) {
		components = append(components, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	for index := len(components) - 1; index >= 0; index-- {
		info, err := os.Lstat(components[index])
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("path contains a symbolic link: %s", components[index])
		}
		if index == 0 {
			return info, nil
		}
	}
	return nil, errors.New("path inspection failed")
}

// ValidateRegularFileTargetNoSymlink validates a file path that may not exist
// yet. Existing targets must be regular files, and the target's parent and all
// of its ancestors must be real directories. This is used immediately before
// atomic replacement so a stale symlink cannot be silently accepted.
func ValidateRegularFileTargetNoSymlink(path string) error {
	directory := filepath.Dir(path)
	if _, err := LstatNoSymlink(directory); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("path is linked or not a regular file: %s", path)
	}
	_, err = LstatNoSymlink(path)
	return err
}

// MkdirAllNoSymlink creates directory without traversing symbolic-link
// ancestors. Existing path components must all be real directories.
func MkdirAllNoSymlink(directory string, perm os.FileMode) error {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	missing := make([]string, 0, 4)
	current := absolute
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("directory path is linked or not a directory: %s", current)
			}
			if _, err := LstatNoSymlink(current); err != nil {
				return err
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("cannot resolve existing directory ancestor for %s", absolute)
		}
		current = parent
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := os.Mkdir(missing[index], perm); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(missing[index])
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("created directory is linked or invalid: %s", missing[index])
		}
	}
	return nil
}

// OpenRegularNoSymlink opens a regular file only when neither the file nor any
// ancestor is a symbolic link. The post-open identity and path checks narrow
// replacement races between validation and open.
func OpenRegularNoSymlink(path string) (*os.File, os.FileInfo, error) {
	// Inspect the parent first. LstatNoSymlink(path) intentionally returns
	// ENOENT for a missing leaf without walking ancestors, but a missing file
	// below a symlinked directory must still be rejected.
	if _, err := LstatNoSymlink(filepath.Dir(path)); err != nil {
		return nil, nil, err
	}
	expected, err := LstatNoSymlink(path)
	if err != nil {
		return nil, nil, err
	}
	if !expected.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("path is not a regular file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	actual, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		file.Close()
		return nil, nil, fmt.Errorf("file changed while opening: %s", path)
	}
	current, err := LstatNoSymlink(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(expected, current) || !os.SameFile(current, actual) {
		file.Close()
		return nil, nil, fmt.Errorf("file path changed while opening: %s", path)
	}
	return file, actual, nil
}

// OpenDirectoryNoSymlink opens a real directory and verifies its identity
// after opening. It is primarily used for durable directory fsyncs.
func OpenDirectoryNoSymlink(path string) (*os.File, error) {
	expected, err := LstatNoSymlink(path)
	if err != nil {
		return nil, err
	}
	if !expected.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", path)
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	actual, err := directory.Stat()
	if err != nil {
		directory.Close()
		return nil, err
	}
	if !actual.IsDir() || !os.SameFile(expected, actual) {
		directory.Close()
		return nil, fmt.Errorf("directory changed while opening: %s", path)
	}
	current, err := LstatNoSymlink(path)
	if err != nil || !current.IsDir() || !os.SameFile(expected, current) || !os.SameFile(current, actual) {
		directory.Close()
		return nil, fmt.Errorf("directory path changed while opening: %s", path)
	}
	return directory, nil
}

// ChmodRegularNoSymlink changes permissions through an already-open file
// descriptor, avoiding a second path lookup that could follow a replacement.
func ChmodRegularNoSymlink(path string, perm os.FileMode) error {
	file, _, err := OpenRegularNoSymlink(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Chmod(perm)
}

// ReplaceRegularFileNoSymlink replaces target with a verified temporary file.
// The rollback path is retained for platforms whose Rename cannot overwrite
// an existing file (notably Windows). Both target and rollback paths reject
// symbolic links and non-regular files.
func ReplaceRegularFileNoSymlink(temporaryPath, target string) error {
	if err := ValidateRegularFileTargetNoSymlink(temporaryPath); err != nil {
		return fmt.Errorf("invalid temporary file: %w", err)
	}
	if err := ValidateRegularFileTargetNoSymlink(target); err != nil {
		return fmt.Errorf("invalid target file: %w", err)
	}
	backup := target + ".bak"
	if err := ValidateRegularFileTargetNoSymlink(backup); err != nil {
		return fmt.Errorf("invalid rollback file: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err == nil {
		return nil
	} else {
		if err := ValidateRegularFileTargetNoSymlink(target); err != nil {
			return fmt.Errorf("recheck target file: %w", err)
		}
		if err := ValidateRegularFileTargetNoSymlink(backup); err != nil {
			return fmt.Errorf("recheck rollback file: %w", err)
		}
		if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove previous rollback file: %w", err)
		}
		if existingErr := os.Rename(target, backup); existingErr != nil && !errors.Is(existingErr, os.ErrNotExist) {
			return fmt.Errorf("prepare file replacement: %w", err)
		}
		if replacementErr := os.Rename(temporaryPath, target); replacementErr != nil {
			_ = os.Rename(backup, target)
			return fmt.Errorf("replace file: %w", replacementErr)
		}
		// The new file is already committed. A cleanup error must not make a
		// caller keep its old in-memory state after publishing new disk state.
		// A retained regular rollback file is checked on the next replacement.
		_ = os.Remove(backup)
		return nil
	}
}

func ReadJSON[T any](archivePath, name string) (T, error) {
	var result T
	limits := DefaultLimits()
	archive, reader, err := openArchive(archivePath, limits.MaxArchiveBytes, limits.MaxFiles)
	if err != nil {
		return result, fmt.Errorf("open package: %w", err)
	}
	defer archive.Close()

	found := false
	for _, file := range reader.File {
		if file.NonUTF8 {
			return result, fmt.Errorf("package path is not UTF-8: %s", file.Name)
		}
		canonical, pathErr := validateArchivePath(file.Name)
		if pathErr != nil {
			return result, pathErr
		}
		fileInfo := file.FileInfo()
		if err := validateCanonicalArchiveEntry(file.Name, canonical, fileInfo.IsDir()); err != nil {
			return result, err
		}
		if !fileInfo.IsDir() && !fileInfo.Mode().IsRegular() {
			return result, fmt.Errorf("package contains unsupported special file: %s", file.Name)
		}
		if fileInfo.IsDir() {
			if err := ValidateZIPDirectory(file); err != nil {
				return result, err
			}
		}
		if canonical != name {
			continue
		}
		if found || fileInfo.IsDir() {
			return result, fmt.Errorf("package contains duplicate or directory manifest %s", name)
		}
		found = true
		if file.UncompressedSize64 > 1<<20 {
			return result, fmt.Errorf("manifest %s is too large", name)
		}
		stream, err := file.Open()
		if err != nil {
			return result, err
		}
		decoded, decodeErr := DecodeJSON[T](stream, 1<<20, name)
		closeErr := stream.Close()
		if decodeErr != nil {
			return result, decodeErr
		}
		if closeErr != nil {
			return result, fmt.Errorf("close %s: %w", name, closeErr)
		}
		result = decoded
	}
	if !found {
		return result, fmt.Errorf("package is missing %s", name)
	}
	return result, nil
}

// DecodeJSON reads exactly one bounded JSON document and rejects duplicate
// object keys, aliases of the same struct field or integer map key, invalid UTF-8,
// unpaired UTF-16 surrogates, unknown fields and trailing data.
func DecodeJSON[T any](reader io.Reader, limit int64, label string) (T, error) {
	var result T
	err := DecodeJSONInto(reader, limit, label, &result)
	return result, err
}

// DecodeJSONWithNumbers applies DecodeJSON's validation while retaining numbers
// in interface fields as json.Number. Use it for durable metadata that must not
// be rounded through float64 when it is decoded and saved again.
func DecodeJSONWithNumbers[T any](reader io.Reader, limit int64, label string) (T, error) {
	var result T
	err := decodeJSONInto(reader, limit, label, &result, true)
	return result, err
}

// DecodeJSONInto applies DecodeJSON's checks to an existing destination, retaining
// defaults for omitted fields. Ambiguous documents are rejected before decoding.
func DecodeJSONInto(reader io.Reader, limit int64, label string, target any) error {
	return decodeJSONInto(reader, limit, label, target, false)
}

func decodeJSONInto(reader io.Reader, limit int64, label string, target any, useNumbers bool) error {
	if reader == nil || limit <= 0 || limit == math.MaxInt64 {
		return fmt.Errorf("decode %s: invalid JSON reader or limit", label)
	}
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	if len(payload) == 0 {
		return fmt.Errorf("decode %s: JSON document is empty", label)
	}
	if int64(len(payload)) > limit {
		return fmt.Errorf("decode %s: JSON document exceeds %d bytes", label, limit)
	}
	if err := ValidateUniqueJSONFor(payload, target); err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if useNumbers {
		decoder.UseNumber()
	}
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode %s: JSON document contains trailing data", label)
		}
		return fmt.Errorf("decode %s: trailing JSON is invalid: %w", label, err)
	}
	return nil
}

// Extract expands an archive into an empty, non-symlink directory. All archive
// metadata is validated before the first output is created.
func Extract(archivePath, destination string, limits Limits) (err error) {
	if limits.MaxArchiveBytes <= 0 || limits.MaxFiles <= 0 || limits.MaxFileBytes <= 0 || limits.MaxTotal <= 0 ||
		limits.MaxArchiveBytes == math.MaxInt64 || limits.MaxFileBytes == math.MaxInt64 || limits.MaxTotal == math.MaxInt64 {
		return errors.New("invalid package extraction limits")
	}
	archive, reader, err := openArchive(archivePath, limits.MaxArchiveBytes, limits.MaxFiles)
	if err != nil {
		return fmt.Errorf("open package: %w", err)
	}
	defer archive.Close()

	if len(reader.File) > limits.MaxFiles {
		return fmt.Errorf("package contains %d files; limit is %d", len(reader.File), limits.MaxFiles)
	}

	root, err := filepath.Abs(destination)
	if err != nil {
		return err
	}

	type archiveEntry struct {
		file *zip.File
		name string
		dir  bool
	}
	entries := make([]archiveEntry, 0, len(reader.File))
	seen := make(map[string]archiveEntry, len(reader.File))
	var total int64
	for _, file := range reader.File {
		if file.NonUTF8 {
			return fmt.Errorf("package path is not UTF-8: %s", file.Name)
		}
		cleanArchiveName, err := validateArchivePath(file.Name)
		if err != nil {
			return err
		}
		fileInfo := file.FileInfo()
		if err := validateCanonicalArchiveEntry(file.Name, cleanArchiveName, fileInfo.IsDir()); err != nil {
			return err
		}
		entry := archiveEntry{file: file, name: cleanArchiveName, dir: fileInfo.IsDir()}
		collisionKey := strings.ToLower(cleanArchiveName)
		if previous, exists := seen[collisionKey]; exists {
			return fmt.Errorf("package paths collide: %s and %s", previous.name, file.Name)
		}
		for parent := path.Dir(cleanArchiveName); parent != "."; parent = path.Dir(parent) {
			if previous, exists := seen[strings.ToLower(parent)]; exists && !previous.dir {
				return fmt.Errorf("package path conflicts with file %s: %s", previous.name, file.Name)
			}
		}
		seen[collisionKey] = entry
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("package symlinks are not allowed: %s", file.Name)
		}
		if !fileInfo.IsDir() && !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("package contains unsupported special file: %s", file.Name)
		}
		if file.UncompressedSize64 > uint64(limits.MaxFileBytes) {
			return fmt.Errorf("package file is too large: %s", file.Name)
		}
		if file.UncompressedSize64 > uint64(limits.MaxTotal) || total > limits.MaxTotal-int64(file.UncompressedSize64) {
			return fmt.Errorf("expanded package exceeds %d bytes", limits.MaxTotal)
		}
		if file.UncompressedSize64 > 1<<20 && compressionRatioExceeds(file.UncompressedSize64, file.CompressedSize64, limits.MaxCompressionRatio) {
			return fmt.Errorf("package file compression ratio is too high: %s", file.Name)
		}
		if fileInfo.IsDir() {
			if err := ValidateZIPDirectory(file); err != nil {
				return err
			}
		}
		if file.UncompressedSize64 > 0 {
			total += int64(file.UncompressedSize64)
		}
		entries = append(entries, entry)
	}
	// Register implicit parents after all explicit entries so consistent directory
	// declarations are accepted regardless of their position in the archive.
	for _, entry := range entries {
		for parent := path.Dir(entry.name); parent != "."; parent = path.Dir(parent) {
			key := strings.ToLower(parent)
			if previous, exists := seen[key]; exists {
				if !previous.dir {
					return fmt.Errorf("package path conflicts with file %s: %s", previous.name, entry.file.Name)
				}
				if previous.name != parent {
					return fmt.Errorf("package paths collide: %s and %s", previous.name, parent)
				}
			} else {
				seen[key] = archiveEntry{name: parent, dir: true}
			}
		}
	}
	if err := ensureEmptyDirectory(root); err != nil {
		return err
	}

	created := make([]string, 0, len(entries))
	defer func() {
		if err != nil {
			for index := len(created) - 1; index >= 0; index-- {
				_ = os.Remove(created[index])
			}
		}
	}()
	var writtenTotal int64
	for _, entry := range entries {
		if entry.dir {
			if err = ensureDirectoryPath(root, entry.name, &created); err != nil {
				return err
			}
			continue
		}
		parent := path.Dir(entry.name)
		if parent != "." {
			if err = ensureDirectoryPath(root, parent, &created); err != nil {
				return err
			}
		}
		target := filepath.Join(root, filepath.FromSlash(entry.name))
		maxBytes := limits.MaxFileBytes
		if remaining := limits.MaxTotal - writtenTotal; remaining < maxBytes {
			maxBytes = remaining
		}
		var written int64
		written, err = copyFile(entry.file, target, maxBytes)
		if err != nil {
			return err
		}
		writtenTotal += written
		created = append(created, target)
	}
	return nil
}

const maxZIPDirectoryBytes = 16 << 20

// OpenZIP bounds archive and directory allocation before archive/zip parses
// attacker-controlled central-directory entries.
func OpenZIP(archivePath string, maxBytes int64, maxEntries int) (*os.File, *zip.Reader, error) {
	if maxBytes <= 0 || maxBytes == math.MaxInt64 || maxEntries <= 0 {
		return nil, nil, errors.New("invalid package archive limits")
	}
	archive, info, err := OpenRegularNoSymlink(archivePath)
	if err != nil {
		return nil, nil, err
	}
	if info.Size() > maxBytes {
		archive.Close()
		return nil, nil, fmt.Errorf("package archive exceeds %d bytes", maxBytes)
	}
	count, err := preflightZIP(archive, info.Size(), maxEntries)
	if err != nil {
		archive.Close()
		return nil, nil, err
	}
	reader, err := zip.NewReader(archive, info.Size())
	if err != nil {
		archive.Close()
		return nil, nil, err
	}
	if len(reader.File) != count {
		archive.Close()
		return nil, nil, errors.New("package ZIP entry count is inconsistent")
	}
	return archive, reader, nil
}

func openArchive(archivePath string, maxBytes int64, maxEntries int) (*os.File, *zip.Reader, error) {
	return OpenZIP(archivePath, maxBytes, maxEntries)
}

func preflightZIP(archive *os.File, size int64, maxEntries int) (int, error) {
	if size < 22 {
		return 0, errors.New("package ZIP end record is missing or truncated")
	}
	length := min(size, int64(22+65535))
	tail := make([]byte, int(length))
	if _, err := archive.ReadAt(tail, size-length); err != nil {
		return 0, err
	}
	for index := len(tail) - 22; index >= 0; index-- {
		if binary.LittleEndian.Uint32(tail[index:index+4]) != 0x06054b50 {
			continue
		}
		commentLength := int(binary.LittleEndian.Uint16(tail[index+20 : index+22]))
		if index+22+commentLength != len(tail) {
			continue
		}
		footer := tail[index : index+22]
		entriesOnDisk := binary.LittleEndian.Uint16(footer[8:10])
		entries := binary.LittleEndian.Uint16(footer[10:12])
		directorySize := uint64(binary.LittleEndian.Uint32(footer[12:16]))
		directoryOffset := uint64(binary.LittleEndian.Uint32(footer[16:20]))
		if binary.LittleEndian.Uint16(footer[4:6]) != 0 || binary.LittleEndian.Uint16(footer[6:8]) != 0 ||
			entriesOnDisk != entries || entries == 0xffff || directorySize == 0xffffffff || directoryOffset == 0xffffffff {
			return 0, errors.New("package ZIP is multi-volume, ZIP64 or inconsistent")
		}
		if int(entries) > maxEntries {
			return 0, fmt.Errorf("package ZIP entry count %d exceeds limit %d", entries, maxEntries)
		}
		if directorySize > maxZIPDirectoryBytes {
			return 0, errors.New("package ZIP directory exceeds size limit")
		}
		endOffset := uint64(size - length + int64(index))
		if directoryOffset > endOffset || directorySize > endOffset-directoryOffset ||
			(entries == 0) != (directorySize == 0) {
			return 0, errors.New("package ZIP directory bounds are inconsistent")
		}
		return int(entries), nil
	}
	return 0, errors.New("package ZIP end record is missing or truncated")
}

func ensureEmptyDirectory(root string) error {
	info, err := LstatNoSymlink(root)
	if err != nil {
		return fmt.Errorf("inspect extraction destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("extraction destination must be a real directory")
	}
	children, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("inspect extraction destination: %w", err)
	}
	if len(children) != 0 {
		return errors.New("extraction destination must be empty")
	}
	return nil
}

func ensureDirectoryPath(root, relative string, created *[]string) error {
	current := root
	for _, segment := range strings.Split(relative, "/") {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, filepath.FromSlash(segment))
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o750); err != nil {
				return fmt.Errorf("create extraction directory %s: %w", relative, err)
			}
			*created = append(*created, current)
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect extraction directory %s: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("extraction path is not a real directory: %s", relative)
		}
	}
	return nil
}

// ValidateUniqueJSON rejects duplicate object keys and anything other than one
// complete JSON value. Invalid UTF-8 and unpaired UTF-16 surrogates are rejected
// before decoding can replace them. Callers still decide whether unknown fields
// are valid.
func ValidateUniqueJSON(payload []byte) error {
	return validateUniqueJSON(payload, nil)
}

// ValidateUniqueJSONFor also rejects distinct keys resolving to the same declared
// struct field or integer map key. String keys remain case-sensitive; custom JSON
// and text decoders own their mappings. Callers still choose how to handle
// unknown fields and numbers.
func ValidateUniqueJSONFor(payload []byte, target any) error {
	return validateUniqueJSON(payload, reflect.TypeOf(target))
}

func validateUniqueJSON(payload []byte, target reflect.Type) error {
	if err := validateJSONUnicode(payload); err != nil {
		return err
	}
	// Token reads opening delimiters separately, so it does not enforce the
	// full-document nesting limit used by Decode. Check syntax before our
	// recursive duplicate-field walk can descend into an over-nested value.
	if !json.Valid(payload) {
		var raw json.RawMessage
		return json.Unmarshal(payload, &raw) // Preserve the standard syntax error.
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder, target); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON token %v", token)
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder, target reflect.Type) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	target = jsonDestinationType(target)
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		fields := make(map[string]string)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key must be a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			child, field := jsonObjectField(target, key)
			if field != "" {
				if previous, exists := fields[field]; exists {
					return fmt.Errorf("duplicate JSON field %q (alias of %q)", key, previous)
				}
				fields[field] = key
			}
			if err := consumeUniqueJSONValue(decoder, child); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		index := 0
		for decoder.More() {
			var child reflect.Type
			if target != nil && (target.Kind() == reflect.Slice || target.Kind() == reflect.Array && index < target.Len()) {
				child = target.Elem()
			}
			if err := consumeUniqueJSONValue(decoder, child); err != nil {
				return err
			}
			index++
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func validateArchivePath(name string) (string, error) {
	if name == "" || !utf8.ValidString(name) || !norm.NFC.IsNormalString(name) || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") || strings.Contains(name, ":") {
		return "", fmt.Errorf("unsafe package path: %s", name)
	}
	cleanName := path.Clean(name)
	if cleanName == "." || strings.HasPrefix(cleanName, "/") || cleanName == ".." || strings.HasPrefix(cleanName, "../") {
		return "", fmt.Errorf("unsafe package path: %s", name)
	}
	for _, segment := range strings.Split(cleanName, "/") {
		trimmed := strings.TrimRight(segment, ". ")
		if trimmed != segment || reservedWindowsName(trimmed) {
			return "", fmt.Errorf("package path is not portable: %s", name)
		}
	}
	return cleanName, nil
}

func validateCanonicalArchiveEntry(name, canonical string, directory bool) error {
	if name == canonical || directory && name == canonical+"/" {
		return nil
	}
	return fmt.Errorf("package path must be canonical: %s", name)
}

func compressionRatioExceeds(uncompressed, compressed, maximum uint64) bool {
	if maximum == 0 || uncompressed == 0 {
		return false
	}
	if compressed == 0 {
		return true
	}
	quotient := uncompressed / compressed
	return quotient > maximum || quotient == maximum && uncompressed%compressed != 0
}

func reservedWindowsName(segment string) bool {
	base := strings.ToUpper(strings.SplitN(segment, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
		base == "COM¹" || base == "COM²" || base == "COM³" ||
		base == "LPT¹" || base == "LPT²" || base == "LPT³" {
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

func copyFile(source *zip.File, target string, maxBytes int64) (int64, error) {
	input, err := source.Open()
	if err != nil {
		return 0, err
	}
	defer input.Close()

	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maxBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return written, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return written, closeErr
	}
	if written > maxBytes {
		_ = os.Remove(target)
		return written, errors.New("package file exceeded extraction limit")
	}
	return written, nil
}
