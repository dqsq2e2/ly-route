#!/usr/bin/python3
"""Single-radio VPP business AP and management client."""
import argparse
import fcntl
import hashlib
import ipaddress
import json
import os
import re
import secrets
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
AP_SUBNET = "192.168.89.0/24"
AP_POOL_START = "192.168.89.100"
AP_POOL_END = "192.168.89.200"
BRIDGE = "lywifi-br"
DATA_HOST = "lywifi-data"
CONTROL_HOST = "lywifi-host"
VPP_INTERFACE = "lywifi-ap"
TAP_ID = 3800
AP_MAC = "02:4c:59:89:00:01"
BUSINESS_STATE = "business.json"
REGDB = Path("/lib/firmware/regulatory.db")
REGDB_UPSTREAM = Path("/lib/firmware/regulatory.db-upstream")
SCAN_INTERFACE = "lywifi-scan"
NET = Path("/sys/class/net")


class WirelessError(RuntimeError):
    def __init__(self, code):
        super().__init__(code)
        self.code = code


def ap_network(config=None):
    config = config or {}
    address = ipaddress.ip_interface(config.get("ap_cidr") or AP_ADDRESS)
    first = ipaddress.ip_address(config.get("dhcp_pool_start") or AP_POOL_START)
    last = ipaddress.ip_address(config.get("dhcp_pool_end") or AP_POOL_END)
    if address.version != 4 or first.version != 4 or last.version != 4:
        raise ValueError("wireless business network must use IPv4")
    subnet = address.network
    if subnet.prefixlen > 30 or address.ip in (subnet.network_address, subnet.broadcast_address):
        raise ValueError("invalid wireless gateway address")
    if any(ip.is_unspecified or ip.is_loopback or ip.is_multicast or ip.is_link_local
           for ip in (address.ip, first, last)):
        raise ValueError("invalid wireless business address")
    if not (subnet.network_address < first <= last < subnet.broadcast_address) or first <= address.ip <= last:
        raise ValueError("DHCP pool must be inside the subnet and exclude the gateway")
    return address, first, last


def command(*args, timeout=12, check=True):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    if check and result.returncode:
        if args[0] == "iw" and "scan" in args:
            if "(-95)" in result.stderr:
                raise WirelessError("wifi_scan_unsupported")
            if "(-16)" in result.stderr:
                raise WirelessError("wifi_scan_busy")
        raise RuntimeError("wireless command failed: " + args[0])
    return result.stdout


def regulatory_country():
    text = command("iw", "reg", "get")
    match = re.search(r"^global\ncountry ([A-Z0-9]{2}):", text, re.M)
    return match[1] if match else ""


def prepare_regulatory(country):
    # This board's upstream kernel trusts upstream regdb keys, not Debian's key.
    upstream_signature = REGDB_UPSTREAM.with_name("regulatory.db.p7s-upstream")
    signature = REGDB.with_name("regulatory.db.p7s")
    if not REGDB_UPSTREAM.is_file() or not upstream_signature.is_file():
        raise RuntimeError("signed upstream regulatory database unavailable")
    if REGDB.resolve() != REGDB_UPSTREAM.resolve() or signature.resolve() != upstream_signature.resolve():
        command("update-alternatives", "--set", "regulatory.db", str(REGDB_UPSTREAM))
        command("iw", "reg", "reload")
    for attempt in range(2):
        if attempt:
            # Reprocess the signed database after a driver hint left an intersection.
            command("iw", "reg", "reload")
        command("iw", "reg", "set", country)
        for _ in range(30):
            if regulatory_country() == country:
                return
            time.sleep(0.1)
    raise RuntimeError("requested regulatory country did not become active")


def radio():
    interfaces = sorted(path.parent.name for path in NET.glob("*/wireless")
                        if path.parent.name != SCAN_INTERFACE)
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


def vpp(text, check=True, result=""):
    output = command("vppctl", text, check=check).strip()
    if check and output and output != result:
        raise RuntimeError("wireless VPP operation failed")
    return output


