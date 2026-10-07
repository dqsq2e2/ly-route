#!/usr/bin/env python3
import argparse
import os
import socket
import subprocess
import tempfile
import time
from pathlib import Path


def connect(path, process):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("QEMU exited before exposing its console")
        try:
            connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            connection.connect(str(path))
            connection.settimeout(1)
            return connection
        except (FileNotFoundError, ConnectionRefusedError):
            connection.close()
            time.sleep(0.1)
    raise RuntimeError(f"QEMU socket did not appear: {path}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--iso", required=True, type=Path)
    parser.add_argument("--timeout", type=int, default=240)
    parser.add_argument("--out", type=Path, default=Path("dist/verification"))
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    output = args.out / "velo5x0-usb-serial.log"
    with tempfile.TemporaryDirectory(prefix="ly-route-usb-boot-") as directory:
        work = Path(directory)
        disk = work / "target.img"
        with disk.open("wb") as target:
            target.truncate(8 * 1024 ** 3)
        command = [
            "qemu-system-x86_64", "-machine", "pc", "-m", "2048",
            "-cpu", "Nehalem", "-display", "none", "-vga", "none",
            "-serial", "null",
            "-serial", f"unix:{work / 'serial'},server=on,wait=off",
            "-monitor", f"unix:{work / 'monitor'},server=on,wait=off",
            "-no-reboot", "-device", "qemu-xhci",
            "-drive", f"file={args.iso.resolve()},format=raw,if=none,id=usb,readonly=on",
            "-device", "usb-storage,drive=usb,bootindex=1",
            "-drive", f"file={disk},format=raw,if=virtio",
            "-netdev", "user,id=management", "-device", "e1000,netdev=management",
        ]
        if os.access("/dev/kvm", os.R_OK | os.W_OK):
            command.extend(["-accel", "kvm"])
        with (args.out / "velo5x0-qemu.log").open("wb") as errors:
            process = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=errors)
            serial = monitor = None
            transcript = bytearray()
            cancelled = False
            try:
                serial = connect(work / "serial", process)
                monitor = connect(work / "monitor", process)
                deadline = time.monotonic() + args.timeout
                answered = set()
                prompts = {
                    b"Management IPv4/CIDR": b"\n",
                    b"Management gateway": b"\n",
                    b"Enter yes/y to format": b"no\n",
                }
                while time.monotonic() < deadline and process.poll() is None:
                    try:
                        data = serial.recv(65536)
                    except socket.timeout:
                        continue
                    if not data:
                        break
                    transcript.extend(data)
                    for marker, answer in prompts.items():
                        if marker in transcript and marker not in answered:
                            serial.sendall(answer)
                            answered.add(marker)
                    if b"Installation cancelled. No data was changed." in transcript:
                        cancelled = True
                        break
                output.write_bytes(transcript)
                if not cancelled:
                    monitor.sendall(b"info registers\n")
                    time.sleep(0.3)
                    print(monitor.recv(65536).decode("utf-8", "replace"))
                    raise RuntimeError(f"USB boot did not reach the serial installer; see {output}")
                print("USB HDD boot passed: no VGA, ttyS1 prompts, Nehalem CPU without AVX.")
                print("Installation was cancelled before disk writes.")
            finally:
                if monitor is not None:
                    try:
                        monitor.sendall(b"quit\n")
                    except OSError:
                        pass
                    monitor.close()
                if serial is not None:
                    serial.close()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == "__main__":
    main()
