#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: bash scripts/build-velo5x0-kernel.sh [options]

Build exactly one Bookworm amd64 kernel package, without installing it.
  --out DIR             Package output directory (default: dist/hardware/velo5x0).
  --work DIR            Parent of a fresh retained work tree (default: tmp/velo5x0-kernel).
  --cache DIR           Verified tarball cache (default: WORK/cache).
  --jobs N              Parallel compile jobs (default: 2).
  --package-version V   Debian version (default: 6.18.54-1).
  --check               Offline input/fragment checks only; no make or downloads.
  --config-only         Download/patch/configure only; never compile the kernel.
  --help                Show this help.

Requires Debian 12 (Bookworm), amd64, with build prerequisites already installed.
Linux version, URL, SHA-256 and the patch filenames cannot be overridden.
SOURCE_DATE_EPOCH defaults to 0 for reproducible timestamps.
The final stdout line is the absolute linux-image*.deb path. Progress goes to stderr.
EOF
}

die() { printf 'velo5x0-kernel: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "required command missing: $1"; }

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
hardware_dir=$repo_root/packaging/hardware/velo5x0
kernel_version=6.18.54
kernel_release=$kernel_version-velo5x0-r2
kernel_sha256=9df30b02dd8102bbd0be52556288ef6889ddbe7f1ddb96fbf847d0becf3eacac
kernel_url=https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-$kernel_version.tar.xz
package_name=linux-image-$kernel_release
package_version=$kernel_version-1
out_dir=$repo_root/dist/hardware/velo5x0
work_parent=$repo_root/tmp/velo5x0-kernel
cache_dir=
jobs=2
mode=build
patches=(
  200-igb-velocloud-edge5x0.patch
  210-mdio-gpio-clear-level-before-input.patch
  220-xhci-ti-tusb73x0-force-hcrst.patch
  230-dsa-pdata-own-tree.patch
  240-ath10k-qca988x-ct-temperature.patch
)

while [ "$#" -gt 0 ]; do
  case "$1" in
    --out|--work|--cache|--jobs|--package-version)
      [ "$#" -ge 2 ] && [ -n "$2" ] || die "$1 requires a value"
      case "$1" in
        --out) out_dir=$2 ;;
        --work) work_parent=$2 ;;
        --cache) cache_dir=$2 ;;
        --jobs) jobs=$2 ;;
        --package-version) package_version=$2 ;;
      esac
      shift 2 ;;
    --check|--config-only)
      [ "$mode" = build ] || die "--check and --config-only are mutually exclusive"
      mode=${1#--}
      shift ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

[[ "$jobs" =~ ^[1-9][0-9]*$ ]] || die "--jobs must be a positive integer"
[[ "$package_version" =~ ^6\.18\.54-[0-9][0-9A-Za-z.+~]*$ ]] ||
  die "--package-version must start with 6.18.54- and a numeric revision"

emit_maint_script() {
  printf '#!/bin/sh\nset -eu\nkernel_release=%s\naction=%s\n' "$kernel_release" "$1"
  cat <<'EOF'
image=/boot/vmlinuz-$kernel_release
export DEB_MAINT_PARAMS="$*"
export INITRD=Yes

case "$action:${1:-}" in
  preinst:install|preinst:upgrade|postinst:configure|prerm:remove|postrm:remove|postrm:purge) ;;
  *) exit 0 ;;
esac
if [ "$action" = postinst ]; then
  depmod -a "$kernel_release"
  if [ -s "/boot/initrd.img-$kernel_release" ]; then
    update-initramfs -u -k "$kernel_release"
  else
    update-initramfs -c -k "$kernel_release"
  fi
fi
# Run initramfs-tools, live-build and bootloader hooks with Debian's two arguments.
hookdir=/etc/kernel/$action.d
if [ -d "$hookdir" ]; then
  run-parts --exit-on-error --arg="$kernel_release" --arg="$image" "$hookdir"
fi
EOF
}

check_packaged_modules() {
  local module_root=$1 module aliases protocol
  for module in igb mdio-gpio mv88e6xxx tag_dsa vc-edge5x0-mdio vc-edge5x0-dsa tun vhost_net vhost vhost_iotlb; do
    [ -n "$(find "$module_root" -type f -name "$module.ko" -print -quit)" ] ||
      die "required module absent from package: $module"
  done
  # Linux 6.18 builds both DSA and EDSA taggers into tag_dsa.ko.
  aliases=$(modinfo -F alias "$module_root/kernel/net/dsa/tag_dsa.ko")
  for protocol in dsa edsa; do
    grep -Fxq "dsa_tag:$protocol" <<<"$aliases" ||
      die "tag_dsa module lacks $protocol support"
  done
}