def business_status(config):
    address, _, _ = ap_network(config)
    result = {"state": "disabled", "ready": False, "interface": VPP_INTERFACE,
              "gateway": str(address.ip), "dns": str(address.ip), "cidr": str(address),
              "subnet": str(address.network)}
    if config.get("mode") != "ap":
        result["state"] = "management_only"
        return result
    if not config.get("enabled"):
        return result
    result["state"] = "unavailable"
    try:
        addresses = command("vppctl", "show interface address")
        nat = command("vppctl", "show nat44 interfaces")
        dns = command("vppctl", "show ly-route dns-intercept")
        features = command("vppctl", "show interface " + VPP_INTERFACE + " features")
        paired = command("vppctl", "show lcp")
        has_address = bool(re.search(r"^" + VPP_INTERFACE + r" \(up\):\n\s+L3 " +
                                     re.escape(str(address)) + r"(?:\s|$)", addresses, re.M))
        has_inside = bool(re.search(r"^\s*" + VPP_INTERFACE + r"\s+in\s*$", nat, re.M))
        has_outside = bool(re.search(r"^\s*\S+\s+out\s*$", nat, re.M))
        has_dns = "enabled 1" in dns and "ly-route-dns-intercept-ip4" in features
        has_pair = any(VPP_INTERFACE in line.split() and CONTROL_HOST in line.split()
                       for line in paired.splitlines())
        result["ready"] = has_address and has_inside and has_outside and has_dns and has_pair
        result["state"] = "forwarding_ready" if result["ready"] else "waiting_for_dataplane"
    except (OSError, RuntimeError, subprocess.SubprocessError):
        pass
    return result


def reconcile_business(config=None):
    address, _, _ = ap_network(config)
    nat = command("vppctl", "show nat44 interfaces")
    if re.search(r"^\s*\S+\s+out\s*$", nat, re.M):
        if not re.search(r"^\s*" + VPP_INTERFACE + r"\s+in\s*$", nat, re.M):
            vpp("set interface nat44 in " + VPP_INTERFACE)
    dns = command("vppctl", "show ly-route dns-intercept")
    if "enabled 1" in dns:
        vpp("ip route add table 100 " + str(address.network) + " via " + VPP_INTERFACE)
        # Reuse the global DNS service FIB without replacing the wired LAN owner.
        vpp("set interface feature " + VPP_INTERFACE +
            " ly-route-dns-intercept-ip4 arc ip4-unicast")


def start_business(interface, config=None):
    address, first, last = ap_network(config)
    state = RUN / BUSINESS_STATE
    addresses = command("vppctl", "show interface address")
    existing = bool(re.search(r"^" + VPP_INTERFACE + r" \(", addresses, re.M))
    if existing and not state.exists():
        raise RuntimeError("wireless VPP interface is not owned by this service")
    if not state.exists():
        for name in (BRIDGE, DATA_HOST, CONTROL_HOST):
            if (Path("/sys/class/net") / name).exists():
                raise RuntimeError("wireless handoff interface is already in use")
    atomic(state, json.dumps({"interface": interface, "vpp_interface": VPP_INTERFACE,
                             "ap_cidr": str(address), "dhcp_pool_start": str(first),
                             "dhcp_pool_end": str(last)}))
    atomic(NETWORK, "[Match]\nName=" + " ".join((interface, BRIDGE, DATA_HOST, CONTROL_HOST)) +
           "\n[Network]\nDHCP=no\nIPv6AcceptRA=no\nLinkLocalAddressing=no\nKeepConfiguration=static\n")
    command("networkctl", "reload")
    command("networkctl", "reconfigure", interface)
    if not (Path("/sys/class/net") / BRIDGE).exists():
        command("ip", "link", "add", "name", BRIDGE, "type", "bridge")
    command("ip", "link", "set", "dev", BRIDGE, "up")
    if not existing:
        vpp("create tap id " + str(TAP_ID) + " if-name " + VPP_INTERFACE +
            " hw-addr " + AP_MAC + " host-if-name " + DATA_HOST +
            " host-mtu-size 1500", result=VPP_INTERFACE)
    paired = command("vppctl", "show lcp")
    if not any(VPP_INTERFACE in line.split() and CONTROL_HOST in line.split()
               for line in paired.splitlines()):
        vpp("lcp create " + VPP_INTERFACE + " host-if " + CONTROL_HOST)
    vpp("set interface state " + VPP_INTERFACE + " up")
    vpp("set interface mtu 1500 " + VPP_INTERFACE)
    if not re.search(r"^" + VPP_INTERFACE + r" \(up\):\n\s+L3 " +
                     re.escape(str(address)) + r"(?:\s|$)", addresses, re.M):
        vpp("set interface ip address " + VPP_INTERFACE + " " + str(address))
    command("ip", "link", "set", "dev", DATA_HOST, "master", BRIDGE)
    for name in (DATA_HOST, CONTROL_HOST):
        command("ip", "link", "set", "dev", name, "mtu", "1500", "up")
    command("ip", "address", "replace", str(address), "dev", CONTROL_HOST)
    reconcile_business(config)


