package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeFileRejectsWrongABIReturnType(t *testing.T) {
	// Returning the integer 1 is not sufficient: ABI v1 requires an i64 result.
	for _, valueType := range []byte{0x7f, 0x7d, 0x7c} {
		t.Run(map[byte]string{0x7f: "i32", 0x7d: "f32", 0x7c: "f64"}[valueType], func(t *testing.T) {
			module := abiV1Module()
			module[14] = valueType
			switch valueType {
			case 0x7f:
				module[len(module)-3] = 0x41 // i32.const 1
			case 0x7d:
				// The raw f32 bits are 1, which api.Function.Call exposes as uint64(1).
				module = append(module[:len(module)-8], 10, 9, 1, 7, 0, 0x43, 1, 0, 0, 0, 0x0b)
			case 0x7c:
				module = append(module[:len(module)-8], 10, 13, 1, 11, 0, 0x44, 1, 0, 0, 0, 0, 0, 0, 0, 0x0b)
			}
			path := filepath.Join(t.TempDir(), "wrong-abi.wasm")
			if err := os.WriteFile(path, module, 0600); err != nil {
				t.Fatal(err)
			}
			if result, err := ProbeFile(t.Context(), path, "wrong-abi", DefaultSandboxOptions()); err == nil {
				t.Fatalf("accepted wrong return type as ABI v%d", result.ABIVersion)
			}
		})
	}
}
