package audit

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

func ensureLedgerFile(path string) error {
	if _, err := packagefs.LstatNoSymlink(path); err == nil {
		return packagefs.ChmodRegularNoSymlink(path, 0o600)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect audit ledger: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create audit ledger: %w", err)
	}
	if syncErr := file.Sync(); syncErr != nil {
		_ = file.Close()
		return fmt.Errorf("sync audit ledger: %w", syncErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("close audit ledger: %w", closeErr)
	}
	return nil
}

func (ledger *Ledger) load() error {
	file, info, err := packagefs.OpenRegularNoSymlink(ledger.path)
	if err != nil {
		return fmt.Errorf("open audit ledger: %w", err)
	}
	defer file.Close()
	if info.Size() > maxFileBytes {
		return fmt.Errorf("%w: audit ledger exceeds %d bytes", ErrCorrupt, maxFileBytes)
	}
	reader := bufio.NewReaderSize(file, 32<<10)
	for {
		line, readErr := readLine(reader)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("%w: read audit ledger: %v", ErrCorrupt, readErr)
		}
		if len(line) == 0 || len(line) > maxLineBytes {
			return fmt.Errorf("%w: invalid audit record size", ErrCorrupt)
		}
		if !utf8.Valid(line) {
			return fmt.Errorf("%w: audit record is not valid UTF-8", ErrCorrupt)
		}
		var item record
		if err := packagefs.ValidateUniqueJSONFor(line, &item); err != nil {
			return fmt.Errorf("%w: validate audit JSON: %v", ErrCorrupt, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&item); err != nil {
			return fmt.Errorf("%w: decode audit record: %v", ErrCorrupt, err)
		}
		if err := ensureJSONEOF(decoder); err != nil {
			return fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		if err := ledger.accept(item); err != nil {
			return err
		}
		if len(ledger.records) > maxRecords {
			return fmt.Errorf("%w: audit ledger contains too many records", ErrCorrupt)
		}
	}
	actual, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened audit ledger: %w", err)
	}
	current, err := packagefs.LstatNoSymlink(ledger.path)
	if err != nil || !os.SameFile(info, actual) || !os.SameFile(info, current) {
		return fmt.Errorf("%w: audit ledger changed while reading", ErrLedgerUnavailable)
	}
	ledger.identity = actual
	return nil
}

func readLine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, min(maxLineBytes+2, reader.Size()))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxLineBytes+2 {
			return nil, errors.New("audit record exceeds line size limit")
		}
		line = append(line, fragment...)
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) != 0 {
				return nil, errors.New("audit ledger ends with an unterminated record")
			}
			return nil, io.EOF
		}
		return nil, err
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		return nil, errors.New("audit record is not newline terminated")
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return line, nil
}

func (ledger *Ledger) appendLocked(item record) error {
	item.Hash = recordHash(item)
	encoded, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if len(encoded) > maxLineBytes {
		return fmt.Errorf("audit record exceeds %d bytes", maxLineBytes)
	}
	encoded = append(encoded, '\n')
	file, actual, err := openAppendNoSymlink(ledger.path, ledger.identity)
	if err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	current, err := packagefs.LstatNoSymlink(ledger.path)
	if err != nil || !os.SameFile(actual, current) {
		_ = file.Close()
		return errors.New("audit ledger changed while appending")
	}
	if err := file.Close(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if directory, err := packagefs.OpenDirectoryNoSymlink(filepath.Dir(ledger.path)); err == nil {
			_ = directory.Sync()
			_ = directory.Close()
		}
	}
	if err := ledger.accept(item); err != nil {
		return err
	}
	ledger.identity = actual
	return nil
}

func openAppendNoSymlink(path string, original os.FileInfo) (*os.File, os.FileInfo, error) {
	if _, err := packagefs.LstatNoSymlink(filepath.Dir(path)); err != nil {
		return nil, nil, err
	}
	expected, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return nil, nil, err
	}
	if !expected.Mode().IsRegular() {
		return nil, nil, errors.New("audit ledger is not a regular file")
	}
	if original != nil && !os.SameFile(original, expected) {
		return nil, nil, errors.New("audit ledger changed since it was validated")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	actual, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	current, err := packagefs.LstatNoSymlink(path)
	if err != nil || !os.SameFile(expected, actual) || !os.SameFile(expected, current) {
		_ = file.Close()
		return nil, nil, errors.New("audit ledger changed while opening")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, actual, nil
}

func recordHash(item record) string {
	item.Hash = ""
	payload, _ := json.Marshal(item)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func newTransactionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate audit transaction ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// Digest returns the lowercase SHA-256 digest used to bind a fact payload to
// its prepare record.
func Digest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// MissingDigest is the sentinel for a fact that does not yet exist. NewPress
// fact documents are never valid zero-byte files.
func MissingDigest() string { return Digest(nil) }

// HashFact reads a bounded regular file without following symlinks.
func HashFact(path string) (string, error) { return HashFile(path) }

func HashFile(path string) (string, error) {
	file, info, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if info.Size() > maxFileBytes {
		return "", errors.New("audit fact exceeds size limit")
	}
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return "", err
	}
	if err := verifyRegularFileStable(path, file, info, read); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// verifyRegularFileStable closes the validation gap between opening a fact
// file and finishing its digest. A caller still needs a stopped-write window
// for same-size content replacement, but path replacement, truncation and
// append races are rejected deterministically.
func verifyRegularFileStable(path string, file *os.File, expected os.FileInfo, bytesRead int64) error {
	if file == nil || expected == nil {
		return errors.New("audit file identity is unavailable")
	}
	actual, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened audit file: %w", err)
	}
	current, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return fmt.Errorf("inspect audit file path: %w", err)
	}
	if !actual.Mode().IsRegular() || !current.Mode().IsRegular() ||
		!os.SameFile(expected, actual) || !os.SameFile(expected, current) || !os.SameFile(actual, current) {
		return errors.New("audit file changed while reading")
	}
	if actual.Size() != expected.Size() || bytesRead != expected.Size() {
		return errors.New("audit file size changed while reading")
	}
	return nil
}

func hashPathOrMissing(path string) (string, error) {
	digest, err := HashFact(path)
	if errors.Is(err, os.ErrNotExist) {
		digest, err = HashFile(path + ".bak")
	}
	if errors.Is(err, os.ErrNotExist) {
		return MissingDigest(), nil
	}
	return digest, err
}

func projectRecord(item record) Record {
	return Record{Schema: item.Schema, Kind: item.Kind, Sequence: item.Sequence, Transaction: item.Transaction,
		Phase: item.Phase, CreatedAt: item.CreatedAt, FactKey: item.FactKey, FactHash: item.FactHash,
		Action: item.Action, ObjectType: item.ObjectType, ObjectID: item.ObjectID, Actor: item.Actor,
		Metadata: cloneMetadata(item.Metadata), PreviousHash: item.PreviousHash, Hash: item.Hash}
}

func cloneMetadata(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var result map[string]any
	if json.Unmarshal(encoded, &result) != nil {
		return nil
	}
	return result
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("JSON document contains trailing data")
		}
		return fmt.Errorf("trailing JSON is invalid: %w", err)
	}
	return nil
}
