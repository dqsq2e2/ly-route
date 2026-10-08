package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVelo5x0HostInventoryPortLabels(t *testing.T) {
	for _, test := range []struct {
		name, pci, label string
		visible          bool
	}{
		{"renamed0", "0000:00:14.0", "", false},
		{"renamed1", "0000:00:14.1", "", false},
		{"enp0s20f2", "0000:00:14.2", "GE1", true},
		{"enp0s20f3", "0000:00:14.3", "GE2", true},
		{"enp4s0f0", "0000:04:00.0", "SFP1", true},
		{"enp4s0f1", "0000:04:00.1", "SFP2", true},
		{"lan1", "0000:00:14.1", "LAN1", true},
		{"lan8", "0000:00:14.0", "LAN8", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "net", test.name)
			device := filepath.Join(root, "pci", test.pci)
			if err := os.MkdirAll(path, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(device, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "type"), []byte("1\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(device, filepath.Join(path, "device")); err != nil {
				t.Fatal(err)
			}
			label, visible := hostInterfacePortLabel(path, test.name, "velo5x0")
			if label != test.label || visible != test.visible {
				t.Fatalf("port = %q, %t; want %q, %t", label, visible, test.label, test.visible)
			}
			if _, visible := hostInterfacePortLabel(path, test.name, "generic"); !visible {
				t.Fatal("5x0 exclusions must not affect generic hardware")
			}
		})
	}
}

func TestVelo5x0HostInventoryExcludesWirelessAndDSAMarkers(t *testing.T) {
	for _, marker := range []string{"wireless", "dsa", "upper_lan1", "upper_lan5"} {
		t.Run(marker, func(t *testing.T) {
			path := t.TempDir()
			if err := os.WriteFile(filepath.Join(path, "type"), []byte("1\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(path, marker), 0755); err != nil {
				t.Fatal(err)
			}
			if _, visible := hostInterfacePortLabel(path, "eth0", "velo5x0"); visible {
				t.Fatalf("%s interface must not be a physical Ethernet choice", marker)
			}
		})
	}
}

func TestVelo5x0InterfaceSnapshotKeepsPhysicalCarrierAndResolvesLabels(t *testing.T) {
	previous := hostInterfaceInventory
	items := []map[string]any{
		{"id": "enp0s20f2", "name": "enp0s20f2", "port_label": "GE1", "link_state": "up"},
		{"id": "enp4s0f0", "name": "enp4s0f0", "port_label": "SFP1", "link_state": "down"},
		{"id": "lan5", "name": "lan5", "port_label": "LAN5", "link_state": "lowerlayerdown"},
	}
	hostInterfaceInventory = func() []map[string]any { return items }
	t.Cleanup(func() { hostInterfaceInventory = previous })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp0s20f2")
	server := New()
	for label, actual := range map[string]string{"GE1": "enp0s20f2", "SFP1": "enp4s0f0", "LAN5": "lan5"} {
		if got := server.resolveInterfaceID(context.Background(), label); got != actual {
			t.Fatalf("resolve %s = %s, want %s", label, got, actual)
		}
	}
	merged := server.mergeInterfaceInventory(context.Background(), items, []map[string]any{
		{"id": "enp4s0f0", "vpp_interface": "lyroute-enp4s0f0", "link_state": "up"},
	})
	if merged[1]["link_state"] != "down" || merged[1]["vpp_link_state"] != "up" {
		t.Fatalf("VPP admin state must not replace the physical carrier: %#v", merged[1])
	}
	snapshot := server.normalizeInterfaceSnapshot(context.Background(), merged, "running", "", false)
	if snapshot[0]["id"] != "GE1" || snapshot[0]["gateway_role"] != "management" || snapshot[2]["id"] != "LAN5" {
		t.Fatalf("physical labels and management role lost: %#v", snapshot)
	}
}
