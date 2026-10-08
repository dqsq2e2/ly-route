#!/usr/bin/env python3
import ast
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import tempfile
import unittest
from unittest.mock import patch

source = (Path(__file__).resolve().parents[1] / "scripts/build-runtime-debs.sh").read_text()
adapter = source.split('cat > "$package_root/usr/lib/ly-route/vpp-apply" <<\'EOF\'\n', 1)[1].split("\nEOF\n", 1)[0]
tree = ast.parse(adapter)
functions = ast.Module(body=[node for node in tree.body if isinstance(node, ast.FunctionDef)], type_ignores=[])
scope = dict(json=json, os=os, re=re, shlex=shlex, subprocess=subprocess)
exec(compile(functions, "vpp-apply", "exec"), scope)


class AFXDPReplayTests(unittest.TestCase):
    def test_old_installer_plan_restores_all_rx_queues_and_physical_mac(self):
        with tempfile.TemporaryDirectory() as directory:
            address = Path(directory) / "class/net/wan0/address"
            address.parent.mkdir(parents=True)
            address.write_text("f0:8e:db:08:56:10\n")
            operation = {"Name": "vpp.dataplane.attach",
                         "Payload": {"hook": "af_xdp", "linux_interface": "wan0", "vpp_interface": "lyroute-wan0"},
                         "VPPCtlCommands": ["?create interface af_xdp host-if wan0 name lyroute-wan0 zero-copy",
                                            "set interface state lyroute-wan0 up"]}
            with patch.dict(os.environ, LY_ROUTE_SYSFS_ROOT=directory):
                commands = scope["commands_from_operation"](operation)
            self.assertIn("num-rx-queues all zero-copy", commands[0])
            self.assertEqual(commands[1], "set interface mac address lyroute-wan0 f0:8e:db:08:56:10")
            self.assertEqual(commands[2], "set interface state lyroute-wan0 up")

    def test_live_attachment_never_creates_a_second_socket(self):
        hardware = "lyroute-wan0 1 up lyroute-wan0\r\n  netdev wan0\r\n  flags: admin-up zero-copy\r\n"
        with patch.dict(scope, run_vppctl=lambda *args: subprocess.CompletedProcess([], 0, hardware, "")), \
                patch.object(subprocess, "run", return_value=subprocess.CompletedProcess(
                    [], 0, '[{"xdp":{"prog":{"id":12}}}]', "")):
            self.assertTrue(scope["replay_af_xdp_already_present"](
                "vppctl", "create interface af_xdp host-if wan0 name lyroute-wan0 num-rx-queues all zero-copy"))

    def test_detached_xdp_is_not_accepted_as_live(self):
        hardware = "lyroute-wan0 1 up lyroute-wan0\n  netdev wan0\n  flags: admin-up zero-copy\n"
        with patch.dict(scope, run_vppctl=lambda *args: subprocess.CompletedProcess([], 0, hardware, "")), \
                patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "[{}]", "")):
            with self.assertRaisesRegex(ValueError, "lost its XDP"):
                scope["replay_af_xdp_already_present"](
                    "vppctl", "create interface af_xdp host-if wan0 name lyroute-wan0 zero-copy")


if __name__ == "__main__":
    unittest.main()
