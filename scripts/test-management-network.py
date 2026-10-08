#!/usr/bin/env python3
import importlib.util
import json
import pathlib
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
OVERLAY = ROOT / "packaging/rootfs-overlay"
SPEC = importlib.util.spec_from_file_location("management_network", OVERLAY / "usr/lib/ly-route/management-network.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ManagementNetworkTest(unittest.TestCase):
    def business(self, pool="192.168.88.120 - 192.168.88.199"):
        return {"Dhcp4": {
            "interfaces-config": {"interfaces": ["lylan-enp0s20f3"]},
            "subnet4": [{"subnet": "192.168.88.0/24", "pools": [{"pool": pool}]}],
        }}

    def test_source_route_is_independent_of_main_table(self):
        network, _ = MODULE.documents("enp0s20f2", "192.168.88.254/24", "192.168.88.1", "f0:8e:db:08:56:0e")
        self.assertIn("MACAddress=f0:8e:db:08:56:0e", network)
        self.assertIn("Destination=192.168.88.0/24\nScope=link\nPreferredSource=192.168.88.254\nTable=19088", network)
        self.assertIn("From=192.168.88.254/32\nTable=19088\nPriority=110", network)

    def test_management_has_its_own_listener_and_lease_database(self):
        _, document = MODULE.documents("enp0s20f2", "192.168.88.254/24")
        dhcp = document["Dhcp4"]
        self.assertEqual(dhcp["interfaces-config"]["interfaces"], ["enp0s20f2"])
        self.assertEqual(dhcp["lease-database"]["name"], "/var/lib/kea/management-leases4.csv")
        self.assertEqual(dhcp["subnet4"][0]["interface"], "enp0s20f2")

    def test_client_gets_on_link_host_route_not_business_default(self):
        _, document = MODULE.documents("enp0s20f2", "192.168.88.254/24")
        option = next(o for o in document["Dhcp4"]["subnet4"][0]["option-data"] if o.get("code") == 121)
        self.assertFalse(option["csv-format"])
        self.assertEqual(bytes.fromhex(option["data"]), b"\x20\xc0\xa8\x58\xfe\x00\x00\x00\x00")

    def test_overlapping_business_pool_is_excluded(self):
        _, document = MODULE.documents("enp0s20f2", "192.168.88.254/24", business=self.business())
        self.assertEqual(document["Dhcp4"]["subnet4"][0]["pools"], [{"pool": "192.168.88.100 - 192.168.88.119"}])

    def test_full_preferred_pool_falls_back_without_overlap(self):
        _, document = MODULE.documents("mgmt0", "192.168.88.254/24", "192.168.88.1", business=self.business("192.168.88.100 - 192.168.88.199"))
        self.assertEqual(document["Dhcp4"]["subnet4"][0]["pools"], [
            {"pool": "192.168.88.2 - 192.168.88.99"}, {"pool": "192.168.88.200 - 192.168.88.253"},
        ])

    def test_small_subnet_excludes_router_and_gateway(self):
        _, document = MODULE.documents("mgmt0", "10.0.0.1/29", "10.0.0.2")
        self.assertEqual(document["Dhcp4"]["subnet4"][0]["pools"], [{"pool": "10.0.0.3 - 10.0.0.6"}])

    def test_invalid_input_and_exhausted_pool_are_rejected(self):
        for interface, cidr in [("bad\nname", "192.168.88.254/24"), ("mgmt0", "192.168.88.255/24"), ("mgmt0", "10.0.0.1/32")]:
            with self.assertRaises(ValueError):
                MODULE.documents(interface, cidr)
        with self.assertRaises(ValueError):
            MODULE.documents("mgmt0", "192.168.88.254/24", business=self.business("192.168.88.1 - 192.168.88.253"))

    def test_business_configuration_survives_repeated_management_setup(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            business = root / "business.json"
            original = json.dumps(self.business(), indent=2) + "\n"
            business.write_text(original, encoding="utf-8")
            for _ in range(2):
                MODULE.configure("enp0s20f2", "192.168.88.254/24", "", "", "", root / "management.network", root / "management.json", business)
                self.assertEqual(business.read_text(encoding="utf-8"), original)
                self.assertEqual(json.loads((root / "management.json").read_text())["Dhcp4"]["interfaces-config"]["interfaces"], ["enp0s20f2"])

    def test_only_legacy_management_config_is_migrated(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            business = root / "business.json"
            old = self.business()
            old["Dhcp4"]["interfaces-config"]["interfaces"] = ["enp0s20f2"]
            business.write_text(json.dumps(old), encoding="utf-8")
            MODULE.configure("enp0s20f2", "192.168.88.254/24", "", "", "", root / "management.network", root / "management.json", business)
            self.assertEqual(json.loads(business.read_text())["Dhcp4"]["subnet4"], [])
            self.assertEqual(json.loads((root / "management.json").read_text())["Dhcp4"]["subnet4"][0]["pools"], [{"pool": "192.168.88.100 - 192.168.88.199"}])

    def test_profile_without_kea_only_gets_network_configuration(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            MODULE.configure("mgmt0", "192.168.88.254/24", "", "", "", root / "management.network", "", root / "absent-business.json")
            self.assertEqual([p.name for p in root.iterdir()], ["management.network"])

    def test_management_service_is_not_owned_by_business_runtime(self):
        unit = (OVERLAY / "etc/systemd/system/kea-dhcp4-management-server.service").read_text()
        self.assertIn("kea-dhcp4-management.conf", unit)
        self.assertIn("KEA_PIDFILE_DIR=/run/kea-management", unit)
        self.assertNotIn("vpp.service", unit)
        self.assertNotIn("BindsTo=", unit)
        firstboot = (OVERLAY / "usr/lib/ly-route/firstboot.sh").read_text()
        self.assertIn("management-network.py", firstboot)
        self.assertIn("restart --no-block kea-dhcp4-management-server.service", firstboot)


if __name__ == "__main__":
    unittest.main()
