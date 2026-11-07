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
            "orphanRunnerhubSidecarIds": ["a-orphan", "resolved-orphan"],
            "danglingDockerVolumes": ["named-backup"],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-07T14:00:00Z"
        after["runnerhubContainers"] = []
        after["orphanRunnerhubSidecarIds"] = ["a-orphan", "new-orphan"]
        after["danglingDockerVolumes"].append("new-unknown-volume")
        after["dockerFilesystem"]["freeBytes"] = 9_000
        comparison = audit.compare(before, after)
        self.assertEqual(comparison["newDanglingVolumeNamesUnattributed"], ["new-unknown-volume"])
        self.assertEqual(comparison["dockerFilesystemFreeBytesDelta"], -1_000)
        self.assertEqual(comparison["remainingOriginalRunnerhubContainerIds"], [])
        self.assertEqual(comparison["orphanSidecarCountBefore"], 2)
        self.assertEqual(comparison["orphanSidecarCountAfter"], 2)
        self.assertEqual(comparison["newOrphanSidecarIds"], ["new-orphan"])
        self.assertEqual(comparison["resolvedOrphanSidecarIds"], ["resolved-orphan"])
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

    def test_sidecar_parent_must_be_same_worker_and_runner(self):
        runner_id = "a" * 64
        invalid_id = "b" * 64
        valid_id = "c" * 64
        parent_sidecar_id = "d" * 64
        nested_sidecar_id = "e" * 64
        commands = {
            ("info", "--format", "{{.DockerRootDir}}"): "/tmp",
            ("info", "--format", "{{.ID}}"): "daemon-compute",
            (
                "ps", "-a", "--no-trunc", "--filter", "label=io.runnerhub.worker",
                "--format",
                '{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Label "io.runnerhub.worker"}}|{{.Label "io.runnerhub.role"}}|{{.Label "io.runnerhub.parent"}}',
            ): (
                f"{runner_id}|runner|runner-image|Up|w1|runner|\n"
                f"{invalid_id}|wrong-worker-dind|docker:dind|Up|w2|dind|{runner_id}\n"
                f"{valid_id}|correct-worker-dind|docker:dind|Up|w1|dind|{runner_id}\n"
                f"{parent_sidecar_id}|sidecar-parent|docker:dind|Up|w1|dind|{runner_id}\n"
                f"{nested_sidecar_id}|sidecar-child|docker:dind|Up|w1|dind|{parent_sidecar_id}\n"
            ),
            ("volume", "ls", "--filter", "dangling=true", "--format", "{{.Name}}"): "",
            ("system", "df", "--format", "{{json .}}"): "",
        }
        result = audit.snapshot(run=lambda *args: commands[args])
        self.assertEqual(result["orphanRunnerhubSidecarIds"], [invalid_id, nested_sidecar_id])
        self.assertEqual(
            [(item["id"], item["reason"]) for item in result["orphanRunnerhubSidecarReasons"]],
            [(invalid_id, "worker_mismatch"), (nested_sidecar_id, "parent_not_runner")],
        )

    def test_duplicate_container_ids_fail_closed(self):
        same_id = "a" * 64
        def fake_run(*args):
            if args[0] == "info":
                return "daemon-compute" if args[-1] == "{{.ID}}" else "/tmp"
            if args[0] == "ps":
                return (
                    f"{same_id}|first|runner-image|Up|w1|runner|\n"
                    f"{same_id}|second|runner-image|Up|w2|runner|\n"
                )
            raise AssertionError("duplicate IDs must fail before storage inventory")
        with self.assertRaisesRegex(ValueError, "duplicate RunnerHub container IDs"):
            audit.snapshot(run=fake_run)

    def test_missing_parent_reason_is_explicit(self):
        runner_id = "a" * 64
        sidecar_id = "b" * 64
        commands = {
            ("info", "--format", "{{.DockerRootDir}}"): "/tmp",
            ("info", "--format", "{{.ID}}"): "daemon-compute",
            (
                "ps", "-a", "--no-trunc", "--filter", "label=io.runnerhub.worker",
                "--format",
                '{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Label "io.runnerhub.worker"}}|{{.Label "io.runnerhub.role"}}|{{.Label "io.runnerhub.parent"}}',
            ): (
                f"{runner_id}|runner|runner-image|Up|w1|runner|\n"
                f"{sidecar_id}|sidecar|docker:dind|Up|w1|dind|\n"
            ),
            ("volume", "ls", "--filter", "dangling=true", "--format", "{{.Name}}"): "",
            ("system", "df", "--format", "{{json .}}"): "",
        }
        result = audit.snapshot(run=lambda *args: commands[args])
        self.assertEqual(result["orphanRunnerhubSidecarIds"], [sidecar_id])
        self.assertEqual(result["orphanRunnerhubSidecarReasons"], [{
            "id": sidecar_id,
            "parentId": "",
            "workerId": "w1",
            "reason": "missing_parent",
        }])

    def test_unknown_daemon_identity_fails_closed(self):
        def fake_run(*args):
            return "<no value>" if args[-1] == "{{.ID}}" else "/tmp"

        with self.assertRaisesRegex(ValueError, "no daemon ID"):
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
