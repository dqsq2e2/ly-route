#!/usr/bin/python3
import argparse
import glob
import hashlib
import json
import logging
import math
import os
import re
import signal
import subprocess
import tempfile
import time
from pathlib import Path

from smbus import SMBus

CONFIG_PATH = Path("/etc/ly-route/velo5x0-fan.json")
STATUS_PATH = Path("/run/ly-route/velo5x0-fan-status.json")
DEFAULT_CONFIG = {
    "mode": "curve",
    "manual_pwm": 31,
    "temp_source": "cpu",
    "cpu_statistic": "max",
    "cpu_sensor": "temp2_input",
    "curve_profile": "linear",
    "min_pwm": 16,
    "stop_temperature": 42,
    "start_temperature": 45,
    "full_temperature": 60,
    "poll_interval": 3,
    "curve": [
        {"temperature": 45, "pwm": 16},
        {"temperature": 50, "pwm": 43},
        {"temperature": 55, "pwm": 71},
        {"temperature": 60, "pwm": 100},
    ],
}


def supported():
    return Path("/sys/class/dmi/id/board_name").read_text().strip() in (
        "EDGE520", "EDGE540"
    )


def load(name, *options):
    subprocess.run(["modprobe", name, *options], check=True)


def straps(bus, running):
    # Preserve PoE and all unrelated outputs on the board expander.
    output = bus.read_byte_data(0x1C, 1)
    bus.write_byte_data(0x1C, 1, (output & ~0xC0) | (0x40 if running else 0x80))
    direction = bus.read_byte_data(0x1C, 3)
    bus.write_byte_data(0x1C, 3, direction & ~0xC0)


def fan_setup(bus):
    if bus.read_byte_data(0x2F, 0xFD) != 0x1D:
        raise RuntimeError("EMC2104 was not found on board I2C bus 9")
    for register, value in ((0x50, 0), (0x42, 0x10), (0x2B, 0x0C), (0x41, 1)):
        bus.write_byte_data(0x2F, register, value)
    straps(bus, True)
    bus.write_byte_data(0x2F, 0x40, 48)


def wan_reset(bus):
    # Pin 4 resets only the two WAN PHYs, not either mini-PCIe slot.
    output = bus.read_byte_data(0x18, 1)
    bus.write_byte_data(0x18, 1, output & ~0x10)
    bus.write_byte_data(0x18, 3, bus.read_byte_data(0x18, 3) & ~0x10)
    time.sleep(1)
    bus.write_byte_data(0x18, 1, output | 0x10)
    time.sleep(1)


def initialize():
    load("gpio_ich")
    load("i2c_gpio")
    load("mdio_gpio")
    load("vc_edge5x0_mdio")
    load("i2c_dev")
    with SMBus(9) as bus:
        fan_setup(bus)
        bound = Path("/sys/bus/pci/drivers/igb")
        if not all((bound / f"0000:00:14.{index}").exists() for index in (2, 3)):
            wan_reset(bus)
    load("igb")
    for index in (2, 3):
        address = f"0000:00:14.{index}"
        if not (Path("/sys/bus/pci/drivers/igb") / address).exists():
            Path("/sys/bus/pci/drivers_probe").write_text(address)
    load("mv88e6xxx")
    load("vc_edge5x0_dsa", "dsa_mask=3")
    load("leds_pca963x")
    load("coretemp")
    ich = next(
        (Path(path) for path in glob.glob("/sys/class/gpio/gpiochip*")
         if (Path(path) / "label").read_text().strip() == "gpio_ich"),
        None,
    )
    if ich is not None:
        pin = int((ich / "base").read_text()) + 39
        gpio = Path(f"/sys/class/gpio/gpio{pin}")
        if not gpio.exists():
            Path("/sys/class/gpio/export").write_text(str(pin))
        (gpio / "direction").write_text("low")
    logging.info("5x0 board initialized; LAN1-8 remain Linux-owned DSA ports")


