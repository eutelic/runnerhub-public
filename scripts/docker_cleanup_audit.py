#!/usr/bin/env python3
"""Read-only, ownership-conservative Docker cleanup audit for RunnerHub hosts.

Run on EACH physical Docker host (for example, compute and TrueNAS), before
and after controlled jobs. Never deletes images, containers or volumes.
"""
import argparse
from datetime import datetime, timezone
import json
import os
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


def unique_json_object(pairs):
    """Reject duplicate JSON keys at every nesting level of audit evidence."""
    record = {}
    for key, value in pairs:
        if key in record:
            raise ValueError("Docker audit evidence contains duplicate JSON keys")
        record[key] = value
    return record


def docker_rows(text):
    return [json.loads(line, object_pairs_hook=unique_json_object) for line in parse_lines(text)]


def require_local_docker(run=docker, environ=None):
    """Refuse misleading host filesystem evidence from a remote Docker daemon.

    Docker's active context may be remote even without DOCKER_HOST. Checking
    both the override and active context fails closed rather than mixing
    Docker's remote volume inventory with the caller's local disk usage.
    """
    if environ is None:
        environ = os.environ
    override = environ.get("DOCKER_HOST", "")
    if override and not override.startswith("unix:///"):
        raise ValueError("Docker cleanup audit requires a local Unix-socket Docker daemon; remote DOCKER_HOST is unsupported")
    if override and not Path(override[len("unix://"):]).is_absolute():
        raise ValueError("Docker cleanup audit requires an absolute local Unix-socket path")
    try:
        endpoints = json.loads(run("context", "inspect", "--format", "{{json .Endpoints}}"))
        endpoint = endpoints["docker"]["Host"]
    except (ValueError, KeyError, TypeError) as exc:
        raise ValueError("cannot verify the active Docker context endpoint") from exc
    if not isinstance(endpoint, str) or not endpoint.startswith("unix:///") or not Path(endpoint[len("unix://"):]).is_absolute():
        raise ValueError("Docker cleanup audit requires a local Unix-socket context; remote Docker contexts are unsupported")
    # A Unix URI with a query/fragment is not a plain local socket path.
    # Docker may interpret such selectors differently from pathlib; refuse
    # ambiguous physical-host evidence rather than normalizing it silently.
    for selector in (override, endpoint):
        if "?" in selector or "#" in selector or "\\" in selector:
            raise ValueError("Docker cleanup audit requires plain Unix-socket paths without URI parameters")
        # Control characters and percent-encoded URI components make the
        # socket identity ambiguous between Docker and filesystem parsers.
        # Do not certify physical-host cleanup against such an endpoint.
        if any(ord(char) < 32 or ord(char) == 127 for char in selector) or "%" in selector:
            raise ValueError("Docker cleanup audit requires unambiguous plain Unix-socket paths")
    # An explicit socket override must agree with the inspected endpoint. A
    # mismatch makes the daemon behind the inventory ambiguous, so do not
    # attach local filesystem evidence to it.
    if override and override != endpoint:
        raise ValueError("Docker cleanup audit requires matching DOCKER_HOST and active context endpoints")
    return endpoint


