package plugin

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"newpress.local/newpress/internal/packagefs"
)

const (
	ABIVersion         = uint64(1)
	wasmPageBytes      = uint32(64 * 1024)
	defaultMemoryMiB   = uint32(32)
	defaultCallTimeout = 250 * time.Millisecond
	maxModuleBytes     = int64(32 << 20)
)

type SandboxOptions struct {
	MemoryLimitMiB uint32
	CallTimeout    time.Duration
}

type ProbeResult struct {
	ABIVersion uint64 `json:"abi_version"`
	SizeBytes  int64  `json:"size_bytes"`
}

func DefaultSandboxOptions() SandboxOptions {
	return SandboxOptions{MemoryLimitMiB: defaultMemoryMiB, CallTimeout: defaultCallTimeout}
}

// ProbeFile compiles and cold-instantiates a plugin with no filesystem, network,
// environment, process or WASI imports. A valid v1 plugin exports
// `np_api_version() -> i64` and returns ABIVersion.
func ProbeFile(ctx context.Context, path, moduleName string, options SandboxOptions) (ProbeResult, error) {
	if ctx == nil {
		return ProbeResult{}, fmt.Errorf("plugin probe context is required")
	}
	moduleBytes, size, err := readModule(path)
	if err != nil {
		return ProbeResult{}, err
	}
	if options.MemoryLimitMiB == 0 {
		options.MemoryLimitMiB = defaultMemoryMiB
	}
	if options.CallTimeout <= 0 {
		options.CallTimeout = defaultCallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, options.CallTimeout)
	defer cancel()

	pages64 := uint64(options.MemoryLimitMiB) * 1024 * 1024 / uint64(wasmPageBytes)
	if pages64 == 0 || pages64 > 65_536 {
		return ProbeResult{}, fmt.Errorf("plugin memory limit must be between 1 MiB and 4096 MiB")
	}
	pages := uint32(pages64)
	cache := wazero.NewCompilationCache()
	defer cache.Close(ctx)
	runtimeConfig := wazero.NewRuntimeConfigCompiler().
		WithMemoryLimitPages(pages).
		WithCloseOnContextDone(true).
		WithCompilationCache(cache)
	runtime := wazero.NewRuntimeWithConfig(ctx, runtimeConfig)
	defer runtime.Close(ctx)
	if err := registerHost(ctx, runtime, nil); err != nil {
		return ProbeResult{}, err
	}

	compiled, err := runtime.CompileModule(ctx, moduleBytes)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("compile WebAssembly plugin: %w", err)
	}
	defer compiled.Close(ctx)

	module, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(moduleName).WithStartFunctions())
	if err != nil {
		return ProbeResult{}, fmt.Errorf("instantiate WebAssembly plugin without capabilities: %w", err)
	}
	defer module.Close(ctx)

	function := module.ExportedFunction("np_api_version")
	if function == nil {
		return ProbeResult{}, fmt.Errorf("plugin does not export np_api_version")
	}
	if !signature(module, "np_api_version", nil, []api.ValueType{api.ValueTypeI64}) {
		return ProbeResult{}, fmt.Errorf("plugin np_api_version must have signature () -> i64")
	}
	callContext, cancel := context.WithTimeout(ctx, options.CallTimeout)
	defer cancel()
	results, err := function.Call(callContext)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("call np_api_version: %w", err)
	}
	if len(results) != 1 || results[0] != ABIVersion {
		return ProbeResult{}, fmt.Errorf("plugin ABI is %v; NewPress requires %d", results, ABIVersion)
	}
	return ProbeResult{ABIVersion: results[0], SizeBytes: size}, nil
}

func readModule(path string) ([]byte, int64, error) {
	file, info, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open plugin module: %w", err)
	}
	defer file.Close()
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxModuleBytes {
		return nil, 0, fmt.Errorf("plugin module size must be between 1 and %d bytes", maxModuleBytes)
	}
	moduleBytes, err := io.ReadAll(io.LimitReader(file, maxModuleBytes+1))
	if err != nil {
		return nil, 0, err
	}
	actualSize := int64(len(moduleBytes))
	if actualSize <= 0 || actualSize > maxModuleBytes {
		return nil, 0, fmt.Errorf("plugin module size must be between 1 and %d bytes", maxModuleBytes)
	}
	if len(moduleBytes) < 8 || string(moduleBytes[:4]) != "\x00asm" {
		return nil, 0, fmt.Errorf("plugin entry is not a WebAssembly binary")
	}
	return moduleBytes, actualSize, nil
}