def sensors(bus):
    values = []
    for directory in glob.glob("/sys/class/hwmon/hwmon*"):
        path = Path(directory)
        try:
            name = (path / "name").read_text().strip()
        except OSError:
            continue
        if name not in ("coretemp", "ath10k_hwmon"):
            continue
        for sensor in path.glob("temp*_input"):
            try:
                value = int(sensor.read_text()) / 1000
                if -20 <= value <= 125:
                    label = sensor.with_name(sensor.name.replace("_input", "_label"))
                    values.append({
                        "source": "cpu" if name == "coretemp" else "wifi",
                        "sensor": sensor.name,
                        "label": label.read_text().strip() if label.exists() else sensor.stem,
                        "value": value,
                    })
            except (OSError, ValueError):
                pass
    for register in (0, 2, 4):
        value = bus.read_byte_data(0x2F, register)
        if value == 128:
            continue
        if value > 127:
            value -= 256
        value += (bus.read_byte_data(0x2F, register + 1) >> 5) * 0.125
        if -20 <= value <= 125:
            values.append({
                "source": "board",
                "sensor": f"emc2104-{register}",
                "label": "EMC2104",
                "value": value,
            })
    return values


def temperature(bus):
    values = sensors(bus)
    if not values:
        raise RuntimeError("no valid temperature sensors")
    return max(item["value"] for item in values)


def control_temperature(config, readings):
    source = config["temp_source"]
    values = readings if source in ("max", "average") else [
        item for item in readings if item["source"] == source
    ]
    if source == "cpu":
        statistic = config["cpu_statistic"]
        if statistic == "single":
            values = [item for item in values if item["sensor"] == config["cpu_sensor"]]
        elif statistic == "average":
            cores = [item for item in values if item["label"].startswith("Core ")]
            values = cores or values
    if not values:
        raise RuntimeError(f"selected temperature source is unavailable: {source}")
    numbers = [item["value"] for item in values]
    if source == "average" or (source == "cpu" and config["cpu_statistic"] == "average"):
        return sum(numbers) / len(numbers)
    return max(numbers)


def validate_config(config):
    if not isinstance(config, dict) or set(config) != set(DEFAULT_CONFIG):
        raise ValueError("fan configuration has missing or unknown fields")
    choices = {
        "mode": ("curve", "manual"),
        "temp_source": ("cpu", "wifi", "board", "max", "average"),
        "cpu_statistic": ("max", "average", "single"),
        "curve_profile": ("linear", "custom"),
    }
    for key, options in choices.items():
        if config[key] not in options:
            raise ValueError(f"invalid {key}")
    if not isinstance(config["cpu_sensor"], str) or not re.fullmatch(r"temp[0-9]+_input", config["cpu_sensor"]):
        raise ValueError("invalid CPU sensor")
    for key in ("manual_pwm", "min_pwm", "poll_interval"):
        high = 30 if key == "poll_interval" else 100
        low = 1 if key == "poll_interval" else 0
        if type(config[key]) is not int or not low <= config[key] <= high:
            raise ValueError(f"invalid {key}")
    for key in ("stop_temperature", "start_temperature", "full_temperature"):
        value = config[key]
        if type(value) not in (int, float) or not math.isfinite(value) or not 0 <= value <= 100:
            raise ValueError(f"invalid {key}")
    if config["full_temperature"] <= config["start_temperature"]:
        raise ValueError("full-speed temperature must exceed start temperature")
    if config["stop_temperature"] and config["stop_temperature"] >= config["start_temperature"]:
        raise ValueError("stop temperature must be below start temperature")
    curve = config["curve"]
    if not isinstance(curve, list) or len(curve) != 4:
        raise ValueError("custom curve needs four points")
    previous = -1
    for point in curve:
        if not isinstance(point, dict) or set(point) != {"temperature", "pwm"}:
            raise ValueError("invalid curve point")
        value, pwm = point["temperature"], point["pwm"]
        if type(value) not in (int, float) or not math.isfinite(value) or not 0 <= value <= 100 or value <= previous:
            raise ValueError("curve temperatures must increase between 0 and 100")
        if type(pwm) is not int or not 0 <= pwm <= 100:
            raise ValueError("invalid curve PWM")
        previous = value
    return config


