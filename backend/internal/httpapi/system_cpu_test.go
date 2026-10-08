package httpapi

import (
	"testing"
	"time"
)

func TestSystemCPUSamplerUsesWholeRefreshIntervalAndCachesConcurrentReads(t *testing.T) {
	now := time.Unix(1000, 0)
	reads := 0
	inputs := []procCPUCounters{{1000, 500}, {1080, 504}, {3080, 1504}}
	sampler := procCPUSampler{
		now:  func() time.Time { return now },
		wait: func(duration time.Duration) { now = now.Add(duration) },
		read: func() (procCPUCounters, error) {
			value := inputs[reads]
			reads++
			return value, nil
		},
	}
	if value, err := sampler.sample(); err != nil || value != 95 {
		t.Fatalf("initial spike = %v, %v", value, err)
	}
	if value, err := sampler.sample(); err != nil || value != 95 || reads != 2 {
		t.Fatalf("concurrent request must reuse sample: %v, %v, reads=%d", value, err, reads)
	}
	now = now.Add(5 * time.Second)
	if value, err := sampler.sample(); err != nil || value != 50 || reads != 3 {
		t.Fatalf("refresh interval must replace spike with measured 50%%: %v, %v", value, err)
	}
}

func TestSystemCPUIntervalPercent(t *testing.T) {
	for _, test := range []struct {
		name          string
		before, after procCPUCounters
		want          float64
		invalid       bool
	}{
		{"recent_idle_not_lifetime_average", procCPUCounters{10000, 1000}, procCPUCounters{10100, 1100}, 0, false},
		{"two_busy_of_four_cores", procCPUCounters{10000, 1000}, procCPUCounters{10400, 1200}, 50, false},
		{"fully_busy", procCPUCounters{10000, 1000}, procCPUCounters{10400, 1000}, 100, false},
		{"no_advance", procCPUCounters{10000, 1000}, procCPUCounters{10000, 1000}, 0, true},
		{"counter_reset", procCPUCounters{10000, 1000}, procCPUCounters{100, 10}, 0, true},
		{"idle_reset", procCPUCounters{10000, 1000}, procCPUCounters{10400, 900}, 0, true},
		{"invalid_idle_delta", procCPUCounters{10000, 1000}, procCPUCounters{10400, 1500}, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := procCPUIntervalPercent(test.before, test.after)
			if (err != nil) != test.invalid || (!test.invalid && value != test.want) {
				t.Fatalf("CPU = %v, %v; want %v, invalid=%t", value, err, test.want, test.invalid)
			}
		})
	}
}

func TestSystemCPUDecodeCountersExcludesGuest(t *testing.T) {
	counters, err := decodeProcCPUCounters("cpu 100 20 30 400 50 6 7 8 90 10\ncpu0 1 2 3 4\n")
	if err != nil || counters.total != 621 || counters.idle != 450 {
		t.Fatalf("counters = %#v, %v; guest must not be counted twice", counters, err)
	}
	for _, content := range []string{"", "cpu0 1 2 3 4 5 6 7 8", "cpu 1 2 3", "cpu 1 2 invalid 4 5 6 7 8", "cpu 0 0 0 0 0 0 0 0"} {
		if _, err := decodeProcCPUCounters(content); err == nil {
			t.Fatalf("invalid counters accepted: %q", content)
		}
	}
}
