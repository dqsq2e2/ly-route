#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

usage() {
  cat <<'USAGE'
Usage:
  scripts/hotfix-deploy.sh --manifest dist/hotfix/ly-route-control/HASH/ly-route-control.manifest \
    --host root@gateway --remote /usr/lib/ly-route/ly-route-control \
    --service ly-route-control-api

Use --serial-port COM10 instead of --host when only a logged-in root serial
console is available. This transport also verifies the remote artifact hash.

Use --install-kernel with --host and a remote .deb path to install a sealed
linux-image package. The package must have a distinct kernel release from the
running kernel. No service restart or reboot is performed in this mode.

The artifact path is derived from the manifest. Deployment is rejected when
the current source fingerprint or artifact SHA-256 differs from that manifest.
USAGE
}

manifest=
host=
serial_port=
remote_file=
service=
install_kernel=false
while (($#)); do
  case "$1" in
    --manifest) manifest=${2:?missing value for --manifest}; shift 2 ;;
    --host) host=${2:?missing value for --host}; shift 2 ;;
    --serial-port) serial_port=${2:?missing value for --serial-port}; shift 2 ;;
    --remote) remote_file=${2:?missing value for --remote}; shift 2 ;;
    --service) service=${2:?missing value for --service}; shift 2 ;;
    --install-kernel) install_kernel=true; shift ;;
    --help|-h) usage; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

required_values=(manifest remote_file)
if [[ $install_kernel == false ]]; then required_values+=(service); fi
for value in "${required_values[@]}"; do
  if [[ -z ${!value} ]]; then
    printf '%s is required\n' "$value" >&2
    usage >&2
    exit 2
  fi
done

if [[ -z $host && -z $serial_port ]] || [[ -n $host && -n $serial_port ]]; then
  printf '%s\n' 'specify exactly one of --host or --serial-port' >&2
  exit 2
fi
if [[ $install_kernel == true && -n $serial_port ]]; then
  printf '%s\n' 'kernel installation requires the SSH transport' >&2
  exit 2
fi

[[ -f $manifest ]] || { printf 'manifest not found: %s\n' "$manifest" >&2; exit 2; }

manifest_value() {
  local key=$1
  awk -F= -v key="$key" '
    $1 == key { sub(/^[^=]*=/, ""); print; found=1; exit }
    END { if (!found) exit 1 }
  ' "$manifest"
}

format=$(manifest_value format)
artifact_file=$(manifest_value artifact_file)
expected_sha=$(manifest_value artifact_sha256)
expected_fingerprint=$(manifest_value source_fingerprint)
scope_csv=$(manifest_value source_scopes)

[[ $format == ly-route-hotfix-v1 ]] || { printf 'unsupported manifest format: %s\n' "$format" >&2; exit 2; }
[[ $artifact_file == "$(basename -- "$artifact_file")" ]] || { printf '%s\n' 'manifest artifact_file must be a basename' >&2; exit 2; }
local_file="$(dirname -- "$manifest")/$artifact_file"
[[ -f $local_file ]] || { printf 'sealed artifact not found: %s\n' "$local_file" >&2; exit 2; }

IFS=, read -r -a scopes <<<"$scope_csv"
current_fingerprint=$("$repo_root/scripts/source-fingerprint.sh" "${scopes[@]}")
if [[ $current_fingerprint != "$expected_fingerprint" ]]; then
  printf '%s\n' 'refusing stale hotfix: source changed after artifact build' >&2
  printf 'manifest=%s current=%s\n' "$expected_fingerprint" "$current_fingerprint" >&2
  exit 3
fi

local_sha=$(sha256sum "$local_file" | awk '{print $1}')
if [[ $local_sha != "$expected_sha" ]]; then
  printf '%s\n' 'refusing hotfix: artifact SHA-256 does not match manifest' >&2
  exit 3
fi

