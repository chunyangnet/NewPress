package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"newpress.local/newpress/internal/packagefs"
)

type archive struct {
	path     string
	file     *os.File
	info     os.FileInfo
	manifest Manifest
	entries  map[string]*zip.File
}

func openArchive(ctx context.Context, archivePath string) (*archive, error) {
	file, info, err := packagefs.OpenRegularNoSymlink(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open backup: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = file.Close()
		}
	}()
	count, err := preflightZIP(file, info.Size())
	if err != nil {
		return nil, err
	}
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return nil, fmt.Errorf("read backup ZIP: %w", err)
	}
	if len(reader.File) != count || len(reader.File) > MaxEntries+1 {
		return nil, errors.New("backup ZIP entry count is inconsistent or excessive")
	}
	entries := make(map[string]*zip.File, count)
	var expanded uint64
	for _, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name
		isDirectory := entry.FileInfo().IsDir()
		if isDirectory {
			name = strings.TrimSuffix(name, "/")
		}
		if entry.NonUTF8 || entry.Flags&1 != 0 || entry.ReaderVersion > 20 ||
			(entry.Method != zip.Store && entry.Method != zip.Deflate) ||
			(!isDirectory && !entry.Mode().IsRegular()) || entry.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unsupported backup ZIP entry: %q", entry.Name)
		}
		if name == ManifestName {
			if isDirectory || entry.UncompressedSize64 > maxManifestBytes {
				return nil, errors.New("backup manifest is a directory or oversized")
			}
		} else if err := validatePath(name); err != nil {
			return nil, err
		}
		if isDirectory && (entry.Name != name+"/" || entry.UncompressedSize64 != 0) {
			return nil, errors.New("backup directory entry is not canonical or empty")
		}
		if entry.UncompressedSize64 > MaxFileBytes || entry.CompressedSize64 > uint64(MaxArchiveBytes) {
			return nil, errors.New("backup ZIP file exceeds size limits")
		}
		if isDirectory {
			if err := packagefs.ValidateZIPDirectory(entry); err != nil {
				return nil, err
			}
		}
		expanded += entry.UncompressedSize64
		if expanded > uint64(MaxTotalBytes)+maxManifestBytes {
			return nil, errors.New("backup ZIP expanded size exceeds limits")
		}
		key := strings.ToLower(name)
		if previous, exists := entries[key]; exists {
			return nil, fmt.Errorf("duplicate or colliding backup paths: %s and %s", previous.Name, entry.Name)
		}
		entries[key] = entry
	}
	manifestFile, exists := entries[ManifestName]
	if !exists || manifestFile.Name != ManifestName {
		return nil, errors.New("backup is missing manifest.json")
	}
	stream, err := manifestFile.Open()
	if err != nil {
		return nil, err
	}
	manifest, decodeErr := packagefs.DecodeJSON[Manifest](contextReader{ctx, stream}, maxManifestBytes, "backup manifest")
	closeErr := stream.Close()
	if err := errors.Join(decodeErr, closeErr); err != nil {
		return nil, err
	}
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	if len(entries) != len(manifest.Files)+len(manifest.Directories)+1 {
		return nil, errors.New("backup ZIP does not match manifest inventory")
	}
	for _, directory := range manifest.Directories {
		entry, exists := entries[strings.ToLower(directory)]
		if !exists || entry.Name != directory+"/" || !entry.FileInfo().IsDir() {
			return nil, fmt.Errorf("backup directory missing or inconsistent: %s", directory)
		}
	}
	for _, file := range manifest.Files {
		entry, exists := entries[strings.ToLower(file.Path)]
		if !exists || entry.Name != file.Path || entry.FileInfo().IsDir() || entry.UncompressedSize64 != uint64(file.Size) {
			return nil, fmt.Errorf("backup file missing or inconsistent: %s", file.Path)
		}
	}
	complete = true
	return &archive{path: archivePath, file: file, info: info, manifest: manifest, entries: entries}, nil
}

// preflightZIP bounds directory allocation before archive/zip parses it. The
// local format uses a single ordinary ZIP volume below 2 GiB, so ZIP64 and
// self-extracting or multi-volume variants are deliberately not accepted.
func preflightZIP(file *os.File, size int64) (int, error) {
	if size < 22 || size > MaxArchiveBytes {
		return 0, errors.New("backup archive must be a ZIP of at most 2 GiB")
	}
	length := min(size, 22+65535)
	tail := make([]byte, int(length))
	if _, err := file.ReadAt(tail, size-length); err != nil {
		return 0, err
	}
	for index := len(tail) - 22; index >= 0; index-- {
		if binary.LittleEndian.Uint32(tail[index:]) != 0x06054b50 {
			continue
		}
		footer := tail[index:]
		commentLength := int(binary.LittleEndian.Uint16(footer[20:]))
		if index+22+commentLength != len(tail) {
			continue
		}
		count := int(binary.LittleEndian.Uint16(footer[10:]))
		directorySize := uint64(binary.LittleEndian.Uint32(footer[12:]))
		directoryOffset := uint64(binary.LittleEndian.Uint32(footer[16:]))
		footerOffset := uint64(size - length + int64(index))
		if binary.LittleEndian.Uint16(footer[4:]) != 0 || binary.LittleEndian.Uint16(footer[6:]) != 0 ||
			int(binary.LittleEndian.Uint16(footer[8:])) != count || count == 0 || count > MaxEntries+1 ||
			directorySize == 0 || directorySize > maxDirectoryBytes || directoryOffset == 0 ||
			directoryOffset+directorySize != footerOffset {
			return 0, errors.New("backup ZIP directory is oversized, ZIP64, multi-volume or inconsistent")
		}
		return count, nil
	}
	return 0, errors.New("backup ZIP end record is missing or truncated")
}

