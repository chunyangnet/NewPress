//go:build !windows && !linux

package systeminfo

func collectPlatform(string) (Snapshot, cpuCounters) {
	return Snapshot{}, cpuCounters{}
}