check_fragment() {
  awk '
    /^[[:space:]]*$/ { next }
    /^# CONFIG_[A-Z0-9_]+ is not set$/ { key=$2 }
    /^CONFIG_[A-Z0-9_]+=(y|m|[0-9]+|0x[0-9a-fA-F]+|".*")$/ {
      split($0, setting, "="); key=setting[1]
    }
    /^#/ && !/^# CONFIG_[A-Z0-9_]+ is not set$/ { next }
    {
      if (!key) { print "invalid fragment line " NR ": " $0 > "/dev/stderr"; bad=1; next }
      if (seen[key]++) { print "duplicate fragment key: " key > "/dev/stderr"; bad=1 }
      key=""
    }
    END { exit bad }
  ' "$hardware_dir/kernel.config"
  local setting
  for setting in \
    CONFIG_X86_64=y CONFIG_MODULES=y 'CONFIG_LOCALVERSION="-velo5x0-r2"' \
    CONFIG_IGB=m CONFIG_PHYLIB=m CONFIG_MDIO_BUS=m \
    CONFIG_MDIO_BITBANG=m CONFIG_MDIO_GPIO=m CONFIG_GPIO_ICH=y CONFIG_LPC_ICH=y \
    CONFIG_I2C=y CONFIG_I2C_GPIO=y CONFIG_GPIO_PCA953X=m CONFIG_LEDS_PCA963X=m \
    CONFIG_NET_DSA=m CONFIG_NET_DSA_MV88E6XXX=m CONFIG_NET_DSA_TAG_DSA=m \
    CONFIG_NET_DSA_TAG_EDSA=m CONFIG_ITCO_WDT=y CONFIG_WATCHDOG_CORE=y \
    CONFIG_WATCHDOG_HANDLE_BOOT_ENABLED=y CONFIG_WATCHDOG_OPEN_TIMEOUT=180 \
    CONFIG_SENSORS_CORETEMP=m CONFIG_ATH10K_PCI=m CONFIG_XDP_SOCKETS=y \
    CONFIG_VFIO_PCI=m CONFIG_TUN=m CONFIG_VHOST_NET=m CONFIG_VHOST=m \
    CONFIG_VHOST_IOTLB=m CONFIG_VHOST_TASK=y CONFIG_PPP=m CONFIG_PPPOE=m CONFIG_NF_TABLES=m \
    CONFIG_USB_XHCI_PCI=y CONFIG_USB_STORAGE=y CONFIG_SATA_AHCI=y \
    CONFIG_MMC_SDHCI_PCI=y CONFIG_EXT4_FS=y CONFIG_SQUASHFS=y CONFIG_OVERLAY_FS=y \
    CONFIG_BLK_DEV_INITRD=y CONFIG_CGROUPS=y CONFIG_SECCOMP_FILTER=y; do
    grep -Fxq "$setting" "$hardware_dir/kernel.config" || die "required setting missing: $setting"
  done
}

