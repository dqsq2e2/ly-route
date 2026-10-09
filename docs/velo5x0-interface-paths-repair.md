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

The subsequent Windows GE2 APIPA failure was localized to the computer side.
Before its reboot, the router reported 100 Mbps while Windows reported 1 Gbps.
A marked probe sent from the GE2 USB adapter did not reach any appliance port;
the corresponding GE1 control probe reached enp0s20f2. GE2 RX counters did not
advance and Kea saw no new request during capture. A separate Windows onboard
NIC capture-open call also hung. These observations do not identify a specific
Windows driver and do not establish another AF_XDP warm-RX failure.

The user rebooted the computer and reported recovery. Independent verification
then passed without replacing or recreating the appliance's GE2 attachment:

- The same client MAC 00:e0:4c:68:02:10 received 192.168.88.120/24.
  Its Windows interface index changed from 13 to 15; the adapter GUID and MAC
  were used to identify it again instead of assuming a stable index.
- A real DHCP renewal completed with exit 0. Captured REQUEST and ACK shared
  transaction a1f9aaaf; ACK option 3 and server identifier both contained
  192.168.88.66. The allocated address was 192.168.88.120.
- Windows DhcpDefaultGateway contains only 192.168.88.66. Its separate manual
  DefaultGateway value retains 192.168.1.2 and 192.168.100.1; those addresses
  are not supplied by this DHCP ACK. The preferred GE2 default route uses .66.
- With both the source address and Windows egress index explicitly bound to
  GE2, DNS through 192.168.88.66 resolved www.baidu.com to 183.2.172.177, and
  certificate-verified Baidu HTTPS returned HTTP/1.1 200 OK (29935 bytes).
  This test did not rely on the computer's Wi-Fi/default route.
- GE2 again negotiated 1000 Mbps/full duplex. Its XDP program remained 211;
  SFP1 remained 239. Both hardware readbacks report admin-up zero-copy with
  matching Linux netdev identities and no device error. Both received and
  transmitted physical traffic. WAN DHCP retains 192.168.1.221/24 and VPP
  public ICMP to 223.5.5.5 passed 3/3.

Current physical GE2 native-path DHCP and HTTPS acceptance therefore passes.
The DPDK/VFIO hardware prerequisite remains blocked as described above;
this successful native test does not qualify DPDK, sustained throughput or
LAN1-8. No appliance source change, deployment or appliance reboot was needed
for this computer-side recovery. The prepared administrator adapter-reset
script was not executed. The computer's Wi-Fi connection was not changed.

Management SSH/HTTPS and both DHCP services remain active. Wi-Fi configuration
and hostapd hashes match the prior values, and the user's selected password
and derived PSK are unchanged. The final failed-systemd-unit list is empty.

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
