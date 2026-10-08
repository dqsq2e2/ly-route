# VeloCloud 5x0 Bookworm Kernel

This is the self-contained hardware kernel input for the optional `velo5x0`
amd64 profile. It is not a rootfs, disk or ISO builder. No sibling checkout,
OpenWrt build system, DKMS, mdio-tools or mdio-netlink module is needed to build
or use the package. Runtime services and `overlay/**` belong to the hardware
profile's runtime integration.

## Build Contract

Run in a native Debian 12 Bookworm amd64 environment with prerequisites already
installed. The builder never runs apt, installs packages, mounts filesystems,
changes the host bootloader, or accesses a board.

Build prerequisites: `build-essential`, `gcc`, `binutils`, `make`, `flex`,
`bison`, `libssl-dev`, `libelf-dev`, `pkg-config`, `bc`, `perl`, `curl`,
`ca-certificates`, `xz-utils`, `patch`, `dpkg-dev`, `kmod`, `coreutils`,
`findutils`, `gawk`. Bookworm's GCC 12 is supported; no CPU-native flags are
used. Firmware is supplied separately by Bookworm `firmware-atheros` from
`non-free-firmware`, not downloaded or embedded by this builder.

```sh
bash scripts/build-velo5x0-kernel.sh --check
bash packaging/hardware/velo5x0/glue/tests/check.sh
bash scripts/build-velo5x0-kernel.sh --config-only --work /build/velo5x0
bash scripts/build-velo5x0-kernel.sh --out /artifacts/velo5x0 --jobs 2
```

`--check` is offline and checks input hashes, the exact patch set, fragment
syntax/required settings, and all four generated maintainer script syntaxes.
The test script compiles only a userspace fake-MDIO harness, never a kernel.
`--config-only` downloads, verifies, extracts, applies patches and resolves
Kconfig. It builds Kconfig's small host utilities but no kernel or modules.
The release build runs from committed source in the hardware profile's CI.

Options are listed by `--help`. `--work` is a parent directory, not an existing
kernel source tree; every invocation allocates a fresh, retained `build.*`
directory. `--cache` optionally reuses the pinned tarball, reverified every
time. Failure never reuses a partly patched source tree or replaces an earlier
package. Compile jobs default to two to limit memory use on Atom-class systems.

The default single binary package is:

- Package: `linux-image-6.18.54-velo5x0-r2`
- Version: `6.18.54-1` (override only the Debian revision with `--package-version`)
- Architecture: `amd64`
- Filename: `linux-image-6.18.54-velo5x0-r2_6.18.54-1_amd64.deb`
- Default output: `dist/hardware/velo5x0/`; `--out DIR` is the integration contract.
- The final stdout line is its absolute path; logs go to stderr.
- A sibling `<filename>.sha256` file covers the finished package.

No headers, debug, libc-dev or separate glue package is emitted. Both glue
modules are built against the just-built kernel and `Module.symvers`, installed
under `/lib/modules/6.18.54-velo5x0-r2/extra/velo5x0/`, and included in the image
package. The package also includes `/boot/vmlinuz-6.18.54-velo5x0-r2`,
`/boot/config-6.18.54-velo5x0-r2`, `/boot/System.map-6.18.54-velo5x0-r2`, all selected
kernel modules, builtin metadata and depmod indexes. Private build/source
symlinks are removed.

Linux 6.18 provides both DSA and EDSA protocols through `tag_dsa.ko`, not a
separate `tag_edsa.ko`. Package checks require both protocol aliases in that
module as well as the board drivers. Revision r2 additionally requires tun,
vhost_net, vhost and vhost_iotlb for VPP LCP control interfaces, with VHOST_TASK
built in. Its distinct release and module directory preserve the old kernel
for rollback when installing it on an existing appliance.

`Depends: kmod, initramfs-tools, linux-base (>= 4.5), debianutils`;
`Recommends: firmware-atheros`; `Provides: linux-image`.
On configure, the package runs depmod, creates or updates its initrd explicitly,
then invokes `/etc/kernel/postinst.d` with the kernel release and image path.
`INITRD=Yes` and `DEB_MAINT_PARAMS` are exported. The analogous Debian
preinst/prerm/postrm hooks run for installation/removal, including initramfs
cleanup and bootloader updates supplied by those packages. Hook failures are
fatal. Direct initrd creation avoids leaving live-build/chroot bootloader hooks
with only a deferred dpkg trigger. There is no custom initramfs hook or board
service in this package.

`SOURCE_DATE_EPOCH` defaults to zero; build user/host/version and timestamps are
fixed. The package embeds the original licenses, five patches, final glue,
fragment, builder and an input SHA256 manifest in
`/usr/share/doc/linux-image-6.18.54-velo5x0-r2/`, along with kernel URL/hash,
compiler and Ly Route revision. Source licensing obligations include the
upstream kernel tarball plus these adaptation inputs; keep them with release
source. The binary is unsigned: Secure Boot signing is a separate integration
step.

## Configuration Decisions

