#!/usr/bin/python3
"""Single-radio management AP/client. Business traffic is never Linux-routed."""
import argparse
import hashlib
import ipaddress
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

RUN = Path("/run/ly-route/wifi")
CONFIG = RUN / "config.json"
NETWORK = Path("/run/systemd/network/05-ly-route-wifi.network")
AP_ADDRESS = "192.168.89.1/24"


def command(*args, timeout=12, check=True):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError("wireless command failed: " + args[0])
    return result.stdout


def radio():
    interfaces = sorted(path.parent.name for path in Path("/sys/class/net").glob("*/wireless"))
    if len(interfaces) != 1:
        raise RuntimeError("expected one wireless interface")
    interface = interfaces[0]
    if not re.fullmatch(r"[A-Za-z0-9_.-]{1,15}", interface):
        raise RuntimeError("invalid wireless interface identity")
    phy = (Path("/sys/class/net") / interface / "phy80211").resolve().name
    return interface, phy


def atomic(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    name = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, mode="w", delete=False) as output:
            name = output.name
            os.fchmod(output.fileno(), 0o600)
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.replace(name, path)
    finally:
        if name and Path(name).exists():
            Path(name).unlink()


def capabilities(phy):
    text = command("iw", "phy", phy, "info")
    channels = []
    for match in re.finditer(r"\* (\d+) MHz \[(\d+)\]([^\n]*)", text):
        frequency, channel = int(match[1]), int(match[2])
        channels.append({"channel": channel, "frequency": frequency,
                         "band": "2g" if frequency < 3000 else "5g",
                         "disabled": "disabled" in match[3],
                         "no_ir": "no IR" in match[3],
                         "radar": "radar detection" in match[3]})
    return {"channels": channels, "ap": bool(re.search(r"^\s+\* AP$", text, re.M)),
            "client": bool(re.search(r"^\s+\* managed$", text, re.M)), "single_radio": True,
            "widths": [20, 40, 80] if "VHT Capabilities" in text else [20, 40]}


def settings(text):
    return dict(line.split("=", 1) for line in text.splitlines() if "=" in line)


def temperature():
    for path in Path("/sys/class/hwmon").glob("hwmon*"):
        try:
            if (path / "name").read_text().strip() != "ath10k_hwmon":
                continue
            value = int((path / "temp1_input").read_text()) / 1000
            if not 0 <= value <= 125:
                return {"state": "invalid_reading", "value": None}
            return {"state": "ready", "value": value}
        except OSError:
            return {"state": "unavailable", "value": None}
        except ValueError:
            return {"state": "invalid_reading", "value": None}
    return {"state": "unsupported", "value": None}


def status():
    interface, phy = radio()
    link = json.loads(command("ip", "-j", "address", "show", "dev", interface))[0]
    blocked = json.loads(command("rfkill", "--json", check=False) or "{}").get("rfkilldevices", [])
    config = json.loads(CONFIG.read_text()) if CONFIG.exists() else {}
    mode = config.get("mode", "ap")
    connection = settings(command(
        "hostapd_cli" if mode == "ap" else "wpa_cli", "-p", str(RUN / "ctrl"),
        "-i", interface, "status", check=False))
    state = "disabled" if not config.get("enabled") else "starting"
    if mode == "ap" and connection.get("state") == "ENABLED":
        state = "ap_ready"
    if mode == "client" and connection.get("wpa_state") == "COMPLETED":
        state = "connected"
    stations = []
    if state == "ap_ready":
        current = None
        for line in command("iw", "dev", interface, "station", "dump", check=False).splitlines():
            match = re.match(r"Station ([0-9a-f:]{17})", line)
            if match:
                current = {"mac": match[1]}
                stations.append(current)
            elif current is not None:
                for key in ("signal", "rx bytes", "tx bytes", "rx bitrate", "tx bitrate", "connected time"):
                    if line.strip().startswith(key + ":"):
                        current[key.replace(" ", "_")] = line.split(":", 1)[1].strip()
    return {"interface": interface, "phy": phy, "driver": "ath10k_pci",
            "state": state, "mode": mode, "ssid": connection.get("ssid", ""),
            "bssid": connection.get("bssid", ""),
            "frequency": connection.get("freq", ""),
            "addresses": [item["local"] for item in link.get("addr_info", []) if item["family"] == "inet"],
            "temperature": temperature(), "stations": stations, "rfkill": blocked,
            "capabilities": capabilities(phy), "network": "management_only",
            "service": command("systemctl", "is-active", "ly-route-wifi.service", check=False).strip()}


