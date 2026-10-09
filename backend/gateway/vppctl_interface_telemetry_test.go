package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseVPPInterfaceTelemetryStopsAtEveryInterfaceHeader(t *testing.T) {
	output := `              Name               Idx    State  MTU (L3/IP4/IP6/MPLS)     Counter          Count
lyroute-ens192                    1      up          9000/0/0/0     rx packets                   637
                                                                    rx bytes                  80459
                                                                    tx packets                 2630
                                                                    tx bytes                 323794
pppoe_session0                    8      up             0/0/0/0     rx packets                     6
                                                                    rx bytes                    472
                                                                    tx packets                    4
                                                                    tx bytes                    424
lyroute-ens224                    2      up          9000/0/0/0     rx packets                  7635
                                                                    rx bytes                 931460
                                                                    tx packets                 3107
                                                                    tx bytes                 339653
tap4096                           3      up          9000/0/0/0     rx packets                     7
                                                                    rx bytes                    746
                                                                    tx packets                    2
                                                                    tx bytes                    120
`

	items := parseVPPInterfaceTelemetry(output)
	if len(items) != 2 {
		t.Fatalf("interfaces = %#v, want two LY-Route physical interfaces", items)
	}
	if got := items[0]["rx_bytes"]; got != int64(80459) {
		t.Fatalf("LAN rx_bytes = %#v, want 80459", got)
	}
	if got := items[0]["tx_bytes"]; got != int64(323794) {
		t.Fatalf("LAN tx_bytes = %#v, want 323794", got)
	}
	if got := items[1]["rx_bytes"]; got != int64(931460) {
		t.Fatalf("WAN rx_bytes = %#v, want 931460", got)
	}
	if got := items[1]["tx_bytes"]; got != int64(339653) {
		t.Fatalf("WAN tx_bytes = %#v, want 339653", got)
	}
	for _, item := range items {
		if item["active_path"] != "vpp" || item["work_mode"] != "vpp" {
			t.Fatalf("live VPP interface must not depend on a historical attach receipt: %#v", item)
		}
	}
}

func TestInterfaceTelemetryReportsObservedLANSessions(t *testing.T) {
	for _, test := range []struct {
		name        string
		nat         string
		unavailable bool
		want        any
	}{
		{"two LAN sessions", "NAT44 ED sessions:\n i2o 192.168.88.120 proto TCP port 1234 fib 0\n i2o 192.168.88.120 proto UDP port 1235 fib 0\n i2o 192.0.2.10 proto TCP port 5555 fib 0\n", false, int64(2)},
		{"observed zero", "NAT44 ED sessions:\n-------- thread 0 vpp_main: 0 sessions --------\n", false, int64(0)},
		{"unavailable", "", true, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := vppctlInterfaceTelemetry{binary: "vppctl", run: func(_ context.Context, _ string, args ...string) (string, error) {
				switch strings.Join(args, " ") {
				case "show interface":
					return "lyroute-ge2 1 up 1500/0/0/0 rx bytes 1024\n tx bytes 512\nlyroute-spare 2 down 1500/0/0/0\n", nil
				case "show interface address":
					return "lyroute-ge2 (up):\n L3 192.168.88.66/24\nlyroute-spare (dn):\n", nil
				case "show nat44 sessions":
					if test.unavailable {
						return "", errors.New("no NAT")
					}
					return test.nat, nil
				case "show nat44 ei sessions detail":
					return "unknown input `show nat44 ei sessions detail'", nil
				default:
					t.Fatalf("unexpected command %v", args)
					return "", nil
				}
			}}
			items, err := collector.Interfaces(context.Background())
			if err != nil || len(items) != 2 {
				t.Fatalf("items = %#v, %v", items, err)
			}
			if got := items[0]["sessions"]; got != test.want {
				t.Fatalf("LAN sessions = %#v, want %#v", got, test.want)
			}
			if _, exists := items[1]["sessions"]; exists {
				t.Fatal("unaddressed interface must not fabricate zero sessions")
			}
			if items[1]["admin_state"] != "down" {
				t.Fatalf("down interface admin state = %#v", items[1])
			}
		})
	}
}

func TestInterfaceTelemetryRejectsNonInterfaceLines(t *testing.T) {
	if items := parseAllVPPInterfaceTelemetry("unknown input `show interface'\n"); len(items) != 0 {
		t.Fatalf("invalid CLI reply became interfaces: %#v", items)
	}
}
