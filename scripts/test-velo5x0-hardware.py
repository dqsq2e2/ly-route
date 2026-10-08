#!/usr/bin/env python3
import ast
import importlib.util
import copy
import hashlib
import json
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "packaging/hardware/velo5x0/overlay/usr/lib/ly-route/velo5x0-board.py"
sys.modules.setdefault("smbus", types.SimpleNamespace(SMBus=None))
spec = importlib.util.spec_from_file_location("velo5x0_board", SOURCE)
board = importlib.util.module_from_spec(spec)
spec.loader.exec_module(board)


class Bus:
    def __init__(self, registers):
        self.registers = dict(registers)
        self.writes = []
        self.closed = False

    def close(self):
        self.closed = True

    def read_byte_data(self, address, register):
        return self.registers[(address, register)]

    def write_byte_data(self, address, register, value):
        self.registers[(address, register)] = value
        self.writes.append((address, register, value))


class HardwareTests(unittest.TestCase):
    def test_wifi_negative_firmware_reading_is_not_used_for_fan_control(self):
        with tempfile.TemporaryDirectory() as directory:
            hwmon = Path(directory)
            (hwmon / "name").write_text("ath10k_hwmon")
            (hwmon / "temp1_input").write_text("-15000")
            bus = Bus({(0x2F, 0): 128, (0x2F, 2): 128, (0x2F, 4): 128})
            with patch.object(board.glob, "glob", return_value=[directory]):
                self.assertEqual(board.sensors(bus), [])
            (hwmon / "temp1_input").write_text("47000")
            with patch.object(board.glob, "glob", return_value=[directory]):
                self.assertEqual(board.sensors(bus)[0]["value"], 47)

    def test_initialize_closes_debian_smbus_without_context_manager(self):
        bus = Bus({(0x2F, 0xFD): 0xFF})
        with patch.object(board, "load"), patch.object(board, "SMBus", return_value=bus):
            with self.assertRaisesRegex(RuntimeError, "EMC2104"):
                board.initialize()
        self.assertTrue(bus.closed)

    def test_initialize_closes_bus_before_loading_network_drivers(self):
        bus = Bus({})
        paths = MagicMock()
        paths.exists.return_value = True
        paths.__truediv__.return_value.exists.return_value = True

        def load(name, *_options):
            if name == "igb":
                self.assertTrue(bus.closed)

        with patch.object(board, "load", side_effect=load), \
                patch.object(board, "SMBus", return_value=bus), \
                patch.object(board, "fan_setup"), \
                patch.object(board, "Path", return_value=paths), \
                patch.object(board.glob, "glob", return_value=[]):
            board.initialize()
        self.assertTrue(bus.closed)

    def test_pwm_passthrough_preserves_poe(self):
        bus = Bus({(0x1C, 1): 0xA5, (0x1C, 3): 0xFF})
        board.straps(bus, True)
        self.assertEqual(bus.registers[(0x1C, 1)], 0x65)
        self.assertEqual(bus.registers[(0x1C, 3)], 0x3F)
        board.straps(bus, False)
        self.assertEqual(bus.registers[(0x1C, 1)], 0xA5)

    def test_wan_reset_preserves_slot_power_and_reset(self):
        bus = Bus({(0x18, 1): 0xA5, (0x18, 3): 0xFF})
        with patch.object(board.time, "sleep"):
            board.wan_reset(bus)
        self.assertEqual(bus.writes, [(0x18, 1, 0xA5), (0x18, 3, 0xEF), (0x18, 1, 0xB5)])
        for address, register, value in bus.writes:
            original = 0xA5 if register == 1 else 0xFF
            self.assertEqual(value & ~0x10, original & ~0x10)
            self.assertEqual(address, 0x18)

    def test_only_recognized_fan_controller_is_written(self):
        bus = Bus({(0x2F, 0xFD): 0xFF})
        with self.assertRaises(RuntimeError):
            board.fan_setup(bus)
        self.assertFalse(bus.writes)

    def test_fan_setup_uses_open_loop_and_correct_straps(self):
        bus = Bus({(0x2F, 0xFD): 0x1D, (0x1C, 1): 0x83, (0x1C, 3): 0xFF})
        board.fan_setup(bus)
        self.assertEqual(bus.registers[(0x2F, 0x50)], 0)
        self.assertEqual(bus.registers[(0x2F, 0x42)], 0x10)
        self.assertEqual(bus.registers[(0x1C, 1)], 0x43)
        self.assertEqual(bus.registers[(0x2F, 0x40)], 48)

    def test_board_temperature_uses_hottest_valid_sensor(self):
        bus = Bus({(0x2F, 0): 38, (0x2F, 1): 32, (0x2F, 2): 47,
                   (0x2F, 3): 128, (0x2F, 4): 128})
        with patch.object(board.glob, "glob", return_value=[]):
            self.assertEqual(board.temperature(bus), 47.5)

    def test_stop_and_start_hysteresis(self):
        config = copy.deepcopy(board.DEFAULT_CONFIG)
        self.assertEqual(board.output_state(config, 42, True), (False, 0))
        self.assertEqual(board.output_state(config, 44, False), (False, 0))
        self.assertEqual(board.output_state(config, 45, False), (True, 16))
        self.assertEqual(board.output_state(config, 44, True), (True, 16))
        self.assertEqual(board.output_state(config, 60, True), (True, 100))
        config["stop_temperature"] = 0
        self.assertEqual(board.output_state(config, 20, False), (True, 16))

    def test_manual_zero_cuts_power_and_nonzero_restores_passthrough(self):
        config = copy.deepcopy(board.DEFAULT_CONFIG)
        config.update(mode="manual", manual_pwm=0)
        bus = Bus({(0x1C, 1): 0x65, (0x1C, 3): 0xFF})
        powered, pwm = board.output_state(config, None, True)
        self.assertEqual((powered, pwm), (False, 0))
        self.assertEqual(board.write_pwm(bus, powered, pwm), 0)
        self.assertEqual(bus.registers[(0x1C, 1)], 0xA5)
        config["manual_pwm"] = 44
        powered, pwm = board.output_state(config, None, False)
        self.assertEqual(board.write_pwm(bus, powered, pwm), 44)
        self.assertEqual(bus.registers[(0x1C, 1)], 0x65)
        self.assertEqual(bus.registers[(0x2F, 0x40)], 112)
        config["manual_pwm"] = 100
        self.assertEqual(board.write_pwm(bus, *board.output_state(config, None, True)), 100)
        self.assertEqual(bus.registers[(0x2F, 0x40)], 255)

    def test_custom_curve_interpolation_and_hot_end(self):
        config = copy.deepcopy(board.DEFAULT_CONFIG)
        config["curve_profile"] = "custom"
        config["curve"] = [{"temperature": value, "pwm": pwm}
                           for value, pwm in ((45, 10), (50, 30), (55, 50), (60, 90))]
        self.assertEqual(board.curve_pwm(config, 40), 10)
        self.assertEqual(board.curve_pwm(config, 47.5), 20)
        self.assertEqual(board.curve_pwm(config, 60), 90)
        self.assertEqual(board.curve_pwm(config, 61), 100)

    def test_sources_use_available_values_without_inventing_wifi(self):
        readings = [
            {"source": "cpu", "sensor": "temp1_input", "label": "Package id 0", "value": 38},
            {"source": "cpu", "sensor": "temp2_input", "label": "Core 0", "value": 34},
            {"source": "cpu", "sensor": "temp3_input", "label": "Core 1", "value": 36},
            {"source": "board", "sensor": "emc2104-0", "label": "EMC2104", "value": 50},
        ]
        config = copy.deepcopy(board.DEFAULT_CONFIG)
        self.assertEqual(board.control_temperature(config, readings), 38)
        config["cpu_statistic"] = "average"
        self.assertEqual(board.control_temperature(config, readings), 35)
        config["cpu_statistic"] = "single"
        self.assertEqual(board.control_temperature(config, readings), 34)
        config["temp_source"] = "max"
        self.assertEqual(board.control_temperature(config, readings), 50)
        config["temp_source"] = "average"
        self.assertEqual(board.control_temperature(config, readings), 39.5)
        config["temp_source"] = "wifi"
        with self.assertRaises(RuntimeError):
            board.control_temperature(config, readings)

    def test_default_config_matches_overlay_and_load_revision(self):
        config_file = ROOT / "packaging/hardware/velo5x0/overlay/etc/ly-route/velo5x0-fan.json"
        config = json.loads(config_file.read_text())
        self.assertEqual(config, board.DEFAULT_CONFIG)
        with patch.object(board, "CONFIG_PATH", config_file):
            loaded, revision = board.load_config()
        self.assertEqual(loaded, config)
        self.assertEqual(revision, hashlib.sha256(config_file.read_bytes()).hexdigest())

    def test_invalid_settings_fail_before_hardware_write(self):
        invalid = [
            ("manual_pwm", True), ("manual_pwm", -1), ("poll_interval", 31),
            ("start_temperature", float("nan")), ("stop_temperature", 45),
            ("full_temperature", 44), ("cpu_sensor", "../temp2_input"),
        ]
        for key, value in invalid:
            config = copy.deepcopy(board.DEFAULT_CONFIG)
            config[key] = value
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                board.validate_config(config)
        config = copy.deepcopy(board.DEFAULT_CONFIG)
        config["curve"][0]["temperature"] = -0.5
        with self.assertRaises(ValueError):
            board.validate_config(config)

    def test_status_is_atomic_and_reports_group_maxima_and_unix_seconds(self):
        readings = [
            {"source": "cpu", "sensor": "temp2_input", "label": "Core 0", "value": 36},
            {"source": "cpu", "sensor": "temp3_input", "label": "Core 1", "value": 35},
            {"source": "board", "sensor": "emc2104-0", "label": "EMC2104", "value": 50.5},
        ]
        with tempfile.TemporaryDirectory() as directory:
            status_file = Path(directory) / "run/velo5x0-fan-status.json"
            with patch.object(board, "STATUS_PATH", status_file), patch.object(board.time, "time", return_value=1234.5):
                board.write_status(board.DEFAULT_CONFIG, "default", True, 44, readings, 36, "")
            status = json.loads(status_file.read_text())
            self.assertEqual(status["updated_at"], 1234)
            self.assertEqual(status["output_pwm"], 44)
            self.assertEqual(status["temperatures"], {"cpu": 36, "wifi": None, "board": 50.5})
            self.assertEqual(list(status_file.parent.iterdir()), [status_file])

    def test_missing_selected_sensor_runs_full_speed_failsafe(self):
        config = copy.deepcopy(board.DEFAULT_CONFIG)
        config["temp_source"] = "wifi"
        bus = Bus({(0x2F, 0xFD): 0x1D, (0x1C, 1): 0xA5, (0x1C, 3): 0xFF})
        handlers, statuses = {}, []

        def publish(*values):
            statuses.append(values)
            handlers[board.signal.SIGTERM](None, None)

        def register(signum, handler):
            handlers[signum] = handler

        with tempfile.TemporaryDirectory() as directory, \
                patch.object(board, "SMBus", return_value=bus), \
                patch.object(board.signal, "signal", side_effect=register), \
                patch.object(board, "load_config", return_value=(config, "default")), \
                patch.object(board, "sensors", return_value=[]), \
                patch.object(board, "write_status", side_effect=publish), \
                patch.object(board, "STATUS_PATH", Path(directory) / "status.json"), \
                patch.object(board.logging, "exception"):
            board.fan()
        self.assertEqual(statuses[0][2:4], (True, 100))
        self.assertIn("selected temperature source is unavailable: wifi", statuses[0][-1])
        self.assertEqual(bus.registers[(0x1C, 1)], 0x65)
        self.assertEqual(bus.registers[(0x2F, 0x40)], 255)
        self.assertTrue(bus.closed)

    def test_installer_has_both_serial_boot_and_prompt_configuration(self):
        source = (ROOT / "scripts/build-auto-install-iso.sh").read_text(encoding="utf-8")
        grub = (ROOT / "packaging/hardware/velo5x0/installer-grub.cfg").read_text()
        self.assertIn('serial --unit=1 --speed=115200', grub)
        self.assertIn('lyroute.console=ttyS1', source)
        self.assertIn('TTYPath=/dev/ttyS1', source)
        self.assertIn('grub-mkrescue -o', source)
        self.assertNotIn('--linux-packages none', source)
        self.assertIn('dpkg-deb -f "$kernel_deb" Package', source)
        self.assertNotIn('blkid -o device -t LABEL=LYROUTE_ROOT', source)
        self.assertIn('0000:00:14.0|0000:00:14.1) continue', source)
        self.assertIn('Requires=ly-route-velo5x0-board.service', source)

    def test_firstboot_distinguishes_dsa_jacks_with_shared_macs(self):
        source = (ROOT / "packaging/rootfs-overlay/usr/lib/ly-route/firstboot.sh").read_text(encoding="utf-8")
        embedded = source.split("<<'PY'\n", 1)[1].split("\nPY\n", 1)[0]
        tree = ast.parse(embedded)
        tree.body = [item for item in tree.body if isinstance(item, (ast.Import, ast.ImportFrom, ast.FunctionDef))]
        namespace = {}
        exec(compile(tree, "firstboot-identity", "exec"), namespace)
        inventory = {
            "eth1": ("00:11:22:33:44:55", "0000:00:14.1"),
            "lan1": ("00:11:22:33:44:55", "igb-vc-0000:00:14.1:00"),
            "lan2": ("00:11:22:33:44:55", "igb-vc-0000:00:14.1:00"),
        }
        hardware = MagicMock()
        hardware.exists.return_value = True
        hardware.read_text.return_value = "velo5x0\n"
        with patch.object(namespace["pathlib"], "Path", return_value=hardware):
            for jack in ("lan1", "lan2"):
                identity = {"name": jack, "mac": "00:11:22:33:44:55", "pci": "0000:00:14.1"}
                self.assertEqual(namespace["resolve"](identity, inventory), jack)
            hardware.exists.return_value = False
            self.assertEqual(namespace["resolve"]({"mac": "00:11:22:33:44:55"}, inventory), "eth1")


if __name__ == "__main__":
    unittest.main()