def scan():
    interface, _ = radio()
    link = json.loads(command("ip", "-j", "link", "show", "dev", interface))[0]
    was_up = "UP" in link["flags"]
    networks, current = [], None
    try:
        if not was_up:
            command("ip", "link", "set", "dev", interface, "up")
        text = command("iw", "dev", interface, "scan", "passive", timeout=25)
        for line in text.splitlines():
            match = re.match(r"BSS ([0-9a-f:]{17})", line)
            if match:
                current = {"bssid": match[1], "ssid": "", "security": "open"}
                networks.append(current)
            elif current is not None:
                field = line.strip()
                if field.startswith("SSID: "):
                    current["ssid"] = field[6:]
                elif field.startswith("freq: "):
                    current["frequency"] = int(field[6:])
                elif field.startswith("signal: "):
                    current["signal"] = float(field.split()[1])
                elif field.startswith("RSN:"):
                    current["security"] = "wpa2"
                elif "Authentication suites:" in field and "SAE" in field:
                    current["security"] = "mixed" if "PSK" in field else "wpa3"
    finally:
        if not was_up:
            command("ip", "link", "set", "dev", interface, "down", check=False)
    return {"networks": sorted(networks, key=lambda item: item.get("signal", -200), reverse=True)}


def validate(config):
    if type(config.get("enabled")) is not bool or config.get("mode") not in ("ap", "client"):
        raise ValueError("invalid radio configuration")
    if not config["enabled"]:
        return
    if not re.fullmatch(r"[A-Z]{2}", config.get("country", "")):
        raise ValueError("regulatory country is required")
    ssid, password = config.get("ssid", ""), config.get("password", "")
    if not 1 <= len(ssid.encode()) <= 32 or not 8 <= len(password.encode()) <= 63:
        raise ValueError("invalid SSID or password length")
    if any(char in ssid + password for char in "\x00\r\n"):
        raise ValueError("invalid wireless credential")
    if config.get("security") not in ("wpa2", "wpa3", "mixed"):
        raise ValueError("open and obsolete security are prohibited")
    if config.get("width") not in (20, 40, 80) or config.get("band") not in ("2g", "5g"):
        raise ValueError("invalid band or width")
    if not 1 <= config.get("max_clients", 0) <= 128:
        raise ValueError("invalid client limit")


