#!/usr/bin/env python3
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "packaging/rootfs-overlay/usr/lib/ly-route/prepare-vfio.sh"
DATA_PCI = "0000:04:00.1"
MANAGEMENT_PCI = "0000:00:14.2"


class VFIOPreflightTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.sys = self.root / "sys"
        self.network = self.root / "network.json"
        self.startup = self.root / "startup.conf"
        self.startup.write_text("plugins { plugin dpdk_plugin.so { disable } }\n")
        self.mutations = self.root / "mutations"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        for command in ["modprobe", "ip", "sed"]:
            shim = self.bin / command
            shim.write_text('#!/bin/sh\nprintf "%s\\n" "$0 $*" >> "$TEST_MUTATIONS"\nexit 1\n')
            shim.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.bin) + ":" + os.environ["PATH"],
                        LY_ROUTE_SYSFS_ROOT=str(self.sys),
                        LY_ROUTE_VFIO_NETWORK=str(self.network),
                        LY_ROUTE_VFIO_DEVICES=str(self.root / "devices"),
                        LY_ROUTE_VFIO_STARTUP=str(self.startup),
                        TEST_MUTATIONS=str(self.mutations))
        (self.sys / "module/vfio_pci").mkdir(parents=True)
        hugepages = self.sys / "kernel/mm/hugepages/hugepages-2048kB/nr_hugepages"
        hugepages.parent.mkdir(parents=True)
        hugepages.write_text("64\n")
        self.device(DATA_PCI)
        self.write_network([DATA_PCI])

    def write_network(self, devices):
        self.network.write_text(json.dumps({
            "management": {"pci": MANAGEMENT_PCI},
            "data_interfaces": [{"pci": pci, "selected": {"tier": "vpp_dpdk", "hook": "dpdk"}} for pci in devices],
        }))

    def device(self, pci, group="17", driver="igb"):
        device = self.sys / "bus/pci/devices" / pci
        device.mkdir(parents=True)
        (device / "driver_override").write_text("unchanged\n")
        driver_path = self.sys / "bus/pci/drivers" / driver
        driver_path.mkdir(parents=True, exist_ok=True)
        (driver_path / "unbind").write_text("unchanged\n")
        (device / "driver").symlink_to(driver_path)
        if group is not None:
            group_path = self.sys / "kernel/iommu_groups" / group
            (group_path / "devices").mkdir(parents=True, exist_ok=True)
            (device / "iommu_group").symlink_to(group_path)
            (group_path / "devices" / pci).symlink_to(device)
        return device

    def check(self, *args, success=True, reason=None):
        before = {str(path): path.read_bytes() for path in self.root.rglob("*") if path.is_file()}
        result = subprocess.run(["sh", str(SCRIPT), *args], env=self.env,
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        if reason:
            self.assertIn(reason, result.stdout + result.stderr)
        after = {str(path): path.read_bytes() for path in self.root.rglob("*") if path.is_file()}
        self.assertEqual(before, after, "read-only check changed fixture files")
        self.assertFalse(self.mutations.exists(), "read-only check executed a modifying command")
        return result

    def test_isolated_dpdk_mapping_is_checked_without_binding(self):
        self.check("--check", reason="ownership check passed")

    def test_explicit_spare_port_check_is_read_only(self):
        self.write_network([])
        self.check("--check", "--pci", DATA_PCI, reason="ownership check passed")

    def test_missing_iommu_group_is_rejected(self):
        (self.sys / "bus/pci/devices" / DATA_PCI / "iommu_group").unlink()
        self.check("--check", "--pci", DATA_PCI, success=False, reason="no isolated IOMMU group")

    def test_group_shared_with_management_is_rejected(self):
        self.device(MANAGEMENT_PCI)
        self.check("--check", success=False, reason="shared by " + MANAGEMENT_PCI)

    def test_group_shared_with_unselected_device_is_rejected(self):
        self.device("0000:04:00.0")
        self.check("--check", success=False, reason="shared by 0000:04:00.0")

    def test_group_shared_only_with_selected_devices_is_accepted(self):
        self.device("0000:04:00.0")
        self.write_network([DATA_PCI, "0000:04:00.0"])
        self.check("--check", reason="ownership check passed")

    def test_management_device_is_never_eligible(self):
        self.check("--check", "--pci", MANAGEMENT_PCI, success=False, reason="includes management PCI")

    def test_explicit_device_without_check_is_rejected(self):
        self.check("--pci", DATA_PCI, success=False, reason="--pci requires --check")

    def test_missing_vfio_module_is_rejected(self):
        shutil.rmtree(self.sys / "module/vfio_pci")
        self.check("--check", success=False, reason="vfio-pci module is unavailable")

    def test_no_hugepages_is_rejected(self):
        (self.sys / "kernel/mm/hugepages/hugepages-2048kB/nr_hugepages").write_text("0\n")
        self.check("--check", success=False, reason="hugepages are not allocated")

    def test_native_mapping_preserves_drivers(self):
        self.write_network([])
        self.check("--check", reason="native path selected")

    def test_invalid_pci_is_rejected_before_device_access(self):
        self.check("--check", "--pci", "../management", success=False, reason="invalid selected DPDK PCI")

    def test_unknown_arguments_are_rejected(self):
        self.check("--force", success=False, reason="unknown argument")

    def test_empty_pci_argument_is_rejected(self):
        self.check("--check", "--pci", "", success=False, reason="--pci requires an address")

    def test_explicit_check_without_management_mapping_is_rejected(self):
        self.network.unlink()
        self.check("--check", "--pci", DATA_PCI, success=False, reason="requires an installer NIC mapping")

    def test_invalid_saved_device_is_rejected_before_device_access(self):
        self.write_network(["../management"])
        self.check("--check", success=False, reason="invalid selected DPDK PCI")


if __name__ == "__main__":
    unittest.main()
