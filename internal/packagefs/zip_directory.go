package packagefs

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Empty deflate streams can contain arbitrarily many empty blocks. Bound the
// encoded input as well as the decoded output before asking the ZIP reader to
// process one, including when many entries refer to the same archive bytes.
const maxZIPDirectoryDataBytes = 64 << 10

// ValidateZIPDirectory verifies that a directory entry actually has empty,
// readable contents. Callers must bound archive and entry sizes before calling.
func ValidateZIPDirectory(file *zip.File) error {
	if file == nil || !file.FileInfo().IsDir() {
		return errors.New("ZIP entry is not a directory")
	}
	if file.UncompressedSize64 != 0 || file.CRC32 != 0 || file.Flags&1 != 0 {
		return fmt.Errorf("ZIP directory is not empty or is encrypted: %s", file.Name)
	}
	if file.CompressedSize64 > maxZIPDirectoryDataBytes {
		return fmt.Errorf("ZIP directory compressed data exceeds %d bytes: %s", maxZIPDirectoryDataBytes, file.Name)
	}
	// zip.File.Open short-circuits trailing-slash names without decompressing
	// or checking CRCs/data descriptors. A copy without that suffix follows the
	// regular reader while retaining the original entry's offsets and metadata.
	checked := *file
	checked.Name = strings.TrimSuffix(file.Name, "/")
	stream, err := checked.Open()
	if err != nil {
		return fmt.Errorf("open ZIP directory %s: %w", file.Name, err)
	}
	read, readErr := io.Copy(io.Discard, io.LimitReader(stream, 1))
	closeErr := stream.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return fmt.Errorf("verify ZIP directory %s: %w", file.Name, err)
	}
	if read != 0 {
		return fmt.Errorf("ZIP directory contains data: %s", file.Name)
	}
	return nil
}
