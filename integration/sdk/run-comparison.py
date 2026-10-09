#!/usr/bin/env python3
"""Run pinned, unmodified SDK smoke suites against both backends.

This produces component evidence, not full F01-F11 acceptance. Real endpoints
and isolated test credentials are mandatory; missing prerequisites fail.
"""
import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parent
SOURCE = ROOT.parent.parent / "AgentENV/scripts/tests/e2e"


def required(env, key):
    value = env.get(key, "").strip()
    if not value:
        raise ValueError(f"required environment variable missing: {key}")
    return value


def config(env):
    image = required(env, "E2B_COMPAT_USER_IMAGE")
    if not re.fullmatch(r".+@sha256:[0-9a-f]{64}", image):
        raise ValueError("E2B_COMPAT_USER_IMAGE must use an immutable sha256 digest")
    required(env, "AENV_TEMPLATE_ID")
    result = {}
    for backend in ("standalone", "substrate"):
        prefix = "AENV_" + backend.upper() + "_"
        result[backend] = {name: required(env, prefix + name)
                           for name in ("API_URL", "SANDBOX_URL", "API_KEY")}
        for field in ("API_URL", "SANDBOX_URL"):
            from urllib.parse import urlsplit
            url = urlsplit(result[backend][field])
            if url.scheme not in ("http", "https") or not url.hostname or url.username or url.password:
                raise ValueError(f"invalid backend {field}")
    if result["standalone"]["API_URL"] == result["substrate"]["API_URL"]:
        raise ValueError("comparison requires two distinct backend API endpoints")
    return result


def execute(command, env, timeout):
    process = subprocess.Popen(command, env=env, cwd=ROOT, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, text=True, start_new_session=True)
    try:
        output, _ = process.communicate(timeout=timeout)
        return process.returncode, output
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        output, _ = process.communicate()
        return 124, output + "\nSDK suite exceeded its deadline\n"


def redact(output, secrets):
    for secret in secrets:
        output = output.replace(secret, "[REDACTED]")
    output = re.sub(r"(?i)(bearer\s+)[A-Za-z0-9._~-]+", r"\1[REDACTED]", output)
    output = re.sub(r'(?i)((?:envdAccessToken|trafficAccessToken|apiKey|access_token)[\"\s:=]+)[^\s,\"}]+',
                    r'\1[REDACTED]', output)
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report-dir", type=Path, required=True)
    parser.add_argument("--timeout", type=int, default=900)
    args = parser.parse_args()
    if args.timeout < 1:
        raise ValueError("positive timeout required")
    targets = config(os.environ)
    versions = json.loads((ROOT / "versions.json").read_text())
    if sys.version_info[:2] != (3, 12):
        raise ValueError("SDK suite requires Python 3.12")
    node = shutil.which("node")
    if not node:
        raise ValueError("Node.js is required")
    node_version = subprocess.check_output([node, "--version"], text=True).strip()
    if node_version.split(".")[0] != "v22":
        raise ValueError("SDK suite requires Node.js 22")
    for package, key in (("e2b", "e2bPython"), ("e2b-code-interpreter", "codeInterpreterPython")):
        if importlib.metadata.version(package) != versions[key]:
            raise ValueError(f"incorrect installed {package} version")
    for package, key in (("e2b", "e2bTypeScript"), ("tsx", "tsx"), ("@e2b/cli", "e2bCLI")):
        data = json.loads((ROOT / "node_modules" / package / "package.json").read_text())
        if data["version"] != versions[key]:
            raise ValueError(f"incorrect installed {package} version")
    if not shutil.which("node"):
        raise ValueError("Node.js is required")
    args.report_dir.mkdir(parents=True, exist_ok=False, mode=0o700)
    report = {"kind": "agentenv-substrate-sdk-smoke-v1", "versions": versions,
              "fullAcceptance": False, "results": []}
    scripts = {"python": SOURCE / "e2b_python_sdk_compat.py",
               "typescript": SOURCE / "e2b_ts_sdk_compat.ts"}
    report["sourceSHA256"] = {key: hashlib.sha256(path.read_bytes()).hexdigest()
                              for key, path in scripts.items()}
    secrets = [item["API_KEY"] for item in targets.values()]
    # Copy only for Node's package resolution; bytes remain identical to the
    # upstream compatibility suite. Python uses the current locked environment.
    with tempfile.TemporaryDirectory(prefix=".sdk-run-", dir=ROOT) as temporary:
        ts = Path(temporary) / "compat.ts"
        shutil.copyfile(scripts["typescript"], ts)
        for backend, target in targets.items():
            env = os.environ.copy()
            env.update({"E2B_" + key: value for key, value in target.items()})
            env["E2B_COMPAT_TEST_PAUSE"] = "1"
            for language in scripts:
                command = ([sys.executable, str(scripts[language])] if language == "python"
                           else [node, "--import", "tsx", str(ts)])
                started = time.monotonic()
                code, output = execute(command, env, args.timeout)
                skipped = "skipping" in output.lower() or "skipped" in output.lower()
                status = "passed" if code == 0 and not skipped else "failed"
                filename = backend + "-" + language + ".log"
                (args.report_dir / filename).write_text(redact(output, secrets))
                report["results"].append({"backend": backend, "language": language,
                    "status": status, "exitCode": code, "skipDetected": skipped,
                    "seconds": round(time.monotonic() - started, 3), "log": filename})
                (args.report_dir / "report.json").write_text(json.dumps(report, indent=2))
                print(f"{backend}/{language}: {status}", flush=True)
    return 0 if all(item["status"] == "passed" for item in report["results"]) else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, OSError, importlib.metadata.PackageNotFoundError) as error:
        print(f"SDK comparison not executed: {error}", file=sys.stderr)
        sys.exit(1)
