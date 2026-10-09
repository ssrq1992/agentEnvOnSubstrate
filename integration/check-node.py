#!/usr/bin/env python3
"""Read-only Worker device preflight. Failure is never converted into a skip."""

import fcntl
import json
import os
import platform
import shutil
import stat
import sys
from pathlib import Path, PurePosixPath


def check_device(path, ioctl_request=None):
    if not stat.S_ISCHR(os.stat(path).st_mode):
        raise RuntimeError(f"{path} is not a character device")
    fd = os.open(path, os.O_RDWR | os.O_CLOEXEC)
    try:
        if ioctl_request is not None and fcntl.ioctl(fd, ioctl_request, 0) != 12:
            raise RuntimeError("unsupported KVM API version (expected 12)")
    finally:
        os.close(fd)



def worker_cgroup_scope(contents, mount="/sys/fs/cgroup"):
    for line in contents.strip().splitlines():
        if line.startswith("0::"):
            relative = line[3:]
            path = PurePosixPath(relative)
            if (not path.is_absolute() or str(path) != relative
                    or ".." in path.parts or "\x00" in relative or "\\" in relative):
                raise RuntimeError("invalid unified Worker cgroup scope")
            return Path(mount).joinpath(relative.lstrip("/"))
    raise RuntimeError("unified cgroup v2 Worker scope required")

def run_checks():
    results = []

    def record(name, action):
        try:
            action()
            results.append({"name": name, "status": "passed"})
        except (OSError, RuntimeError) as error:
            results.append({"name": name, "status": "failed", "reason": str(error)})

    def require(condition, message):
        if not condition:
            raise RuntimeError(message)

    record("linux-amd64", lambda: require(
        platform.system() == "Linux" and platform.machine() == "x86_64",
        "initial execution target requires Linux amd64"))
    record("kvm-api", lambda: check_device("/dev/kvm", 0xAE00))
    record("ublk-control-access", lambda: check_device("/dev/ublk-control"))
    record("tun-access", lambda: check_device("/dev/net/tun"))

    def controllers():
        scope = worker_cgroup_scope(Path("/proc/self/cgroup").read_text())
        enabled = scope.joinpath("cgroup.controllers").read_text().split()
        require({"cpu", "memory", "pids"}.issubset(enabled),
                "cgroup v2 cpu, memory and pids controllers required")

    record("cgroup-v2-controllers", controllers)

    def worker_limits():
        scope = worker_cgroup_scope(Path("/proc/self/cgroup").read_text())
        require(str(os.getpid()) in scope.joinpath("cgroup.procs").read_text().split(),
                "resolved cgroup must contain the Worker process")
        for name in ("cpu.max", "memory.max"):
            fields = scope.joinpath(name).read_text().split()
            require(bool(fields) and fields[0].isdecimal() and int(fields[0]) > 0,
                    f"finite Worker {name} required")

    record("worker-cgroup-limits", worker_limits)
    record("network-namespace", lambda: require(
        Path("/proc/self/ns/net").exists(), "network namespace unavailable"))
    for executable in ("ip", "iptables", "iptables-restore"):
        record(executable, lambda exe=executable: require(
            shutil.which(exe) is not None, f"missing executable: {exe}"))
    return results


def exit_code(results):
    return 0 if results and all(item["status"] == "passed" for item in results) else 1


def main():
    results = run_checks()
    print(json.dumps({"kind": "agentenv-worker-preflight-v1", "checks": results,
                      "scope": "device access only; no VM, ublk IO or cluster acceptance performed"},
                     indent=2))
    return exit_code(results)


if __name__ == "__main__":
    sys.exit(main())
