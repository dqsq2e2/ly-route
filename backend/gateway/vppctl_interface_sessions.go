package gateway

import (
	"context"
	"net/netip"
	"strings"
)

func decorateInterfaceNATSessions(ctx context.Context, binary string, run gatewayVPPCTLRunner, items []map[string]any) {
	if len(items) == 0 {
		return
	}
	output, err := run(ctx, binary, "show", "interface", "address")
	if err != nil {
		return
	}
	addresses, _, err := parseWANRuntimeAddresses(output)
	if err != nil {
		return
	}
	sources := map[netip.Addr]int64{}
	available := false
	for _, args := range [][]string{{"show", "nat44", "sessions"}, {"show", "nat44", "ei", "sessions", "detail"}} {
		output, err := run(ctx, binary, args...)
		if err != nil || !strings.HasPrefix(strings.TrimSpace(output), "NAT44") {
			continue
		}
		available = true
		for _, line := range strings.Split(output, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[0] != "i2o" || fields[2] != "proto" {
				continue
			}
			if address, err := netip.ParseAddr(fields[1]); err == nil {
				sources[address]++
			}
		}
	}
	if !available {
		return
	}
	for _, item := range items {
		current := addresses[telemetryString(item, "vpp_interface")]
		if len(current) == 0 {
			continue
		}
		count := int64(0)
		for source, sessions := range sources {
			for _, address := range current {
				if address.prefix.Masked().Contains(source) {
					count += sessions
					break
				}
			}
		}
		item["sessions"] = count
	}
}
