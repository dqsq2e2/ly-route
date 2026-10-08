#!/usr/bin/env bash
set -euo pipefail
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
mkdir "$work/bin"
printf 'fake kernel package\n' >"$work/kernel.deb"
bash scripts/seal-hotfix-artifact.sh --artifact "$work/kernel.deb" \
  --name kernel-deploy-test --source-scope scripts/hotfix-deploy.sh >"$work/seal"
manifest=$(sed -n 's/^manifest_path=//p' "$work/seal")
cat >"$work/bin/dpkg-deb" <<'EOF'
#!/bin/sh
case "$3" in
  Package) printf '%s\n' "${TEST_PACKAGE:-linux-image-6.18.54-velo5x0-r2}" ;;
  Architecture) printf '%s\n' "${TEST_ARCH:-amd64}" ;;
  *) exit 1 ;;
esac
EOF
cat >"$work/bin/scp" <<'EOF'
#!/bin/sh
printf 'scp %s\n' "$*" >>"$TEST_LOG"
EOF
cat >"$work/bin/ssh" <<'EOF'
#!/bin/sh
printf 'ssh %s\n' "$*" >>"$TEST_LOG"
if [ "${TEST_PREFLIGHT_FAIL:-0}" = 1 ]; then exit 1; fi
EOF
chmod +x "$work/bin/"*
export PATH="$work/bin:$PATH" TEST_LOG="$work/log"
unset SSHPASS LY_HOTFIX_PASSWORD
deploy=(bash scripts/hotfix-deploy.sh --manifest "$manifest" --host root@fixture \
  --remote /var/cache/kernel-test.deb --install-kernel)
expect_rejection() {
  : >"$TEST_LOG"
  if "$@" >"$work/stdout" 2>"$work/stderr"; then
    printf '%s\n' 'invalid kernel deployment was accepted' >&2
    exit 1
  fi
  ! grep -q '^scp ' "$TEST_LOG"
}
expect_rejection env TEST_PACKAGE=unrelated-package "${deploy[@]}"
expect_rejection env TEST_ARCH=arm64 "${deploy[@]}"
expect_rejection env TEST_PREFLIGHT_FAIL=1 "${deploy[@]}"
: >"$TEST_LOG"
"${deploy[@]}" >"$work/stdout"
grep -Fq "test \"\$(uname -r)\" != '6.18.54-velo5x0-r2'" "$TEST_LOG"
grep -Fq "test ! -d '/lib/modules/6.18.54-velo5x0-r2'" "$TEST_LOG"
grep -Fq "dpkg -i '/var/cache/kernel-test.deb'" "$TEST_LOG"
grep -Fq "test -s '/boot/initrd.img-6.18.54-velo5x0-r2'" "$TEST_LOG"
! grep -q 'systemctl restart\|reboot' "$TEST_LOG"
printf '%s\n' 'Sealed kernel deployment validates package identity and preserves the running release.'
