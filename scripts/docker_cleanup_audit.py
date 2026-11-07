#!/usr/bin/env python3
"""Read-only, ownership-conservative Docker cleanup audit for RunnerHub hosts.

Run on EACH physical Docker host (for example, compute and TrueNAS), before
and after controlled jobs. Never deletes images, containers or volumes.
"""
import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import shutil
import socket
import subprocess
import sys


def docker(*args):
    command = ["docker", *args]
    result = subprocess.run(command, text=True, capture_output=True, timeout=45, check=False)
    if result.returncode:
        raise RuntimeError(
            "read-only Docker command failed (" + " ".join(command[:3]) + "): "
            + result.stderr.strip()[:300]
        )
    return result.stdout.strip()


def parse_lines(text):
    return [line.strip() for line in text.splitlines() if line.strip()]


def docker_rows(text):
    return [json.loads(line) for line in parse_lines(text)]


def snapshot(run=docker):
    root = run("info", "--format", "{{.DockerRootDir}}").strip()
    if not root or not Path(root).is_absolute():
        raise ValueError("Docker returned an invalid root directory")
    daemon_id = run("info", "--format", "{{.ID}}").strip()
    if not daemon_id or daemon_id == "<no value>":
        raise ValueError("Docker returned no daemon ID (or an unknown value); refusing unverifiable snapshots")
    container_output = run(
        "ps", "-a", "--no-trunc", "--filter", "label=io.runnerhub.worker",
        "--format",
        '{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Label "io.runnerhub.worker"}}|{{.Label "io.runnerhub.role"}}|{{.Label "io.runnerhub.parent"}}',
    )
    containers = []
    for line in parse_lines(container_output):
        fields = line.split("|")
        if len(fields) != 7 or not fields[0] or not fields[4]:
            raise ValueError("Docker returned an unparseable RunnerHub container record")
        containers.append(dict(zip(
            ("id", "name", "image", "status", "workerId", "role", "parentId"),
            fields,
        )))
    containers.sort(key=lambda item: item["id"])
    containers_by_id = {container["id"]: container for container in containers}

    # Duplicate IDs make parent ownership ambiguous. Do not silently trust one.
    if len(containers_by_id) != len(containers):
        raise ValueError("Docker returned duplicate RunnerHub container IDs")

    orphan_reasons = []
    for container in containers:
        if container["role"] != "dind":
            continue
        parent_id = container["parentId"]
        parent = containers_by_id.get(parent_id)
        if not parent_id or parent is None:
            reason = "missing_parent"
        elif parent["role"] != "runner":
            reason = "parent_not_runner"
        elif parent["workerId"] != container["workerId"]:
            reason = "worker_mismatch"
        else:
            continue
        orphan_reasons.append({
            "id": container["id"],
            "parentId": parent_id,
            "workerId": container["workerId"],
            "reason": reason,
        })
    orphan_sidecars = [item["id"] for item in orphan_reasons]
    dangling = sorted(set(parse_lines(
        run("volume", "ls", "--filter", "dangling=true", "--format", "{{.Name}}")
    )))
    storage = docker_rows(run("system", "df", "--format", "{{json .}}"))
    try:
        usage = shutil.disk_usage(root)
        filesystem = {"totalBytes": usage.total, "usedBytes": usage.used, "freeBytes": usage.free}
    except OSError:
        filesystem = None

    return {
        "schemaVersion": 2,
        "capturedAtUTC": datetime.now(timezone.utc).isoformat(),
        "host": socket.gethostname(),
        "dockerRootDir": root,
        "dockerDaemonId": daemon_id,
        "dockerFilesystem": filesystem,
        "dockerStorageSummary": storage,
        "runnerhubContainers": containers,
        "orphanRunnerhubSidecarIds": sorted(orphan_sidecars),
        "orphanRunnerhubSidecarReasons": orphan_reasons,
        "danglingDockerVolumes": dangling,
        "limitations": [
            "Unattached Docker volumes cannot be attributed to RunnerHub without independent ownership evidence.",
            "Filesystem and Docker storage changes may be caused by unrelated workloads.",
            "Container image tags are not proof of the deployed immutable image digest.",
            "The filesystem measurement is local; run on the physical Docker daemon host, not through a remote context.",
            "A worker's deleted state in RunnerHub does not prove physical Docker reclamation.",
        ],
    }


def compare(before, after):
    for value in (before, after):
        if value.get("schemaVersion") != 2:
            raise ValueError("unrecognized Docker cleanup audit schema")
    if before["host"] != after["host"] or before["dockerRootDir"] != after["dockerRootDir"]:
        raise ValueError("snapshots must come from the same host and Docker root")
    if not before.get("dockerDaemonId") or before["dockerDaemonId"] != after.get("dockerDaemonId"):
        raise ValueError("snapshots must come from the same Docker daemon")
    initial = set(before["danglingDockerVolumes"])
    final = set(after["danglingDockerVolumes"])
    initial_orphans = set(before["orphanRunnerhubSidecarIds"])
    final_orphans = set(after["orphanRunnerhubSidecarIds"])
    before_containers = {c["id"] for c in before["runnerhubContainers"]}
    after_containers = {c["id"] for c in after["runnerhubContainers"]}
    first_disk = before.get("dockerFilesystem")
    last_disk = after.get("dockerFilesystem")
    free_delta = None
    if first_disk is not None and last_disk is not None:
        free_delta = last_disk["freeBytes"] - first_disk["freeBytes"]
    return {
        "host": before["host"],
        "dockerDaemonId": before["dockerDaemonId"],
        "before": before["capturedAtUTC"],
        "after": after["capturedAtUTC"],
        "runnerhubContainerCountBefore": len(before_containers),
        "runnerhubContainerCountAfter": len(after_containers),
        "newRunnerhubContainerIds": sorted(after_containers - before_containers),
        "remainingOriginalRunnerhubContainerIds": sorted(before_containers & after_containers),
        "orphanSidecarIdsAfter": sorted(final_orphans),
        "orphanSidecarCountBefore": len(initial_orphans),
        "orphanSidecarCountAfter": len(final_orphans),
        "newOrphanSidecarIds": sorted(final_orphans - initial_orphans),
        "resolvedOrphanSidecarIds": sorted(initial_orphans - final_orphans),
        "danglingVolumeCountBefore": len(initial),
        "danglingVolumeCountAfter": len(final),
        "newDanglingVolumeNamesUnattributed": sorted(final - initial),
        "dockerFilesystemFreeBytesDelta": free_delta,
        "conclusion": (
            "Read-only comparison only. Neither new dangling volumes nor disk deltas "
            "establish RunnerHub ownership or safe deletion; correlate with real jobs."
        ),
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    capture = subparsers.add_parser("snapshot", help="Capture a local read-only Docker baseline")
    capture.add_argument("--output", required=True, type=Path)
    delta = subparsers.add_parser("compare", help="Compare two snapshots from the same host")
    delta.add_argument("--before", required=True, type=Path)
    delta.add_argument("--after", required=True, type=Path)
    args = parser.parse_args(argv)
    if args.command == "snapshot":
        result = snapshot()
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(f"Wrote read-only Docker snapshot to {args.output}")
    else:
        before = json.loads(args.before.read_text(encoding="utf-8"))
        after = json.loads(args.after.read_text(encoding="utf-8"))
        print(json.dumps(compare(before, after), indent=2, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, ValueError, subprocess.TimeoutExpired) as exc:
        print(f"Docker audit failed: {exc}", file=sys.stderr)
        sys.exit(1)