def stop_business(interface):
    state = RUN / BUSINESS_STATE
    if not state.exists():
        return
    address, _, _ = ap_network(json.loads(state.read_text()))
    for text in (
        "set interface feature " + VPP_INTERFACE + " ly-route-dns-intercept-ip4 arc ip4-unicast disable",
        "ip route del table 100 " + str(address.network) + " via " + VPP_INTERFACE,
        "set interface nat44 in " + VPP_INTERFACE + " del",
        "lcp delete " + VPP_INTERFACE,
        "delete tap " + VPP_INTERFACE,
    ):
        vpp(text, check=False)
    command("ip", "link", "set", "dev", interface, "nomaster", check=False)
    command("ip", "link", "delete", "dev", BRIDGE, check=False)
    state.unlink(missing_ok=True)


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
    business = business_status(config)
    if mode == "ap" and config.get("enabled"):
        control = json.loads(command("ip", "-j", "address", "show", "dev", CONTROL_HOST,
                                     check=False) or "[]")
        if control:
            link = control[0]
    return {"interface": interface, "phy": phy, "driver": "ath10k_pci",
            "state": state, "mode": mode, "ssid": connection.get("ssid", ""),
            "bssid": connection.get("bssid", ""),
            "frequency": connection.get("freq", ""),
            "addresses": [item["local"] for item in link.get("addr_info", []) if item["family"] == "inet"],
            "temperature": temperature(), "stations": stations, "rfkill": blocked,
            "capabilities": capabilities(phy),
            "regulatory": {"requested": config.get("country", ""), "active": regulatory_country()},
            "network": "business_lan" if mode == "ap" else "management_only", "business": business,
            "service": command("systemctl", "is-active", "ly-route-wifi.service", check=False).strip()}


