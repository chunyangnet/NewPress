package content

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
)

func TestContentFileReadBuffersExactAndChangingLength(t *testing.T) {
	for _, size := range []int{1, 511, 512, 513, 65537} {
		payload := bytes.Repeat([]byte("x"), size)
		for _, reader := range []io.Reader{bytes.NewReader(payload), iotest.OneByteReader(bytes.NewReader(payload)), iotest.DataErrReader(bytes.NewReader(payload))} {
			got, err := readContentFileBytes(reader, int64(size))
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatal("exact file bytes changed", size, err)
			}
		}
		for _, observed := range []int64{int64(size + 1), int64(size - 1)} {
			if _, err := readContentFileBytes(bytes.NewReader(payload), observed); err == nil {
				t.Fatal("changed length accepted", size, observed)
			}
		}
	}
	reader := bytes.NewReader(bytes.Repeat([]byte("x"), 65536))
	if _, err := readContentFileBytes(reader, 16); err == nil || reader.Len() != 65536-17 {
		t.Fatal("growth detection consumed beyond observed length plus one", err, reader.Len())
	}
	for _, size := range []int64{0, -1, maxContentFileBytes + 1} {
		reader := bytes.NewReader([]byte("unchanged"))
		if _, err := readContentFileBytes(reader, size); err == nil || reader.Len() != 9 {
			t.Fatal("invalid size read input", size, err)
		}
	}
	failure := errors.New("read failed")
	if _, err := readContentFileBytes(iotest.ErrReader(failure), 8); !errors.Is(err, failure) {
		t.Fatal("reader failure lost", err)
	}
}

func TestContentFileReadBuffersFileChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	payload := bytes.Repeat([]byte("a"), 65537)
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readContentFile(path)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("file bytes changed", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("shortened"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkContentFileRead(path, before, int64(len(payload))); err == nil {
		t.Fatal("post-read file length change accepted")
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := readContentFile(path); err == nil {
		t.Fatal("empty file accepted")
	}
	if err := os.Truncate(path, maxContentFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := readContentFile(path); err == nil {
		t.Fatal("oversize file accepted")
	}
}
