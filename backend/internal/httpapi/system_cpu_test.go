package httpapi

import "testing"

func TestSystemCPUDecodeCountersMatchesOriginalBranch(t *testing.T) {
	counters, err := decodeProcCPUCounters("cpu 100 20 30 400 50 6 7 8 90 10\ncpu0 1 2 3 4\n")
	if err != nil || counters.total != 721 || counters.idle != 450 {
		t.Fatalf("counters = %#v, %v; want the original branch's complete aggregate row", counters, err)
	}
	percent := round2(float64(counters.total-counters.idle) * 100 / float64(counters.total))
	if percent != 37.59 {
		t.Fatalf("original cumulative CPU percent = %v, want 37.59", percent)
	}
	for _, content := range []string{"", "cpu0 1 2 3 4 5 6 7 8", "cpu 1 2 3", "cpu 1 2 invalid 4 5 6 7 8", "cpu 0 0 0 0 0 0 0 0", "cpu 1 2 3 4 5 6 7 invalid"} {
		if _, err := decodeProcCPUCounters(content); err == nil {
			t.Fatalf("invalid counters accepted: %q", content)
		}
	}
}

func TestSystemCPULifetimeAverageDoesNotBecomeLatestBurst(t *testing.T) {
	before, err := decodeProcCPUCounters("cpu 5000 0 0 5000 0 0 0 0")
	if err != nil {
		t.Fatal(err)
	}
	after, err := decodeProcCPUCounters("cpu 5190 0 0 5010 0 0 0 0")
	if err != nil {
		t.Fatal(err)
	}
	// The new 200 ticks are 95% busy; the original branch still reports
	// the lifetime average, without clipping or inventing an idle sample.
	percent := round2(float64(after.total-after.idle) * 100 / float64(after.total))
	if before.total != 10000 || percent != 50.88 {
		t.Fatalf("cumulative CPU after burst = %v, want 50.88", percent)
	}
}