def scan():
    RUN.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (RUN / "scan.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise WirelessError("wifi_scan_busy") from None
        return scan_radio()


def scan_radio():
    interface, _ = radio()
    link = json.loads(command("ip", "-j", "link", "show", "dev", interface))[0]
    was_up = "UP" in link["flags"]
    info = command("iw", "dev", interface, "info")
    is_ap = bool(re.search(r"^\s*type AP$", info, re.M))
    frequency = re.search(r"channel \d+ \((\d+) MHz\)", info) if was_up and is_ap else None
    target = interface
    created = False
    networks, current = [], None
    try:
        if is_ap:
            if (NET / SCAN_INTERFACE).exists():
                raise WirelessError("wifi_scan_busy")
            # The AP vif rejects scan requests. A distinct MAC is required for
            # bringing up another managed vif on this ath10k radio.
            mac = "02:" + ":".join(f"{value:02x}" for value in secrets.token_bytes(5))
            command("iw", "dev", interface, "interface", "add", SCAN_INTERFACE,
                    "type", "managed", "addr", mac)
            created = True
            target = SCAN_INTERFACE
            command("ip", "link", "set", "dev", target, "up")
        elif not was_up:
            command("ip", "link", "set", "dev", interface, "up")
        # The live single-channel AP firmware aborts off-channel scans. Keep
        # the AP running and report this limited scope explicitly to the UI.
        args = ["freq", frequency[1]] if frequency else []
        text = command("iw", "dev", target, "scan", *args, "passive", timeout=25)
        if "scan aborted!" in text:
            raise WirelessError("wifi_scan_aborted")
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
        if created:
            command("iw", "dev", target, "del")
        elif not was_up and not is_ap:
            command("ip", "link", "set", "dev", interface, "down", check=False)
    return {"networks": sorted(networks, key=lambda item: item.get("signal", -200), reverse=True),
            "scope": "current_channel" if frequency else "all_channels",
            "frequency": int(frequency[1]) if frequency else None}


def validate(config):
    if type(config.get("enabled")) is not bool or config.get("mode") not in ("ap", "client"):
        raise ValueError("invalid radio configuration")
    if config["mode"] == "ap":
        ap_network(config)
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
    lines = ["driver=nl80211", "interface=" + interface, "bridge=" + BRIDGE,
             "ctrl_interface=" + str(RUN / "ctrl"),
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
    stop_business(interface)
    if NETWORK.exists():
        NETWORK.unlink()
        command("networkctl", "reload", check=False)
        command("networkctl", "reconfigure", interface, check=False)
    command("ip", "address", "flush", "dev", interface, "scope", "global", check=False)
    command("ip", "link", "set", "dev", interface, "down", check=False)


def ap_dhcp_config(config=None):
    address, first, last = ap_network(config)
    gateway = str(address.ip)
    lease_file = "leases.csv" if str(address.network) == AP_SUBNET else (
        "leases-" + hashlib.sha256(str(address.network).encode()).hexdigest()[:12] + ".csv")
    return {"Dhcp4": {
        "interfaces-config": {"interfaces": [CONTROL_HOST]},
        "lease-database": {"type": "memfile", "name": str(RUN / lease_file), "persist": True},
        "valid-lifetime": 3600,
        "subnet4": [{"id": 89, "subnet": str(address.network), "interface": CONTROL_HOST,
                     "pools": [{"pool": str(first) + " - " + str(last)}],
                     "option-data": [{"name": "routers", "data": gateway},
                                     {"name": "domain-name-servers", "data": gateway}]}]
    }}


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
        prepare_regulatory(config["country"])
        command("ip", "link", "set", "dev", interface, "up")
        if config["mode"] == "ap":
            start_business(interface, config)
            atomic(RUN / "hostapd.conf", hostapd_config(config, interface))
            atomic(RUN / "kea.json", json.dumps(ap_dhcp_config(config)))
            command("kea-dhcp4", "-t", str(RUN / "kea.json"))
            commands = [["hostapd", str(RUN / "hostapd.conf")], ["kea-dhcp4", "-c", str(RUN / "kea.json")]]
        else:
            atomic(RUN / "supplicant.conf", supplicant_config(config))
            atomic(NETWORK, "[Match]\nName=" + interface + "\n[Network]\nDHCP=ipv4\n"
                   "IPv6AcceptRA=no\nLinkLocalAddressing=no\n[DHCPv4]\nUseRoutes=no\nUseDNS=no\nUseNTP=no\n")
            command("networkctl", "reload")
            command("networkctl", "reconfigure", interface)
            commands = [["wpa_supplicant", "-D", "nl80211", "-i", interface, "-c", str(RUN / "supplicant.conf")]]
        for args in commands:
            environment = dict(os.environ, KEA_PIDFILE_DIR=str(RUN), KEA_LOCKFILE_DIR=str(RUN))
            processes.append(subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                              env=environment))
        next_reconcile = 0
        while not stopping:
            if any(process.poll() is not None for process in processes):
                raise RuntimeError("wireless child service exited")
            if config["mode"] == "ap" and time.monotonic() >= next_reconcile:
                if not (Path("/sys/class/net") / DATA_HOST).exists():
                    raise RuntimeError("wireless VPP handoff disappeared")
                reconcile_business(config)
                next_reconcile = time.monotonic() + 5
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
        prepare_regulatory(config["country"])
        if config["mode"] == "ap":
            subnet = ap_network(config)[0].network
            for link in json.loads(command("ip", "-j", "address", "show")):
                if link["ifname"] == interface:
                    continue
                for address in link.get("addr_info", []):
                    if address["family"] == "inet" and subnet.overlaps(ipaddress.ip_network(
                            address["local"] + "/" + str(address["prefixlen"]), strict=False)):
                        if link["ifname"] != CONTROL_HOST:
                            raise WirelessError("wifi_subnet_overlap")
            # Native WAN addresses exist only in VPP, not on the Linux netdev.
            current_interface = ""
            for line in command("vppctl", "show interface address").splitlines():
                header = re.match(r"^(\S+) \(", line)
                if header:
                    current_interface = header[1]
                assigned = re.match(r"^\s+L3 (\d+\.\d+\.\d+\.\d+/\d+)(?:\s|$)", line)
                if assigned and current_interface != VPP_INTERFACE and subnet.overlaps(
                        ipaddress.ip_network(assigned[1], strict=False)):
                    raise WirelessError("wifi_subnet_overlap")
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
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as failure:
        # Configurations contain credentials; never dump them or child stderr.
        if isinstance(failure, WirelessError):
            print(json.dumps({"error": {"code": failure.code}}))
        print("wireless operation failed", file=sys.stderr)
        raise SystemExit(1)


if __name__ == "__main__":
    main()