def snapshot(run=docker):
    initial_endpoint = require_local_docker(run)
    root = run("info", "--format", "{{.DockerRootDir}}").strip()
    if not root or not Path(root).is_absolute():
        raise ValueError("Docker returned an invalid root directory")
    daemon_id = run("info", "--format", "{{.ID}}").strip()
    if not daemon_id.strip() or daemon_id == "<no value>":
        raise ValueError("Docker returned no daemon ID (or an unknown value); refusing unverifiable snapshots")
    container_output = run(
        "ps", "-a", "--no-trunc", "--filter", "label=io.runnerhub.worker",
        "--format",
        '{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Label "io.runnerhub.worker"}}|{{.Label "io.runnerhub.role"}}|{{.Label "io.runnerhub.parent"}}',
    )
    containers = []
    for line in parse_lines(container_output):
        fields = line.split("|")
        if len(fields) != 7 or not fields[0].strip():
            raise ValueError("Docker returned an unparseable RunnerHub container record")
        if not fields[4].strip() or fields[5] not in ("runner", "dind"):
            raise ValueError("Docker returned invalid RunnerHub worker identity or container role")
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
    dangling_rows = parse_lines(run("volume", "ls", "--filter", "dangling=true", "--format", "{{.Name}}"))
    if len(dangling_rows) != len(set(dangling_rows)):
        raise ValueError("Docker returned duplicate dangling volume names; inventory is ambiguous")
    dangling = sorted(dangling_rows)
    storage = docker_rows(run("system", "df", "--format", "{{json .}}"))
    if any(not isinstance(row, dict) or not isinstance(row.get("Type"), str) or not row["Type"].strip() for row in storage):
        raise ValueError("Docker returned malformed storage summary records")
    # Docker system df reports one aggregate row per resource type. Duplicate
    # types make the physical storage baseline ambiguous, not additive.
    if len({row["Type"] for row in storage}) != len(storage):
        raise ValueError("Docker returned duplicate storage summary resource types")
    try:
        usage = shutil.disk_usage(root)
        filesystem = {"totalBytes": usage.total, "usedBytes": usage.used, "freeBytes": usage.free}
    except OSError:
        filesystem = None

    # A daemon restart or socket switch during collection can combine inventories
    # from different engines into one apparently authoritative snapshot.
    # Refuse that evidence rather than certifying a mixed-host baseline.
    if run("info", "--format", "{{.ID}}").strip() != daemon_id:
        raise ValueError("Docker daemon identity changed during snapshot capture")
    if run("info", "--format", "{{.DockerRootDir}}").strip() != root:
        raise ValueError("Docker root directory changed during snapshot capture")
    # Revalidate the selected endpoint after inventory collection. A context
    # change can redirect subsequent commands even when the daemon reports the
    # same ID and root; such mixed evidence must not be certified.
    if require_local_docker(run) != initial_endpoint:
        raise ValueError("Docker context endpoint changed during snapshot capture")

    return {
        "schemaVersion": 2,
        "capturedAtUTC": datetime.now(timezone.utc).isoformat(),
        "host": socket.gethostname(),
        "dockerRootDir": root,
        "dockerDaemonId": daemon_id,
        "dockerEndpoint": initial_endpoint,
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
    # Files supplied to compare are untrusted JSON. Reject top-level scalars
    # and arrays explicitly rather than crashing with AttributeError or
    # allowing malformed evidence to bypass the audit error path.
    for value in (before, after):
        if not isinstance(value, dict):
            raise ValueError("Docker cleanup snapshots must be JSON objects")
        if value.get("schemaVersion") != 2:
            raise ValueError("unrecognized Docker cleanup audit schema")
    # Identity fields are untrusted JSON too. Missing, blank, or non-string
    # values must not crash the audit or let two empty identities compare equal.
    for label, value in (("before", before), ("after", after)):
        for field in ("host", "dockerRootDir", "dockerDaemonId"):
            identity = value.get(field)
            if not isinstance(identity, str) or not identity.strip():
                raise ValueError(f"{label} snapshot has invalid {field} identity")
        if not Path(value["dockerRootDir"]).is_absolute():
            raise ValueError(f"{label} snapshot has invalid Docker root identity")
    if before["host"] != after["host"] or before["dockerRootDir"] != after["dockerRootDir"]:
        raise ValueError("snapshots must come from the same host and Docker root")
    if before["dockerDaemonId"] != after["dockerDaemonId"]:
        raise ValueError("snapshots must come from the same Docker daemon")
    # Distinct local sockets may expose daemons with identical reported IDs and
    # root paths. Do not compare their physical inventories as one endpoint.
    # Legacy schema-v2 pairs without this field remain comparable, but a
    # mixed old/new pair cannot establish endpoint continuity.
    endpoints = [value.get("dockerEndpoint") for value in (before, after)]
    if any(endpoint is not None for endpoint in endpoints):
        if (any(not isinstance(endpoint, str) or not endpoint.startswith("unix:///")
                or not Path(endpoint[len("unix://"):]).is_absolute()
                or any(char in endpoint for char in ("?", "#", "%", "\\"))
                or any(ord(char) < 32 or ord(char) == 127 for char in endpoint)
                for endpoint in endpoints) or endpoints[0] != endpoints[1]):
            raise ValueError("snapshots must identify the same local Docker endpoint")
    # A reversed or equal-time pair cannot establish a before/after cleanup
    # observation. Require explicit timezone-aware UTC timestamps.
    try:
        times = []
        for value in (before, after):
            timestamp = datetime.fromisoformat(value["capturedAtUTC"].replace("Z", "+00:00"))
            if timestamp.tzinfo is None or timestamp.utcoffset().total_seconds() != 0:
                raise ValueError("snapshot timestamps must be UTC")
            times.append(timestamp)
    except (KeyError, TypeError, AttributeError, ValueError) as exc:
        raise ValueError("snapshots require valid UTC capture timestamps") from exc
    if times[1] <= times[0]:
        raise ValueError("after snapshot must be captured later than before snapshot")
    # Reject incomplete or malformed snapshots rather than reporting an
    # apparently clean host from missing/duplicated inventory records.
    for label, value in (("before", before), ("after", after)):
        for field in ("danglingDockerVolumes", "orphanRunnerhubSidecarIds"):
            items = value.get(field)
            if (not isinstance(items, list) or any(not isinstance(item, str) or not item.strip() for item in items)
                    or len(set(items)) != len(items)):
                raise ValueError(f"{label} snapshot has invalid {field} inventory")
        containers = value.get("runnerhubContainers")
        if (not isinstance(containers, list) or any(not isinstance(c, dict) or not isinstance(c.get("id"), str)
                or not c["id"].strip() for c in containers)):
            raise ValueError(f"{label} snapshot has invalid runnerhubContainers inventory")
        # Unknown roles must not silently disappear from the orphan accounting.
        # A mislabeled DinD sidecar would otherwise be excluded from both the
        # derived orphan set and the reported orphan count.
        if any(c.get("role") not in ("runner", "dind") for c in containers):
            raise ValueError(f"{label} snapshot has invalid RunnerHub container role")
        # A missing worker identity can make two unrelated containers appear
        # attached (None == None), falsely clearing orphan evidence.
        if any(not isinstance(c.get("workerId"), str) or not c["workerId"].strip()
               for c in containers):
            raise ValueError(f"{label} snapshot has invalid RunnerHub worker identity")
        ids = [c["id"] for c in containers]
        if len(set(ids)) != len(ids):
            raise ValueError(f"{label} snapshot has duplicate RunnerHub container IDs")
    # A snapshot without the Docker storage inventory cannot support a physical
    # cleanup comparison. Empty lists are valid (Docker may report no rows),
    # but missing or malformed records must fail closed.
    for label, value in (("before", before), ("after", after)):
        storage = value.get("dockerStorageSummary")
        if (not isinstance(storage, list) or any(not isinstance(row, dict) or not isinstance(row.get("Type"), str) or not row["Type"].strip() for row in storage)):
            raise ValueError(f"{label} snapshot has invalid Docker storage summary")
        if len({row["Type"] for row in storage}) != len(storage):
            raise ValueError(f"{label} snapshot has duplicate Docker storage summary resource types")
    # Every reported orphan must correspond to an inventoried DinD sidecar.
    # Otherwise a truncated or forged orphan list could appear to prove that
    # cleanup succeeded even though its underlying container evidence is absent.
    for label, value in (("before", before), ("after", after)):
        dind_ids = {
            container["id"] for container in value["runnerhubContainers"]
            if container.get("role") == "dind"
        }
        if not set(value["orphanRunnerhubSidecarIds"]).issubset(dind_ids):
            raise ValueError(f"{label} snapshot has orphan sidecars absent from DinD container inventory")
    # Optional structured reasons must match the orphan ID inventory exactly.
    for label, value in (("before", before), ("after", after)):
        reasons = value.get("orphanRunnerhubSidecarReasons")
        # Orphan inventory completeness is mandatory even for older schema-v2
        # snapshots that omit optional structured reason details.
        containers_by_id = {item["id"]: item for item in value["runnerhubContainers"]}
        derived_orphans = set()
        for container in value["runnerhubContainers"]:
            if container.get("role") != "dind":
                continue
            parent_id = container.get("parentId")
            if parent_id is not None and not isinstance(parent_id, str):
                raise ValueError(f"{label} snapshot has invalid orphan sidecar parent identity")
            parent = containers_by_id.get(parent_id)
            if (not parent_id or parent is None
                    or parent.get("role") != "runner"
                    or parent.get("workerId") != container.get("workerId")):
                derived_orphans.add(container["id"])
        if derived_orphans != set(value["orphanRunnerhubSidecarIds"]):
            raise ValueError(f"{label} snapshot has incomplete orphan sidecar inventory")
        if reasons is None:
            continue  # Older schema-v2 evidence may omit optional detail.
        if (not isinstance(reasons, list)
                or any(not isinstance(item, dict)
                       or not isinstance(item.get("id"), str)
                       or not item["id"]
                       or item.get("reason") not in
                       ("missing_parent", "parent_not_runner", "worker_mismatch")
                       for item in reasons)):
            raise ValueError(f"{label} snapshot has invalid orphan sidecar reasons")
        # Reason labels must be derived from the same container inventory,
        # not merely agree with the orphan ID list. A forged reason could
        # otherwise hide a still-attached sidecar or misstate its ownership.
        containers_by_id = {item["id"]: item for item in value["runnerhubContainers"]}
        for item in reasons:
            sidecar = containers_by_id.get(item["id"])
            if sidecar is None or sidecar.get("role") != "dind":
                raise ValueError(f"{label} snapshot has inconsistent orphan sidecar reasons")
            # Optional capture metadata must agree with the inventoried sidecar.
            # Otherwise a forged reason can carry contradictory parent/worker
            # evidence even when its reason label happens to be correct.
            for field, source in (("parentId", "parentId"), ("workerId", "workerId")):
                if field in item and item[field] != sidecar.get(source):
                    raise ValueError(f"{label} snapshot has inconsistent orphan sidecar metadata")
            parent_id = sidecar.get("parentId")
            if parent_id is not None and not isinstance(parent_id, str):
                raise ValueError(f"{label} snapshot has invalid orphan sidecar parent identity")
            parent = containers_by_id.get(parent_id)
            if not parent_id or parent is None:
                expected = "missing_parent"
            elif parent.get("role") != "runner":
                expected = "parent_not_runner"
            elif parent.get("workerId") != sidecar.get("workerId"):
                expected = "worker_mismatch"
            else:
                expected = None
            if item["reason"] != expected:
                raise ValueError(f"{label} snapshot has inconsistent orphan sidecar reasons")
        reason_ids = [item["id"] for item in reasons]
        if (len(set(reason_ids)) != len(reason_ids)
                or set(reason_ids) != set(value["orphanRunnerhubSidecarIds"])):
            raise ValueError(f"{label} snapshot has inconsistent orphan sidecar reasons")
    initial = set(before["danglingDockerVolumes"])
    final = set(after["danglingDockerVolumes"])
    initial_orphans = set(before["orphanRunnerhubSidecarIds"])
    final_orphans = set(after["orphanRunnerhubSidecarIds"])
    before_containers = {c["id"] for c in before["runnerhubContainers"]}
    after_containers = {c["id"] for c in after["runnerhubContainers"]}
    first_disk = before.get("dockerFilesystem")
    last_disk = after.get("dockerFilesystem")
    # A missing measurement must never be reported as reclaimed space. Reject
    # malformed measurements instead of crashing or trusting negative bytes.
    for label, disk in (("before", first_disk), ("after", last_disk)):
        if disk is None:
            continue
        if (not isinstance(disk, dict) or any(
            type(disk.get(key)) is not int or disk[key] < 0
            for key in ("freeBytes",)
        )):
            raise ValueError(f"{label} snapshot has invalid Docker filesystem measurements")
        # Capture emits all three fields together. Partial measurements
        # cannot establish a trustworthy before/after disk delta.
        if set(disk) != {"totalBytes", "usedBytes", "freeBytes"}:
            raise ValueError(f"{label} snapshot has incomplete Docker filesystem measurements")
        if (any(type(disk[key]) is not int or disk[key] < 0 for key in ("totalBytes", "usedBytes"))
                or disk["freeBytes"] > disk["totalBytes"]
                or disk["usedBytes"] > disk["totalBytes"]
                or disk["freeBytes"] + disk["usedBytes"] > disk["totalBytes"]):
            raise ValueError(f"{label} snapshot has inconsistent Docker filesystem measurements")
    free_delta = None
    if first_disk is not None and last_disk is not None:
        # A changed filesystem capacity can reflect a resize or mount change,
        # not Docker reclamation. Preserve inventories but withhold the delta.
        if first_disk["totalBytes"] == last_disk["totalBytes"]:
            free_delta = last_disk["freeBytes"] - first_disk["freeBytes"]
    return {
        "host": before["host"],
        "dockerDaemonId": before["dockerDaemonId"],
        "before": before["capturedAtUTC"],
        "after": after["capturedAtUTC"],
        "runnerhubContainerCountBefore": len(before_containers),
        "runnerhubContainerCountAfter": len(after_containers),
        "newRunnerhubContainerIds": sorted(after_containers - before_containers),
        "removedRunnerhubContainerIds": sorted(before_containers - after_containers),
        "remainingOriginalRunnerhubContainerIds": sorted(before_containers & after_containers),
        "orphanSidecarIdsAfter": sorted(final_orphans),
        "orphanSidecarCountBefore": len(initial_orphans),
        "orphanSidecarCountAfter": len(final_orphans),
        "newOrphanSidecarIds": sorted(final_orphans - initial_orphans),
        "resolvedOrphanSidecarIds": sorted(initial_orphans - final_orphans),
        # Resolution is not proof of physical deletion: a sidecar may remain
        # present after its parent/worker metadata is repaired.
        "removedOrphanSidecarIds": sorted((initial_orphans - final_orphans) - after_containers),
        "repairedOrphanSidecarIdsStillPresent": sorted((initial_orphans - final_orphans) & after_containers),
        "danglingVolumeCountBefore": len(initial),
        "danglingVolumeCountAfter": len(final),
        "newDanglingVolumeNamesUnattributed": sorted(final - initial),
        "dockerFilesystemFreeBytesDelta": free_delta,
        "dockerFilesystemEvidenceComplete": first_disk is not None and last_disk is not None,
        "dockerFilesystemCapacityChanged": (
            first_disk is not None and last_disk is not None
            and first_disk["totalBytes"] != last_disk["totalBytes"]
        ),
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
        before = json.loads(args.before.read_text(encoding="utf-8"), object_pairs_hook=unique_json_object)
        after = json.loads(args.after.read_text(encoding="utf-8"), object_pairs_hook=unique_json_object)
        print(json.dumps(compare(before, after), indent=2, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, ValueError, subprocess.TimeoutExpired) as exc:
        print(f"Docker audit failed: {exc}", file=sys.stderr)
        sys.exit(1)
