// Package systeminfo collects a small, read-only set of operating-system
// measurements. It never shells out, scans directories, or reads environment
// variables. Missing measurements remain nil instead of becoming zeroes.
package systeminfo

import (
	"math"
	"os"
	"runtime"
	"sync"
	"time"
)

type Memory struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type Disk struct {
	TotalBytes     uint64 `json:"total_bytes"`
	FreeBytes      uint64 `json:"free_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type Snapshot struct {
	CollectedAt      time.Time `json:"collected_at"`
	Hostname         string    `json:"hostname"`
	OS               string    `json:"os"`
	OSVersion        string    `json:"os_version"`
	Architecture     string    `json:"architecture"`
	CPUModel         string    `json:"cpu_model"`
	LogicalCPUs      int       `json:"logical_cpus"`
	CPUPercent       *float64  `json:"cpu_percent"`
	CPUSampleSeconds float64   `json:"cpu_sample_seconds"`
	CPUSupported     bool      `json:"cpu_supported"`
	UptimeSeconds    *float64  `json:"uptime_seconds"`
	Memory           *Memory   `json:"memory"`
	Disk             *Disk     `json:"disk"`
	ProcessRSSBytes  *uint64   `json:"process_rss_bytes"`
}

type cpuCounters struct {
	total, idle uint64
	valid       bool
}

// Collector is safe to share between requests. CPU utilization is the change
// in OS counters between observations, not an invented instantaneous reading.
type Collector struct {
	mu         sync.Mutex
	previous   cpuCounters
	previousAt time.Time
}

func (c *Collector) Collect(dataDir string) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	result, counters := collectPlatform(dataDir)
	result.CollectedAt = time.Now().UTC()
	result.Hostname, _ = os.Hostname()
	result.OS = runtime.GOOS
	result.Architecture = runtime.GOARCH
	result.LogicalCPUs = runtime.NumCPU()
	result.CPUSupported = counters.valid
	if elapsed := result.CollectedAt.Sub(c.previousAt); elapsed >= time.Second {
		result.CPUPercent = cpuPercent(c.previous, counters)
		if result.CPUPercent != nil {
			result.CPUSampleSeconds = elapsed.Seconds()
		}
		c.previous, c.previousAt = counters, result.CollectedAt
	}
	if result.Memory != nil && (result.Memory.TotalBytes == 0 || result.Memory.AvailableBytes > result.Memory.TotalBytes) {
		result.Memory = nil
	}
	if result.Disk != nil && (result.Disk.TotalBytes == 0 || result.Disk.FreeBytes > result.Disk.TotalBytes || result.Disk.AvailableBytes > result.Disk.TotalBytes) {
		result.Disk = nil
	}
	return result
}

func cpuPercent(previous, current cpuCounters) *float64 {
	if !previous.valid || !current.valid || current.total <= previous.total || current.idle < previous.idle {
		return nil
	}
	total, idle := current.total-previous.total, current.idle-previous.idle
	if idle > total {
		return nil
	}
	value := math.Round(1000*float64(total-idle)/float64(total)) / 10
	return &value
}
