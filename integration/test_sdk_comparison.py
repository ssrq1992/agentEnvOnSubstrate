import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("sdk_comparison", Path(__file__).parent / "sdk/run-comparison.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ComparisonGuardTest(unittest.TestCase):
    def test_missing_targets_fail(self):
        with self.assertRaises(ValueError):
            module.config({})

    def test_same_backend_and_mutable_image_fail(self):
        env = {"E2B_COMPAT_USER_IMAGE": "registry/image@sha256:" + "a" * 64,
               "AENV_TEMPLATE_ID": "shared-template"}
        for backend in ("STANDALONE", "SUBSTRATE"):
            for key, value in (("API_URL", "https://same.example"),
                               ("SANDBOX_URL", "https://sandbox.example"), ("API_KEY", "secret")):
                env[f"AENV_{backend}_{key}"] = value
        with self.assertRaises(ValueError):
            module.config(env)
        env["AENV_SUBSTRATE_API_URL"] = "https://fusion.example"
        self.assertEqual(len(module.config(env)), 2)
        env["E2B_COMPAT_USER_IMAGE"] = "registry/image:latest"
        with self.assertRaises(ValueError):
            module.config(env)

    def test_report_redacts_credentials(self):
        result = module.redact('key=secret Bearer jwt.token.value envdAccessToken: runtime-token', ["secret"])
        self.assertNotIn("secret", result)
        self.assertNotIn("jwt.token", result)
        self.assertNotIn("runtime-token", result)
