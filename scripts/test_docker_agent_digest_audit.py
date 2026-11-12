#!/usr/bin/env python3
"""Deterministic tests for read-only deployed-image evidence."""
import unittest
import docker_agent_digest_audit as audit

class DigestAuditTests(unittest.TestCase):
    def test_exact_image_identity_and_digest_caveat(self):
        calls = []
        def fake(*args):
            calls.append(args)
            if args[1] == "runnerhub-agent":
                return {"Name": "/runnerhub-agent", "Image": "sha256:abc", "Id": "container1",
                        "Config": {"Image": "example/agent:latest"}, "State": {"Running": True}}
            return {"Id": "sha256:abc", "RepoDigests": ["example/agent@sha256:def"]}
        result = audit.evidence(inspect_fn=fake)
        self.assertEqual(result["containerImageId"], "sha256:abc")
        self.assertEqual(result["localImageRepoDigests"], ["example/agent@sha256:def"])
        self.assertIn("not proof", result["limitations"][0])
        self.assertEqual(calls[1], ("--type", "image", "sha256:abc"))

    def test_image_mismatch_fails_closed(self):
        def fake(*args):
            if args[1] == "runnerhub-agent":
                return {"Name": "/runnerhub-agent", "Image": "sha256:abc", "Config": {"Image": "example/agent:latest"}, "State": {"Running": True}}
            return {"Id": "sha256:other", "RepoDigests": []}
        with self.assertRaisesRegex(ValueError, "does not match"):
            audit.evidence(inspect_fn=fake)

    def test_name_mismatch_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "name mismatch"):
            audit.evidence(inspect_fn=lambda *args: {"Name": "/other", "Image": "sha256:abc"})

    def test_malformed_digest_fails_closed(self):
        def fake(*args):
            if args[1] == "runnerhub-agent":
                return {"Name": "/runnerhub-agent", "Image": "sha256:abc"}
            return {"Id": "sha256:abc", "RepoDigests": ["mutable:latest"]}
        with self.assertRaisesRegex(ValueError, "invalid image repository digest"):
            audit.evidence(inspect_fn=fake)

    def test_missing_configured_reference_rejected(self):
        def fake(*args):
            if args[1] == "runnerhub-agent":
                return {"Name": "/runnerhub-agent", "Image": "sha256:abc",
                        "Config": {"Image": "  "}, "State": {"Running": True}}
            return {"Id": "sha256:abc", "RepoDigests": []}
        with self.assertRaisesRegex(ValueError, "configured container image"):
            audit.evidence(inspect_fn=fake)

    def test_nonboolean_running_state_rejected(self):
        def fake(*args):
            if args[1] == "runnerhub-agent":
                return {"Name": "/runnerhub-agent", "Image": "sha256:abc",
                        "Config": {"Image": "example/agent:latest"}, "State": {"Running": "false"}}
            return {"Id": "sha256:abc", "RepoDigests": []}
        with self.assertRaisesRegex(ValueError, "boolean container running state"):
            audit.evidence(inspect_fn=fake)

if __name__ == "__main__":
    unittest.main()