def hostapd_config(config, interface):
    channel, width = config["channel"], config["width"]
    if config["band"] == "2g" and width == 80:
        raise ValueError("80 MHz requires 5 GHz")
    lines = ["driver=nl80211", "interface=" + interface, "ctrl_interface=" + str(RUN / "ctrl"),
             "ssid2=" + config["ssid"].encode().hex(), "country_code=" + config["country"],
             "ieee80211d=1", "channel=" + str(channel), "hw_mode=" + ("g" if config["band"] == "2g" else "a"),
             "wmm_enabled=1", "ieee80211n=1", "auth_algs=1", "wpa=2", "rsn_pairwise=CCMP",
             "ignore_broadcast_ssid=" + str(int(config.get("hidden", False))),
             "ap_isolate=" + str(int(config.get("isolate", True))),
             "max_num_sta=" + str(config["max_clients"])]
    security, password = config["security"], config["password"]
    psk = hashlib.pbkdf2_hmac("sha1", password.encode(), config["ssid"].encode(), 4096, 32).hex()
    if security == "wpa2":
        lines.extend(["wpa_psk=" + psk, "wpa_key_mgmt=WPA-PSK"])
    else:
        lines.extend(["wpa_passphrase=" + password,
                      "wpa_key_mgmt=" + ("WPA-PSK SAE" if security == "mixed" else "SAE")])
    if security in ("wpa3", "mixed"):
        lines.append("ieee80211w=" + ("2" if security == "wpa3" else "1"))
    if width > 20:
        secondary = "+" if channel <= 7 or channel in (36, 44, 52, 60, 149, 157) else "-"
        lines.append("ht_capab=[HT40" + secondary + "]")
    if config["band"] == "5g":
        lines.append("ieee80211ac=1")
        if width == 80:
            centers = {36: 42, 40: 42, 44: 42, 48: 42, 149: 155, 153: 155, 157: 155, 161: 155}
            if channel not in centers:
                raise ValueError("unsupported 80 MHz channel block")
            lines.extend(["vht_oper_chwidth=1", "vht_oper_centr_freq_seg0_idx=" + str(centers[channel])])
    return "\n".join(lines) + "\n"


def supplicant_config(config):
    security = config["security"]
    lines = ["ctrl_interface=" + str(RUN / "ctrl"), "country=" + config["country"], "network={",
             "ssid=" + config["ssid"].encode().hex(), "scan_ssid=1",
             "key_mgmt=" + {"wpa2": "WPA-PSK", "wpa3": "SAE", "mixed": "WPA-PSK SAE"}[security]]
    if security in ("wpa2", "mixed"):
        psk = hashlib.pbkdf2_hmac("sha1", config["password"].encode(), config["ssid"].encode(), 4096, 32).hex()
        lines.append("psk=" + psk)
    if security in ("wpa3", "mixed"):
        quoted = config["password"].replace("\\", "\\\\").replace('"', '\\"')
        lines.extend(['sae_password="' + quoted + '"', "ieee80211w=" + ("2" if security == "wpa3" else "1")])
    lines.extend(["pairwise=CCMP", "group=CCMP", "}"])
    return "\n".join(lines) + "\n"


def stop_network(interface):
    if NETWORK.exists():
        NETWORK.unlink()
        command("networkctl", "reload", check=False)
        command("networkctl", "reconfigure", interface, check=False)
    command("ip", "address", "flush", "dev", interface, "scope", "global", check=False)
    command("ip", "link", "set", "dev", interface, "down", check=False)


