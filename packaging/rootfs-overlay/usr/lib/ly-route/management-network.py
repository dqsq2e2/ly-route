#!/usr/bin/env python3
import argparse
import ipaddress
import json
import os
import pathlib
import re
import tempfile


def subtract(ranges, first, last):
    result = []
    for low, high in ranges:
        if last < low or first > high:
            result.append((low, high))
            continue
        if low < first:
            result.append((low, first - 1))
        if last < high:
            result.append((last + 1, high))
    return result


def legacy_management(document, interface):
    dhcp = document.get("Dhcp4", {})
    return dhcp.get("interfaces-config", {}).get("interfaces") == [interface]


def documents(interface, cidr, gateway="", mac="", name_match="", business=None):
    if not re.fullmatch(r"[A-Za-z0-9_.:-]{1,15}", interface):
        raise ValueError("invalid management interface")
    address = ipaddress.IPv4Interface(cidr)
    network = address.network
    router = address.ip
    if mac and not re.fullmatch(r"(?:[0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}", mac):
        raise ValueError("invalid management MAC")
    if name_match and name_match != f"Name={interface}":
        raise ValueError("invalid management name match")
    if gateway:
        ipaddress.IPv4Address(gateway)

    first = int(network.network_address) + 1
    last = int(network.broadcast_address) - 1
    if first > last or int(router) < first or int(router) > last:
        raise ValueError("management subnet has no usable DHCP range")
    blocked = [(int(router), int(router))]
    if gateway:
        blocked.append((int(ipaddress.IPv4Address(gateway)),) * 2)
    if business and not legacy_management(business, interface):
        for subnet in business.get("Dhcp4", {}).get("subnet4", []):
            if not network.overlaps(ipaddress.IPv4Network(subnet["subnet"], strict=False)):
                continue
            for pool in subnet.get("pools", []):
                value = pool["pool"]
                if "-" in value:
                    low, high = [int(ipaddress.IPv4Address(v.strip())) for v in value.split("-", 1)]
                else:
                    prefix = ipaddress.IPv4Network(value, strict=False)
                    low, high = int(prefix.network_address), int(prefix.broadcast_address)
                blocked.append((low, high))

    base = int(network.network_address)
    preferred = [(base + 100, min(base + 199, last))] if last - first + 1 >= 220 else [(first, min(base + 199, last))]
    pools = preferred
    for low, high in blocked:
        pools = subtract(pools, low, high)
    if not pools:
        pools = [(first, last)]
        for low, high in blocked:
            pools = subtract(pools, low, high)
    if not pools:
        raise ValueError("business pools leave no management DHCP addresses")

    match = f"MACAddress={mac}\n{name_match}" if mac else f"Name={interface}"
    network_text = (
        f"[Match]\n{match}\n\n[Network]\nAddress={address}\nDHCP=no\n"
        f"LinkLocalAddressing=ipv4\nIPv6AcceptRA=yes\nGateway={gateway}\n\n"
        "[Route]\n"
        f"Destination={network}\nScope=link\nPreferredSource={router}\nTable=19088\n\n"
        "[RoutingPolicyRule]\n"
        f"From={router}/32\nTable=19088\nPriority=110\n"
    )
    # RFC 3442: a /32 on-link route keeps management traffic on this NIC
    # even when the client also connects to a same-prefix business LAN.
    host_route = (bytes([32]) + router.packed + bytes(4)).hex()
    dhcp = {
        "Dhcp4": {
            "interfaces-config": {"interfaces": [interface]},
            "lease-database": {"type": "memfile", "name": "/var/lib/kea/management-leases4.csv"},
            "subnet4": [{
                "id": 1,
                "interface": interface,
                "subnet": str(network),
                "valid-lifetime": 7200,
                "pools": [{"pool": f"{ipaddress.IPv4Address(low)} - {ipaddress.IPv4Address(high)}"} for low, high in pools],
                "option-data": [
                    {"name": "routers", "data": str(router)},
                    {"name": "domain-name-servers", "data": str(router)},
                    {"code": 121, "csv-format": False, "data": host_route},
                ],
            }],
        },
    }
    return network_text, dhcp


def write_atomic(path, content):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            output.write(content)
        os.chmod(temporary, 0o644)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def configure(interface, cidr, gateway, mac, name_match, network_file, dhcp_file, business_file):
    business_path = pathlib.Path(business_file)
    business = json.loads(business_path.read_text(encoding="utf-8")) if business_path.exists() else None
    network, dhcp = documents(interface, cidr, gateway, mac, name_match, business)
    write_atomic(network_file, network)
    if dhcp_file:
        write_atomic(dhcp_file, json.dumps(dhcp, indent=2) + "\n")
    if business and legacy_management(business, interface):
        # Migrate only the old management-only factory config, not business plans.
        write_atomic(business_file, json.dumps({"Dhcp4": {"interfaces-config": {"interfaces": []}, "subnet4": []}}, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser()
    for name in ("interface", "cidr", "network-file", "dhcp-file", "business-file"):
        parser.add_argument(f"--{name}", required=True)
    for name in ("gateway", "mac", "name-match"):
        parser.add_argument(f"--{name}", default="")
    args = parser.parse_args()
    configure(**vars(args))


if __name__ == "__main__":
    main()
