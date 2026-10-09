# EDGE520 VFIO Qualification Blocker

Verified on 2026-10-09, Asia/Shanghai. This is a failed physical prerequisite
assessment, not successful VFIO binding, DPDK takeover or DPDK traffic acceptance.

## Live Platform

- CPU: Intel Atom C2558, four cores.
- Board: VeloCloud EDGE520, version 2.8.
- Firmware: coreboot VELOCLOUD-EDGE-01.00.00.05, dated 2018-04-27.
- Kernel: 6.18.54-velo5x0-r2, with CONFIG_DMAR_TABLE and CONFIG_INTEL_IOMMU
  enabled, and VFIO PCI/type1 modules loaded.
- Boot arguments already include intel_iommu=on and iommu=pt.
- 512 2-MiB hugepages are allocated.
- There is no ACPI DMAR table, IOMMU device or IOMMU group.
- CONFIG_VFIO_NOIOMMU is disabled.

The boot message "DMAR: IOMMU enabled" alone is not evidence of an active
DMA-remapping unit: the actual table, device and group readbacks are absent.
This is not explained by missing VFIO modules, missing hugepages or a missing
Intel IOMMU boot flag. The current platform does not expose the prerequisite
for isolated VFIO. These observations do not by themselves distinguish
unsupported hardware from a firmware limitation; no processor support claim
or speculative BIOS fix is recorded as established.

## Real PCI Checks

The installed helper was run in read-only mode against both physical SFP
functions. Both are currently igb-owned:

- SFP1, 0000:04:00.0: rejected, no isolated IOMMU group.
- SFP2, 0000:04:00.1: rejected, no isolated IOMMU group.
- GE1, 0000:00:14.2: rejected as the protected management PCI function.

SFP1 remains an active AF_XDP zero-copy WAN. SFP2 has no carrier. The board's
0000:00:14.* I354 functions are separately reserved for the patched kernel
driver's MDIO/switch integration and are not installer DPDK candidates.
Even after resolving IOMMU, a physical DPDK LAN/WAN acceptance topology
would need supported data functions and working client/uplink connections.

No driver unbind, driver_override write, forced VPP restart, firmware flash,
appliance reboot, no-IOMMU bypass or UIO substitute was attempted. Binding
devices despite the failed prerequisite would not qualify the protected
VFIO path requested by the user.

## Installer False Positive Repaired

The saved installation map listed vfio_pci as a hardware-preflight candidate.
The installer used readlink -f and checked only whether its output was
nonempty. GNU readlink -f can return a path when its final component does
not exist. This was reproduced on the actual SFP1/SFP2 sysfs paths.

The owning installer source now uses readlink -e and requires the group's
devices directory. Candidate serialization also avoids a trailing comma
when native is the only candidate. New regressions cover missing and
dangling groups, a missing devices directory, valid groups, native-first
selection, VFIO-only JSON and the all-unavailable locked state.

Running the old and repaired probe definitions against the live machine,
with modprobe disabled, changed both SFP candidate lists from
zero_copy/vfio_pci to zero_copy only. The native selection is unchanged.
The old installed map was preserved as historical installation evidence;
it is not current VFIO capability evidence.

This is an installer-source correction. It does not repair missing platform
IOMMU support. No rootfs/ISO was built or installed, and no runtime hotfix
was needed or deployed.

## Verification and Remaining Acceptance

The 16 VFIO preflight regressions, 10 installer scenarios, native/DPDK path
selection scenarios and targeted daily source gate passed. The installer
script was sealed under dist/hotfix with its source fingerprint.

All checked configuration hashes and PCI driver bindings remained unchanged.
VPP retained PID 114106; GE2/SFP1 retained XDP ids 211/239 and zero-copy
attachments. The six checked services were active, with no failed systemd
units. An independent Windows client bound to GE2 resolved Baidu through
192.168.88.66 and received certificate-verified HTTPS 200. The PC's Wi-Fi
connection and the user's AP password were not changed.

Acceptance status:

- Isolated VFIO binding: blocked by missing platform IOMMU exposure.
- VPP DPDK takeover: not run.
- Independent DPDK DHCP/DNS/NAT/HTTPS traffic: not run.
- DPDK fallback/recovery, reboot and sustained load: not run.

Proceed only on a platform/firmware combination exposing real isolated
IOMMU groups. Then validate binding and VPP ownership, the independent
business flow, recovery to the original driver/path, and management
reachability. A fixture result or native-path HTTPS does not replace these.

Evidence: dist/verification/vfio-platform-inspection-20261009.jsonl,
dist/verification/vfio-real-qualification-20261009.jsonl,
dist/verification/vfio-installer-physical-probe-20261009.jsonl and
dist/verification/ge2-client-acceptance.json.