// verify optionally extracts into a private staging directory. Every output
// is bound to an inventory digest and is created exclusively beneath os.Root.
func (a *archive) verify(ctx context.Context, destination string) (string, error) {
	var root *os.Root
	if destination != "" {
		var err error
		root, err = os.OpenRoot(destination)
		if err != nil {
			return "", err
		}
		defer root.Close()
		for _, directory := range a.manifest.Directories {
			if err := root.MkdirAll(directory, 0o700); err != nil {
				return "", err
			}
		}
	}
	for _, file := range a.manifest.Files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		entry := a.entries[strings.ToLower(file.Path)]
		if err := verifyEntry(ctx, entry, file, root); err != nil {
			return "", fmt.Errorf("verify backup file %s: %w", file.Path, err)
		}
	}
	if root != nil {
		payload, err := json.MarshalIndent(a.manifest, "", "  ")
		if err != nil {
			return "", err
		}
		if err := writeRootFile(root, ManifestName, append(payload, '\n')); err != nil {
			return "", err
		}
		for index := len(a.manifest.Directories) - 1; index >= 0; index-- {
			if err := syncDirectory(filepath.Join(destination, filepath.FromSlash(a.manifest.Directories[index]))); err != nil {
				return "", err
			}
		}
		if err := syncDirectory(destination); err != nil {
			return "", err
		}
	}
	digest := sha256.New()
	read, err := io.Copy(digest, contextReader{ctx, io.NewSectionReader(a.file, 0, a.info.Size())})
	if err != nil {
		return "", err
	}
	if err := stableFile(a.path, a.file, a.info, read); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func verifyEntry(ctx context.Context, entry *zip.File, expected File, root *os.Root) error {
	stream, err := entry.Open()
	if err != nil {
		return err
	}
	defer stream.Close()
	digest := sha256.New()
	var writer io.Writer = digest
	var output *os.File
	if root != nil {
		output, err = root.OpenFile(expected.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer output.Close()
		writer = io.MultiWriter(output, digest)
	}
	read, err := io.Copy(writer, io.LimitReader(contextReader{ctx, stream}, expected.Size+1))
	if err != nil {
		return err
	}
	if read != expected.Size || hex.EncodeToString(digest.Sum(nil)) != expected.SHA256 {
		return errors.New("file length or SHA-256 does not match manifest")
	}
	if output != nil {
		if err := output.Sync(); err != nil {
			return err
		}
		return output.Close()
	}
	return nil
}

func writeRootFile(root *os.Root, path string, payload []byte) error {
	file, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func writeArchive(ctx context.Context, output *os.File, roots []source, manifest Manifest) error {
	writer := zip.NewWriter(&limitedWriter{writer: output, remaining: MaxArchiveBytes})
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if len(payload)+1 > maxManifestBytes {
		return errors.New("backup manifest exceeds size limit")
	}
	manifestHeader := &zip.FileHeader{Name: ManifestName, Method: zip.Deflate, Modified: manifest.CreatedAt}
	manifestHeader.SetMode(0o600)
	manifestWriter, err := writer.CreateHeader(manifestHeader)
	if err != nil {
		return err
	}
	if _, err := manifestWriter.Write(append(payload, '\n')); err != nil {
		return err
	}
	for _, directory := range manifest.Directories {
		header := &zip.FileHeader{Name: directory + "/", Method: zip.Store, Modified: manifest.CreatedAt}
		header.SetMode(os.ModeDir | 0o700)
		if _, err := writer.CreateHeader(header); err != nil {
			return err
		}
	}
	rootPaths := make(map[string]string, len(roots))
	for _, root := range roots {
		rootPaths[root.name] = root.path
	}
	for _, file := range manifest.Files {
		header := &zip.FileHeader{Name: file.Path, Method: zip.Deflate, Modified: manifest.CreatedAt}
		header.SetMode(0o600)
		output, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		parts := strings.SplitN(file.Path, "/", 2)
		actual, err := copySource(ctx, filepath.Join(rootPaths[parts[0]], filepath.FromSlash(parts[1])), output)
		if err != nil {
			return err
		}
		if actual.Size != file.Size || actual.SHA256 != file.SHA256 {
			return fmt.Errorf("source changed while archiving %s", file.Path)
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return output.Sync()
}

type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *limitedWriter) Write(payload []byte) (int, error) {
	if int64(len(payload)) > writer.remaining {
		return 0, errors.New("compressed backup exceeds 2 GiB limit")
	}
	written, err := writer.writer.Write(payload)
	writer.remaining -= int64(written)
	return written, err
}
