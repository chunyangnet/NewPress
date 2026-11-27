package plugin

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

// Replace one export's body in a real ABI module. The normal handler returns
// an empty JSON object at address 2048, so failures in np_free are reachable.
func businessFailureModule(t *testing.T, export string, body []byte) []byte {
	t.Helper()
	value := uint64(2048)<<32 | 2
	handler := []byte{0x42}
	for {
		b := byte(value & 127)
		value >>= 7
		if value == 0 && b&64 == 0 {
			handler = append(handler, b, 0x0b)
			break
		}
		handler = append(handler, b|128)
	}
	module := limitModule(handler, false)
	target := slices.Index([]string{"np_api_version", "np_alloc", "np_free", "np_handle"}, export)
	if target < 0 {
		t.Fatal("unknown failure export", export)
	}
	for offset := 8; offset < len(module); {
		id := module[offset]
		size, n := binary.Uvarint(module[offset+1:])
		start := offset + 1 + n
		end := start + int(size)
		if id != 10 {
			offset = end
			continue
		}
		count, n := binary.Uvarint(module[start:])
		pos := start + n
		code := binary.AppendUvarint(nil, count)
		for index := range int(count) {
			size, n = binary.Uvarint(module[pos:])
			pos += n
			function := module[pos : pos+int(size)]
			pos += int(size)
			if index == target {
				function = append([]byte{0}, body...)
			}
			code = binary.AppendUvarint(code, uint64(len(function)))
			code = append(code, function...)
		}
		out := append([]byte(nil), module[:offset]...)
		out = append(out, 10)
		out = binary.AppendUvarint(out, uint64(len(code)))
		out = append(out, code...)
		out = append(out, module[end:]...)
		data := []byte{1, 0, 0x41, 0x80, 0x10, 0x0b, 2, '{', '}'}
		out = append(out, 11, byte(len(data)))
		return append(out, data...)
	}
	t.Fatal("missing code section")
	return nil
}

func TestBusinessExecutionPreservesFailureCause(t *testing.T) {
	manifest := Manifest{Slug: "failure-cause", Hooks: map[string][]string{"admin.action": {"np_handle"}}}
	for _, export := range []string{"np_api_version", "np_alloc", "np_handle", "np_free"} {
		t.Run(export+"/trap", func(t *testing.T) {
			code := businessFailureModule(t, export, []byte{0, 0x0b})
			_, err := executeBusiness(t.Context(), code, manifest, "np_handle", []byte(`{}`), nil)
			if err == nil || !strings.Contains(err.Error(), export) || !strings.Contains(err.Error(), "unreachable") {
				t.Fatalf("failure lost its export or underlying trap: %v", err)
			}
		})
		t.Run(export+"/cancel", func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			ctx = experimental.WithFunctionListenerFactory(ctx, experimental.FunctionListenerFactoryFunc(func(def api.FunctionDefinition) experimental.FunctionListener {
				if !slices.Contains(def.ExportNames(), export) {
					return nil
				}
				return experimental.FunctionListenerFunc(func(context.Context, api.Module, api.FunctionDefinition, []uint64, experimental.StackIterator) {
					called = true
					cancel()
				})
			}))
			loop := []byte{0x03, 0x40, 0x0c, 0, 0x0b, 0, 0x0b}
			_, err := executeBusiness(ctx, businessFailureModule(t, export, loop), manifest, "np_handle", []byte(`{}`), nil)
			if !called || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), export) {
				t.Fatalf("failure lost cancellation at %s: called=%v err=%v", export, called, err)
			}
		})
	}
}

func TestBusinessExecutionPreservesDeadlineCause(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	loop := []byte{0x03, 0x40, 0x0c, 0, 0x0b, 0, 0x0b}
	manifest := Manifest{Slug: "deadline-cause", Hooks: map[string][]string{"admin.action": {"np_handle"}}}
	_, err := executeBusiness(ctx, businessFailureModule(t, "np_handle", loop), manifest, "np_handle", []byte(`{}`), nil)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "np_handle") {
		t.Fatalf("failure lost its execution deadline: %v", err)
	}
}
