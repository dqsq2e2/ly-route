#!/usr/bin/env python3
import argparse
import base64
import hashlib
import re
import secrets
import shlex
import time
from pathlib import Path


class SerialShell:
    def __init__(self, connection):
        self.connection = connection

    def run(self, command, timeout=90):
        marker = "LY_HOTFIX_" + secrets.token_hex(12)
        script = f"\n(\n{command}\n)\nprintf '\\n{marker}:%s\\n' \"$?\"\n"
        encoded = script.encode("utf-8")
        for start in range(0, len(encoded), 256):
            self.connection.write(encoded[start:start + 256])
            self.connection.flush()
            time.sleep(0.04)
        data = bytearray()
        pattern = re.compile(rb"\r?\n" + marker.encode() + rb":([0-9]+)\r?\n")
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            data.extend(self.connection.read(4096))
            match = pattern.search(data)
            if match:
                output = data[:match.start()].decode("utf-8", "replace")
                status = int(match.group(1))
                if status:
                    raise RuntimeError(f"serial command failed ({status}):\n{output}")
                return output
        raise TimeoutError("serial command did not return its completion marker")

    def upload(self, contents, destination):
        marker = "LY_DATA_" + secrets.token_hex(12)
        encoded = base64.encodebytes(contents).decode("ascii")
        self.run(
            f"umask 077\nbase64 -d > {shlex.quote(destination)} <<'{marker}'\n"
            f"{encoded}{marker}"
        )


def deployment_command(remote, service, staging, expected_sha):
    target = shlex.quote(remote)
    unit = shlex.quote(service)
    backup = shlex.quote(remote + ".pre-hotfix")
    artifact = shlex.quote(staging)
    manifest = shlex.quote(staging + ".manifest")
    manifest_target = shlex.quote(f"/var/lib/ly-route/hotfix-manifests/{service}.manifest")
    return f"""set -eu
test "$(sha256sum {artifact} | cut -d' ' -f1)" = {shlex.quote(expected_sha)}
test ! -f {target} || cp -a {target} {backup}
install -m 0755 {artifact} {target}
test "$(sha256sum {target} | cut -d' ' -f1)" = {shlex.quote(expected_sha)}
mkdir -p /var/lib/ly-route/hotfix-manifests
install -m 0644 {manifest} {manifest_target}
systemctl restart {unit}
systemctl is-active --quiet {unit}
rm -f {artifact} {manifest}
printf 'hotfix_sha256=%s\\nbackup=%s\\n' {shlex.quote(expected_sha)} {backup}
"""


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", required=True)
    parser.add_argument("--artifact", required=True, type=Path)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--remote", required=True)
    parser.add_argument("--service", required=True)
    args = parser.parse_args()
    contents = args.artifact.read_bytes()
    if hashlib.sha256(contents).hexdigest() != args.sha256:
        raise RuntimeError("artifact changed after manifest validation")
    if not args.remote.startswith("/") or not re.fullmatch(r"[A-Za-z0-9_.@-]+", args.service):
        raise ValueError("invalid remote path or service name")

    import serial

    connection = serial.Serial(port=None, baudrate=115200, timeout=0.2, write_timeout=15)
    connection.dtr = False
    connection.rts = False
    connection.port = args.port
    connection.open()
    shell = SerialShell(connection)
    terminal_state = None
    staging = "/tmp/ly-route-hotfix-" + secrets.token_hex(12)
    try:
        shell.run('set -eu\ntest "$(id -u)" = 0\ncommand -v base64\ncommand -v sha256sum')
        output = shell.run("stty -g")
        states = re.findall(r"(?:[0-9a-f]+:)+[0-9a-f]+", output)
        if not states:
            raise RuntimeError("could not save serial terminal settings")
        terminal_state = states[-1]
        shell.run("stty -echo")
        shell.upload(contents, staging)
        shell.upload(args.manifest.read_bytes(), staging + ".manifest")
        print(shell.run(deployment_command(args.remote, args.service, staging, args.sha256)), flush=True)
    finally:
        if terminal_state is not None:
            try:
                connection.write(b"\x03")
                shell.run("stty " + shlex.quote(terminal_state), timeout=10)
            except (OSError, RuntimeError, TimeoutError):
                pass
        connection.close()


if __name__ == "__main__":
    main()
