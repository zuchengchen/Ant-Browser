import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parents[1]
TAG = "czc-v2.3.4"
MOCK_GH = """#!/usr/bin/env python3
from pathlib import Path
import json, os, sys
args = sys.argv[1:]
state = Path("draft-state")
with Path("calls").open("a") as log:
    print(json.dumps(args), file=log)
if args[1] == "view":
    if not state.exists(): sys.exit(1)
    print(state.read_text())
elif args[1] == "create":
    if state.exists(): sys.exit(1)
    if "--notes-file" in args:
        Path("release-body").write_text(Path(args[args.index("--notes-file") + 1]).read_text())
    state.write_text("true")
elif args[1] == "upload":
    if state.read_text() != "true" or os.environ.get("FAIL_UPLOAD"): sys.exit(1)
elif args[1] == "edit":
    if state.read_text() != "true": sys.exit(1)
    state.write_text("false")
else:
    sys.exit(1)
"""


class PublishForkReleaseTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        (self.root / "scripts").mkdir()
        (self.root / "frontend").mkdir()
        shutil.copyfile(SCRIPTS / "resolve-release-version.py", self.root / "scripts/resolve-release-version.py")
        self.write("wails.json", {"info": {"productVersion": "2.3.4"}})
        self.write("frontend/package.json", {"version": "2.3.4"})
        self.write("frontend/package-lock.json", {"version": "2.3.4", "packages": {"": {"version": "2.3.4"}}})
        self.asset = self.root / "release-artifacts/package.zip"
        self.asset.parent.mkdir()
        self.asset.write_text("asset")
        gh = self.root / "gh"
        gh.write_text(MOCK_GH)
        gh.chmod(0o755)
        self.git("init", "-b", "master")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Test")
        self.git("commit", "--allow-empty", "-m", "initial")
        self.git("update-ref", "refs/remotes/origin/master", "HEAD")

    def write(self, file, payload):
        (self.root / file).write_text(json.dumps(payload))

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.root, stderr=subprocess.PIPE, text=True).strip()

    def publish(self, **env):
        variables = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ["PATH"],
                         GITHUB_EVENT_NAME="push", GITHUB_REF_TYPE="tag", GITHUB_REF_NAME=TAG,
                         GH_REPO="example/fork")
        variables.update(env)
        return subprocess.run(["bash", str(SCRIPTS / "publish-fork-release.sh")], cwd=self.root,
                              env=variables, text=True, capture_output=True)

    def calls(self):
        path = self.root / "calls"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def test_gate_rejects_manual_branch_upstream_and_mismatched_version(self):
        for env in ({"GITHUB_EVENT_NAME": "workflow_dispatch"}, {"GITHUB_REF_TYPE": "branch"},
                    {"GITHUB_REF_NAME": "v2.3.4"}, {"GITHUB_REF_NAME": "czc-v2.3.5"}):
            with self.subTest(env=env):
                self.assertNotEqual(self.publish(**env).returncode, 0)
        self.assertEqual(self.calls(), [])

    def test_unmerged_commit_cannot_publish(self):
        self.git("switch", "-c", "unmerged")
        self.git("commit", "--allow-empty", "-m", "feature in progress")
        result = self.publish()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("outside origin/master", result.stderr)
        self.assertEqual(self.calls(), [])

    def test_all_assets_publish_to_draft_of_existing_tag_then_become_public(self):
        result = self.publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls()
        self.assertEqual([args[1] for args in calls], ["view", "create", "upload", "edit"])
        self.assertTrue(all(args[2] == TAG for args in calls))
        self.assertIn("--verify-tag", calls[1])
        self.assertIn("--draft", calls[1])
        self.assertNotIn("--target", calls[1])
        self.assertIn("release-artifacts/package.zip", calls[2])
        self.assertIn("--draft=false", calls[3])

    def test_retry_continues_draft_and_skips_published_release(self):
        self.assertNotEqual(self.publish(FAIL_UPLOAD="1").returncode, 0)
        self.assertEqual((self.root / "draft-state").read_text(), "true")
        result = self.publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([args[1] for args in self.calls()], ["view", "create", "upload", "view", "upload", "edit"])
        result = self.publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([args[1] for args in self.calls()][-2:], ["edit", "view"])

    def test_no_asset_does_not_create_release(self):
        self.asset.unlink()
        self.assertNotEqual(self.publish().returncode, 0)
        self.assertEqual([args[1] for args in self.calls()], ["view"])

    def test_notes_use_fork_tag_and_incorporate_actual_upstream_merge(self):
        self.git("switch", "-c", "upstream")
        self.git("commit", "--allow-empty", "-m", "upstream change")
        sha = self.git("rev-parse", "HEAD")
        self.git("switch", "master")
        self.git("merge", "--no-ff", "upstream", "-m", "Merge upstream\n\nUpstream-Ref: refs/tags/v8.9.0\nUpstream-Commit: " + sha)
        self.git("update-ref", "refs/remotes/origin/master", "HEAD")
        changelogs = self.root / "docs/changelogs"
        changelogs.mkdir(parents=True)
        (changelogs / "2.3.4.md").write_text("A different upstream release")
        (changelogs / (TAG + ".md")).write_text("Fork release notes\n")
        result = self.publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        body = (self.root / "release-body").read_text()
        self.assertIn("Fork release notes", body)
        self.assertIn("refs/tags/v8.9.0", body)
        self.assertIn(sha, body)
        self.assertNotIn("A different upstream release", body)
