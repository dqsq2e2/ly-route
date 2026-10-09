#!/usr/bin/env python3
import copy
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "packaging/hardware/velo5x0/overlay/usr/lib/ly-route/wifi-runtime.py"
spec = importlib.util.spec_from_file_location("wifi_runtime", SOURCE)
wifi = importlib.util.module_from_spec(spec)
spec.loader.exec_module(wifi)


class WiFiTests(unittest.TestCase):
    def setUp(self):
        self.config = {
            "enabled": True, "mode": "ap", "country": "CN", "ssid": "test-radio",
            "password": "private-password", "security": "wpa2", "band": "2g",
            "channel": 1, "width": 20, "isolate": True, "hidden": False, "max_clients": 32,
        }

    def test_ap_credentials_are_hex_encoded_without_plaintext_psk(self):
        result = wifi.hostapd_config(self.config, "wlp1s0")
        self.assertIn("ssid2=" + self.config["ssid"].encode().hex(), result)
        self.assertNotIn(self.config["password"], result)
        self.assertIn("ap_isolate=1", result)
        self.assertIn("rsn_pairwise=CCMP", result)
        self.assertIn("bridge=lywifi-br", result)

    def test_business_dhcp_has_router_dns_and_isolated_control_interface(self):
        config = wifi.ap_dhcp_config()["Dhcp4"]
        self.assertEqual(config["interfaces-config"]["interfaces"], ["lywifi-host"])
        subnet = config["subnet4"][0]
        self.assertEqual(subnet["subnet"], "192.168.89.0/24")
        self.assertEqual(subnet["option-data"], [
            {"name": "routers", "data": "192.168.89.1"},
            {"name": "domain-name-servers", "data": "192.168.89.1"},
        ])

    def test_custom_subnet_drives_dhcp_gateway_pool_and_separate_leases(self):
        self.config.update(ap_cidr="10.42.7.1/24", dhcp_pool_start="10.42.7.20", dhcp_pool_end="10.42.7.90")
        config = wifi.ap_dhcp_config(self.config)["Dhcp4"]
        subnet = config["subnet4"][0]
        self.assertEqual(subnet["subnet"], "10.42.7.0/24")
        self.assertEqual(subnet["pools"], [{"pool": "10.42.7.20 - 10.42.7.90"}])
        self.assertEqual(subnet["option-data"], [
            {"name": "routers", "data": "10.42.7.1"},
            {"name": "domain-name-servers", "data": "10.42.7.1"},
        ])
        self.assertNotEqual(config["lease-database"]["name"], str(wifi.RUN / "leases.csv"))

    def test_custom_subnet_vpp_and_lcp_cleanup_use_owned_previous_subnet(self):
        self.config.update(ap_cidr="10.42.7.1/24", dhcp_pool_start="10.42.7.20", dhcp_pool_end="10.42.7.90")
        def answer(*args, **kwargs):
            if "create tap" in str(args):
                return "lywifi-ap\n"
            if args == ("vppctl", "show ly-route dns-intercept"):
                return "enabled 1"
            return ""
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(wifi, "RUN", Path(directory)), patch.object(wifi, "NETWORK", Path(directory) / "network"), \
                    patch.object(wifi, "command", side_effect=answer) as command:
                wifi.start_business("wlp1s0", self.config)
                wifi.stop_business("wlp1s0")
            calls = [call.args for call in command.call_args_list]
        self.assertIn(("vppctl", "set interface ip address lywifi-ap 10.42.7.1/24"), calls)
        self.assertIn(("ip", "address", "replace", "10.42.7.1/24", "dev", "lywifi-host"), calls)
        self.assertIn(("vppctl", "ip route add table 100 10.42.7.0/24 via lywifi-ap"), calls)
        self.assertIn(("vppctl", "ip route del table 100 10.42.7.0/24 via lywifi-ap"), calls)
        self.assertNotIn(("vppctl", "ip route del table 100 192.168.89.0/24 via lywifi-ap"), calls)

    def test_invalid_custom_network_is_rejected_before_any_operation(self):
        for change in (
            {"ap_cidr": "10.42.7.0/24"}, {"ap_cidr": "::1/64"},
            {"dhcp_pool_start": "192.168.89.201"}, {"dhcp_pool_end": "10.1.1.2"},
            {"dhcp_pool_start": "192.168.89.1"}, {"dhcp_pool_end": "192.168.89.255"},
        ):
            config = dict(self.config, **change)
            with self.subTest(change=change), patch.object(wifi, "command") as command:
                with self.assertRaises(ValueError):
                    wifi.apply(config)
                command.assert_not_called()

    def test_overlap_does_not_write_config_or_restart_ap(self):
        links = [{"ifname": "enp0s20f2", "addr_info": [
            {"family": "inet", "local": "192.168.89.254", "prefixlen": 24}]}]
        with patch.object(wifi, "radio", return_value=("wlp1s0", "phy0")), \
                patch.object(wifi.shutil, "which", return_value="tool"), \
                patch.object(wifi, "prepare_regulatory"), \
                patch.object(wifi, "command", side_effect=lambda *args, **kwargs:
                             json.dumps(links) if args[0] == "ip" else "{}") as command, \
                patch.object(wifi, "atomic") as atomic:
            with self.assertRaises(wifi.WirelessError) as failure:
                wifi.apply(self.config)
            self.assertEqual(failure.exception.code, "wifi_subnet_overlap")
            atomic.assert_not_called()
            self.assertFalse(any(call.args[0] == "systemctl" for call in command.call_args_list))

    def test_ap_scan_uses_distinct_managed_vif_and_current_channel_without_restart(self):
        text = ("BSS aa:bb:cc:dd:ee:ff(on lywifi-scan)\n"
                "\tfreq: 5745\n\tsignal: -44.50 dBm\n\tSSID: nearby\n\tRSN:\n"
                "\tAuthentication suites: PSK SAE\n")
        def answer(*args, **kwargs):
            if args[0] == "ip" and "-j" in args:
                return '[{"flags":["UP"]}]'
            if args[-1] == "info":
                return "type AP\nchannel 149 (5745 MHz), width: 80 MHz\n"
            if "scan" in args:
                return text
            return ""
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(wifi, "NET", Path(directory)), \
                    patch.object(wifi, "radio", return_value=("wlp1s0", "phy0")), \
                    patch.object(wifi, "command", side_effect=answer) as command:
                result = wifi.scan_radio()
        self.assertEqual(result["scope"], "current_channel")
        self.assertEqual(result["frequency"], 5745)
        self.assertEqual(result["networks"][0]["security"], "mixed")
        calls = [call.args for call in command.call_args_list]
        self.assertIn(("iw", "dev", "lywifi-scan", "scan", "freq", "5745", "passive"), calls)
        self.assertIn(("iw", "dev", "lywifi-scan", "del"), calls)
        self.assertFalse(any(call[0] == "systemctl" for call in calls))
        self.assertFalse(any(call[:6] == ("ip", "link", "set", "dev", "wlp1s0", "down") for call in calls))
        created = next(call for call in calls if "add" in call)
        self.assertEqual(created[-2], "addr")
        self.assertTrue(created[-1].startswith("02:"))

    def test_aborted_scan_is_failure_and_temp_interface_is_cleaned(self):
        def answer(*args, **kwargs):
            if args[0] == "ip" and "-j" in args:
                return '[{"flags":["UP"]}]'
            if args[-1] == "info":
                return "type AP\nchannel 149 (5745 MHz)\n"
            return "scan aborted!" if "scan" in args else ""
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(wifi, "NET", Path(directory)), \
                    patch.object(wifi, "radio", return_value=("wlp1s0", "phy0")), \
                    patch.object(wifi, "command", side_effect=answer) as command:
                with self.assertRaises(wifi.WirelessError) as failure:
                    wifi.scan_radio()
                self.assertEqual(failure.exception.code, "wifi_scan_aborted")
                self.assertEqual(command.call_args_list[-1].args, ("iw", "dev", "lywifi-scan", "del"))

    def test_scan_preserves_occupied_interface_and_down_client_state(self):
        with tempfile.TemporaryDirectory() as directory:
            occupied = Path(directory) / wifi.SCAN_INTERFACE
            occupied.mkdir()
            with patch.object(wifi, "NET", Path(directory)), \
                    patch.object(wifi, "radio", return_value=("wlp1s0", "phy0")), \
                    patch.object(wifi, "command", side_effect=['[{"flags":["UP"]}]', "type AP\n"]) as command:
                with self.assertRaises(wifi.WirelessError):
                    wifi.scan_radio()
                self.assertEqual(command.call_count, 2)
            with patch.object(wifi, "radio", return_value=("wlp1s0", "phy0")), \
                    patch.object(wifi, "command", side_effect=['[{"flags":[]}]', "type managed\n", "", "", ""]) as command:
                self.assertEqual(wifi.scan_radio()["scope"], "all_channels")
                self.assertEqual(command.call_args_list[-1].args,
                                 ("ip", "link", "set", "dev", "wlp1s0", "down"))

    def test_business_ap_uses_vpp_tap_not_linux_routing(self):
        def answer(*args, **kwargs):
            if args == ("vppctl", "create tap id 3800 if-name lywifi-ap hw-addr 02:4c:59:89:00:01 host-if-name lywifi-data host-mtu-size 1500"):
                return "lywifi-ap\n"
            if args == ("vppctl", "show nat44 interfaces"):
                return "NAT44 interfaces:\n lyroute-wan0 out\n"
            if args == ("vppctl", "show ly-route dns-intercept"):
                return "enabled 1 interface lyroute-lan0 fib-index 1\n"
            return ""
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory)
            with patch.object(wifi, "RUN", runtime), patch.object(wifi, "NETWORK", runtime / "network"), \
                    patch.object(wifi, "command", side_effect=answer) as command:
                wifi.start_business("wlp1s0")
            calls = [call.args for call in command.call_args_list]
            self.assertIn(("vppctl", "lcp create lywifi-ap host-if lywifi-host"), calls)
            self.assertIn(("vppctl", "set interface nat44 in lywifi-ap"), calls)
            self.assertIn(("vppctl", "set interface feature lywifi-ap ly-route-dns-intercept-ip4 arc ip4-unicast"), calls)
            self.assertIn(("ip", "link", "set", "dev", "lywifi-data", "master", "lywifi-br"), calls)
            self.assertIn(("ip", "address", "replace", "192.168.89.1/24", "dev", "lywifi-host"), calls)
            self.assertFalse(any("ip_forward" in str(call) or "MASQUERADE" in str(call) for call in calls))
            self.assertTrue((runtime / wifi.BUSINESS_STATE).exists())

    def test_business_readiness_requires_lcp_nat_and_dns_observations(self):
        outputs = {
            "show interface address": "lywifi-ap (up):\n  L3 192.168.89.1/24\n",
            "show nat44 interfaces": "NAT44 interfaces:\n lywifi-ap in\n lyroute-wan0 out\n",
            "show ly-route dns-intercept": "enabled 1 interface lyroute-lan0 fib-index 1\n",
            "show interface lywifi-ap features": " ly-route-dns-intercept-ip4\n",
            "show lcp": "itf-pair: [1] lywifi-ap tap4097 lywifi-host 20 type tap\n",
        }
        with patch.object(wifi, "command", side_effect=lambda *args, **kwargs: outputs[args[1]]):
            self.assertTrue(wifi.business_status(self.config)["ready"])
            outputs["show nat44 interfaces"] = "NAT44 interfaces:\n lywifi-ap in\n"
            self.assertFalse(wifi.business_status(self.config)["ready"])
            outputs["show nat44 interfaces"] += " lyroute-wan0 out\n"
            outputs["show lcp"] = ""
            self.assertFalse(wifi.business_status(self.config)["ready"])

    def test_business_setup_rejects_unowned_vpp_interface(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(wifi, "RUN", Path(directory)), patch.object(
                    wifi, "command", return_value="lywifi-ap (up):\n") as command:
                with self.assertRaises(RuntimeError):
                    wifi.start_business("wlp1s0")
            command.assert_called_once_with("vppctl", "show interface address")

    def test_business_cleanup_does_not_disable_wired_dns_or_global_nat(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory)
            (runtime / wifi.BUSINESS_STATE).write_text("{}")
            with patch.object(wifi, "RUN", runtime), patch.object(wifi, "command", return_value="") as command:
                wifi.stop_business("wlp1s0")
            calls = [call.args for call in command.call_args_list]
            self.assertIn(("vppctl", "set interface nat44 in lywifi-ap del"), calls)
            self.assertIn(("vppctl", "lcp delete lywifi-ap"), calls)
            self.assertNotIn(("vppctl", "set ly-route dns-intercept disable"), calls)
            self.assertNotIn(("vppctl", "nat44 plugin disable"), calls)
            self.assertFalse((runtime / wifi.BUSINESS_STATE).exists())

    def test_client_password_quotes_cannot_inject_config(self):
        self.config.update(mode="client", security="wpa3", password='with "quote" and \\ slash')
        result = wifi.supplicant_config(self.config)
        self.assertIn('sae_password="with \\"quote\\" and \\\\ slash"', result)
        self.assertIn("ieee80211w=2", result)

    def test_sae_password_is_not_interpreted_as_per_station_metadata(self):
        self.config.update(security="wpa3", password="valid|mac=aa:bb:cc:dd:ee:ff")
        result = wifi.hostapd_config(self.config, "wlp1s0")
        self.assertIn("wpa_passphrase=valid|mac=aa:bb:cc:dd:ee:ff", result)
        self.assertNotIn("sae_password=", result)

    def test_credentials_and_regulatory_code_validation(self):
        for name, value in (("ssid", "evil\ninterface=eth0"), ("password", "short"),
                            ("country", ""), ("security", "open"), ("max_clients", 500)):
            with self.subTest(name=name):
                config = copy.deepcopy(self.config)
                config[name] = value
                with self.assertRaises(ValueError):
                    wifi.validate(config)

    def test_regulatory_setup_selects_matching_signed_upstream_pair(self):
        with tempfile.TemporaryDirectory() as directory:
            firmware = Path(directory)
            upstream = firmware / "regulatory.db-upstream"
            upstream.write_bytes(b"database")
            (firmware / "regulatory.db.p7s-upstream").write_bytes(b"signature")
            database = firmware / "regulatory.db"
            database.write_bytes(b"debian database")
            with patch.object(wifi, "REGDB", database), patch.object(wifi, "REGDB_UPSTREAM", upstream), \
                    patch.object(wifi, "command", return_value="global\ncountry CN: DFS-FCC\n") as command:
                wifi.prepare_regulatory("CN")
            self.assertEqual([call.args for call in command.call_args_list], [
                ("update-alternatives", "--set", "regulatory.db", str(upstream)),
                ("iw", "reg", "reload"), ("iw", "reg", "set", "CN"), ("iw", "reg", "get"),
            ])

    def test_regulatory_setup_keeps_already_selected_pair(self):
        with tempfile.TemporaryDirectory() as directory:
            firmware = Path(directory)
            upstream = firmware / "regulatory.db-upstream"
            upstream.write_bytes(b"database")
            signature = firmware / "regulatory.db.p7s-upstream"
            signature.write_bytes(b"signature")
            database = firmware / "regulatory.db"
            database.symlink_to(upstream)
            (firmware / "regulatory.db.p7s").symlink_to(signature)
            with patch.object(wifi, "REGDB", database), patch.object(wifi, "REGDB_UPSTREAM", upstream), \
                    patch.object(wifi, "command", return_value="global\ncountry CN: DFS-FCC\n") as command:
                wifi.prepare_regulatory("CN")
            self.assertEqual([call.args for call in command.call_args_list], [
                ("iw", "reg", "set", "CN"), ("iw", "reg", "get"),
            ])

    def test_regulatory_setup_rejects_missing_signature(self):
        with tempfile.TemporaryDirectory() as directory:
            upstream = Path(directory) / "regulatory.db-upstream"
            upstream.write_bytes(b"database")
            with patch.object(wifi, "REGDB_UPSTREAM", upstream), patch.object(wifi, "command") as command:
                with self.assertRaises(RuntimeError):
                    wifi.prepare_regulatory("CN")
            command.assert_not_called()

    def test_regulatory_setup_waits_for_async_country_and_rejects_world_fallback(self):
        with tempfile.TemporaryDirectory() as directory:
            firmware = Path(directory)
            upstream = firmware / "regulatory.db-upstream"
            upstream.write_bytes(b"database")
            (firmware / "regulatory.db.p7s-upstream").write_bytes(b"signature")
            with patch.object(wifi, "REGDB", firmware / "regulatory.db"), \
                    patch.object(wifi, "REGDB_UPSTREAM", upstream), \
                    patch.object(wifi, "command", return_value=""), patch.object(wifi.time, "sleep"), \
                    patch.object(wifi, "regulatory_country", side_effect=["00", "CN"]) as observed:
                wifi.prepare_regulatory("CN")
                self.assertEqual(observed.call_count, 2)
            with patch.object(wifi, "REGDB", firmware / "regulatory.db"), \
                    patch.object(wifi, "REGDB_UPSTREAM", upstream), \
                    patch.object(wifi, "command", return_value=""), patch.object(wifi.time, "sleep"), \
                    patch.object(wifi, "regulatory_country", return_value="00"):
                with self.assertRaises(RuntimeError):
                    wifi.prepare_regulatory("CN")

    def test_regulatory_country_observes_global_not_driver_world_domain(self):
        with patch.object(wifi, "command", return_value=(
                "global\ncountry CN: DFS-FCC\n\nphy#0\ncountry 99: DFS-UNSET\n")):
            self.assertEqual(wifi.regulatory_country(), "CN")
        with patch.object(wifi, "command", return_value="phy#0\ncountry CN: DFS-FCC\n"):
            self.assertEqual(wifi.regulatory_country(), "")

    def test_regulatory_setup_reloads_driver_intersection_then_requires_real_country(self):
        with tempfile.TemporaryDirectory() as directory:
            firmware = Path(directory)
            upstream = firmware / "regulatory.db-upstream"
            upstream.write_bytes(b"database")
            signature = firmware / "regulatory.db.p7s-upstream"
            signature.write_bytes(b"signature")
            database = firmware / "regulatory.db"
            database.symlink_to(upstream)
            (firmware / "regulatory.db.p7s").symlink_to(signature)
            with patch.object(wifi, "REGDB", database), patch.object(wifi, "REGDB_UPSTREAM", upstream), \
                    patch.object(wifi, "command", return_value="") as command, \
                    patch.object(wifi.time, "sleep"), \
                    patch.object(wifi, "regulatory_country", side_effect=["98"] * 30 + ["CN"]):
                wifi.prepare_regulatory("CN")
            self.assertEqual([call.args for call in command.call_args_list], [
                ("iw", "reg", "set", "CN"), ("iw", "reg", "reload"), ("iw", "reg", "set", "CN"),
            ])

    def test_image_selects_trusted_regdb_before_first_wireless_probe(self):
        source = (Path(__file__).resolve().parents[1] / "scripts/build-rootfs.sh").read_text()
        self.assertIn(
            'chroot "$rootfs" update-alternatives --set regulatory.db /lib/firmware/regulatory.db-upstream',
            source,
        )

    def test_capabilities_preserve_real_disabled_no_ir_and_dfs_flags(self):
        raw = ("Supported interface modes:\n\t * managed\n\t * AP\n"
               "\tVHT Capabilities (0x123):\n"
               "\t * 2412 MHz [1] (20.0 dBm)\n"
               "\t * 5180 MHz [36] (30.0 dBm) (no IR)\n"
               "\t * 5260 MHz [52] (30.0 dBm) (radar detection)\n"
               "\t * 5500 MHz [100] (disabled)\n")
        with patch.object(wifi, "command", return_value=raw):
            caps = wifi.capabilities("phy0")
        self.assertTrue(caps["ap"])
        self.assertTrue(caps["client"])
        self.assertTrue(caps["single_radio"])
        self.assertTrue(caps["channels"][1]["no_ir"])
        self.assertTrue(caps["channels"][2]["radar"])
        self.assertTrue(caps["channels"][3]["disabled"])

    def test_80mhz_channel_requires_valid_block(self):
        self.config.update(band="5g", channel=36, width=80)
        self.assertIn("vht_oper_centr_freq_seg0_idx=42", wifi.hostapd_config(self.config, "wlp1s0"))
        self.config["channel"] = 165
        with self.assertRaises(ValueError):
            wifi.hostapd_config(self.config, "wlp1s0")

    def test_management_wireless_never_installs_linux_forwarding(self):
        source = SOURCE.read_text()
        self.assertIn("UseRoutes=no", source)
        self.assertIn("UseDNS=no", source)
        self.assertNotIn("ip_forward", source)
        self.assertNotIn("MASQUERADE", source)

    def test_reapplying_disabled_config_stops_a_radio_with_runtime_drift(self):
        config = copy.deepcopy(self.config)
        config["enabled"] = False
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory)
            stored = runtime / "config.json"
            stored.write_text(json.dumps(config))
            with patch.object(wifi, "RUN", runtime), patch.object(wifi, "CONFIG", stored), \
                    patch.object(wifi, "radio", return_value=("wlp1s0", "phy0")), \
                    patch.object(wifi.shutil, "which", return_value="/usr/bin/tool"), \
                    patch.object(wifi, "command") as command, \
                    patch.object(wifi, "stop_network") as stop_network:
                self.assertEqual(wifi.apply(config), {"applied": True})
            command.assert_called_once_with("systemctl", "stop", "ly-route-wifi.service")
            stop_network.assert_called_once_with("wlp1s0")


if __name__ == "__main__":
    unittest.main()
