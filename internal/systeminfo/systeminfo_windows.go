package systeminfo

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	getSystemTimes       = kernel32.NewProc("GetSystemTimes")
	globalMemoryStatus   = kernel32.NewProc("GlobalMemoryStatusEx")
	getProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
)

type memoryStatus struct {
	Length, Load                                             uint32
	TotalPhysical, AvailablePhysical                         uint64
	TotalPageFile, AvailablePageFile                         uint64
	TotalVirtual, AvailableVirtual, AvailableExtendedVirtual uint64
}

type processMemoryCounters struct {
	Size, PageFaultCount                               uint32
	PeakWorkingSetSize, WorkingSetSize                 uintptr
	QuotaPeakPagedPoolUsage, QuotaPagedPoolUsage       uintptr
	QuotaPeakNonPagedPoolUsage, QuotaNonPagedPoolUsage uintptr
	PagefileUsage, PeakPagefileUsage                   uintptr
}

func collectPlatform(dataDir string) (Snapshot, cpuCounters) {
	result := Snapshot{}
	version := windows.RtlGetVersion()
	result.OSVersion = fmt.Sprintf("Windows NT %d.%d · Build %d", version.MajorVersion, version.MinorVersion, version.BuildNumber)
	if key, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		result.CPUModel, _, _ = key.GetStringValue("ProcessorNameString")
		result.CPUModel = strings.TrimSpace(result.CPUModel)
		key.Close()
	}
	uptime := windows.DurationSinceBoot().Seconds()
	result.UptimeSeconds = &uptime
	status := memoryStatus{}
	status.Length = uint32(unsafe.Sizeof(status))
	if ok, _, _ := globalMemoryStatus.Call(uintptr(unsafe.Pointer(&status))); ok != 0 {
		result.Memory = &Memory{TotalBytes: status.TotalPhysical, AvailableBytes: status.AvailablePhysical}
	}
	if dataDir != "" {
		if path, err := windows.UTF16PtrFromString(dataDir); err == nil {
			disk := Disk{}
			if windows.GetDiskFreeSpaceEx(path, &disk.AvailableBytes, &disk.TotalBytes, &disk.FreeBytes) == nil {
				result.Disk = &disk
			}
		}
	}
	process := processMemoryCounters{}
	process.Size = uint32(unsafe.Sizeof(process))
	if ok, _, _ := getProcessMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&process)), uintptr(process.Size)); ok != 0 {
		rss := uint64(process.WorkingSetSize)
		result.ProcessRSSBytes = &rss
	}
	var idle, kernel, user windows.Filetime
	counters := cpuCounters{}
	if ok, _, _ := getSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok != 0 {
		ticks := func(t windows.Filetime) uint64 { return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime) }
		counters = cpuCounters{total: ticks(kernel) + ticks(user), idle: ticks(idle), valid: true}
	}
	return result, counters
}
