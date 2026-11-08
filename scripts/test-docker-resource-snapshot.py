"""Offline regression tests for the read-only Docker inventory."""
import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "inventory", Path(__file__).with_name("docker-resource-snapshot.py"))
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)


class SnapshotTests(unittest.TestCase):
    def test_unlabeled_volume_never_assumed_owned(self):
        outputs = {
            ("ps", "-a", "--format", "{{json .}}"): '{"ID":"abc"}\n',
            ("volume", "ls", "--format", "{{json .}}"): '{"Name":"anonymous"}\n{"Name":"owned"}\n',
            ("volume", "inspect", "anonymous"): '[{"Labels":null}]',
            ("volume", "inspect", "owned"): '[{"Labels":{"org.runnerhub.worker":"42"}}]',
            ("inspect", "abc"): '[{"Config":{"Labels":{"other":"value"}},"State":{"Status":"exited"}}]',
            ("system", "df", "--format", "{{json .}}"): '{"Type":"Volumes","Size":"12GB"}\n',
        }
        with patch.object(inventory, "docker", side_effect=lambda *args: outputs[args]):
            result = inventory.snapshot()
        self.assertEqual(result["runnerhub_labeled_volume_names"], ["owned"])
        self.assertEqual(result["unattributed_volumes"], 1)
        self.assertEqual(result["runnerhub_labeled_containers"], [])
        self.assertEqual(result["volumes_total"], 2)

    def test_labeled_container_has_only_safe_fields(self):
        outputs = {
            ("ps", "-a", "--format", "{{json .}}"): '{"ID":"abc"}\n',
            ("volume", "ls", "--format", "{{json .}}"): "",
            ("inspect", "abc"): '[{"Config":{"Labels":{"runnerhub.worker":"1"},"Env":["SECRET=hidden"]},"State":{"Status":"exited"}}]',
            ("system", "df", "--format", "{{json .}}"): "",
        }
        with patch.object(inventory, "docker", side_effect=lambda *args: outputs[args]):
            result = inventory.snapshot()
        self.assertEqual(result["runnerhub_labeled_containers"], [{"id":"abc","state":"exited"}])
        self.assertNotIn("SECRET", json.dumps(result))


if __name__ == "__main__":
    unittest.main()
