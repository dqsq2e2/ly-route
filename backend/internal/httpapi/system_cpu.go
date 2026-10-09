package httpapi

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type procCPUCounters struct {
	total uint64
	idle  uint64
}

func parseProcStat() (float64, error) {
	counters, err := readProcCPUCounters()
	if err != nil {
		return 0, err
	}
	// Match origin/main: cumulative CPU usage since boot, not a refresh interval.
	return round2(float64(counters.total-counters.idle) * 100 / float64(counters.total)), nil
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
	for index := 1; index < len(fields); index++ {
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