Start from the pinned upstream `x86_64_defconfig`, merge `kernel.config`, run
`olddefconfig`, then require every requested positive setting and disabled
setting to resolve exactly. A dependency mismatch aborts before compilation.
There is no `localmodconfig` and no reliance on the CI host's attached devices.

Use baseline x86-64, `-O2`, 16 possible CPUs, 250 Hz, no NUMA, non-preemptible
kernel, no local/native CPU tuning, no debug information and no unrelated
audio/media or large GPU drivers. SMP, ACPI, EFI/BIOS display, serial consoles,
virtualized guests and security mitigations remain available. This targets
C2000 Atom while keeping conservative QEMU and ESXi validation support.

- Built in: LPC/`gpio_ich`, I2C/`i2c_gpio` (open drain comes from the copied
  lookup table), I801, watchdog core/`iTCO_wdt` and vendor support, xHCI with the
  TI quirk, USB storage, SATA/AHCI, PCI/ACPI SDHCI eMMC, ext4, squashfs with
  gzip/XZ/LZ4/Zstd, overlay, loop, ISO9660, VFAT, initrd decompression, virtio
  boot media/network, legacy/UEFI consoles and systemd/security prerequisites.
- Modules: patched `igb`, explicit `PHYLIB`/`MDIO_BUS`,
  `mdio_gpio`/bitbang, Marvell PHY, DSA/`mv88e6xxx`/DSA and EDSA taggers,
  both board glue modules, PCA9557 through `gpio_pca953x`, `leds_pca963x`,
  `coretemp`, ADT7475 fan hwmon, standard `ath10k_pci`, tun/tap, bridges,
  VLANs, PPP/PPPoE/L2TP, nftables/conntrack/NAT/transparent proxy/ipset,
  qdiscs, IPsec, WireGuard and VFIO PCI/type1.
- `CONFIG_THERMAL=y` and `CONFIG_HWMON=y` include stock ATH10K thermal/hwmon
  support without enabling debugfs. A WiFi temperature sensor is registered
  only when the loaded firmware advertises thermal support and provides the
  temperature operation; its presence is not guaranteed by the kernel config.
  The additional local patch ports the CT 10.1 temperature command/event ABI
  (feature bit 44, command 0x906d, event 0x9022) from ath10k-ct commit
  `fcbdb70debc261f9df6734bbb76d81cdc88e0e26`. Only QCA988x on EDGE520/EDGE540
  prefer the separately supplied `firmware-ct.bin` automatically; absent that
  file, upstream firmware selection remains unchanged. Unimplemented CT feature
  bits are ignored rather than advertised as driver support. The hardware rootfs profile downloads
  CT full-community firmware and requires SHA-256
  `0723e73558e7187f099219bc5de2152336f27c40aa8ca6f2ed7e4f7cbd6049bd`.
  This is a bounded thermal ABI port, not the complete ath10k-ct driver.
  Live EDGE520 testing on 2026-10-08 measured 34/36/36/37/37/38 C with a
  WPA2 AP running on channel 1. Passive scanning and receive-only monitor mode
  returned the invalid -15 C firmware value before AP operation; a down radio
  returns ENETDOWN. Fan control rejects the invalid value and reports unavailable
  sensors explicitly. This temperature check does not qualify client traffic,
  WPA3, DHCP or VPP forwarding.
- AF_XDP sockets, BPF/JIT, network namespaces, cgroups, seccomp, AppArmor,
  Landlock, IPv6, policy routing and hugepages are enabled. VFIO retains the
  classic group/container API; unsafe no-IOMMU mode is disabled.
- `CONFIG_ITCO_WDT=y`, `CONFIG_WATCHDOG_HANDLE_BOOT_ENABLED=y` and
  `CONFIG_WATCHDOG_OPEN_TIMEOUT=180` make the driver available without module
  loading and bound boot-watchdog handoff to userspace. This does not change
  upstream initcall order or itself enable a watchdog that firmware left off.
  Runtime integration supplies early initramfs/systemd `RuntimeWatchdogSec`
  ownership.

The module/builtin split lets runtime integration control physical board reset ordering while
ensuring installer media, initrd and watchdog availability do not depend on
board runtime services.

## Runtime Boundary

The hardware profile supplies detection, initramfs/runtime services, firmware installation,
fan policy, GPIO resets, network naming and optional hardware-profile
rootfs/ISO/CI wiring. No autoload or modprobe policy is installed here.

Required board startup order:

1. `modprobe vc_edge5x0_mdio` creates external `gpio-0` MDIO and I2C adapter 9.
   Ensure `mdio_gpio` is available; its platform alias resolves through depmod.
2. Runtime integration initializes fan control with PCA9557 `0x1c` pin 6 high/pin 7 low,
   preserving all PoE bits, and pulses WAN reset only at `0x18` pin 4 before
   binding the WAN igb functions. Do not reset other pins.
3. `modprobe igb` exposes `igb-vc-0000:00:14.0` and `.1`, and binds WAN PHYs
   on `gpio-0`. Runtime integration handles any early auto-probe/deferred or failed WAN probe
   with bounded readiness and reprobe logic after reset.