if [[ $install_kernel == true ]]; then
  package=$(dpkg-deb -f "$local_file" Package)
  architecture=$(dpkg-deb -f "$local_file" Architecture)
  [[ $package =~ ^linux-image-[A-Za-z0-9.+_-]+$ && $architecture == amd64 && $remote_file == /*.deb ]] || {
    printf '%s\n' 'kernel deployment requires an amd64 linux-image package and an absolute .deb target' >&2
    exit 2
  }
  kernel_release=${package#linux-image-}
  service=$package
fi

if [[ -n $serial_port ]]; then
  MSYS2_ARG_CONV_EXCL="$remote_file" "${LY_HOTFIX_PYTHON:-python3}" "$repo_root/scripts/hotfix-serial.py" \
    --port "$serial_port" --artifact "$local_file" --manifest "$manifest" \
    --sha256 "$local_sha" --remote "$remote_file" --service "$service"
  printf 'source_fingerprint=%s\n' "$expected_fingerprint"
  exit 0
fi

stamp=$(date -u +%Y%m%dT%H%M%SZ)
remote_tmp="/tmp/ly-route-hotfix-${stamp}-$$"
remote_manifest_tmp="${remote_tmp}.manifest"
remote_backup="${remote_file}.pre-hotfix"
remote_manifest_dir=/var/lib/ly-route/hotfix-manifests

scp_command=(scp)
ssh_command=(ssh)
askpass_file=
if [[ -n ${LY_HOTFIX_PASSWORD:-} ]]; then
  command -v setsid >/dev/null 2>&1 || { printf '%s\n' 'LY_HOTFIX_PASSWORD requires setsid' >&2; exit 2; }
  askpass_file=$(mktemp)
  trap 'rm -f "$askpass_file"' EXIT
  cat >"$askpass_file" <<'EOF'
#!/bin/sh
printf '%s\n' "$LY_HOTFIX_PASSWORD"
EOF
  chmod 0700 "$askpass_file"
  export SSH_ASKPASS="$askpass_file" SSH_ASKPASS_REQUIRE=force DISPLAY=:0
  scp_command=(setsid -w scp)
  ssh_command=(setsid -w ssh)
elif [[ -n ${SSHPASS:-} ]]; then
  command -v sshpass >/dev/null 2>&1 || { printf '%s\n' 'SSHPASS is set but sshpass is unavailable' >&2; exit 2; }
  scp_command=(sshpass -e scp)
  ssh_command=(sshpass -e ssh)
fi

remote_action="systemctl restart '$service'
systemctl is-active --quiet '$service'"
if [[ $install_kernel == true ]]; then
  "${ssh_command[@]}" "$host" "set -eu
test \"\$(uname -m)\" = x86_64
test \"\$(uname -r)\" != '$kernel_release'
test ! -e '/boot/vmlinuz-$kernel_release'
test ! -d '/lib/modules/$kernel_release'"
  remote_action="dpkg -i '$remote_file'
test -s '/boot/vmlinuz-$kernel_release'
test -s '/boot/initrd.img-$kernel_release'
test -s '/boot/config-$kernel_release'
printf 'installed_kernel=%s\\n' '$kernel_release'"
fi

printf 'Deploying sealed artifact %s -> %s:%s\n' "$local_file" "$host" "$remote_file"
"${scp_command[@]}" "$local_file" "$host:$remote_tmp"
"${scp_command[@]}" "$manifest" "$host:$remote_manifest_tmp"
"${ssh_command[@]}" "$host" "set -eu
test -f '$remote_file' && cp -a '$remote_file' '$remote_backup' || true
install -m 0755 '$remote_tmp' '$remote_file'
remote_sha=\$(sha256sum '$remote_file' | awk '{print \$1}')
test \"\$remote_sha\" = '$local_sha'
mkdir -p '$remote_manifest_dir'
install -m 0644 '$remote_manifest_tmp' '$remote_manifest_dir/$service.manifest'
$remote_action
rm -f '$remote_tmp' '$remote_manifest_tmp'
printf 'hotfix_sha256=%s\\n' \"\$remote_sha\"
printf 'source_fingerprint=%s\\n' '$expected_fingerprint'
printf 'backup=%s\\n' '$remote_backup'"

if [[ $install_kernel == true ]]; then
  printf 'Kernel %s installed; the running kernel and reboot state are unchanged.\n' "$kernel_release"
else
  printf 'Hotfix deployed and %s is active. Re-run the same scenario now.\n' "$service"
fi
