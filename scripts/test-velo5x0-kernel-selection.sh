#!/usr/bin/env bash
set -euo pipefail
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
source "$repo/scripts/lib/velo5x0-profile.sh"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

package_fixture() {
  local scenario=$1 package=$2 architecture=${3:-amd64}
  local root="$work/$scenario-root" output="$work/$scenario"
  mkdir -p "$root/DEBIAN" "$output"
  printf 'Package: %s\nVersion: 6.18.54-1\nArchitecture: %s\nMaintainer: Fixture <fixture@example.invalid>\nDescription: Kernel selection fixture\n' \
    "$package" "$architecture" >"$root/DEBIAN/control"
  dpkg-deb --build --root-owner-group "$root" "$output/${package}_6.18.54-1_${architecture}.deb" >/dev/null
}

expect_rejection() {
  local directory=$1
  if LY_ROUTE_KERNEL_DEBS_DIR="$directory" velo5x0_kernel_deb >"$work/result" 2>"$work/error"; then
    printf 'invalid kernel selection accepted: %s\n' "$directory" >&2
    exit 1
  fi
}

for release in 6.18.54-velo5x0 6.18.54-velo5x0-r2 6.18.54-velo5x0-r12; do
  package_fixture "$release" "linux-image-$release"
  expected="$work/$release/linux-image-${release}_6.18.54-1_amd64.deb"
  actual=$(LY_ROUTE_KERNEL_DEBS_DIR="$work/$release" velo5x0_kernel_deb)
  test "$actual" = "$expected"
done
package_fixture arm64 linux-image-6.18.54-velo5x0-r2 arm64
expect_rejection "$work/arm64"
package_fixture invalid-revision linux-image-6.18.54-velo5x0-r2bad
expect_rejection "$work/invalid-revision"
package_fixture wrong-identity linux-image-generic
mv "$work/wrong-identity/linux-image-generic_6.18.54-1_amd64.deb" \
  "$work/wrong-identity/linux-image-6.18.54-velo5x0-r2_6.18.54-1_amd64.deb"
expect_rejection "$work/wrong-identity"
mkdir "$work/multiple" "$work/empty"
cp "$work/6.18.54-velo5x0/"*.deb "$work/6.18.54-velo5x0-r2/"*.deb "$work/multiple/"
expect_rejection "$work/multiple"
expect_rejection "$work/empty"
printf '%s\n' '5x0 kernel selection accepts revisioned amd64 packages and rejects ambiguous/invalid inputs.'
