package media

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestMemoryUploadStageOwnsItsPayload(t *testing.T) {
	manager, err := NewManager("")
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Repeat([]byte("staged upload"), 4096)
	source := bytes.Clone(original)
	staged, err := manager.stageUpload(t.Context(), bytes.NewReader(source), int64(len(source)))
	if err != nil {
		t.Fatal(err)
	}
	defer staged.discard()
	clear(source)
	reader, err := staged.open()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("input mutation changed staged upload: %v", err)
	}
}

func TestUploadNilContext(t *testing.T) {
	manager, err := NewManager("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Upload(nil, bytes.NewReader(testPNG(t, 3, 2)), UploadInput{Filename: "nil-context.png"}, 1024); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryUploadStageBoundsSpareCapacity(t *testing.T) {
	manager, err := NewManager("")
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), 5<<20)
	staged, err := manager.stageUpload(t.Context(), bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	defer staged.discard()
	if cap(staged.payload)-len(staged.payload) > 64<<10 {
		t.Fatalf("stage retained %d excess bytes", cap(staged.payload)-len(staged.payload))
	}
}

func BenchmarkMemoryUploadStaging(b *testing.B) {
	for _, tc := range []struct {
		name  string
		size  int
		limit int64
	}{
		{"1MiB", 1 << 20, 1 << 20},
		{"8MiB", 8 << 20, 8 << 20},
		{"8MiBRejected", (8 << 20) + 1, 8 << 20},
	} {
		b.Run(tc.name, func(b *testing.B) {
			manager, err := NewManager("")
			if err != nil {
				b.Fatal(err)
			}
			payload := bytes.Repeat([]byte("x"), tc.size)
			b.SetBytes(int64(tc.size))
			b.ReportAllocs()
			for b.Loop() {
				staged, err := manager.stageUpload(b.Context(), bytes.NewReader(payload), tc.limit)
				if int64(tc.size) > tc.limit {
					if !errors.Is(err, ErrTooLarge) || staged != nil {
						b.Fatalf("oversize upload stage=%v error=%v", staged, err)
					}
				} else {
					if err != nil || staged.size != int64(tc.size) {
						b.Fatalf("stage size or error: %v", err)
					}
					staged.discard()
				}
			}
		})
	}
}
