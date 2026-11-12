#!/usr/bin/env python3
"""Read-only evidence for the deployed RunnerHub Docker agent image on a host.

Run locally on each physical Docker host. This deliberately never pulls,
restarts, or removes containers and never equates a mutable tag with a digest.
"""
import argparse
import json
import subprocess
import sys

def inspect(*args):
    result = subprocess.run(["docker", "inspect", *args], capture_output=True, text=True, timeout=30, check=False)
    if result.returncode:
        raise RuntimeError("Docker inspect failed: " + result.stderr.strip()[:200])
    rows = json.loads(result.stdout)
    if not isinstance(rows, list) or len(rows) != 1 or not isinstance(rows[0], dict):
        raise ValueError("expected exactly one Docker inspection record")
    return rows[0]

def evidence(container_name="runnerhub-agent", inspect_fn=inspect):
    container = inspect_fn("--type", "container", container_name)
    if container.get("Name") != "/" + container_name:
        raise ValueError("container name mismatch; refusing ambiguous evidence")
    image_id = container.get("Image")
    if not isinstance(image_id, str) or not image_id.startswith("sha256:"):
        raise ValueError("missing immutable container image ID")
    image = inspect_fn("--type", "image", image_id)
    if image.get("Id") != image_id:
        raise ValueError("container image ID does not match inspected image")
    refs = image.get("RepoDigests") or []
    if not isinstance(refs, list) or any(not isinstance(ref, str) or "@sha256:" not in ref for ref in refs):
        raise ValueError("invalid image repository digest evidence")
    config = container.get("Config")
    state = container.get("State")
    if not isinstance(config, dict) or not isinstance(config.get("Image"), str) or not config["Image"].strip():
        raise ValueError("missing configured container image reference")
    if not isinstance(state, dict) or not isinstance(state.get("Running"), bool):
        raise ValueError("missing boolean container running state")
    return {
        "containerName": container_name,
        "containerId": container.get("Id"),
        "containerImageId": image_id,
        "configuredImageReference": config.get("Image"),
        "localImageRepoDigests": sorted(refs),
        "running": state["Running"],
        "limitations": [
            "A local RepoDigest is not proof that the container was deployed from that digest.",
            "Compare the image ID to the release's independently attested platform manifest.",
            "This read-only report does not verify agent host identity, cleanup, or job success.",
        ],
    }

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", default="runnerhub-agent")
    args = parser.parse_args()
    try:
        print(json.dumps(evidence(args.container), indent=2, sort_keys=True))
    except (ValueError, RuntimeError, KeyError, json.JSONDecodeError) as exc:
        print(str(exc), file=sys.stderr)
        return 1
    return 0

if __name__ == "__main__":
    sys.exit(main())
