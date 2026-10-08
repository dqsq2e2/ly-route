#!/usr/bin/env python3
import importlib.util
import re
import unittest
from pathlib import Path
from unittest.mock import patch

SOURCE = Path(__file__).with_name("hotfix-serial.py")
spec = importlib.util.spec_from_file_location("hotfix_serial", SOURCE)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class Connection:
    def __init__(self, status=0):
        self.written = bytearray()
        self.status = status
        self.responded = False

    def write(self, data):
        self.written.extend(data)

    def flush(self):
        pass

    def read(self, _size):
        if self.responded:
            return b""
        self.responded = True
        marker = re.search(rb"LY_HOTFIX_[0-9a-f]+", self.written).group()
        return b"\r\noutput\r\n" + marker + b":" + str(self.status).encode() + b"\r\n"


class SerialTests(unittest.TestCase):
    @patch.object(module.time, "sleep")
    def test_command_waits_for_status_and_returns_output(self, _sleep):
        shell = module.SerialShell(Connection())
        self.assertIn("output", shell.run("true"))

    @patch.object(module.time, "sleep")
    def test_remote_failure_is_not_reported_as_success(self, _sleep):
        shell = module.SerialShell(Connection(status=7))
        with self.assertRaisesRegex(RuntimeError, "failed \\(7\\)"):
            shell.run("false")

    @patch.object(module.time, "sleep")
    def test_upload_uses_short_base64_lines_and_quoted_path(self, _sleep):
        connection = Connection()
        module.SerialShell(connection).upload(b"\x00\xff" * 4096, "/tmp/a b")
        script = connection.written.decode()
        self.assertIn("base64 -d > '/tmp/a b'", script)
        self.assertLessEqual(max(map(len, script.splitlines())), 100)

    def test_deploy_verifies_hash_before_install_and_checks_service(self):
        command = module.deployment_command("/usr/lib/board.py", "board.service", "/tmp/staged", "abc")
        self.assertLess(command.index("sha256sum"), command.index("install -m 0755"))
        self.assertIn("cp -a /usr/lib/board.py /usr/lib/board.py.pre-hotfix", command)
        self.assertIn("systemctl is-active --quiet board.service", command)


if __name__ == "__main__":
    unittest.main()