def run():
    config = json.loads(CONFIG.read_text())
    validate(config)
    interface, _ = radio()
    processes = []
    stopping = False

    def stop(_signum, _frame):
        nonlocal stopping
        stopping = True

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        if not config["enabled"]:
            return
        command("iw", "reg", "set", config["country"])
        command("ip", "link", "set", "dev", interface, "up")
        if config["mode"] == "ap":
            atomic(RUN / "hostapd.conf", hostapd_config(config, interface))
            command("ip", "address", "replace", AP_ADDRESS, "dev", interface)
            leases = RUN / "leases.csv"
            kea = {"Dhcp4": {"interfaces-config": {"interfaces": [interface]},
                            "lease-database": {"type": "memfile", "name": str(leases), "persist": True},
                            "valid-lifetime": 3600,
                            "subnet4": [{"id": 89, "subnet": "192.168.89.0/24", "interface": interface,
                                         "pools": [{"pool": "192.168.89.100 - 192.168.89.200"}]}]}}
            atomic(RUN / "kea.json", json.dumps(kea))
            commands = [["hostapd", str(RUN / "hostapd.conf")], ["kea-dhcp4", "-c", str(RUN / "kea.json")]]
        else:
            atomic(RUN / "supplicant.conf", supplicant_config(config))
            atomic(NETWORK, "[Match]\nName=" + interface + "\n[Network]\nDHCP=ipv4\n"
                   "IPv6AcceptRA=no\nLinkLocalAddressing=no\n[DHCPv4]\nUseRoutes=no\nUseDNS=no\nUseNTP=no\n")
            command("networkctl", "reload")
            command("networkctl", "reconfigure", interface)
            commands = [["wpa_supplicant", "-D", "nl80211", "-i", interface, "-c", str(RUN / "supplicant.conf")]]
        for args in commands:
            processes.append(subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
        while not stopping:
            if any(process.poll() is not None for process in processes):
                raise RuntimeError("wireless child service exited")
            time.sleep(0.5)
    finally:
        for process in processes:
            if process.poll() is None:
                process.terminate()
        for process in processes:
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        stop_network(interface)


def apply(config):
    validate(config)
    interface, phy = radio()
    for executable in ("iw", "rfkill", "hostapd", "hostapd_cli", "wpa_supplicant", "wpa_cli", "kea-dhcp4"):
        if not shutil.which(executable):
            raise RuntimeError("wireless dependency unavailable: " + executable)
    if config["enabled"]:
        blocked = json.loads(command("rfkill", "--json")).get("rfkilldevices", [])
        if any(item.get("type") == "wlan" and
               (item.get("soft") == "blocked" or item.get("hard") == "blocked") for item in blocked):
            raise RuntimeError("wireless radio is blocked")
        command("iw", "reg", "set", config["country"])
        time.sleep(0.3)
        if config["mode"] == "ap":
            subnet = ipaddress.ip_network(AP_ADDRESS, strict=False)
            for link in json.loads(command("ip", "-j", "address", "show")):
                if link["ifname"] == interface:
                    continue
                for address in link.get("addr_info", []):
                    if address["family"] == "inet" and subnet.overlaps(ipaddress.ip_network(
                            address["local"] + "/" + str(address["prefixlen"]), strict=False)):
                        raise ValueError("wireless management subnet overlaps an existing interface")
            caps = capabilities(phy)
            candidates = [item for item in caps["channels"] if item["band"] == config["band"] and
                          item["channel"] == config["channel"] and not item["disabled"] and
                          not item["no_ir"] and not item["radar"]]
            if not candidates:
                raise ValueError("channel cannot initiate AP transmission under the current regulatory domain")
            # Validate all rendering before writing the live config or stopping services.
            hostapd_config(config, interface)
    RUN.mkdir(parents=True, exist_ok=True, mode=0o700)
    config.pop("revision", None)
    content = json.dumps(config, sort_keys=True)
    if config["enabled"] and CONFIG.exists() and json.loads(CONFIG.read_text()) == config and (
            status()["state"] in ("ap_ready", "connected")):
        return {"applied": True}
    atomic(CONFIG, content)
    if not config["enabled"]:
        command("systemctl", "stop", "ly-route-wifi.service")
        stop_network(interface)
        return {"applied": True}
    command("systemctl", "restart", "ly-route-wifi.service")
    for _ in range(30):
        observed = status()
        if observed["state"] in ("ap_ready", "connected") and observed["addresses"]:
            return {"applied": True}
        if observed["service"] == "failed":
            break
        time.sleep(0.5)
    raise RuntimeError("wireless activation or DHCP timed out")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("status", "scan", "apply", "run"))
    args = parser.parse_args()
    try:
        if args.action == "run":
            run()
            return
        if args.action == "status":
            result = status()
        elif args.action == "scan":
            result = scan()
        else:
            result = apply(json.load(sys.stdin))
        print(json.dumps(result, allow_nan=False))
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError):
        # Configurations contain credentials; never dump them or child stderr.
        print("wireless operation failed", file=sys.stderr)
        raise SystemExit(1)


if __name__ == "__main__":
    main()
