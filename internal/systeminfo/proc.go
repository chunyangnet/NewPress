package systeminfo

import (
	"bufio"
	"strconv"
	"strings"
)

// Parsing is platform-independent so Linux accounting rules are testable on
// every development host, including Windows.
func parseCPU(text string) cpuCounters {
	line, _, _ := strings.Cut(text, "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuCounters{}
	}
	var result cpuCounters
	// guest and guest_nice (fields 9+) are already included in user/nice.
	for i := 1; i < len(fields) && i <= 8; i++ {
		n, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil || n > ^uint64(0)-result.total {
			return cpuCounters{}
		}
		result.total += n
		if i == 4 || i == 5 {
			result.idle += n
		}
	}
	result.valid = true
	return result
}

func parseMemory(text string) *Memory {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		if fields[0] != "MemTotal:" && fields[0] != "MemAvailable:" {
			continue
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || n > ^uint64(0)/1024 {
			return nil
		}
		values[fields[0]] = n * 1024
	}
	total, ok := values["MemTotal:"]
	available, availableOK := values["MemAvailable:"]
	if !ok || !availableOK || total == 0 || available > total {
		return nil
	}
	return &Memory{TotalBytes: total, AvailableBytes: available}
}
