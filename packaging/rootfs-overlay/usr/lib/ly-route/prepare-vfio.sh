#!/bin/sh
set -eu

fail() {
  printf '%s\n' "VPP ownership locked: $*" >&2
  exit 1
}

check_only=false
check_pci=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --check) check_only=true; shift ;;
    --pci) [ "$#" -ge 2 ] && [ -n "$2" ] || fail '--pci requires an address'; check_pci=$2; shift 2 ;;
    *) fail "unknown argument $1" ;;
  esac
done
if [ -n "$check_pci" ]; then
  [ "$check_only" = true ] || fail '--pci requires --check; explicit device checks never bind'
  case "$check_pci" in
    [0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f].[0-7]) ;;
    *) fail "invalid selected DPDK PCI address: $check_pci" ;;
  esac
fi
if [ "$check_only" = false ]; then
  modprobe vfio >/dev/null 2>&1 || true
  modprobe vfio-pci >/dev/null 2>&1 || true
fi

network=${LY_ROUTE_VFIO_NETWORK:-/etc/ly-route/installed-network.json}
rows_file=${LY_ROUTE_VFIO_DEVICES:-/etc/ly-route/vfio-devices}
startup=${LY_ROUTE_VFIO_STARTUP:-/etc/vpp/startup.conf}
sysfs=${LY_ROUTE_SYSFS_ROOT:-/sys}
management_pci=
pci_rows=
if [ -r "$rows_file" ]; then
  management_pci=$(awk -F'|' '$4 == "management" {print $3; exit}' "$rows_file")
  pci_rows=$(awk -F'|' '$4 == "data" {print $3}' "$rows_file")
elif [ -r "$network" ]; then
  network_rows=$(python3 - "$network" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    document = json.load(source)
management = document.get("management", {})
print("management|" + str(management.get("pci", "")).strip())
for interface in document.get("data_interfaces", []):
    selected = interface.get("selected")
    if not isinstance(selected, dict):
        continue
    if selected.get("tier") == "vpp_dpdk" and selected.get("hook") == "dpdk":
        print("data|" + str(interface.get("pci", "")).strip())
PY
  ) || fail 'installer NIC mapping is invalid'
  management_pci=$(printf '%s\n' "$network_rows" | awk -F'|' '$1 == "management" {print $2; exit}')
  pci_rows=$(printf '%s\n' "$network_rows" | awk -F'|' '$1 == "data" && $2 != "" {print $2}')
else
  [ -z "$check_pci" ] || fail 'explicit device check requires an installer NIC mapping'
  printf '%s\n' 'VPP ownership preflight skipped: no installer NIC mapping'
  exit 0
fi
[ -n "$management_pci" ] || fail 'management PCI identity is missing'
[ -z "$check_pci" ] || pci_rows=$check_pci
if [ -z "$pci_rows" ]; then
  printf '%s\n' 'VPP ownership preflight: native path selected; Linux retains NIC drivers'
  exit 0
fi

is_selected() {
  candidate=$1
  for selected_pci in $pci_rows; do
    [ "$candidate" != "$selected_pci" ] || return 0
  done
  return 1
}

driver_name() {
  device=$1
  basename "$(readlink -f "$sysfs/bus/pci/devices/$device/driver" 2>/dev/null || true)"
}

check_group_viable() {
  pci=$1
  device=$sysfs/bus/pci/devices/$pci
  group_link=$device/iommu_group
  if [ -e "$group_link" ]; then
    group=$(basename "$(readlink -f "$group_link")")
    group_dir=$sysfs/kernel/iommu_groups/$group/devices
    [ -d "$group_dir" ] || fail "$pci has no readable IOMMU group $group"
    for member in "$group_dir"/*; do
      [ -e "$member" ] || continue
      member_pci=$(basename "$member")
      [ "$member_pci" = "$pci" ] && continue
      member_driver=$(driver_name "$member_pci")
      if ! is_selected "$member_pci" && [ -n "$member_driver" ]; then
        fail "$pci IOMMU group $group is shared by $member_pci ($member_driver)"
      fi
    done
    return 0
  fi
  fail "$pci has no isolated IOMMU group"
}

bind_one() {
  pci=$1
  current_driver=$(driver_name "$pci")
  if [ "$current_driver" != vfio-pci ]; then
    printf '%s\n' vfio-pci > "$sysfs/bus/pci/devices/$pci/driver_override" || \
      fail "$pci cannot set vfio-pci driver override"
    if [ -n "$current_driver" ]; then
      printf '%s\n' "$pci" > "$sysfs/bus/pci/drivers/$current_driver/unbind" || \
        fail "$pci cannot unbind $current_driver"
    fi
    printf '%s\n' "$pci" > "$sysfs/bus/pci/drivers/vfio-pci/bind" || \
      fail "$pci cannot bind vfio-pci"
  fi
  [ "$(driver_name "$pci")" = vfio-pci ] || fail "$pci ownership is not vfio-pci"
}

for pci in $pci_rows; do
  case "$pci" in
    [0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f].[0-7]) ;;
    *) fail "invalid selected DPDK PCI address: $pci" ;;
  esac
  [ "$pci" = "$management_pci" ] && fail "data mapping includes management PCI $pci"
  [ -e "$sysfs/bus/pci/devices/$pci" ] || fail "configured data PCI $pci is absent"
  check_group_viable "$pci"
done

if [ "$check_only" = true ]; then
  [ -d "$sysfs/module/vfio_pci" ] || fail 'vfio-pci module is unavailable'
  hugepages=$(cat "$sysfs/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages" 2>/dev/null) || \
    fail 'hugepage allocation is unavailable'
  case "$hugepages" in ''|*[!0-9]*) fail 'hugepage allocation is invalid' ;; esac
  [ "$hugepages" -gt 0 ] || fail 'hugepages are not allocated'
  printf '%s\n' "VPP ownership check passed: $pci_rows; no drivers or startup files changed"
  exit 0
fi

for pci in $pci_rows; do
  iface=
  for net in "$sysfs/bus/pci/devices/$pci/net/"*; do
    [ -e "$net" ] || continue
    iface=$(basename "$net")
    break
  done
  [ -z "$iface" ] || ip link set dev "$iface" down 2>/dev/null || true
  bind_one "$pci"
  printf '%s\n' "VPP ownership prepared: ${iface:-pci} $pci"
done

[ -f "$startup" ] || fail 'VPP startup configuration is missing'
sed -i '/^# BEGIN LY ROUTE DPDK$/,/^# END LY ROUTE DPDK$/d' "$startup"
if grep -q 'plugin dpdk_plugin.so { disable }' "$startup"; then
  sed -i 's/plugin dpdk_plugin\.so { disable }/plugin dpdk_plugin.so { enable }/' "$startup"
else
  fail 'VPP startup configuration does not declare the DPDK plugin'
fi
{
  printf '\n# BEGIN LY ROUTE DPDK\n'
  printf 'dpdk {\n'
  for pci in $pci_rows; do
    case "$pci" in
      [0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f].[0-7]) ;;
      *) fail "invalid selected DPDK PCI address: $pci" ;;
    esac
    printf '  dev %s\n' "$pci"
  done
  printf '}\n# END LY ROUTE DPDK\n'
} >> "$startup"
