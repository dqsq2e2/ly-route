#!/bin/sh

install_velo5x0_overlay() {
  destination=$1
  firmware=$(mktemp)
  if ! curl -fsSL --retry 3 --connect-timeout 15 \
    https://www.candelatech.com/downloads/ath10k-fw-beta/firmware-2-ct-full-community.bin \
    -o "$firmware" ||
    ! printf '%s  %s\n' 0723e73558e7187f099219bc5de2152336f27c40aa8ca6f2ed7e4f7cbd6049bd "$firmware" |
      sha256sum --check --status; then
    rm -f "$firmware"
    echo "QCA988x CT firmware download or checksum verification failed" >&2
    return 1
  fi
  mkdir -p "$destination/lib/firmware/ath10k/QCA988X/hw2.0"
  install -m 0644 "$firmware" "$destination/lib/firmware/ath10k/QCA988X/hw2.0/firmware-ct.bin"
  rm -f "$firmware"
  cp -a "$repo_root/packaging/hardware/velo5x0/overlay/." "$destination/"
  chmod 0755 "$destination/usr/lib/ly-route/velo5x0-board.py"
  chmod 0755 "$destination/usr/lib/ly-route/wifi-runtime.py"
  chmod 0600 "$destination/etc/ly-route/velo5x0-fan.json"
  find "$destination/etc/systemd" -type f -exec chmod 0644 {} +
  mkdir -p "$destination/etc/systemd/system/multi-user.target.wants"
  for unit in ly-route-velo5x0-board.service ly-route-velo5x0-fan.service; do
    ln -sf "../$unit" "$destination/etc/systemd/system/multi-user.target.wants/$unit"
  done
  mkdir -p "$destination/etc/ly-route"
  printf 'velo5x0\n' > "$destination/etc/ly-route/hardware"
}

velo5x0_kernel_deb() {
  directory=${LY_ROUTE_KERNEL_DEBS_DIR:?LY_ROUTE_KERNEL_DEBS_DIR is required for velo5x0}
  set -- "$directory"/linux-image-*-velo5x0_*.deb
  [ "$#" -eq 1 ] && [ -s "$1" ] || {
    echo "expected one 5x0 kernel package in $directory" >&2
    return 1
  }
  printf '%s\n' "$1"
}
