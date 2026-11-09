"""Exercise fork syncing against disposable local Git remotes, without GitHub."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("release_version", SCRIPTS / "resolve-release-version.py")
RELEASE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RELEASE)


class SyncUpstreamTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="ant-fork-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.env = {
            **os.environ,
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_TERMINAL_PROMPT": "0",
        }
        self.upstream = self.root / "upstream.git"
        self.origin = self.root / "origin.git"
        self.seed = self.root / "seed"
        self.work = self.root / "work"
        for remote in (self.upstream, self.origin):
            self.git("init", "--bare", "--initial-branch=master", str(remote), cwd=self.root)
        self.git("init", "--initial-branch=master", str(self.seed), cwd=self.root)
        self.configure_author(self.seed)
        self.write(self.seed, "shared.txt", "base\n")
        self.commit(self.seed, "upstream base")
        self.git("remote", "add", "origin", str(self.upstream), cwd=self.seed)
        self.git("push", "origin", "master", cwd=self.seed)
        self.git("push", str(self.origin), "master", cwd=self.seed)
        self.git("clone", str(self.origin), str(self.work), cwd=self.root)
        self.configure_author(self.work)
        self.git("remote", "add", "upstream", str(self.upstream))
        (self.work / "scripts").mkdir()
        shutil.copy2(SCRIPTS / "sync-upstream.sh", self.work / "scripts/sync-upstream.sh")
        self.write(self.work, "fork-only.txt", "keep the fork feature\n")
        self.commit(self.work, "fork feature and sync script")
        self.original_head = self.git("rev-parse", "HEAD").strip()
        self.original_remote = self.git("rev-parse", "origin/master").strip()

    def git(self, *args, cwd=None):
        result = subprocess.run(
            ["git", *args], cwd=cwd or self.work, env=self.env,
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        )
        if result.returncode:
            self.fail(f"git {args} failed:\n{result.stdout}")
        return result.stdout

    def configure_author(self, directory):
        self.git("config", "user.name", "Fork Test", cwd=directory)
        self.git("config", "user.email", "fork-test@example.invalid", cwd=directory)

    def write(self, directory, name, contents):
        (directory / name).write_text(contents, encoding="utf-8")

    def commit(self, directory, message):
        self.git("add", ".", cwd=directory)
        self.git("commit", "-m", message, cwd=directory)
        return self.git("rev-parse", "HEAD", cwd=directory).strip()

    def upstream_change(self, name="upstream-only.txt", contents="upstream improvement\n"):
        self.write(self.seed, name, contents)
        sha = self.commit(self.seed, "upstream improvement")
        self.git("push", "origin", "master", cwd=self.seed)
        return sha

    def sync(self, *args):
        return subprocess.run(
            ["bash", "scripts/sync-upstream.sh", *args], cwd=self.work, env=self.env,
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        )

    def test_already_integrated_preserves_fork_commits(self):
        result = self.sync()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("Already integrated", result.stdout)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.original_head)
        self.assertEqual(self.git("status", "--porcelain"), "")

    def test_check_allows_local_work_and_does_not_merge(self):
        upstream_sha = self.upstream_change()
        self.write(self.work, "local-work.txt", "unfinished work\n")
        before = self.git("status", "--porcelain")
        result = self.sync("--check")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn(upstream_sha, result.stdout)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.original_head)
        self.assertEqual(self.git("status", "--porcelain"), before)
        self.assertFalse((self.work / "upstream-only.txt").exists())

    def test_merge_preserves_both_histories_and_waits_for_commit(self):
        upstream_sha = self.upstream_change()
        result = self.sync()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.original_head)
        self.assertEqual(self.git("rev-parse", "MERGE_HEAD").strip(), upstream_sha)
        self.assertTrue((self.work / "upstream-only.txt").exists())
        self.assertEqual((self.work / "fork-only.txt").read_text(), "keep the fork feature\n")
        self.git("commit", "--no-edit")
        self.git("merge-base", "--is-ancestor", upstream_sha, "HEAD")
        self.git("merge-base", "--is-ancestor", self.original_head, "HEAD")
        self.assertIn(f"Upstream-Commit: {upstream_sha}", self.git("log", "-1", "--format=%B"))
        remote_head = self.git("--git-dir", str(self.origin), "rev-parse", "master", cwd=self.root).strip()
        self.assertEqual(remote_head, self.original_remote)

    def test_conflict_is_left_for_resolution_and_can_be_aborted(self):
        self.write(self.work, "shared.txt", "fork behaviour\n")
        fork_head = self.commit(self.work, "change fork behaviour")
        upstream_sha = self.upstream_change("shared.txt", "upstream behaviour\n")
        result = self.sync()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), fork_head)
        self.assertEqual(self.git("rev-parse", "MERGE_HEAD").strip(), upstream_sha)
        self.assertIn("shared.txt", self.git("diff", "--name-only", "--diff-filter=U"))
        self.assertNotEqual(self.sync().returncode, 0)
        self.git("merge", "--abort")
        self.assertEqual((self.work / "shared.txt").read_text(), "fork behaviour\n")
        self.assertEqual(self.git("status", "--porcelain"), "")

    def test_dirty_worktree_is_rejected_without_losing_changes(self):
        self.upstream_change()
        self.write(self.work, "shared.txt", "uncommitted work\n")
        result = self.sync()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("dirty", result.stdout)
        self.assertEqual((self.work / "shared.txt").read_text(), "uncommitted work\n")
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.original_head)

    def test_wrong_branch_is_rejected(self):
        self.git("switch", "-c", "feature/work")
        result = self.sync()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Switch to master", result.stdout)
        self.assertEqual(self.git("branch", "--show-current").strip(), "feature/work")

    def test_new_origin_work_must_be_reconciled_first(self):
        self.upstream_change()
        self.git("push", str(self.origin), "master", cwd=self.seed)
        result = self.sync()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not contain origin/master", result.stdout)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.original_head)
        self.assertEqual(self.git("status", "--porcelain"), "")

    def test_selected_tag_does_not_import_tags_or_later_commits(self):
        release_sha = self.upstream_change()
        self.git("tag", "-a", "v1.9.0", "-m", "upstream release", cwd=self.seed)
        self.git("push", "origin", "refs/tags/v1.9.0", cwd=self.seed)
        self.upstream_change("later.txt", "not in selected release\n")
        result = self.sync("--ref", "refs/tags/v1.9.0")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertEqual(self.git("rev-parse", "MERGE_HEAD").strip(), release_sha)
        self.assertEqual(self.git("tag", "--list"), "")
        self.assertFalse((self.work / "later.txt").exists())


class ReleaseVersionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="ant-release-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / "frontend").mkdir()
        self.write("wails.json", {"info": {"productVersion": "1.9.0"}})
        self.write("frontend/package.json", {"version": "1.9.0"})
        self.write("frontend/package-lock.json", {"version": "1.9.0", "packages": {"": {"version": "1.9.0"}}})

    def write(self, name, value):
        (self.root / name).write_text(json.dumps(value), encoding="utf-8")

    def test_fork_tag_and_manual_build_resolve_same_embedded_version(self):
        self.assertEqual(RELEASE.resolve_version(self.root, "tag", "czc-v1.9.0", ""), "1.9.0")
        self.assertEqual(RELEASE.resolve_version(self.root, "branch", "master", ""), "1.9.0")
        self.assertEqual(RELEASE.resolve_version(self.root, "branch", "master", "1.9.0"), "1.9.0")

    def test_upstream_and_malformed_tags_cannot_be_released(self):
        for tag in ("v1.9.0", "1.9.0", "czc-v1.9.0-extra", "czc-v01.9.0", "czc-v1.9.0.1"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                RELEASE.resolve_version(self.root, "tag", tag, "")

    def test_tag_cannot_claim_a_different_version_than_the_app(self):
        with self.assertRaises(ValueError):
            RELEASE.resolve_version(self.root, "tag", "czc-v1.9.1", "")
        with self.assertRaises(ValueError):
            RELEASE.resolve_version(self.root, "tag", "czc-v1.9.0", "1.9.1")
        with self.assertRaises(ValueError):
            RELEASE.resolve_version(self.root, "branch", "master", "1.9.1")

    def test_lockfile_root_package_version_must_match(self):
        self.write("frontend/package-lock.json", {"version": "1.9.0", "packages": {"": {"version": "1.8.0"}}})
        with self.assertRaises(ValueError):
            RELEASE.resolve_version(self.root, "tag", "czc-v1.9.0", "")


if __name__ == "__main__":
    unittest.main()
