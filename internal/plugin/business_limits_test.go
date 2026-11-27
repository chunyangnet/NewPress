package plugin

import (
	"context"
	"encoding/binary"
	"testing"
	"time"
)

// A tiny real module gives deterministic hostile exports without a toolchain.
func limitModule(body []byte, start bool) []byte {
	out := []byte{0, 97, 115, 109, 1, 0, 0, 0}
	section := func(id byte, data []byte) {
		out = append(out, id)
		out = binary.AppendUvarint(out, uint64(len(data)))
		out = append(out, data...)
	}
	section(1, []byte{5, 0x60, 0, 1, 0x7e, 0x60, 1, 0x7f, 1, 0x7f, 0x60, 2, 0x7f, 0x7f, 0, 0x60, 2, 0x7f, 0x7f, 1, 0x7e, 0x60, 0, 0})
	section(3, []byte{5, 0, 1, 2, 3, 4})
	section(5, []byte{1, 1, 1, 1})
	exports := []byte{5}
	for i, name := range []string{"np_api_version", "np_alloc", "np_free", "np_handle", "memory"} {
		exports = append(exports, byte(len(name)))
		exports = append(exports, []byte(name)...)
		if i == 4 {
			exports = append(exports, 2, 0)
		} else {
			exports = append(exports, 0, byte(i))
		}
	}
	section(7, exports)
	if start {
		section(8, []byte{4})
	}
	bodies := [][]byte{{0, 0x42, 1, 0x0b}, {0, 0x41, 0x80, 0x08, 0x0b}, {0, 0x0b}, append([]byte{0}, body...), {0, 0x03, 0x40, 0x0c, 0, 0x0b, 0x0b}}
	code := []byte{5}
	for _, f := range bodies {
		code = binary.AppendUvarint(code, uint64(len(f)))
		code = append(code, f...)
	}
	section(10, code)
	return out
}
func TestBusinessInterruptsLoopAndStartAndRejectsOutOfBounds(t *testing.T) {
	manifest := Manifest{Slug: "hostile", Hooks: map[string][]string{"content.render": {"np_handle"}}}
	loop := []byte{0x03, 0x40, 0x0c, 0, 0x0b, 0x42, 0, 0x0b}
	for _, start := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		began := time.Now()
		_, e := executeBusiness(ctx, limitModule(loop, start), manifest, "np_handle", []byte(`{}`), nil)
		cancel()
		if e == nil || time.Since(began) > 2*time.Second {
			t.Fatal("loop was not bounded", start, e)
		}
	}
	// Encoded pointer = 0xffffffff, length = 16. Signed LEB of -4294967280.
	n := int64(-4294967280)
	constant := []byte{0x42}
	for {
		b := byte(n & 127)
		n >>= 7
		done := (n == 0 && b&64 == 0) || (n == -1 && b&64 != 0)
		if !done {
			b |= 128
		}
		constant = append(constant, b)
		if done {
			break
		}
	}
	constant = append(constant, 0x0b)
	if _, e := executeBusiness(t.Context(), limitModule(constant, false), manifest, "np_handle", []byte(`{}`), nil); e == nil {
		t.Fatal("out of bounds response accepted")
	}
}
