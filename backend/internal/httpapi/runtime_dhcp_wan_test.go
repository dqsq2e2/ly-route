package httpapi

import (
	"context"
	"testing"

	"ly-route/backend/internal/persistence"
)

func TestRuntimeDHCPWANRetainsDesiredNAT(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:runtime-dhcp-wan-nat-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	oldInventory := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any { return nil }
	t.Cleanup(func() { hostInterfaceInventory = oldInventory })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "mgmt0")
	for _, item := range []map[string]any{
		{"id": "wan1", "interface_id": "wan1", "gateway_role": "wan", "type": "dhcp4", "nat": true},
		{"id": "wan2", "interface_id": "wan2", "gateway_role": "wan", "type": "dhcp4", "nat": false},
	} {
		if err := store.SaveConfig(ctx, configDocument(t, "wan_link", stringField(item, "id"), item, fixedClock()())); err != nil {
			t.Fatal(err)
		}
	}
	server := New(WithStore(store))
	assignments, err := server.runtimeAddressAssignments(ctx)
	if err != nil || len(assignments) != 2 {
		t.Fatalf("WAN assignments = %#v, %v", assignments, err)
	}
	for _, assignment := range assignments {
		if assignment.Mode != "dhcp4" || assignment.CIDR != "" || assignment.NAT != (assignment.ID == "wan1") {
			t.Fatalf("WAN addressing or NAT was lost: %#v", assignment)
		}
	}
}
