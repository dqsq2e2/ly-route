package httpapi

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type procCPUCounters struct {
	total uint64
	idle  uint64
}

type procCPUSampler struct {
	mu       sync.Mutex
	read     func() (procCPUCounters, error)
	now      func() time.Time
	wait     func(time.Duration)
	before   procCPUCounters
	lastAt   time.Time
	last     float64
	hasValue bool
}

var hostCPUSampler = procCPUSampler{read: readProcCPUCounters, now: time.Now, wait: time.Sleep}

func parseProcStat() (float64, error) {
	return hostCPUSampler.sample()
}

func (sampler *procCPUSampler) sample() (float64, error) {
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	now := sampler.now()
	age := now.Sub(sampler.lastAt)
	if sampler.hasValue && age >= 0 && age < time.Second {
		return sampler.last, nil
	}
	after, err := sampler.read()
	if err != nil {
		return 0, err
	}
	before := sampler.before
	if !sampler.hasValue || age <= 0 || age > 30*time.Second {
		before = after
		sampler.wait(200 * time.Millisecond)
		after, err = sampler.read()
		if err != nil {
			return 0, err
		}
		now = sampler.now()
	}
	// Normal refreshes cover the entire interval, not a request-correlated
	// 200 ms burst from concurrent management-plane collectors.
	value, err := procCPUIntervalPercent(before, after)
	sampler.before, sampler.lastAt = after, now
	sampler.hasValue = err == nil
	if err == nil {
		sampler.last = value
	}
	return value, err
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
