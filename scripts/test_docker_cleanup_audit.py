import copy
import json
import unittest

import docker_cleanup_audit as audit


class DockerCleanupAuditTests(unittest.TestCase):
    def test_snapshot_uses_only_read_only_docker_calls_and_detects_orphan(self):
        calls = []
        runner_id = "a" * 64
        sidecar_id = "b" * 64
        commands = {
            ("context", "inspect", "--format", "{{json .Endpoints}}"): '{"docker":{"Host":"unix:///var/run/docker.sock"}}',
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
        self.assertEqual(result["dockerEndpoint"], "unix:///var/run/docker.sock")
        self.assertEqual(len(result["runnerhubContainers"]), 2)
        self.assertEqual(result["orphanRunnerhubSidecarIds"], [sidecar_id])
        self.assertEqual(len(result["danglingDockerVolumes"]), 2)
        self.assertIn("persistent-backup", result["danglingDockerVolumes"])
        self.assertEqual(result["dockerStorageSummary"][0]["Type"], "Local Volumes")
        self.assertEqual(len(calls), 9)
        for cmd in calls:
            self.assertNotIn(cmd[0], ("rm", "prune", "run", "stop"))
            self.assertNotIn("rm", cmd)
            self.assertNotIn("prune", cmd)

    def test_snapshot_rejects_context_switch_after_inventory(self):
        context_checks = 0

        def fake_run(*args):
            nonlocal context_checks
            if args[0] == "context":
                context_checks += 1
                endpoint = ("unix:///var/run/docker.sock" if context_checks == 1
                            else "ssh://remote-host")
                return json.dumps({"docker": {"Host": endpoint}})
            if args[0] == "info":
                return "daemon-one" if args[-1] == "{{.ID}}" else "/tmp"
            if args[0] in ("ps", "volume", "system"):
                return ""
            raise AssertionError(f"unexpected Docker command: {args}")

        with self.assertRaisesRegex(ValueError, "remote Docker contexts"):
            audit.snapshot(run=fake_run)
        self.assertEqual(context_checks, 2)

    def test_snapshot_rejects_switch_between_two_local_docker_sockets(self):
        context_checks = 0

        def fake_run(*args):
            nonlocal context_checks
            if args[0] == "context":
                context_checks += 1
                endpoint = ("unix:///var/run/docker.sock" if context_checks == 1
                            else "unix:///run/alternate-docker.sock")
                return json.dumps({"docker": {"Host": endpoint}})
            if args[0] == "info":
                return "daemon-one" if args[-1] == "{{.ID}}" else "/tmp"
            if args[0] in ("ps", "volume", "system"):
                return ""
            raise AssertionError(f"unexpected Docker command: {args}")

        with self.assertRaisesRegex(ValueError, "context endpoint changed"):
            audit.snapshot(run=fake_run)
        self.assertEqual(context_checks, 2)

    def test_snapshot_rejects_whitespace_only_daemon_identity(self):
        def fake_run(*args):
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            if args == ("info", "--format", "{{.DockerRootDir}}"):
                return "/tmp"
            if args == ("info", "--format", "{{.ID}}"):
                return "   "
            raise AssertionError(f"inventory must not run without daemon identity: {args}")

        with self.assertRaisesRegex(ValueError, "no daemon ID"):
            audit.snapshot(run=fake_run)

    def test_snapshot_rejects_daemon_switch_during_collection(self):
        daemon_checks = 0

        def fake_run(*args):
            nonlocal daemon_checks
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            if args == ("info", "--format", "{{.DockerRootDir}}"):
                return "/tmp"
            if args == ("info", "--format", "{{.ID}}"):
                daemon_checks += 1
                return "daemon-one" if daemon_checks == 1 else "daemon-two"
            if args[0] in ("ps", "volume", "system"):
                return ""
            raise AssertionError(f"unexpected Docker command: {args}")

        with self.assertRaisesRegex(ValueError, "identity changed during snapshot"):
            audit.snapshot(run=fake_run)
        self.assertEqual(daemon_checks, 2)

    def test_compare_preserves_uncertain_ownership_and_checks_host_identity(self):
        before = {
            "schemaVersion": 2,
            "dockerDaemonId": "daemon-one",
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerStorageSummary": [],
            "capturedAtUTC": "2026-11-07T13:00:00Z",
            "dockerFilesystem": {"freeBytes": 10_000, "usedBytes": 90_000, "totalBytes": 100_000},
            "runnerhubContainers": [
                {"id": "a", "role": "runner", "workerId": "worker-a"},
                {"id": "a-orphan", "role": "dind", "workerId": "worker-b", "parentId": "missing"},
                {"id": "resolved-orphan", "role": "dind", "workerId": "worker-c", "parentId": "missing"},
            ],
            "orphanRunnerhubSidecarIds": ["a-orphan", "resolved-orphan"],
            "danglingDockerVolumes": ["named-backup"],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-07T14:00:00Z"
        after["runnerhubContainers"] = [
            {"id": "a-orphan", "role": "dind", "workerId": "worker-b", "parentId": "missing"},
            {"id": "new-orphan", "role": "dind", "workerId": "worker-d", "parentId": "missing"},
        ]
        after["orphanRunnerhubSidecarIds"] = ["a-orphan", "new-orphan"]
        after["danglingDockerVolumes"].append("new-unknown-volume")
        after["dockerFilesystem"]["freeBytes"] = 9_000
        comparison = audit.compare(before, after)
        self.assertEqual(comparison["newDanglingVolumeNamesUnattributed"], ["new-unknown-volume"])
        self.assertEqual(comparison["dockerFilesystemFreeBytesDelta"], -1_000)
        self.assertEqual(comparison["remainingOriginalRunnerhubContainerIds"], ["a-orphan"])
        self.assertEqual(comparison["removedRunnerhubContainerIds"], ["a", "resolved-orphan"])
        self.assertEqual(comparison["newRunnerhubContainerIds"], ["new-orphan"])
        self.assertEqual(comparison["orphanSidecarCountBefore"], 2)
        self.assertEqual(comparison["orphanSidecarCountAfter"], 2)
        self.assertEqual(comparison["newOrphanSidecarIds"], ["new-orphan"])
        self.assertEqual(comparison["resolvedOrphanSidecarIds"], ["resolved-orphan"])
        self.assertEqual(comparison["removedOrphanSidecarIds"], ["resolved-orphan"])
        self.assertEqual(comparison["repairedOrphanSidecarIdsStillPresent"], [])
        self.assertIn("Neither new dangling volumes", comparison["conclusion"])
        self.assertEqual(comparison["dockerDaemonId"], "daemon-one")
        other_daemon = copy.deepcopy(after)
        other_daemon["dockerDaemonId"] = "daemon-two"
        with self.assertRaisesRegex(ValueError, "same Docker daemon"):
            audit.compare(before, other_daemon)
        after["host"] = "truenas"
        with self.assertRaisesRegex(ValueError, "same host"):
            audit.compare(before, after)

    def test_repaired_orphan_is_not_reported_as_physically_removed(self):
        before = {
            "schemaVersion": 2, "host": "compute",
            "dockerRootDir": "/var/lib/docker", "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [], "dockerStorageSummary": [],
            "runnerhubContainers": [
                {"id": "parent", "role": "runner", "workerId": "w1"},
                {"id": "sidecar", "role": "dind", "workerId": "w1", "parentId": "missing"},
            ],
            "orphanRunnerhubSidecarIds": ["sidecar"],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        after["runnerhubContainers"][1]["parentId"] = "parent"
        after["orphanRunnerhubSidecarIds"] = []
        result = audit.compare(before, after)
        self.assertEqual(result["resolvedOrphanSidecarIds"], ["sidecar"])
        self.assertEqual(result["removedOrphanSidecarIds"], [])
        self.assertEqual(result["repairedOrphanSidecarIdsStillPresent"], ["sidecar"])
        self.assertEqual(result["remainingOriginalRunnerhubContainerIds"], ["parent", "sidecar"])

    def test_duplicate_json_keys_in_storage_records_fail_closed(self):
        # A repeated Type key must not silently replace the original value.
        # Both capture and imported Docker rows use the same strict parser.
        for payload in (
            '{"Type":"Images","Type":"Local Volumes"}',
            '{"Type":"Images","Size":"1GB","Size":"2GB"}',
        ):
            with self.subTest(payload=payload):
                with self.assertRaisesRegex(ValueError, "duplicate JSON keys"):
                    audit.docker_rows(payload)
        self.assertEqual(audit.docker_rows('{"Type":"Images","Size":"1GB"}'),
                         [{"Type": "Images", "Size": "1GB"}])

    def test_duplicate_storage_resource_types_rejected_at_capture(self):
        def fake_run(*args):
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            if args[0] == "info":
                return "daemon-one" if args[-1] == "{{.ID}}" else "/tmp"
            if args[0] in ("ps", "volume"):
                return ""
            if args[0] == "system":
                return '{"Type":"Images","Size":"1GB"}\n{"Type":"Images","Size":"2GB"}'
            raise AssertionError(f"unexpected Docker command: {args}")

        with self.assertRaisesRegex(ValueError, "duplicate storage summary resource types"):
            audit.snapshot(run=fake_run)

    def test_duplicate_storage_resource_types_rejected_at_comparison(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "runnerhubContainers": [],
            "orphanRunnerhubSidecarIds": [],
            "danglingDockerVolumes": [],
            "dockerStorageSummary": [{"Type": "Images"}, {"Type": "Images"}],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        with self.assertRaisesRegex(ValueError, "duplicate Docker storage summary resource types"):
            audit.compare(before, after)

    def test_untyped_storage_rows_rejected_at_capture(self):
        def fake_run(*args):
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            if args[0] == "info":
                return "daemon-one" if args[-1] == "{{.ID}}" else "/tmp"
            if args[0] in ("ps", "volume"):
                return ""
            if args[0] == "system":
                return '{"Reclaimable":"2GB"}'
            raise AssertionError(f"unexpected Docker command: {args}")

        with self.assertRaisesRegex(ValueError, "malformed storage summary"):
            audit.snapshot(run=fake_run)

    def test_untyped_storage_rows_rejected_at_comparison(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "runnerhubContainers": [],
            "orphanRunnerhubSidecarIds": [],
            "danglingDockerVolumes": [],
            "dockerStorageSummary": [{"Reclaimable": "2GB"}],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        with self.assertRaisesRegex(ValueError, "invalid Docker storage summary"):
            audit.compare(before, after)

    def test_malformed_storage_summary_rejected_at_capture(self):
        def fake_run(*args):
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            if args[0] == "info":
                return "daemon-one" if args[-1] == "{{.ID}}" else "/tmp"
            if args[0] in ("ps", "volume"):
                return ""
            if args[0] == "system":
                return '"not a Docker storage record"'
            raise AssertionError(f"unexpected Docker command: {args}")

        with self.assertRaisesRegex(ValueError, "malformed storage summary"):
            audit.snapshot(run=fake_run)

    def test_malformed_container_records_fail_closed(self):
        def fake_run(*args):
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            if args[0] == "info":
                return "daemon-one" if args[-1] == "{{.ID}}" else "/tmp"
            return "bad-container-row"

        with self.assertRaisesRegex(ValueError, "unparseable"):
            audit.snapshot(run=fake_run)

    def test_snapshot_rejects_invalid_runnerhub_labels_before_writing_evidence(self):
        for worker, role in (("w1", "publisher"), ("w1", "DIND"), (" ", "runner")):
            def fake_run(*args):
                if args[0] == "context":
                    return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
                if args[0] == "info":
                    return "daemon-one" if args[-1] == "{{.ID}}" else "/tmp"
                if args[0] == "ps":
                    return f"container-id|runner|image|Up|{worker}|{role}|\n"
                raise AssertionError("invalid labels must fail before storage inventory")

            with self.subTest(worker=worker, role=role):
                with self.assertRaisesRegex(ValueError, "invalid RunnerHub worker identity or container role"):
                    audit.snapshot(run=fake_run)

    def test_sidecar_parent_must_be_same_worker_and_runner(self):
        runner_id = "a" * 64
        invalid_id = "b" * 64
        valid_id = "c" * 64
        parent_sidecar_id = "d" * 64
        nested_sidecar_id = "e" * 64
        commands = {
            ("context", "inspect", "--format", "{{json .Endpoints}}"): '{"docker":{"Host":"unix:///var/run/docker.sock"}}',
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

    def test_snapshot_rejects_whitespace_only_container_id(self):
        def fake_run(*args):
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            if args[0] == "info":
                return "daemon-one" if args[-1] == "{{.ID}}" else "/tmp"
            if args[0] == "ps":
                return "   |runner|image|Up|w1|runner|\n"
            raise AssertionError("blank ID must fail before storage inventory")

        with self.assertRaisesRegex(ValueError, "unparseable RunnerHub container record"):
            audit.snapshot(run=fake_run)

    def test_duplicate_container_ids_fail_closed(self):
        same_id = "a" * 64
        def fake_run(*args):
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
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

    def test_duplicate_dangling_volume_names_fail_closed_at_capture(self):
        calls = []
        def fake_run(*args):
            calls.append(args)
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            if args[0] == "info":
                return "daemon-one" if args[-1] == "{{.ID}}" else "/tmp"
            if args[0] == "ps":
                return ""
            if args[0] == "volume":
                return "duplicate-volume\nduplicate-volume\n"
            raise AssertionError("ambiguous inventory must fail before storage summary")
        with self.assertRaisesRegex(ValueError, "duplicate dangling volume names"):
            audit.snapshot(run=fake_run)
        self.assertEqual(calls[-1][0], "volume")

    def test_missing_parent_reason_is_explicit(self):
        runner_id = "a" * 64
        sidecar_id = "b" * 64
        commands = {
            ("context", "inspect", "--format", "{{json .Endpoints}}"): '{"docker":{"Host":"unix:///var/run/docker.sock"}}',
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

    def test_docker_root_switch_during_snapshot_fails_closed(self):
        root_checks = 0

        def fake_run(*args):
            nonlocal root_checks
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            if args[0] == "info" and args[-1] == "{{.DockerRootDir}}":
                root_checks += 1
                return "/tmp" if root_checks == 1 else "/other-docker-root"
            if args[0] == "info":
                return "daemon-one"
            if args[0] in ("ps", "volume", "system"):
                return ""
            raise AssertionError(f"unexpected Docker command: {args}")

        with self.assertRaisesRegex(ValueError, "root directory changed"):
            audit.snapshot(run=fake_run)
        self.assertEqual(root_checks, 2)

    def test_unknown_daemon_identity_fails_closed(self):
        def fake_run(*args):
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            return "<no value>" if args[-1] == "{{.ID}}" else "/tmp"

        with self.assertRaisesRegex(ValueError, "no daemon ID"):
            audit.snapshot(run=fake_run)

    def test_blank_daemon_identity_fails_closed(self):
        def fake_run(*args):
            if args[0] == "context":
                return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'
            return "" if args[-1] == "{{.ID}}" else "/tmp"

        with self.assertRaisesRegex(ValueError, "no daemon ID"):
            audit.snapshot(run=fake_run)


    def test_remote_docker_contexts_fail_closed_before_inventory(self):
        for endpoint in ("tcp://127.0.0.1:2375", "ssh://remote-host", "npipe:////./pipe/docker_engine", ""):
            calls = []
            def fake_run(*args):
                calls.append(args)
                return json.dumps({"docker": {"Host": endpoint}})
            with self.subTest(endpoint=endpoint), self.assertRaisesRegex(ValueError, "local Unix-socket context"):
                audit.snapshot(run=fake_run)
            self.assertEqual(calls, [("context", "inspect", "--format", "{{json .Endpoints}}")])

    def test_unverified_docker_context_fails_closed(self):
        for result in ("not json", "{}", '{"docker":{}}'):
            with self.subTest(result=result), self.assertRaisesRegex(ValueError, "cannot verify"):
                audit.require_local_docker(run=lambda *args: result, environ={})

    def test_remote_docker_host_override_fails_closed(self):
        def unexpected_run(*args):
            raise AssertionError("must reject remote DOCKER_HOST before calling Docker")
        for override in ("ssh://compute", "tcp://localhost:2375", "npipe:////./pipe/docker_engine"):
            with self.subTest(override=override), self.assertRaisesRegex(ValueError, "remote DOCKER_HOST"):
                audit.require_local_docker(run=unexpected_run, environ={"DOCKER_HOST": override})

    def test_local_override_cannot_mask_remote_context(self):
        # Docker context selection can override DOCKER_HOST. Never infer a
        # local daemon merely because DOCKER_HOST names a Unix socket.
        for endpoint in ("ssh://compute", "tcp://127.0.0.1:2375", "npipe:////./pipe/docker_engine"):
            calls = []
            def context(*args):
                calls.append(args)
                return json.dumps({"docker": {"Host": endpoint}})
            with self.subTest(endpoint=endpoint), self.assertRaisesRegex(ValueError, "remote Docker contexts"):
                audit.require_local_docker(
                    run=context,
                    environ={"DOCKER_HOST": "unix:///var/run/docker.sock", "DOCKER_CONTEXT": "remote"},
                )
            self.assertEqual(calls, [("context", "inspect", "--format", "{{json .Endpoints}}")])

    def test_conflicting_local_docker_socket_selectors_fail_closed(self):
        def context(*args):
            self.assertEqual(args, ("context", "inspect", "--format", "{{json .Endpoints}}"))
            return '{"docker":{"Host":"unix:///var/run/docker.sock"}}'

        with self.assertRaisesRegex(ValueError, "matching DOCKER_HOST"):
            audit.require_local_docker(
                run=context,
                environ={"DOCKER_HOST": "unix:///run/alternate-docker.sock"},
            )

    def test_matching_local_docker_socket_selectors_pass(self):
        audit.require_local_docker(
            run=lambda *args: '{"docker":{"Host":"unix:///run/docker.sock"}}',
            environ={"DOCKER_HOST": "unix:///run/docker.sock"},
        )

    def test_unix_socket_uri_parameters_are_rejected(self):
        for selector in (
            "unix:///var/run/docker.sock?host=remote",
            "unix:///var/run/docker.sock#alternate",
        ):
            with self.subTest(selector=selector), self.assertRaisesRegex(ValueError, "URI parameters"):
                audit.require_local_docker(
                    run=lambda *args: json.dumps({"docker": {"Host": selector}}),
                    environ={},
                )
            with self.subTest(override=selector), self.assertRaisesRegex(ValueError, "URI parameters"):
                audit.require_local_docker(
                    run=lambda *args: json.dumps({"docker": {"Host": selector}}),
                    environ={"DOCKER_HOST": selector},
                )

    def test_ambiguous_local_socket_selectors_fail_closed(self):
        for selector in (
            "unix:///var/run/docker.sock%3Fremote",
            "unix:///var/run/docker.sock%2Fother",
            "unix:///var/run/docker.sock\n",
            "unix:///var/run/docker.sock\x00",
            "unix:///var/run/docker.sock\x7f",
        ):
            with self.subTest(context=repr(selector)), self.assertRaisesRegex(
                ValueError, "unambiguous plain Unix-socket paths"
            ):
                audit.require_local_docker(
                    run=lambda *args: json.dumps({"docker": {"Host": selector}}),
                    environ={},
                )
            with self.subTest(override=repr(selector)), self.assertRaisesRegex(
                ValueError, "unambiguous plain Unix-socket paths"
            ):
                audit.require_local_docker(
                    run=lambda *args: json.dumps({"docker": {"Host": selector}}),
                    environ={"DOCKER_HOST": selector},
                )

    def test_local_unix_socket_context_passes(self):
        audit.require_local_docker(
            run=lambda *args: '{"docker":{"Host":"unix:///var/run/docker.sock"}}',
            environ={"DOCKER_HOST": "unix:///var/run/docker.sock"},
        )

    def test_compare_rejects_reversed_equal_or_non_utc_capture_times(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerStorageSummary": [],
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
        }
        for later in ("2026-11-10T12:00:00Z", "2026-11-10T13:00:00Z"):
            after = dict(before, capturedAtUTC=later)
            with self.subTest(later=later), self.assertRaisesRegex(ValueError, "later than"):
                audit.compare(before, after)
        for invalid in ("2026-11-10T14:00:00", "2026-11-10T14:00:00+01:00"):
            after = dict(before, capturedAtUTC=invalid)
            with self.subTest(invalid=invalid), self.assertRaisesRegex(ValueError, "UTC"):
                audit.compare(before, after)
        after = dict(before, capturedAtUTC="2026-11-10T14:00:00Z")
        after["dockerDaemonId"] = "different"
        with self.assertRaisesRegex(ValueError, "same Docker daemon"):
            audit.compare(before, after)

    def test_compare_rejects_malformed_capture_timestamps_consistently(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerStorageSummary": [],
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
        }
        for malformed in ("not-a-date", "", "2026-13-10T13:00:00Z", None, 123):
            after = dict(before, capturedAtUTC=malformed)
            with self.subTest(malformed=malformed), self.assertRaisesRegex(
                ValueError, "valid UTC capture timestamps"
            ):
                audit.compare(before, after)

    def test_compare_rejects_missing_and_duplicate_inventory(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerStorageSummary": [],
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [],
        }
        after = dict(before, capturedAtUTC="2026-11-10T14:00:00Z")
        for field in ("danglingDockerVolumes", "orphanRunnerhubSidecarIds", "runnerhubContainers"):
            broken = dict(after)
            del broken[field]
            with self.subTest(missing=field), self.assertRaisesRegex(ValueError, "invalid .* inventory"):
                audit.compare(before, broken)
        for field, duplicate in (
            ("danglingDockerVolumes", ["same", "same"]),
            ("orphanRunnerhubSidecarIds", ["same", "same"]),
            ("runnerhubContainers", [{"id": "same"}, {"id": "same"}]),
        ):
            broken = dict(after, **{field: duplicate})
            with self.subTest(duplicate=field), self.assertRaisesRegex(ValueError, "duplicate|invalid"):
                audit.compare(before, broken)
        self.assertEqual(audit.compare(before, after)["runnerhubContainerCountAfter"], 0)

    def test_compare_rejects_orphan_ids_missing_from_dind_inventory(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerStorageSummary": [],
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        for containers in ([], [{"id": "sidecar", "role": "runner", "workerId": "w"}]):
            broken = copy.deepcopy(after)
            broken["runnerhubContainers"] = containers
            broken["orphanRunnerhubSidecarIds"] = ["sidecar"]
            with self.subTest(containers=containers), self.assertRaisesRegex(
                ValueError, "orphan sidecars absent from DinD"
            ):
                audit.compare(before, broken)
        after["runnerhubContainers"] = [{"id": "sidecar", "role": "dind", "workerId": "w"}]
        after["orphanRunnerhubSidecarIds"] = ["sidecar"]
        self.assertEqual(audit.compare(before, after)["orphanSidecarCountAfter"], 1)

    def test_compare_rejects_malformed_filesystem_evidence(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerStorageSummary": [],
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [],
            "dockerFilesystem": {"freeBytes": 100, "usedBytes": 100, "totalBytes": 200},
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        self.assertEqual(audit.compare(before, after)["dockerFilesystemFreeBytesDelta"], 0)
        for malformed in (
            {"freeBytes": -1},
            {"freeBytes": "100"},
            {"freeBytes": True},
            {"freeBytes": 201, "usedBytes": 0, "totalBytes": 200},
            {"freeBytes": 100, "usedBytes": 201, "totalBytes": 200},
            {"freeBytes": 100, "usedBytes": 150, "totalBytes": 200},
            {"freeBytes": 100, "totalBytes": 200},
            {"freeBytes": 100, "usedBytes": 100},
            {"freeBytes": 100},
            [],
        ):
            broken = copy.deepcopy(after)
            broken["dockerFilesystem"] = malformed
            with self.subTest(malformed=malformed), self.assertRaisesRegex(ValueError, "filesystem measurements"):
                audit.compare(before, broken)

    def test_compare_withholds_disk_delta_after_filesystem_resize(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [],
            "dockerStorageSummary": [],
            "dockerFilesystem": {"totalBytes": 1000, "usedBytes": 800, "freeBytes": 200},
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        after["dockerFilesystem"] = {"totalBytes": 2000, "usedBytes": 800, "freeBytes": 1200}
        result = audit.compare(before, after)
        self.assertIsNone(result["dockerFilesystemFreeBytesDelta"])
        self.assertTrue(result["dockerFilesystemEvidenceComplete"])
        self.assertTrue(result["dockerFilesystemCapacityChanged"])
        self.assertEqual(result["danglingVolumeCountAfter"], 0)
        after["dockerFilesystem"] = {"totalBytes": 1000, "usedBytes": 700, "freeBytes": 300}
        same_capacity = audit.compare(before, after)
        self.assertEqual(same_capacity["dockerFilesystemFreeBytesDelta"], 100)
        self.assertTrue(same_capacity["dockerFilesystemEvidenceComplete"])
        self.assertFalse(same_capacity["dockerFilesystemCapacityChanged"])
        after["dockerFilesystem"] = None
        missing = audit.compare(before, after)
        self.assertIsNone(missing["dockerFilesystemFreeBytesDelta"])
        self.assertFalse(missing["dockerFilesystemEvidenceComplete"])
        self.assertFalse(missing["dockerFilesystemCapacityChanged"])

    def test_compare_rejects_missing_or_malformed_storage_summary(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [],
            "dockerStorageSummary": [],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        self.assertEqual(audit.compare(before, after)["danglingVolumeCountAfter"], 0)
        for malformed in (None, {}, "unknown", [None], ["not-a-row"]):
            broken = copy.deepcopy(after)
            broken["dockerStorageSummary"] = malformed
            with self.subTest(malformed=malformed), self.assertRaisesRegex(ValueError, "storage summary"):
                audit.compare(before, broken)
        del after["dockerStorageSummary"]
        with self.assertRaisesRegex(ValueError, "storage summary"):
            audit.compare(before, after)

    def test_compare_rejects_inconsistent_orphan_reason_evidence(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": ["sidecar"],
            "orphanRunnerhubSidecarReasons": [
                {"id": "sidecar", "reason": "missing_parent"}
            ],
            "runnerhubContainers": [{"id": "sidecar", "role": "dind", "workerId": "w"}],
            "dockerStorageSummary": [],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        self.assertEqual(audit.compare(before, after)["orphanSidecarCountAfter"], 1)
        for reasons in (
            [],
            [{"id": "unrelated", "reason": "missing_parent"}],
            [{"id": "sidecar", "reason": "unknown"}],
            [{"id": "sidecar", "reason": "missing_parent"}] * 2,
            "invalid",
        ):
            broken = copy.deepcopy(after)
            broken["orphanRunnerhubSidecarReasons"] = reasons
            with self.subTest(reasons=reasons), self.assertRaisesRegex(
                ValueError, "orphan sidecar reasons"
            ):
                audit.compare(before, broken)

    def test_compare_rejects_orphan_reasons_conflicting_with_parent_inventory(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": ["sidecar"],
            "orphanRunnerhubSidecarReasons": [
                {"id": "sidecar", "reason": "worker_mismatch"}
            ],
            "runnerhubContainers": [
                {"id": "parent", "role": "runner", "workerId": "worker-one"},
                {"id": "sidecar", "role": "dind", "parentId": "parent", "workerId": "worker-two"},
            ],
            "dockerStorageSummary": [],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        self.assertEqual(audit.compare(before, after)["orphanSidecarCountAfter"], 1)
        for reason in ("missing_parent", "parent_not_runner"):
            broken = copy.deepcopy(after)
            broken["orphanRunnerhubSidecarReasons"][0]["reason"] = reason
            with self.subTest(reason=reason), self.assertRaisesRegex(
                ValueError, "inconsistent orphan sidecar reasons"
            ):
                audit.compare(before, broken)
        attached = copy.deepcopy(after)
        attached["runnerhubContainers"][1]["workerId"] = "worker-one"
        with self.assertRaisesRegex(ValueError, "incomplete orphan sidecar inventory"):
            audit.compare(before, attached)

    def test_compare_rejects_omitted_orphan_from_structured_evidence(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": [],
            "orphanRunnerhubSidecarReasons": [],
            "runnerhubContainers": [
                {"id": "orphan", "role": "dind", "parentId": "missing", "workerId": "w"}
            ],
            "dockerStorageSummary": [],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        for side in ("before", "after"):
            with self.subTest(side=side), self.assertRaisesRegex(
                ValueError, "incomplete orphan sidecar inventory"
            ):
                audit.compare(before, after)
            # Repair the tested side and then check the other independently.
            repaired = before if side == "before" else after
            repaired["orphanRunnerhubSidecarIds"] = ["orphan"]
            repaired["orphanRunnerhubSidecarReasons"] = [
                {"id": "orphan", "reason": "missing_parent"}
            ]
        self.assertEqual(audit.compare(before, after)["orphanSidecarCountAfter"], 1)

    def test_compare_rejects_omitted_orphan_without_optional_reasons(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [
                {"id": "orphan", "role": "dind", "parentId": "missing", "workerId": "w"}
            ],
            "dockerStorageSummary": [],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        for side in ("before", "after"):
            pair = [copy.deepcopy(before), copy.deepcopy(after)]
            pair[0 if side == "before" else 1]["orphanRunnerhubSidecarIds"] = []
            pair[1 if side == "before" else 0]["orphanRunnerhubSidecarIds"] = ["orphan"]
            with self.subTest(side=side), self.assertRaisesRegex(
                ValueError, "incomplete orphan sidecar inventory"
            ):
                audit.compare(*pair)
        before["orphanRunnerhubSidecarIds"] = ["orphan"]
        after["orphanRunnerhubSidecarIds"] = ["orphan"]
        self.assertEqual(audit.compare(before, after)["orphanSidecarCountAfter"], 1)

    def test_compare_rejects_unhashable_orphan_parent_identity(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": ["sidecar"],
            "orphanRunnerhubSidecarReasons": [{"id": "sidecar", "reason": "missing_parent"}],
            "runnerhubContainers": [{"id": "sidecar", "role": "dind", "parentId": [], "workerId": "w"}],
            "dockerStorageSummary": [],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        for malformed in ([], {}, 42, True):
            for snapshot in (before, after):
                broken = copy.deepcopy(snapshot)
                broken["runnerhubContainers"][0]["parentId"] = malformed
                pair = (broken, after) if snapshot is before else (before, broken)
                with self.subTest(parent=malformed, side="before" if snapshot is before else "after"):
                    with self.assertRaisesRegex(ValueError, "invalid orphan sidecar parent identity"):
                        audit.compare(*pair)

    def test_compare_rejects_non_object_snapshot_json(self):
        valid = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [],
            "dockerStorageSummary": [],
        }
        for malformed in (None, [], "text", 123, True):
            with self.subTest(before=malformed), self.assertRaisesRegex(
                ValueError, "JSON objects"
            ):
                audit.compare(malformed, valid)
            with self.subTest(after=malformed), self.assertRaisesRegex(
                ValueError, "JSON objects"
            ):
                audit.compare(valid, malformed)

    def test_compare_rejects_missing_or_invalid_host_identity(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [],
            "dockerStorageSummary": [],
        }
        after = dict(before, capturedAtUTC="2026-11-10T14:00:00Z")
        for field in ("host", "dockerRootDir", "dockerDaemonId"):
            for malformed in (None, "", "  ", [], 123):
                for side in ("before", "after"):
                    pair = [copy.deepcopy(before), copy.deepcopy(after)]
                    pair[0 if side == "before" else 1][field] = malformed
                    with self.subTest(field=field, value=malformed, side=side):
                        with self.assertRaisesRegex(ValueError, "invalid .* identity"):
                            audit.compare(*pair)
            for side in ("before", "after"):
                pair = [copy.deepcopy(before), copy.deepcopy(after)]
                del pair[0 if side == "before" else 1][field]
                with self.subTest(field=field, missing=side):
                    with self.assertRaisesRegex(ValueError, "invalid .* identity"):
                        audit.compare(*pair)
        for root in ("relative/docker", "."):
            broken = dict(after, dockerRootDir=root)
            with self.subTest(root=root), self.assertRaisesRegex(ValueError, "invalid Docker root identity"):
                audit.compare(before, broken)

    def test_compare_rejects_contradictory_optional_orphan_metadata(self):
        before = {
            "schemaVersion": 2,
            "host": "compute",
            "dockerRootDir": "/var/lib/docker",
            "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [],
            "orphanRunnerhubSidecarIds": ["sidecar"],
            "orphanRunnerhubSidecarReasons": [
                {"id": "sidecar", "reason": "missing_parent",
                 "parentId": "missing", "workerId": "worker-one"}
            ],
            "runnerhubContainers": [
                {"id": "sidecar", "role": "dind",
                 "parentId": "missing", "workerId": "worker-one"}
            ],
            "dockerStorageSummary": [],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        self.assertEqual(audit.compare(before, after)["orphanSidecarCountAfter"], 1)
        for field, incorrect in (("parentId", "other-parent"), ("workerId", "other-worker")):
            for side in ("before", "after"):
                pair = [copy.deepcopy(before), copy.deepcopy(after)]
                pair[0 if side == "before" else 1]["orphanRunnerhubSidecarReasons"][0][field] = incorrect
                with self.subTest(field=field, side=side), self.assertRaisesRegex(
                    ValueError, "inconsistent orphan sidecar metadata"
                ):
                    audit.compare(*pair)

    def test_compare_rejects_missing_worker_identity_on_both_sides(self):
        before = {
            "schemaVersion": 2, "host": "compute",
            "dockerRootDir": "/var/lib/docker", "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [], "orphanRunnerhubSidecarIds": [],
            "dockerStorageSummary": [],
            "runnerhubContainers": [
                {"id": "runner", "role": "runner", "workerId": "worker-one"},
                {"id": "sidecar", "role": "dind", "workerId": "worker-one", "parentId": "runner"},
            ],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        self.assertEqual(audit.compare(before, after)["orphanSidecarCountAfter"], 0)
        for malformed in (None, "", " ", [], 42, True):
            for side in ("before", "after"):
                for index in (0, 1):
                    pair = [copy.deepcopy(before), copy.deepcopy(after)]
                    pair[0 if side == "before" else 1]["runnerhubContainers"][index]["workerId"] = malformed
                    with self.subTest(side=side, index=index, worker=malformed):
                        with self.assertRaisesRegex(ValueError, "invalid RunnerHub worker identity"):
                            audit.compare(*pair)

    def test_compare_rejects_unknown_container_roles_on_both_sides(self):
        before = {
            "schemaVersion": 2, "host": "compute",
            "dockerRootDir": "/var/lib/docker", "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [], "orphanRunnerhubSidecarIds": [],
            "dockerStorageSummary": [],
            "runnerhubContainers": [
                {"id": "runner", "role": "runner", "workerId": "w1"},
                {"id": "sidecar", "role": "dind", "workerId": "w1", "parentId": "runner"},
            ],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        self.assertEqual(audit.compare(before, after)["orphanSidecarCountAfter"], 0)
        for invalid in (None, "", "publisher", "DIND", 7, [], {}):
            for side in ("before", "after"):
                pair = [copy.deepcopy(before), copy.deepcopy(after)]
                pair[0 if side == "before" else 1]["runnerhubContainers"][1]["role"] = invalid
                with self.subTest(role=invalid, side=side):
                    with self.assertRaisesRegex(ValueError, "invalid RunnerHub container role"):
                        audit.compare(*pair)

    def test_compare_rejects_whitespace_only_inventory_identifiers(self):
        before = {
            "schemaVersion": 2, "host": "compute",
            "dockerRootDir": "/var/lib/docker", "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [], "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [], "dockerStorageSummary": [],
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        for field in ("danglingDockerVolumes", "orphanRunnerhubSidecarIds"):
            for side in ("before", "after"):
                pair = [copy.deepcopy(before), copy.deepcopy(after)]
                pair[0 if side == "before" else 1][field] = ["   "]
                with self.subTest(field=field, side=side), self.assertRaisesRegex(
                    ValueError, "invalid " + field + " inventory"
                ):
                    audit.compare(*pair)
        for side in ("before", "after"):
            pair = [copy.deepcopy(before), copy.deepcopy(after)]
            pair[0 if side == "before" else 1]["runnerhubContainers"] = [
                {"id": "   ", "role": "runner", "workerId": "worker-one"}
            ]
            with self.subTest(side=side), self.assertRaisesRegex(
                ValueError, "invalid runnerhubContainers inventory"
            ):
                audit.compare(*pair)

    def test_compare_rejects_changed_or_unverifiable_local_endpoint(self):
        before = {
            "schemaVersion": 2, "host": "compute",
            "dockerRootDir": "/var/lib/docker", "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [], "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [], "dockerStorageSummary": [],
            "dockerEndpoint": "unix:///var/run/docker.sock",
        }
        after = copy.deepcopy(before)
        after["capturedAtUTC"] = "2026-11-10T14:00:00Z"
        self.assertEqual(audit.compare(before, after)["orphanSidecarCountAfter"], 0)
        for endpoint in ("unix:///run/other.sock", "ssh://remote", "", None, 5):
            pair = [copy.deepcopy(before), copy.deepcopy(after)]
            pair[1]["dockerEndpoint"] = endpoint
            with self.subTest(endpoint=endpoint):
                with self.assertRaisesRegex(ValueError, "same local Docker endpoint"):
                    audit.compare(*pair)
        # Even matching endpoints are untrustworthy if URI parsing is ambiguous.
        for endpoint in ("unix:///var/run/docker.sock?host=remote",
                         "unix:///var/run/docker.sock#fragment",
                         "unix:///var/run/%64ocker.sock",
                         "unix:///var/run/docker.sock\\nother"):
            pair = [copy.deepcopy(before), copy.deepcopy(after)]
            pair[0]["dockerEndpoint"] = endpoint
            pair[1]["dockerEndpoint"] = endpoint
            with self.subTest(ambiguous_endpoint=endpoint):
                with self.assertRaisesRegex(ValueError, "same local Docker endpoint"):
                    audit.compare(*pair)
        legacy_before = copy.deepcopy(before)
        legacy_after = copy.deepcopy(after)
        del legacy_before["dockerEndpoint"]
        del legacy_after["dockerEndpoint"]
        self.assertEqual(audit.compare(legacy_before, legacy_after)["orphanSidecarCountAfter"], 0)
        del legacy_after["capturedAtUTC"]
        # A mixed legacy/new pair is not equivalent to two legacy snapshots.
        with self.assertRaisesRegex(ValueError, "same local Docker endpoint"):
            audit.compare(legacy_before, after)

    def test_imported_snapshots_reject_duplicate_json_keys(self):
        # Comparison reads files supplied by an operator. A duplicate key
        # could otherwise replace the true inventory before validation.
        import json
        import tempfile
        from pathlib import Path
        from unittest import mock

        valid = {
            "schemaVersion": 2, "host": "compute",
            "dockerRootDir": "/var/lib/docker", "dockerDaemonId": "daemon-one",
            "capturedAtUTC": "2026-11-10T13:00:00Z",
            "danglingDockerVolumes": [], "orphanRunnerhubSidecarIds": [],
            "runnerhubContainers": [], "dockerStorageSummary": [],
        }
        after = dict(valid, capturedAtUTC="2026-11-10T14:00:00Z")
        with tempfile.TemporaryDirectory() as directory:
            before_path = Path(directory) / "before.json"
            after_path = Path(directory) / "after.json"
            after_path.write_text(json.dumps(after), encoding="utf-8")
            for injected in (
                '{"schemaVersion":2,"schemaVersion":1}',
                '{"dockerStorageSummary":[],"dockerStorageSummary":[{"Type":"Volumes"}]}',
                '{"dockerStorageSummary":[{"Type":"Volumes","Type":"Images"}]}',
            ):
                # Add the duplicate-bearing object to otherwise valid evidence.
                extra = injected[1:-1]
                before_path.write_text(json.dumps(valid)[:-1] + "," + extra + "}", encoding="utf-8")
                with self.subTest(duplicate=injected):
                    with self.assertRaisesRegex(ValueError, "duplicate JSON keys"):
                        audit.main(["compare", "--before", str(before_path), "--after", str(after_path)])
            before_path.write_text(json.dumps(valid), encoding="utf-8")
            with mock.patch("builtins.print") as output:
                audit.main(["compare", "--before", str(before_path), "--after", str(after_path)])
            self.assertTrue(output.called)

    def test_legacy_snapshots_fail_closed(self):
        with self.assertRaisesRegex(ValueError, "unrecognized"):
            audit.compare({"schemaVersion": 1}, {"schemaVersion": 1})


if __name__ == "__main__":
    unittest.main()
