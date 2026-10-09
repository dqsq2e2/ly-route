package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
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

func TestVelo5x0InterfaceSnapshotUsesLiveVPPPathWithoutAttachReceipt(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{
			{"id": "enp0s20f2", "name": "enp0s20f2", "port_label": "GE1", "active_path": "kernel_stack", "work_mode": "kernel_stack", "link_state": "up"},
			{"id": "enp0s20f3", "name": "enp0s20f3", "port_label": "GE2", "active_path": "kernel_stack", "work_mode": "kernel_stack", "link_state": "up"},
			{"id": "lan1", "name": "lan1", "port_label": "LAN1", "active_path": "kernel_stack", "work_mode": "kernel_stack", "link_state": "down"},
		}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp0s20f2")
	server := New(WithVPPReceiptPath(filepath.Join(t.TempDir(), "missing-receipt.json")),
		WithInterfaceTelemetry(fakeInterfaceTelemetry{items: []map[string]any{
			{"id": "enp0s20f2", "vpp_interface": "lyroute-enp0s20f2", "active_path": "vpp", "work_mode": "vpp"},
			{"id": "enp0s20f3", "vpp_interface": "lyroute-enp0s20f3", "active_path": "vpp", "work_mode": "vpp", "rx_bytes": int64(1024), "tx_bytes": int64(2048)},
		}}))
	for _, endpoint := range []string{"/api/v1/interfaces", "/api/v1/interfaces/GE2/stats"} {
		response := request(t, server, http.MethodGet, endpoint)
		if response.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", endpoint, response.Code, response.Body.String())
		}
		var body struct {
			Items []map[string]any `json:"items"`
			Item  map[string]any   `json:"item"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Item != nil {
			body.Items = append(body.Items, body.Item)
		}
		foundGE2 := false
		for _, item := range body.Items {
			switch item["id"] {
			case "GE2":
				foundGE2 = true
				if item["active_path"] != "vpp" || item["work_mode"] != "vpp" || item["rx_bytes"] != float64(1024) || item["tx_bytes"] != float64(2048) {
					t.Fatalf("GE2 live path or counters lost: %#v", item)
				}
			case "GE1", "LAN1":
				if item["active_path"] != "kernel_stack" {
					t.Fatalf("management or unattached DSA port became VPP: %#v", item)
				}
			}
		}
		if !foundGE2 {
			t.Fatalf("%s omitted GE2: %s", endpoint, response.Body.String())
		}
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
