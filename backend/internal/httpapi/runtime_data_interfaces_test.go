package httpapi

import (
	"context"
	"reflect"
	"testing"

	"ly-route/backend/internal/persistence"
)

func TestRuntimeDataInterfacesRoleOnlyDoesNotBlockAddressedLAN(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:runtime-data-role-only-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	oldInventory := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any { return nil }
	t.Cleanup(func() { hostInterfaceInventory = oldInventory })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "mgmt0")
	for _, document := range []persistence.ConfigDocument{
		configDocument(t, "interface", "lan1", map[string]any{"id": "lan1", "gateway_role": "lan", "role_configured": true}, fixedClock()()),
		configDocument(t, "interface", "ge2", map[string]any{"id": "ge2", "gateway_role": "lan", "role_configured": true, "cidr": "192.0.2.1/24"}, fixedClock()()),
		configDocument(t, "interface", "wan0", map[string]any{"id": "wan0", "gateway_role": "wan", "role_configured": true}, fixedClock()()),
		configDocument(t, "wan_link", "dhcp-wan", map[string]any{"id": "dhcp-wan", "interface_id": "wan0", "gateway_role": "wan", "type": "dhcp4"}, fixedClock()()),
	} {
		if err := store.SaveConfig(ctx, document); err != nil {
			t.Fatal(err)
		}
	}
	server := New(WithStore(store))
	if got := server.runtimeDataInterfaces(ctx); !reflect.DeepEqual(got, []string{"ge2", "wan0"}) {
		t.Fatalf("requested data interfaces = %v", got)
	}
	if item, found, err := server.desiredItem(ctx, "interface", "lan1"); err != nil || !found || item["gateway_role"] != "lan" {
		t.Fatalf("role-only interface configuration was lost: %#v %v", item, err)
	}
}
