#!/usr/bin/env python3
"""Read-only Docker inventory for RunnerHub cleanup acceptance (#218).

Never infer ownership from a Docker volume's anonymous-looking name. Snapshot
all volumes as aggregate counts and separately identify explicitly labeled
RunnerHub resources. No inspect environment, mounts or credentials are emitted.
"""
import argparse
import datetime
import json
import subprocess
import sys


def docker(*args):
    result = subprocess.run(["docker", *args], capture_output=True, text=True, check=True)
    return result.stdout


def json_lines(raw):
    return [json.loads(line) for line in raw.splitlines() if line.strip()]


def runnerhub_labels(labels):
    return any("runnerhub" in key.lower() or "runnerhub" in str(value).lower()
               for key, value in labels.items())


def snapshot():
    containers = json_lines(docker("ps", "-a", "--format", "{{json .}}"))
    volumes = json_lines(docker("volume", "ls", "--format", "{{json .}}"))
    # Docker's volume ls output does not reliably include labels. Inspect each
    # volume to distinguish known ownership from unknown; never guess by name.
    labeled_volumes = []
    unknown_volumes = 0
    for volume in volumes:
        name = volume.get("Name")
        if not name:
            unknown_volumes += 1
            continue
        details = json.loads(docker("volume", "inspect", name))[0]
        labels = details.get("Labels") or {}
        if runnerhub_labels(labels):
            labeled_volumes.append(name)
        else:
            unknown_volumes += 1
    owned_containers = []
    for container in containers:
        cid = container.get("ID")
        if not cid:
            continue
        details = json.loads(docker("inspect", cid))[0]
        if runnerhub_labels((details.get("Config") or {}).get("Labels") or {}):
            owned_containers.append({
                "id": cid,
                "state": (details.get("State") or {}).get("Status", "unknown"),
            })
    return {
        "captured_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "scope": "single-docker-daemon-read-only-snapshot",
        "ownership": "explicit-labels-only; unknown volumes are never safe to delete",
        "containers_total": len(containers),
        "runnerhub_labeled_containers": owned_containers,
        "volumes_total": len(volumes),
        "runnerhub_labeled_volume_names": sorted(labeled_volumes),
        "unattributed_volumes": unknown_volumes,
        "docker_system_df": docker("system", "df", "--format", "{{json .}}").splitlines(),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", help="Write JSON to this path instead of stdout")
    args = parser.parse_args()
    try:
        result = json.dumps(snapshot(), indent=2, sort_keys=True) + "\n"
    except (subprocess.CalledProcessError, OSError, ValueError, IndexError) as exc:
        print(f"read-only Docker inventory failed: {exc}", file=sys.stderr)
        return 1
    if args.output:
        with open(args.output, "w", encoding="utf-8") as output:
            output.write(result)
    else:
        print(result, end="")
    return 0


if __name__ == "__main__":
    sys.exit(main())
