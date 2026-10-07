#!/usr/bin/env bash
set -euo pipefail
tests_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$tests_dir/../../../../.." && pwd)
builder=$repo_root/scripts/build-velo5x0-kernel.sh
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

bash -n "$builder"
bash "$builder" --check
bash "$builder" --help >/dev/null
expect_failure() {
  if bash "$builder" "$@" >"$work/stdout" 2>"$work/stderr"; then
    printf 'unexpected success: %s\n' "$*" >&2
    exit 1
  fi
}
expect_failure --jobs 0 --check
expect_failure --jobs -1 --check
expect_failure --jobs
expect_failure --kernel-version 6.18.55 --check
expect_failure --package-version 6.18.55-1 --check
expect_failure --check --config-only
printf 'Builder syntax, pinned CLI and offline checks passed.\n'

# Use real ELF module metadata to check the combined DSA/EDSA package contract.
eval "$(awk '
  /^die\(\)/ {print}
  /^check_packaged_modules\(\) \{/ {copy=1}
  copy {print}
  copy && /^}/ {exit}
' "$builder")"
module_root=$work/modules
mkdir -p "$module_root/kernel/net/dsa"
for module in igb mdio-gpio mv88e6xxx vc-edge5x0-mdio vc-edge5x0-dsa; do
  touch "$module_root/$module.ko"
done
tagger=$module_root/kernel/net/dsa/tag_dsa.ko
"${CC:-gcc}" -c "$tests_dir/tagger-module.c" -o "$tagger"
check_packaged_modules "$module_root"
expect_module_failure() {
  if (check_packaged_modules "$module_root") >"$work/stdout" 2>"$work/stderr"; then
    printf 'package accepted missing module/tagger: %s\n' "$1" >&2
    exit 1
  fi
  grep -Fq "$1" "$work/stderr"
}
"${CC:-gcc}" -DTEST_OMIT_EDSA -c "$tests_dir/tagger-module.c" -o "$tagger"
expect_module_failure 'tag_dsa module lacks edsa support'
"${CC:-gcc}" -DTEST_OMIT_DSA -c "$tests_dir/tagger-module.c" -o "$tagger"
expect_module_failure 'tag_dsa module lacks dsa support'
"${CC:-gcc}" -c "$tests_dir/tagger-module.c" -o "$tagger"
rm "$module_root/igb.ko"
expect_module_failure 'required module absent from package: igb'
printf 'Package checks: combined DSA/EDSA module accepted; missing protocols/modules rejected.\n'

# Exercise the actual generated maintainer template with a private fake boot root.
eval "$(awk '/^emit_maint_script\(\) \{/ {copy=1} copy {print} copy && /^}/ {exit}' "$builder")"
kernel_release=6.18.54-velo5x0
mkdir -p "$work/root/boot" "$work/root/etc/kernel/postinst.d" "$work/bin"
emit_maint_script postinst |
  sed "s|/boot/|$work/root/boot/|g; s|/etc/kernel/|$work/root/etc/kernel/|g" >"$work/postinst"
cat >"$work/bin/depmod" <<'EOF'
#!/bin/sh
set -eu
[ "$*" = "-a 6.18.54-velo5x0" ]
printf 'depmod\n' >>"$TEST_LOG"
EOF
cat >"$work/bin/update-initramfs" <<'EOF'
#!/bin/sh
set -eu
[ "$INITRD" = Yes ]
[ "$2" = -k ] && [ "$3" = 6.18.54-velo5x0 ]
printf 'initrd %s\n' "$1" >>"$TEST_LOG"
printf 'fake initrd\n' >"$TEST_ROOT/boot/initrd.img-$3"
EOF
cat >"$work/bin/run-parts" <<'EOF'
#!/bin/sh
set -eu
[ "$INITRD" = Yes ]
[ "$DEB_MAINT_PARAMS" = "configure" ]
[ "$1" = --exit-on-error ]
[ "$2" = --arg=6.18.54-velo5x0 ]
[ "$3" = "--arg=$TEST_ROOT/boot/vmlinuz-6.18.54-velo5x0" ]
[ "$4" = "$TEST_ROOT/etc/kernel/postinst.d" ]
[ -s "$TEST_ROOT/boot/initrd.img-6.18.54-velo5x0" ]
printf 'hooks\n' >>"$TEST_LOG"
if [ "${TEST_HOOK_FAIL:-0}" = 1 ]; then exit 9; fi
EOF
chmod +x "$work/bin/"*
export TEST_ROOT=$work/root TEST_LOG=$work/log
PATH="$work/bin:$PATH" sh "$work/postinst" configure
[ "$(cat "$TEST_LOG")" = "$(printf 'depmod\ninitrd -c\nhooks')" ]
: >"$TEST_LOG"
PATH="$work/bin:$PATH" sh "$work/postinst" configure
[ "$(cat "$TEST_LOG")" = "$(printf 'depmod\ninitrd -u\nhooks')" ]
if TEST_HOOK_FAIL=1 PATH="$work/bin:$PATH" sh "$work/postinst" configure; then
  printf 'postinst ignored a failing kernel hook\n' >&2
  exit 1
fi
printf 'Debian postinst: depmod, create/update initrd, hook arguments/order and failure tests passed.\n'

awk '
  /^#define VC_SMI_GLOBAL2/ {copy=1}
  /^static int vc_sw_register/ {exit}
  copy {print}
' "$tests_dir/../vc-edge5x0-dsa.c" >"$work/serdes-helper.inc"
"${CC:-gcc}" -std=gnu11 -O2 -Wall -Wextra -Werror \
  -I"$work" "$tests_dir/serdes-test.c" -o "$work/serdes-test"
"$work/serdes-test"