4. `modprobe mv88e6xxx` (and `tag_dsa` as appropriate).
5. `modprobe vc_edge5x0_dsa dsa_mask=3`.

Glue module filenames use hyphens; modprobe/sysfs names use underscores.
The default `dsa_mask=0` remains opt-in, with jack mapping unchanged:
switch A ports 0..3 = `lan6,lan7,lan5,lan8`; switch B = `lan2,lan1,lan4,lan3`.
Kernel symbol dependencies are handled by depmod, but dependency discovery
cannot encode the board's GPIO reset or mv88e6xxx-before-glue ordering.
The MDIO glue is DMI-gated to `EDGE520`/`EDGE540`, so a QEMU guest must not
unconditionally load it. Upstream source records EDGE540 rev 2.8 testing;
other revisions still require physical qualification. In particular, the
copied rev-A MDIO pin choice and fixed I2C pins overlap at GPIO 11/12; this
port preserves that upstream behavior rather than claiming rev-A validation.

The local DSA addition runs **after** synchronous `mdio_device_register()` /
mv88e6xxx reset, and restores only CPU port 4, following the proven
`wrt_release/.../etc/init.d/velo5x0-switch` `cpu_link_up()`:
check direct port `0x14` register 0 bit 11; if down, select SerDes device `0xf`
page 1 (register 22), read register 0 and write `(value | 0x8100) & ~0x1800`,
wait one second, reread status; clear direct register 0 bit `0x1000` and
write register 1 `0x000e`. Indirect access uses global2 `0x1c` registers
`0x18`/`0x19`, commands `0x9400`/`0x9800`, and a 100 ms busy timeout before
and after every command. MDIO errors/timeouts propagate and failed CPU setup
unregisters that switch. No userspace MDIO or non-mainline module is required.

## Pinned Source And Provenance

Linux tarball:
`https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.18.54.tar.xz`

Literal SHA-256, obtained from kernel.org's
`https://cdn.kernel.org/pub/linux/kernel/v6.x/sha256sums.asc`:

```text
9df30b02dd8102bbd0be52556288ef6889ddbe7f1ddb96fbf847d0becf3eacac  linux-6.18.54.tar.xz
```

It matches `../immortalwrt/target/linux/generic/kernel-6.18`.
Downloads use curl over HTTPS and the literal digest is checked before
extraction. The checksum listing is PGP-signed upstream; this builder pins the
obtained digest, but does **not** claim local GPG signature verification.
Neither the version nor digest can be replaced through environment/CLI.

Adaptation sources were mechanically copied from `../immortalwrt`, origin
`https://github.com/dqsq2e2/immortalwrt`, checkout
`68a3752e7c8fbaf3fa6fca6930806de47011f971`. The source subset was clean;
the latest glue change was commit
`d44b3c1c8704e97ca9f47ac1d626097dc14dc692`.
The four original files from `target/linux/x86/patches-6.18/` are unchanged,
locked by `patches/SHA256SUMS`, and applied in this exact order with GNU patch
`--batch --forward --fuzz=0`:

1. `200-igb-velocloud-edge5x0.patch`: VeloCloud NVM-gated I354 link/MDIO glue;
   retains ordinary igb behavior for other hardware.
2. `210-mdio-gpio-clear-level-before-input.patch`: clear cached GPIO output
   before MDIO turnaround.
3. `220-xhci-ti-tusb73x0-force-hcrst.patch`: TI `104c:8241` reset fallback.
4. `230-dsa-pdata-own-tree.patch`: separate DSA trees for independent
   platform-data switches.

No other OpenWrt generic/x86 patches or firmware/kernel bundles are imported.
The IGB addition records derivation from VeloCloud's GPL code, copyright 2014
Velocloud Inc.; retained patch/source SPDX notices are authoritative.

Original `package/velo5x0-glue/src/` file SHA-256 values:

```text
e6fd11850c3e2ba34270f4274d6a54c771dd306a2bde847005a9ddb1ca26ff46  vc-edge5x0-mdio.c
f3c24523813993d410c7356db29e4f3b769beec2f71a735ff408d34932be7687  vc-edge5x0-dsa.c
```

MDIO source remains unchanged and is hash-checked. DSA source has only the
documented local CPU-link helper/integration addition; the sibling source
checkout is never edited. `glue/Makefile` uses the same two external module
targets as the upstream package. Package provenance hashes the final local
DSA source, not its original digest.
The imported inputs and checksum lists are LF, matching this repository's
Git `eol=lf` normalization; their checksums therefore survive a Linux checkout.

## Validation Scope

Before integration: run the offline check and host helper tests, then run
`--config-only` inside Bookworm to check real Kconfig dependency resolution.
Release validation must compile/package and check installed depmod/module contents and initrd
creation in a Bookworm staging root. QEMU should validate BIOS/UEFI boots,
virtio/IDE/AHCI media, serial console, nft/tun/PPP/VFIO module availability and
systemd startup. It cannot validate physical I354 MDIO, TI handoff, LAN jack
mapping, PoE, fan control, eMMC or the hardware watchdog. Those need separate
5x0 physical acceptance, including both independent DSA trees.
