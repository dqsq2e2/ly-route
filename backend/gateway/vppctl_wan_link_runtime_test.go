package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ly-route/backend/internal/httpapi"
)

const wanTestAddresses = `local0 (dn):
lyroute-enp4s0f0 (up):
  L3 192.168.1.221/24
lyroute-other (up):
  L3 203.0.113.2/24
`

const wanTestDefault = `ipv4-VRF:0, fib_index:0,
0.0.0.0/0 fib:0 index:0
  DHCP refs:1
      [@0]: ipv4 via 192.168.1.1 lyroute-enp4s0f0: mtu:9000
 forwarding: unicast-ip4-chain
  [@0]: dpo-load-balance:
    [0] [@5]: ipv4 via 192.168.1.1 lyroute-enp4s0f0: mtu:9000
ipv4-VRF:100, fib_index:1,
0.0.0.0/0 fib:1 index:7
 forwarding: unicast-ip4-chain
  [@0]: dpo-receive: 0.0.0.0 on local0
`

func TestWANLinkRuntimeUsesLiveDHCPAndForwardingRoute(t *testing.T) {
	observer := vppctlWANLinkRuntime{binary: "vppctl"}
	requests := []httpapi.WANLinkRuntimeRequest{{Interface: "enp4s0f0", Mode: "dhcp4"}}
	bound := "[0] lyroute-enp4s0f0 state DHCP_BOUND installed 1 addr 192.168.1.221/24 gw 192.168.1.1\n"
	for _, test := range []struct {
		name, addresses, fib, dhcp string
		address                    string
		ready                      bool
	}{
		{"bound", wanTestAddresses, wanTestDefault, bound, "192.168.1.221/24", true},
		{"no client", wanTestAddresses, wanTestDefault, "", "", false},
		{"requesting", wanTestAddresses, wanTestDefault, strings.ReplaceAll(bound, "DHCP_BOUND", "DHCP_REQUEST"), "", false},
		{"not installed", wanTestAddresses, wanTestDefault, strings.ReplaceAll(bound, "installed 1", "installed 0"), "", false},
		{"lease drift", wanTestAddresses, wanTestDefault, strings.ReplaceAll(bound, ".221/24", ".222/24"), "", false},
		{"other WAN default", wanTestAddresses, strings.ReplaceAll(wanTestDefault, "lyroute-enp4s0f0:", "lyroute-other:"), bound, "192.168.1.221/24", false},
		{"inactive default", wanTestAddresses, strings.Replace(wanTestDefault, "[0] [@5]: ipv4 via 192.168.1.1 lyroute-enp4s0f0: mtu:9000", "[0] [@5]: dpo-drop ip4", 1), bound, "192.168.1.221/24", false},
		{"wrong VRF", strings.Replace(wanTestAddresses, "L3 192.168.1.221/24", "L3 192.168.1.221/24 ip4 table-id 100 fib-idx 1", 1), wanTestDefault, bound, "192.168.1.221/24", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			observer.run = func(_ context.Context, _ string, args ...string) (string, error) {
				switch strings.Join(args, " ") {
				case "show interface address":
					return test.addresses, nil
				case "show ip fib 0.0.0.0/0":
					return test.fib, nil
				case "show dhcp client":
					return test.dhcp, nil
				case "show interface":
					return "lyroute-enp4s0f0 1 up 9000/0/0/0 rx bytes 1024\n tx bytes 512\n", nil
				case "show nat44 sessions":
					return "NAT44 ED sessions:\n o2i 192.168.1.221 proto TCP port 42000 fib 0\n", nil
				default:
					t.Fatalf("unexpected command %v", args)
					return "", nil
				}
			}
			observed, err := observer.ObserveWANLinks(context.Background(), requests)
			if err != nil {
				t.Fatal(err)
			}
			current := observed["enp4s0f0"]
			if current.Address != test.address || current.RouteReady != test.ready {
				t.Fatalf("observation = %#v", current)
			}
			if current.RxBytes == nil || *current.RxBytes != 1024 || current.TxBytes == nil || *current.TxBytes != 512 {
				t.Fatalf("live counters = %#v", current)
			}
			if test.address != "" && (current.Sessions == nil || *current.Sessions != 1) {
				t.Fatalf("WAN sessions = %#v", current.Sessions)
			}
		})
	}
}

func TestWANLinkRuntimeIPv6AndInvalidReadback(t *testing.T) {
	observer := vppctlWANLinkRuntime{binary: "vppctl", run: func(_ context.Context, _ string, args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "show interface address":
			return "local0 (dn):\nlyroute-wan6 (up):\n L3 fe80::1/64\n L3 2001:db8::2/64 ip6 table-id 7 fib-idx 1\n", nil
		case "show ip6 fib ::/0":
			return "ipv6-VRF:7, fib_index:1,\n::/0 fib:1 index:7\n forwarding: unicast-ip6-chain\n [0] [@5]: ipv6 via fe80::2 lyroute-wan6: mtu:1500\n", nil
		case "show interface":
			return "", errors.New("no counters")
		case "show nat44 sessions", "show nat44 ei sessions detail":
			return "", errors.New("no NAT44")
		default:
			t.Fatalf("unexpected command %v", args)
			return "", nil
		}
	}}
	result, err := observer.ObserveWANLinks(context.Background(), []httpapi.WANLinkRuntimeRequest{{Interface: "wan6", Mode: "static6"}})
	if err != nil || result["wan6"].Address != "2001:db8::2/64" || !result["wan6"].RouteReady {
		t.Fatalf("IPv6 observation = %#v, %v", result, err)
	}
	if _, _, err := parseWANRuntimeAddresses("unknown input `show interface address'"); err == nil {
		t.Fatal("invalid address readback was accepted")
	}
	if _, err := parseWANRuntimeDefaultRoutes("unknown input `show ip fib'"); err == nil {
		t.Fatal("invalid route readback was accepted")
	}
	observer.run = func(context.Context, string, ...string) (string, error) {
		return "", errors.New("VPP unavailable")
	}
	if _, err := observer.ObserveWANLinks(context.Background(), []httpapi.WANLinkRuntimeRequest{{Interface: "wan0"}}); err == nil {
		t.Fatal("failed VPP command was accepted")
	}
}
