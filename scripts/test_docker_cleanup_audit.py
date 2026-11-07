import copy
import unittest

import docker_cleanup_audit as audit


class DockerCleanupAuditTests(unittest.TestCase):
    def test_snapshot_uses_only_read_only_docker_calls_and_detects_orphan(self):
        calls = []
        runner_id = "a" * 64
        sidecar_id = "b" * 64
        commands = {
            ("info", "--format", "{{.DockerRootDir}}"): "/tmp",
            ("info", "--format", "{{.ID}}"): "daemon-one",
            (
                "ps", "-a", "--no-trunc", "--filter", "label=io.runnerhub.worker",
                "--format",
                '{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Label "io.runnerhub.worker"}}|{{.Label "io.runnerhub.role"}}|{{.Label "io.runnerhub.parent"}}',
            ): (
                f"{runner_id}|runnerhub-worker|runner-image|Up 2 minutes|w1|runner|\n"
                f"{sidecar_id}|runnerhub-worker-dind|docker:dind|Exited|w2|dind|{'c' * 64}\n"
            ),
            ("volume", "ls", "--filter", "dangling=true", "--format", "{{.Name}}"): "persistent-backup\nanonymous-example\n",
            ("system", "df", "--format", "{{json .}}"): '{"Type":"Local Volumes","Reclaimable":"2GB (100%)"}\n',
        }

        def fake_run(*args):
            calls.append(args)
            return commands[args]

        result = audit.snapshot(run=fake_run)
        self.assertEqual(result["dockerRootDir"], "/tmp")
        self.assertEqual(result["dockerDaemonId"], "daemon-one")
        self.assertEqual(len(result["runnerhubContainers"]), 2)
        self.assertEqual(result["orphanRunnerhubSidecarIds"], [sidecar_id])
        self.assertEqual(len(result["danglingDockerVolumes"]), 2)
        self.assertIn("persistent-backup", result["danglingDockerVolumes"])
        self.assertEqual(result["dockerStorageSummary"][0]["Type"], "Local Volumes")
        self.assertEqual(len(calls), 5)
        for cmd in calls:
            self.assertNotIn(cmd[0], ("rm", "prune", "run", "stop"))
            self.assertNotIn("rm", cmd)
            self.assertNotIn("prune", cmd)

    def test_compare_preserves_uncertain_ownership_and_checks_host_identity(self):
        before = {
            "schemaVersion": 2,
            "dockerDaemonId": "daemon-one",
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "capturedAtUTC": "2026-11-07T13:00:00Z",
            "dockerFilesystem": {"freeBytes": 10_000},
            "runnerhubContainers": [{"id": "a"}],
            "orphanRunnerhubSidecarIds": [],
            "danglingDockerVolumes": ["named-backup"],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-07T14:00:00Z"
        after["runnerhubContainers"] = []
        after["danglingDockerVolumes"].append("new-unknown-volume")
        after["dockerFilesystem"]["freeBytes"] = 9_000
        comparison = audit.compare(before, after)
        self.assertEqual(comparison["newDanglingVolumeNamesUnattributed"], ["new-unknown-volume"])
        self.assertEqual(comparison["dockerFilesystemFreeBytesDelta"], -1_000)
        self.assertEqual(comparison["remainingOriginalRunnerhubContainerIds"], [])
        self.assertIn("Neither new dangling volumes", comparison["conclusion"])
        self.assertEqual(comparison["dockerDaemonId"], "daemon-one")
        other_daemon = copy.deepcopy(after)
        other_daemon["dockerDaemonId"] = "daemon-two"
        with self.assertRaisesRegex(ValueError, "same Docker daemon"):
            audit.compare(before, other_daemon)
        after["host"] = "truenas"
        with self.assertRaisesRegex(ValueError, "same host"):
            audit.compare(before, after)

    def test_malformed_container_records_fail_closed(self):
        def fake_run(*args):
            if args[0] == "info":
                return "daemon-one" if args[-1] == "{{.ID}}" else "/tmp"
            return "bad-container-row"

        with self.assertRaisesRegex(ValueError, "unparseable"):
            audit.snapshot(run=fake_run)

    def test_blank_daemon_identity_fails_closed(self):
        def fake_run(*args):
            return "" if args[-1] == "{{.ID}}" else "/tmp"

        with self.assertRaisesRegex(ValueError, "no daemon ID"):
            audit.snapshot(run=fake_run)

    def test_legacy_snapshots_fail_closed(self):
        with self.assertRaisesRegex(ValueError, "unrecognized"):
            audit.compare({"schemaVersion": 1}, {"schemaVersion": 1})


if __name__ == "__main__":
    unittest.main()
