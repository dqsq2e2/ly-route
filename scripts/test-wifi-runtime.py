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