check_inputs() {
  require_command awk
  require_command sha256sum
  local name
  local -a found
  shopt -s nullglob
  found=("$hardware_dir"/patches/*.patch)
  shopt -u nullglob
  [ "${#found[@]}" -eq "${#patches[@]}" ] || die "the exact hardware patch set is required"
  for name in "${patches[@]}"; do
    [ -s "$hardware_dir/patches/$name" ] || die "missing patch: $name"
  done
  (cd "$hardware_dir/patches" && sha256sum --strict --check SHA256SUMS) >&2
  (cd "$hardware_dir/glue" && sha256sum --strict --check SHA256SUMS) >&2
  [ -s "$hardware_dir/glue/vc-edge5x0-dsa.c" ] || die "missing DSA glue"
  grep -Fxq 'obj-m := vc-edge5x0-mdio.o vc-edge5x0-dsa.o' "$hardware_dir/glue/Makefile" ||
    die "both board modules must be built"
  check_fragment
  for name in preinst postinst prerm postrm; do
    emit_maint_script "$name" | sh -n
  done
}

check_inputs
if [ "$mode" = check ]; then
  printf 'Pinned inputs and kernel fragment passed offline checks.\n'
  exit 0
fi

[ "$(uname -s)" = Linux ] || die "build/config-only requires Linux; --check is portable"
[ -r /etc/os-release ] || die "cannot identify build distribution"
# shellcheck disable=SC1091
. /etc/os-release
[ "${ID:-}" = debian ] && [ "${VERSION_CODENAME:-}" = bookworm ] ||
  die "run in a Debian Bookworm build environment (no automatic system changes)"
for tool in curl tar xz patch make gcc ld flex bison bc perl pkg-config dpkg dpkg-deb \
  dpkg-architecture depmod modinfo install find sort touch date mktemp xargs du head; do
  require_command "$tool"
done
[ "$(dpkg --print-architecture)" = amd64 ] || die "only native amd64 builds are supported"
[ "$(dpkg-architecture -qDEB_HOST_ARCH)" = amd64 ] || die "cross compilation is not supported"
pkg-config --exists libelf openssl || die "libelf-dev and libssl-dev are required"
unset ARCH CROSS_COMPILE KBUILD_OUTPUT KCONFIG_CONFIG KCONFIG_ALLCONFIG LOCALVERSION
unset KCFLAGS KCPPFLAGS KAFLAGS CFLAGS LDFLAGS LLVM LLVM_IAS
export ARCH=x86
export CC=gcc HOSTCC=gcc HOSTCXX=g++ LD=ld
export KBUILD_BUILD_USER=ly-route KBUILD_BUILD_HOST=bookworm KBUILD_BUILD_VERSION=1
export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-0}
[[ "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]] || die "SOURCE_DATE_EPOCH must be nonnegative"
export KBUILD_BUILD_TIMESTAMP
KBUILD_BUILD_TIMESTAMP=$(LC_ALL=C date -u -d "@$SOURCE_DATE_EPOCH")
export LC_ALL=C TZ=UTC
umask 022

mkdir -p "$out_dir" "$work_parent"
out_dir=$(CDPATH= cd -- "$out_dir" && pwd)
work_parent=$(CDPATH= cd -- "$work_parent" && pwd)
cache_dir=${cache_dir:-$work_parent/cache}
mkdir -p "$cache_dir"
cache_dir=$(CDPATH= cd -- "$cache_dir" && pwd)
work_dir=$(mktemp -d "$work_parent/build.XXXXXXXX")
printf 'Retained work tree: %s\n' "$work_dir" >&2
tarball=$cache_dir/linux-$kernel_version.tar.xz
if [ ! -f "$tarball" ]; then
  download=$(mktemp "$cache_dir/.linux-$kernel_version.XXXXXXXX")
  curl --fail --location --show-error --retry 3 --proto '=https' --tlsv1.2 \
    "$kernel_url" -o "$download" >&2
  printf '%s  %s\n' "$kernel_sha256" "$download" | sha256sum --check - >&2
  mv -- "$download" "$tarball"
fi
printf '%s  %s\n' "$kernel_sha256" "$tarball" | sha256sum --check - >&2
tar -xJf "$tarball" -C "$work_dir" --no-same-owner
source_dir=$work_dir/linux-$kernel_version
build_dir=$work_dir/build
glue_dir=$work_dir/glue
mkdir -p "$build_dir" "$glue_dir"
for name in "${patches[@]}"; do
  patch --batch --forward --fuzz=0 --no-backup-if-mismatch \
    -p1 -d "$source_dir" -i "$hardware_dir/patches/$name" >&2
done
cp "$hardware_dir/glue/Makefile" "$hardware_dir/glue/"*.c "$glue_dir/"

kmake() { make -C "$source_dir" O="$build_dir" ARCH=x86 "$@" >&2; }
kmake x86_64_defconfig
"$source_dir/scripts/kconfig/merge_config.sh" -m -O "$build_dir" \
  "$build_dir/.config" "$hardware_dir/kernel.config" >&2
kmake olddefconfig
kmake syncconfig

# Kconfig silently drops options with unmet dependencies; fail before compiling.
awk '
  FNR==NR {
    if (/^CONFIG_/) { split($0, a, "="); resolved[a[1]]=$0 }
    next
  }
  /^CONFIG_/ {
    split($0, a, "=")
    if (resolved[a[1]] != $0) {
      print "unresolved setting: " $0 " (got " resolved[a[1]] ")" > "/dev/stderr"; bad=1
    }
  }
  /^# CONFIG_[A-Z0-9_]+ is not set$/ {
    if ($2 in resolved) {
      print "must be disabled: " $2 " (got " resolved[$2] ")" > "/dev/stderr"; bad=1
    }
  }
  END { exit bad }
' "$build_dir/.config" "$hardware_dir/kernel.config" || die "kernel fragment did not resolve exactly"
resolved_release=$(make -s -C "$source_dir" O="$build_dir" ARCH=x86 kernelrelease)
[ "$resolved_release" = "$kernel_release" ] ||
  die "kernel release drift: $resolved_release (expected $kernel_release)"
if [ "$mode" = config-only ]; then
  printf 'Configuration only, no kernel compilation: %s\n' "$build_dir/.config"
  exit 0
fi

kmake -j"$jobs" bzImage modules
kmake -j"$jobs" M="$glue_dir" modules
package_root=$work_dir/package
module_root=$package_root/lib/modules/$kernel_release
doc_root=$package_root/usr/share/doc/$package_name
mkdir -p "$package_root/boot" "$package_root/DEBIAN" "$doc_root"
kmake INSTALL_MOD_PATH="$package_root" INSTALL_MOD_STRIP=1 DEPMOD=true modules_install
kmake M="$glue_dir" INSTALL_MOD_PATH="$package_root" INSTALL_MOD_STRIP=1 \
  INSTALL_MOD_DIR=extra/velo5x0 DEPMOD=true modules_install
# Do not ship absolute links to the private build/source directories.
rm -f "$module_root/build" "$module_root/source"
depmod -b "$package_root" -F "$build_dir/System.map" "$kernel_release" >&2
check_packaged_modules "$module_root"
install -m 0644 "$build_dir/arch/x86/boot/bzImage" "$package_root/boot/vmlinuz-$kernel_release"
install -m 0644 "$build_dir/.config" "$package_root/boot/config-$kernel_release"
install -m 0644 "$build_dir/System.map" "$package_root/boot/System.map-$kernel_release"

cat >"$package_root/DEBIAN/control" <<EOF
Package: $package_name
Version: $package_version
Section: kernel
Priority: optional
Architecture: amd64
Maintainer: Ly Route <root@ly-route.local>
Depends: kmod, initramfs-tools, linux-base (>= 4.5), debianutils
Recommends: firmware-atheros
Provides: linux-image
Description: Pinned Linux $kernel_version for VeloCloud Edge 520/540
 Bookworm hardware kernel with I354/MDIO, independent Marvell DSA switches,
 TI xHCI recovery and built-in iTCO watchdog. Includes both board glue modules.
EOF

for action in preinst postinst prerm postrm; do
  emit_maint_script "$action" >"$package_root/DEBIAN/$action"
  chmod 0755 "$package_root/DEBIAN/$action"
  sh -n "$package_root/DEBIAN/$action"
done
install -m 0644 "$hardware_dir/README.md" "$doc_root/README.md"
install -m 0644 "$hardware_dir/kernel.config" "$doc_root/kernel.config"
install -m 0644 "$source_dir/COPYING" "$doc_root/COPYING"
cp -R "$source_dir/LICENSES" "$doc_root/"
mkdir -p "$doc_root/patches" "$doc_root/glue"
cp "$hardware_dir/patches/"* "$doc_root/patches/"
cp "$hardware_dir/glue/Makefile" "$hardware_dir/glue/"*.c "$doc_root/glue/"
install -m 0644 "$repo_root/scripts/build-velo5x0-kernel.sh" "$doc_root/build-velo5x0-kernel.sh"
{
  printf 'Kernel: %s\nKernel-URL: %s\nKernel-SHA256: %s\n' \
    "$kernel_release" "$kernel_url" "$kernel_sha256"
  printf 'Package: %s\nVersion: %s\nSource-Date-Epoch: %s\n' \
    "$package_name" "$package_version" "$SOURCE_DATE_EPOCH"
  printf 'Compiler: %s\n' "$(gcc --version | head -n 1)"
  printf 'Base-Config: x86_64_defconfig\nGlue: external, same build and Module.symvers\n'
  if command -v git >/dev/null 2>&1; then
    printf 'Ly-Route-Revision: %s\n' "$(git -C "$repo_root" rev-parse HEAD 2>/dev/null || printf unknown)"
  fi
} >"$doc_root/build-provenance.txt"
(
  cd "$doc_root"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS
)
installed_size=$(du -sk "$package_root" | awk '{print $1}')
printf 'Installed-Size: %s\n' "$installed_size" >>"$package_root/DEBIAN/control"
find "$package_root" -print0 | xargs -0 touch -h -d "@$SOURCE_DATE_EPOCH"
deb=$out_dir/${package_name}_${package_version}_amd64.deb
# Keep the previous artifact intact on failure; publish from a unique staging file.
pending=$(mktemp "$out_dir/.${package_name}.XXXXXXXX.deb")
dpkg-deb --root-owner-group -Zxz --build "$package_root" "$pending" >&2
[ "$(dpkg-deb -f "$pending" Package)" = "$package_name" ] || die "package name mismatch"
mv -- "$pending" "$deb"
(cd "$out_dir" && sha256sum "$(basename "$deb")" >"$(basename "$deb").sha256")
printf '%s\n' "$deb"
