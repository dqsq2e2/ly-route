package httpapi

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type procCPUCounters struct {
	total uint64
	idle  uint64
}

func parseProcStat() (float64, error) {
	before, err := readProcCPUCounters()
	if err != nil {
		return 0, err
	}
	time.Sleep(200 * time.Millisecond)
	after, err := readProcCPUCounters()
	if err != nil {
		return 0, err
	}
	return procCPUIntervalPercent(before, after)
}

func readProcCPUCounters() (procCPUCounters, error) {
	content, err := os.ReadFile("/proc/stat")
	if err != nil {
		return procCPUCounters{}, fmt.Errorf("/proc/stat unavailable: %w", err)
	}
	return decodeProcCPUCounters(string(content))
}

func decodeProcCPUCounters(content string) (procCPUCounters, error) {
	line, _, _ := strings.Cut(content, "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != "cpu" {
		return procCPUCounters{}, fmt.Errorf("/proc/stat missing aggregate cpu row")
	}
	if len(fields) < 8 {
		return procCPUCounters{}, fmt.Errorf("/proc/stat aggregate cpu row is incomplete")
	}
	var counters procCPUCounters
	// guest and guest_nice are already included in user and nice.
	for index := 1; index < len(fields) && index <= 8; index++ {
		value, err := strconv.ParseUint(fields[index], 10, 64)
		if err != nil {
			return procCPUCounters{}, fmt.Errorf("/proc/stat has invalid cpu counter %q", fields[index])
		}
		counters.total += value
		if index == 4 || index == 5 {
			counters.idle += value
		}
	}
	if counters.total == 0 {
		return procCPUCounters{}, fmt.Errorf("/proc/stat aggregate cpu total is zero")
	}
	return counters, nil
}

func procCPUIntervalPercent(before, after procCPUCounters) (float64, error) {
	if after.total <= before.total || after.idle < before.idle {
		return 0, fmt.Errorf("/proc/stat cpu sampling counters did not advance monotonically")
	}
	total := after.total - before.total
	idle := after.idle - before.idle
	if idle > total {
		return 0, fmt.Errorf("/proc/stat cpu idle delta exceeds total delta")
	}
	return round2(float64(total-idle) * 100 / float64(total)), nil
}
