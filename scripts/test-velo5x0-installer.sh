#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/ly-route-nics.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
mkdir -p "$work/sys/class/net" "$work/devices" "$work/drivers/igb"

# Run the installer function itself against a sysfs fixture.
definition=$(sed -n '/^list_nics() {$/,/^}$/p' "$repo_root/scripts/build-auto-install-iso.sh")
[[ -n $definition ]]
definition=${definition//\/sys\/class\/net/$work/sys/class/net}
definition=${definition//\/etc\/ly-route\/hardware/$work/hardware}
eval "$definition"

nic() {
  local name=$1 device=$2
  mkdir -p "$work/sys/class/net/$name" "$work/devices/$device"
  printf '1\n' > "$work/sys/class/net/$name/type"
  printf '00:11:22:33:44:55\n' > "$work/sys/class/net/$name/address"
  printf 'down\n' > "$work/sys/class/net/$name/operstate"
  ln -s "$work/devices/$device" "$work/sys/class/net/$name/device"
  [ -e "$work/devices/$device/driver" ] ||
    ln -s "$work/drivers/igb" "$work/devices/$device/driver"
}

nic eth0 0000:00:14.0
nic eth1 0000:00:14.1
nic enp0s20f2 0000:00:14.2
nic enp0s20f3 0000:00:14.3
nic enp4s0f0 0000:04:00.0
nic enp4s0f1 0000:04:00.1
for index in {1..8}; do
  nic "lan$index" "switch-port-$index"
  if ((index <= 4)); then conduit=eth1; else conduit=eth0; fi
  ln -s "$work/sys/class/net/$conduit" "$work/sys/class/net/lan$index/lower_$conduit"
done

physical=$(printf '%s\n' enp0s20f{2,3} enp4s0f{0,1} lan{1..8} | LC_ALL=C sort)
check_names() {
  local expected=$1 actual
  actual=$(list_nics | cut -d'|' -f1 | LC_ALL=C sort)
  if [[ $actual != "$expected" ]]; then
    printf 'Expected:\n%s\nActual:\n%s\n' "$expected" "$actual" >&2
    exit 1
  fi
}

printf 'velo5x0\n' > "$work/hardware"
check_names "$physical"
printf '%s\n' 'PASS: missing DSA markers do not expose either switch conduit'
list_nics | awk -F'|' '$1 == "lan1" { if ($3 != "0000:00:14.1") exit 1; found=1 } END { if (!found) exit 1 }'
list_nics | awk -F'|' '$1 == "lan5" { if ($3 != "0000:00:14.0") exit 1; found=1 } END { if (!found) exit 1 }'
printf '%s\n' 'PASS: physical switch jacks retain their conduit PCI identity'

mkdir "$work/sys/class/net/eth0/dsa"
ln -s "$work/sys/class/net/lan1" "$work/sys/class/net/eth1/upper_lan1"
check_names "$physical"
printf '%s\n' 'PASS: initialized DSA exposes all 12 physical ports'

printf 'generic\n' > "$work/hardware"
check_names "$(printf '%s\n' "$physical" eth0 eth1 | LC_ALL=C sort)"
printf '%s\n' 'PASS: generic hardware keeps its PCI interface choices'

definition=$(sed -n '/^probe_interface() {$/,/^}$/p' "$repo_root/scripts/build-auto-install-iso.sh")
[[ -n $definition ]]
definition=${definition//\/sys\//$work/sys/}
definition=${definition//\/etc\/ly-route\/hardware/$work/hardware}
definition=${definition//\/boot\//$work/boot/}
eval "$definition"

uname() { printf '%s\n' installer-test; }
modprobe() { return 1; }
mkdir -p "$work/sys/module/vfio_pci" "$work/boot" "$work/sys/class/net/enp4s0f1/queues/rx-0"
printf 'CONFIG_XDP_SOCKETS=y\n' > "$work/boot/config-installer-test"
device="$work/devices/0000:04:00.1"
row="enp4s0f1|00:11:22:33:44:55|0000:04:00.1|down|igb"

check_vfio_candidate() {
  local expected=$1
  probe_interface "$row" enp4s0f1 igb | python3 -c '
import json, sys
fields = sys.stdin.read().strip().split("|")
selected = json.loads(fields[-2])
candidates = json.loads("[" + fields[-1] + "]")
assert selected["tier"] == "vpp_native", fields
vfio = [item for item in candidates if item["mode"] == "vfio_pci"]
assert bool(vfio) == (sys.argv[1] == "present"), fields
' "$expected"
}

check_vfio_candidate absent
printf '%s\n' 'PASS: a nonexistent IOMMU path does not qualify VFIO'
ln -s "$work/sys/kernel/iommu_groups/17" "$device/iommu_group"
check_vfio_candidate absent
printf '%s\n' 'PASS: a dangling IOMMU group does not qualify VFIO'
mkdir -p "$work/sys/kernel/iommu_groups/17"
check_vfio_candidate absent
printf '%s\n' 'PASS: a missing IOMMU group device list does not qualify VFIO'
mkdir "$work/sys/kernel/iommu_groups/17/devices"
ln -s "$device" "$work/sys/kernel/iommu_groups/17/devices/0000:04:00.1"
check_vfio_candidate present
printf '%s\n' 'PASS: a real IOMMU group retains the VFIO candidate and native-first selection'

printf '# CONFIG_XDP_SOCKETS is not set\n' > "$work/boot/config-installer-test"
probe_interface "$row" enp4s0f1 igb | python3 -c '
import json, sys
fields = sys.stdin.read().strip().split("|")
selected = json.loads(fields[-2])
candidates = json.loads("[" + fields[-1] + "]")
assert selected["mode"] == "vfio_pci" and candidates == [selected], fields
'
printf '%s\n' 'PASS: a VFIO-only candidate is valid JSON'
rmdir "$work/sys/module/vfio_pci"
probe_interface "$row" enp4s0f1 igb | python3 -c '
import json, sys
fields = sys.stdin.read().strip().split("|")
assert fields[5] == "locked" and fields[-2] == "", fields
assert json.loads("[" + fields[-1] + "]") == [], fields
'
printf '%s\n' 'PASS: missing native and VFIO prerequisites leave forwarding locked'
