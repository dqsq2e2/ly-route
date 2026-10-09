# GE2 Path Identity And Physical Path Verification

Verified on 2026-10-09, Asia/Shanghai. The appliance runs
6.18.54-velo5x0-r2 and the pinned VPP 25.10 build.

## GE2 Identity Repair

The live VPP collector returned GE2's interface name and counters but omitted
active_path and work_mode. Merging with the Linux inventory retained
kernel_stack; normalization then consulted a historical attach receipt that
listed only the SFP interfaces. GE2 was displayed as a kernel interface despite
its live AF_XDP attachment.

The collector now reports vpp for interfaces actually observed in the current
VPP reply. It does not guess AF_XDP, RDMA or DPDK from the interface name.
An old or missing attach receipt no longer overrides this live observation.
Management remains explicitly kernel-owned, and an unattached DSA port does
not become VPP merely because a LAN role was saved.

Public interface-list and GE2 stats regressions passed. Desktop 1440px and
mobile 390px Playwright checks show GE2 as VPP forwarding, with no page errors.
Frontend source and CPU sampling/text were not changed.

## VFIO Preflight Repair

The selected-PCI membership check matched a whitespace-delimited string but
the installer supplies newline-separated devices. This incorrectly rejected
a group shared only by selected data devices. Membership now compares PCI
tokens individually. The regression failed before this repair and passed
after it. A group shared with management or another unselected bound device
is still rejected before any binding.

prepare-vfio.sh now supports:

```sh
/usr/lib/ly-route/prepare-vfio.sh --check --pci 0000:04:00.1
```

This checks the device, management exclusion, IOMMU-group isolation, loaded
VFIO module and allocated hugepages without loading modules, changing drivers
or editing startup.conf. Explicit PCI selection is allowed only with --check.
Normal startup behavior and native-first selection remain unchanged.

All 16 focused preflight scenarios passed. Native/DPDK selection fixtures and
focused Go tests for path selection, driver identity, service-chain selection,
interface telemetry, CPU/dashboard and Wi-Fi passed with the race detector.
The daily source gate also passed.

## Physical Results And Limits

GE2 and SFP1 have live AF_XDP zero-copy, admin-up, matching netdev identities
and attached native XDP programs. The real spare-port VFIO check exits 1:

```text
VPP ownership locked: 0000:04:00.1 has no isolated IOMMU group
```

The system has no ACPI DMAR table or IOMMU groups, despite an IOMMU-enabled
kernel and boot parameters. Driver bindings and VPP startup configuration were
unchanged by this check. This is a blocked physical preflight, not successful
DPDK/VFIO binding or traffic acceptance. The firmware/platform cause is not
established; no unsafe no-IOMMU bypass was enabled.

Updating the preflight service also restarted its dependent VPP service.
Desired-state recovery retained GE2 and SFP1, not the previously present,
unused SFP2 attachment. SFP2 has no carrier or business address and is currently
shown as kernel-owned. LAN1-8 remain Linux-owned DSA ports; this repair does
not qualify their forwarding.

The restart reproduced the previously recorded AF_XDP warm-RX stall. Business
link recovery restored WAN reception. A transient WAN TX error persisted in
hardware readback after a Linux link cycle; recreating the affected attachment
and a public runtime apply committed as
runtime-14ef1b4d340f6b47964b3263a094aa13. An ordered queue reset avoided that
error. A subsequent DHCP client rebind restored the actual WAN address
192.168.1.221/24, and VPP ICMP to 223.5.5.5 passed 3/3.
These manual recovery observations do not close warm-restart stability.

The Windows GE2 client currently retains 169.254.53.39 and its DHCP renew
commands timed out. Resetting that specific adapter was denied by Windows
permissions. A GE2 cable reconnect was requested, keeping GE1 and the existing
Wi-Fi connection untouched. No GE2 client HTTPS success is claimed for this
repair until the independent client lease and traffic are verified again.
The computer was not instructed to connect to the appliance Wi-Fi.

Management SSH/HTTPS and both DHCP services remain active. Wi-Fi configuration
and hostapd hashes match the prior values, and the user's selected password
and derived PSK are unchanged.

## Deployment

Only fresh sealed artifacts were deployed using scripts/hotfix-deploy.sh.
No rootfs/ISO build or physical reboot was performed.

- Controller SHA-256:
  6b1d30af757f9c22f607d5aac25b708df4d162562ee1ca4d2944a9ea0c2f987e.
  Source fingerprint:
  8dce1891059002f397312de3d546e3fbd8e1350b2ecd61ad5289345a31bb8322.
- VFIO helper SHA-256:
  0fdf15ead79fb77e8c6a0ff18793719327fc96de45b150c9d8596802014f31f0.
  Source fingerprint:
  8efd19bcf039305431adc802ab77a04096f20a7d37d825e8173d34f9818d2133.