def load_config():
    if not CONFIG_PATH.exists():
        return DEFAULT_CONFIG, "default"
    contents = CONFIG_PATH.read_bytes()
    config = validate_config(json.loads(contents))
    return config, hashlib.sha256(contents).hexdigest()


def curve_pwm(config, current):
    if config["curve_profile"] == "linear":
        progress = min(1, max(0, (current - config["start_temperature"]) /
                             (config["full_temperature"] - config["start_temperature"])))
        return round(config["min_pwm"] + (100 - config["min_pwm"]) * progress)
    points = config["curve"]
    if current <= points[0]["temperature"]:
        return points[0]["pwm"]
    for first, second in zip(points, points[1:]):
        if current <= second["temperature"]:
            progress = (current - first["temperature"]) / (second["temperature"] - first["temperature"])
            return round(first["pwm"] + (second["pwm"] - first["pwm"]) * progress)
    return 100


def output_state(config, current, powered):
    if config["mode"] == "manual":
        return config["manual_pwm"] > 0, config["manual_pwm"]
    if config["stop_temperature"] == 0:
        powered = True
    elif powered and current <= config["stop_temperature"]:
        powered = False
    elif not powered and current >= config["start_temperature"]:
        powered = True
    return powered, curve_pwm(config, current) if powered else 0


def write_pwm(bus, powered, pwm):
    if not powered:
        bus.write_byte_data(0x2F, 0x40, 0)
        straps(bus, False)
    else:
        straps(bus, True)
        bus.write_byte_data(0x2F, 0x40, round(pwm * 255 / 100))
    return round(bus.read_byte_data(0x2F, 0x40) * 100 / 255)


def write_status(config, revision, powered, pwm, readings, effective, error):
    temperatures = {}
    for source in ("cpu", "wifi", "board"):
        values = [item["value"] for item in readings if item["source"] == source]
        temperatures[source] = max(values) if values else None
    status = {
        "running": True,
        "output_pwm": pwm,
        "temperatures": temperatures,
        "sensors": readings,
        "effective_temperature": effective,
        "error": error,
        "mode": config["mode"],
        "config_revision": revision,
        "updated_at": int(time.time()),
        "powered": powered,
    }
    STATUS_PATH.parent.mkdir(parents=True, exist_ok=True)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", dir=STATUS_PATH.parent, delete=False) as output:
            temporary = Path(output.name)
            json.dump(status, output, allow_nan=False)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, STATUS_PATH)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def fan():
    stopping = False

    def stop(_signum, _frame):
        nonlocal stopping
        stopping = True

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    with SMBus(9) as bus:
        fan_setup(bus)
        powered = True
        config = DEFAULT_CONFIG
        try:
            while not stopping:
                readings, effective, error, revision = [], None, "", "default"
                try:
                    config, revision = load_config()
                    readings = sensors(bus)
                    if config["mode"] != "manual":
                        effective = control_temperature(config, readings)
                    powered, pwm = output_state(config, effective, powered)
                    pwm = write_pwm(bus, powered, pwm)
                except (OSError, RuntimeError, ValueError) as failure:
                    logging.exception("temperature control failed; using full-speed cooling")
                    straps(bus, True)
                    bus.write_byte_data(0x2F, 0x40, 255)
                    powered, pwm, error = True, 100, str(failure)
                try:
                    write_status(config, revision, powered, pwm, readings, effective, error)
                except OSError:
                    logging.exception("could not publish fan status")
                try:
                    modified = CONFIG_PATH.stat().st_mtime_ns
                except FileNotFoundError:
                    modified = None
                for _ in range(config["poll_interval"] * 4):
                    if stopping:
                        break
                    time.sleep(0.25)
                    try:
                        if CONFIG_PATH.stat().st_mtime_ns != modified:
                            break
                    except FileNotFoundError:
                        if modified is not None:
                            break
        finally:
            straps(bus, True)
            bus.write_byte_data(0x2F, 0x40, 255)
            STATUS_PATH.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("initialize", "fan"))
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(levelname)s: %(message)s")
    if not supported():
        return
    initialize() if args.action == "initialize" else fan()


if __name__ == "__main__":
    main()
