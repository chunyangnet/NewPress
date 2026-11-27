package systeminfo

import (
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func collectPlatform(dataDir string) (Snapshot, cpuCounters) {
	result := Snapshot{OSVersion: strings.TrimSpace(readSystemFile("/proc/sys/kernel/osrelease"))}
	for _, line := range strings.Split(readSystemFile("/proc/cpuinfo"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && (strings.TrimSpace(key) == "model name" || strings.TrimSpace(key) == "Hardware") {
			result.CPUModel = strings.TrimSpace(value)
			break
		}
	}
	var info unix.Sysinfo_t
	if unix.Sysinfo(&info) == nil {
		uptime := float64(info.Uptime)
		result.UptimeSeconds = &uptime
	}
	result.Memory = parseMemory(readSystemFile("/proc/meminfo"))
	if dataDir != "" {
		var stat unix.Statfs_t
		if unix.Statfs(dataDir, &stat) == nil && stat.Bsize > 0 {
			result.Disk = &Disk{TotalBytes: stat.Blocks * uint64(stat.Bsize), FreeBytes: stat.Bfree * uint64(stat.Bsize), AvailableBytes: stat.Bavail * uint64(stat.Bsize)}
		}
	}
	if fields := strings.Fields(readSystemFile("/proc/self/statm")); len(fields) >= 2 {
		if pages, err := strconv.ParseUint(fields[1], 10, 64); err == nil && pages <= ^uint64(0)/uint64(os.Getpagesize()) {
			rss := pages * uint64(os.Getpagesize())
			result.ProcessRSSBytes = &rss
		}
	}
	return result, parseCPU(readSystemFile("/proc/stat"))
}

func readSystemFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	return string(data)
}
