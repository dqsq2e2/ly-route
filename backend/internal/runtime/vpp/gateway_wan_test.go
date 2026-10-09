package vpp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/nat"
)

func TestGatewayWANOperationsIncludeDHCPAndNATEgress(t *testing.T) {
	plan := provenPlan(Plan{RequestID: "wan-business", AddressAssignments: []AddressAssignment{
		{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Role: "lan", CIDR: "192.0.2.1/24"},
		{ID: "wan", LinuxInterface: "eth2", VPPInterface: "lyroute-eth2", Role: "wan", Mode: "dhcp4", NAT: true},
	}}, "eth1", "eth2")
	interfaces, err := supplementalOperations(plan, SupplementalInterfaces)
	if err != nil {
		t.Fatal(err)
	}
	foundDHCP := false
	for _, operation := range interfaces {
		if assignment, ok := operation.Payload.(AddressAssignment); ok && assignment.Mode == "dhcp4" {
			foundDHCP = operationHasCommand(operation, "set dhcp client intfc lyroute-eth2")
		}
	}
	if !foundDHCP {
		t.Fatal("production interface owner omitted the WAN DHCP client")
	}
	routes, err := supplementalOperations(plan, SupplementalRoutes)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Name != "vpp.nat44.egress" {
		t.Fatalf("NAT egress was omitted: %#v", routes)
	}
	for _, command := range []string{"nat44 plugin enable", "set interface nat44 in lyroute-eth1", "set interface nat44 out lyroute-eth2", "nat44 add interface address lyroute-eth2"} {
		if !operationHasCommand(routes[0], command) {
			t.Fatalf("missing %s", command)
		}
	}
	plan.AddressAssignments[1].NAT = false
	if got := gatewayNATEgressOperations(plan); len(got) != 0 {
		t.Fatalf("disabled NAT produced egress operations: %#v", got)
	}
}

func TestGatewayDHCPReadbackRequiresAnExactClient(t *testing.T) {
	for _, test := range []struct {
		output string
		want   bool
	}{
		{"[0] lyroute-wan state DHCP_DISCOVER installed 0 addr 0.0.0.0/0\n", true},
		{"[0] lyroute-wan state DHCP_BOUND installed 1 addr 198.51.100.4/24\n", true},
		{"[0] lyroute-other state DHCP_BOUND installed 1 addr 198.51.100.4/24\n", false},
		{"show dhcp client: unknown input\n", false},
		{"lyroute-wan present\n", false},
		{"", false},
	} {
		if got := gatewayDHCPClientPresent(test.output, "lyroute-wan"); got != test.want {
			t.Fatalf("DHCP readback %q = %t, want %t", test.output, got, test.want)
		}
	}
}

func TestGatewayDHCPApplyPreservesExistingLease(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "vppctl")
	log := filepath.Join(directory, "commands")
	t.Setenv("WAN_TEST_LOG", log)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$WAN_TEST_LOG"
case "$*" in
  'show dhcp client') printf '[0] lyroute-wan state DHCP_BOUND installed 1 addr 198.51.100.4/24\n' ;;
  'show interface address lyroute-wan') printf 'lyroute-wan (up):\n  L3 198.51.100.4/24\n' ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	assignment := AddressAssignment{ID: "wan", VPPInterface: "lyroute-wan", Mode: "dhcp4", RemoveCIDRs: []string{"192.0.2.4/24"}}
	channel := vppctlChannel{binary: binary}
	reply, err := channel.Do(context.Background(), Operation{Name: "vpp.interface.address", Payload: assignment})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reply.Payload.(VPPCTLReplyPayload); !ok {
		t.Fatal("DHCP apply lacks typed readback")
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "set dhcp client") || strings.Contains(string(commands), "set interface ip address del") {
		t.Fatalf("existing lease was restarted: %s", commands)
	}
}

func TestGatewayDHCPApplyStartsMissingClient(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "vppctl")
	log := filepath.Join(directory, "commands")
	t.Setenv("WAN_TEST_LOG", log)
	t.Setenv("WAN_TEST_STATE", filepath.Join(directory, "started"))
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$WAN_TEST_LOG"
case "$*" in
  'set dhcp client intfc lyroute-wan') touch "$WAN_TEST_STATE" ;;
  'show dhcp client')
    if [ -f "$WAN_TEST_STATE" ]; then
      printf '[0] lyroute-wan state DHCP_DISCOVER installed 0 addr 0.0.0.0/0\n'
    fi ;;
  'show interface address lyroute-wan') printf 'lyroute-wan (up):\n' ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	assignment := AddressAssignment{ID: "wan", VPPInterface: "lyroute-wan", Mode: "dhcp4"}
	channel := vppctlChannel{binary: binary}
	if _, err := channel.Do(context.Background(), Operation{Name: "vpp.interface.address", Payload: assignment}); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(commands), "set dhcp client intfc lyroute-wan") {
		t.Fatalf("missing client was not started: %s, %v", commands, err)
	}
}

