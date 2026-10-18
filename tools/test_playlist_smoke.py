"""Protect pre-render synchronization without advancing the screen cycle."""

import copy
import importlib.util
import json
import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "playlist_smoke", Path(__file__).with_name("playlist-smoke.py")
)
playlist = importlib.util.module_from_spec(spec)
spec.loader.exec_module(playlist)


class JobPublicationTests(unittest.TestCase):
    def test_completed_job_is_not_ready_until_current_image_is_published(self):
        jobs = {
            "items": [
                {"status": {"conditions": [{"type": "Complete", "status": "True"}]}}
            ]
        }
        screen = {"metadata": {"generation": 2}, "status": {"conditions": []}}
        calls = []

        def kubectl(*args):
            calls.append(args)
            value = jobs if args[3] == "jobs" else screen
            return subprocess.CompletedProcess(args, 0, json.dumps(value), "")

        with patch.object(playlist, "kubectl", side_effect=kubectl):
            # Kubernetes completion precedes collection and publication. Neither
            # missing readiness nor a pending/failed collection permits restart.
            self.assertFalse(playlist.job_output_ready("isolated"))
            condition = {
                "type": "Ready",
                "status": "False",
                "reason": "JobPending",
                "observedGeneration": 2,
            }
            screen["status"]["conditions"] = [condition]
            self.assertFalse(playlist.job_output_ready("isolated"))
            condition.update(reason="CollectionFailed")
            self.assertFalse(playlist.job_output_ready("isolated"))
            # Readiness for a previous definition cannot satisfy pre-rendering.
            condition.update(status="True", reason="ImageReady", observedGeneration=1)
            self.assertFalse(playlist.job_output_ready("isolated"))
            condition.update(observedGeneration=2)
            self.assertTrue(playlist.job_output_ready("isolated"))
        self.assertEqual(
            set(calls),
            {
                ("-n", "isolated", "get", "jobs", "-o", "json"),
                ("-n", "isolated", "get", "arprtscreen", "c-job", "-o", "json"),
            },
        )

    def test_missing_or_running_job_cannot_satisfy_pre_rendering(self):
        screen = {
            "metadata": {"generation": 1},
            "status": {
                "conditions": [
                    {
                        "type": "Ready",
                        "status": "True",
                        "reason": "ImageReady",
                        "observedGeneration": 1,
                    }
                ]
            },
        }
        for conditions in [
            None,
            [],
            [{"type": "Complete", "status": "False"}],
            [{"type": "Failed", "status": "True"}],
        ]:
            jobs = {
                "items": []
                if conditions is None
                else [{"status": {"conditions": copy.deepcopy(conditions)}}]
            }
            with (
                self.subTest(conditions=conditions),
                patch.object(playlist, "kubectl") as kubectl,
            ):
                kubectl.side_effect = [
                    subprocess.CompletedProcess([], 0, json.dumps(value), "")
                    for value in [jobs, screen]
                ]
                self.assertFalse(playlist.job_output_ready("isolated"))
                self.assertEqual(kubectl.call_count, 1)


if __name__ == "__main__":
    unittest.main()
