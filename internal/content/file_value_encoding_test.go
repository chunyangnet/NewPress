package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type chunkedContentValueWriter struct {
	writer io.Writer
	size   int
}

func (w chunkedContentValueWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) != 0 {
		chunk := data[:min(w.size, len(data))]
		n, err := w.writer.Write(chunk)
		written += n
		if err != nil {
			return written, err
		}
		if n != len(chunk) {
			return written, io.ErrShortWrite
		}
		data = data[len(chunk):]
	}
	return written, nil
}

func TestContentFileValueEncodingChunks(t *testing.T) {
	// Large/small/nil values must retain their own final byte, with no newline
	// or bytes left over from the previous value at any writer chunk boundary.
	values := []any{strings.Repeat("中文 <>&\n\"\\\u2028", 3000), 12, nil,
		json.RawMessage("{\n\"number\": 9007199254740993, \"text\":\"end\\n\"\n}"),
		Post{Title: "small", Meta: map[string]any{"exact": json.Number("9007199254740993"), "tail": "😀"}},
		strings.Repeat("z", 35000), false, ""}
	want, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	for _, chunkSize := range []int{1, 2, 7, 32768, len(want)} {
		for _, limit := range []int{len(want), len(want) - 1} {
			t.Run(fmt.Sprintf("chunk-%d-limit-%d", chunkSize, limit), func(t *testing.T) {
				var output bytes.Buffer
				e := contentJSONEncoder{writer: &output, limit: limit}
				e.valueEncoder = json.NewEncoder(chunkedContentValueWriter{&e, chunkSize})
				e.text("[")
				for i, value := range values {
					if i != 0 {
						e.text(",")
					}
					e.value(value)
				}
				e.text("]\n")
				if limit == len(want) {
					if e.err != nil || e.size != len(want) || !bytes.Equal(output.Bytes(), want) {
						t.Fatal("chunked values changed document bytes", e.size, output.Len(), e.err)
					}
				} else if !errors.Is(e.err, errContentFileCapacity) || output.Len() > limit || !bytes.HasPrefix(want, output.Bytes()) {
					t.Fatal("capacity limit lost", e.size, output.Len(), e.err)
				}
			})
		}
	}
}

type interruptedContentValueWriter struct {
	bytes.Buffer
	remaining int
	failure   error
}

func (w *interruptedContentValueWriter) Write(data []byte) (int, error) {
	n := min(w.remaining, len(data))
	w.Buffer.Write(data[:n])
	w.remaining -= n
	if n < len(data) {
		return n, w.failure
	}
	return n, nil
}

func TestContentFileValueEncodingWriteFailures(t *testing.T) {
	value := "prefix <中文> " + strings.Repeat("x", 80)
	want, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected value write failure")
	for _, chunkSize := range []int{1, 7, len(want) + 1} {
		for _, failAfter := range []int{0, 1, 9, len(want) - 1} {
			for _, short := range []bool{false, true} {
				t.Run(fmt.Sprintf("chunk-%d-after-%d-short-%t", chunkSize, failAfter, short), func(t *testing.T) {
					writer := &interruptedContentValueWriter{remaining: failAfter, failure: failure}
					wantErr := failure
					if short {
						writer.failure, wantErr = nil, io.ErrShortWrite
					}
					e := contentJSONEncoder{writer: writer, limit: len(want)}
					e.valueEncoder = json.NewEncoder(chunkedContentValueWriter{&e, chunkSize})
					e.value(value)
					if !errors.Is(e.err, wantErr) || e.size != failAfter || !bytes.Equal(writer.Bytes(), want[:failAfter]) {
						t.Fatal("partial value failure lost", e.size, writer.Len(), e.err)
					}
					e.value("later")
					e.text("}")
					if !errors.Is(e.err, wantErr) || writer.Len() != failAfter {
						t.Fatal("output continued after failure", writer.Len(), e.err)
					}
				})
			}
		}
	}
}

func TestContentFileValueEncodingCapacity(t *testing.T) {
	value := "bounded <中文> \n"
	want, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunkSize := range []int{1, 7, len(want) + 1} {
		for _, limit := range []int{0, 1, len(want) - 1, len(want)} {
			t.Run(fmt.Sprintf("chunk-%d-limit-%d", chunkSize, limit), func(t *testing.T) {
				var output bytes.Buffer
				e := contentJSONEncoder{writer: &output, limit: limit}
				e.valueEncoder = json.NewEncoder(chunkedContentValueWriter{&e, chunkSize})
				e.value(value)
				if limit == len(want) {
					if e.err != nil || e.size != limit || !bytes.Equal(output.Bytes(), want) {
						t.Fatal("value newline used document capacity", e.size, output.Len(), e.err)
					}
				} else if !errors.Is(e.err, errContentFileCapacity) || e.size != output.Len() || output.Len() > limit || !bytes.HasPrefix(want, output.Bytes()) {
					t.Fatal("value exceeded capacity", e.size, output.Len(), e.err)
				}
			})
		}
	}
}