func TestGatewayDHCPAddressContractDoesNotDeleteLearnedIPv4(t *testing.T) {
	observed := InterfaceState{Name: "lyroute-wan", AdminState: "up", LinkState: "up", Addresses: []string{"198.51.100.4/24"}}
	desired := InterfaceState{Name: "lyroute-wan", AdminState: "up", LinkState: "up", AddressMode: "dhcp4"}
	if !InterfaceStateMatchesDesired(observed, desired) {
		t.Fatal("DHCP-owned IPv4 was treated as static drift")
	}
	desired.AddressMode = ""
	if InterfaceStateMatchesDesired(observed, desired) {
		t.Fatal("unexpected static IPv4 was accepted")
	}
	desired.AddressMode = "dhcp4"
	observed.Addresses = []string{"not-an-address"}
	if InterfaceStateMatchesDesired(observed, desired) {
		t.Fatal("malformed dynamic address was accepted")
	}
}

func TestGatewayNATEgressReadbackProvesRolesAndAddressTracking(t *testing.T) {
	for _, behavior := range []nat.Behavior{nat.BehaviorEndpointDependent, nat.BehaviorFullCone} {
		payload := GatewayNATEgress{Inside: []string{"lyroute-lan"}, Outside: "lyroute-wan", Behavior: behavior}
		prefix := gatewayNATPrefix(behavior)
		results := []VPPCTLCommandResult{
			{Command: "show " + prefix + " interfaces", Stdout: "NAT44 interfaces:\n  lyroute-lan in\n  lyroute-wan out\n"},
			{Command: "show " + prefix + " interface address", Stdout: "NAT44 pool address interfaces:\n  lyroute-wan\n"},
		}
		if err := verifyGatewayNATEgress(payload, results); err != nil {
			t.Fatal(err)
		}
		results[1].Stdout = "NAT44 pool address interfaces:\n"
		if err := verifyGatewayNATEgress(payload, results); err == nil {
			t.Fatal("missing address tracking was accepted")
		}
		results[1].Stdout = "lyroute-wan"
		results[0].Stdout = "lyroute-lan out\nlyroute-wan in\n"
		if err := verifyGatewayNATEgress(payload, results); err == nil {
			t.Fatal("reversed NAT roles were accepted")
		}
	}
}

func TestGatewayNATEgressCleanupRetainsSharedInsideOnly(t *testing.T) {
	prior := Plan{RequestID: "before", AddressAssignments: []AddressAssignment{
		{ID: "lan", VPPInterface: "lyroute-lan", Role: "lan", CIDR: "192.0.2.1/24"},
		{ID: "wan1", VPPInterface: "lyroute-wan1", Role: "wan", NAT: true},
		{ID: "wan2", VPPInterface: "lyroute-wan2", Role: "wan", NAT: true},
	}}
	desired := prior
	desired.AddressAssignments = append([]AddressAssignment(nil), prior.AddressAssignments...)
	desired.AddressAssignments[1].NAT = false
	cleanup, err := SupplementalReconciliationCleanupOperations(prior, desired, SupplementalRoutes)
	if err != nil || len(cleanup) != 1 {
		t.Fatalf("cleanup = %#v, %v", cleanup, err)
	}
	if operationHasCommand(cleanup[0], "in lyroute-lan del") || !operationHasCommand(cleanup[0], "out lyroute-wan1 del") {
		t.Fatalf("retiring one WAN damaged a shared inside role: %#v", cleanup[0])
	}
	desired.AddressAssignments[2].NAT = false
	cleanup, err = SupplementalReconciliationCleanupOperations(prior, desired, SupplementalRoutes)
	if err != nil || len(cleanup) != 2 || !operationHasCommand(cleanup[0], "in lyroute-lan del") {
		t.Fatalf("retiring all WANs left a NAT inside role: %#v, %v", cleanup, err)
	}
}
