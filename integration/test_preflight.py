import importlib.util
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("preflight", Path(__file__).with_name("check-node.py"))
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)


class PreflightTests(unittest.TestCase):
    def test_missing_device_is_failure_even_in_ci(self):
        with patch.dict("os.environ", {"CI": "true"}), \
             patch.object(preflight, "check_device", side_effect=FileNotFoundError("missing device")):
            results = preflight.run_checks()
        device_checks = [r for r in results if r["name"] in
                         {"kvm-api", "ublk-control-access", "tun-access"}]
        self.assertEqual(len(device_checks), 3)
        self.assertTrue(all(r["status"] == "failed" for r in device_checks))
        self.assertEqual(preflight.exit_code(results), 1)

    def test_worker_scope_handles_private_and_host_namespaces(self):
        self.assertEqual(preflight.worker_cgroup_scope("0::/"), Path("/sys/fs/cgroup"))
        self.assertEqual(preflight.worker_cgroup_scope("0::/kubepods/container.scope"),
                         Path("/sys/fs/cgroup/kubepods/container.scope"))
        for value in ("1:cpu:/worker", "0::relative", "0::/../host", "0::/a/../host",
                      "0::/a//b", "0::/a\\b", "0::/bad\x00"):
            with self.assertRaises(RuntimeError):
                preflight.worker_cgroup_scope(value)

    def test_empty_or_skipped_results_cannot_pass(self):
        self.assertEqual(preflight.exit_code([]), 1)
        self.assertEqual(preflight.exit_code([{"status": "skipped"}]), 1)


if __name__ == "__main__":
    unittest.main()
