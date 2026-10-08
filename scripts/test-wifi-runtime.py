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
