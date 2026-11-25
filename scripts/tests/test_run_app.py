import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


LAUNCHER = Path(__file__).resolve().parents[1] / "run-app.sh"


class RunAppTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="ant-run-app-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.launcher = self.root / "scripts/run-app.sh"
        self.launcher.parent.mkdir()
        shutil.copyfile(LAUNCHER, self.launcher)
        self.commands = self.root / "commands"
        self.commands.mkdir()
        self.log_file = self.root / "data/logs/dev-stdout.log"

    def executable(self, path, text):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("#!/usr/bin/env bash\n" + text + "\n", encoding="utf-8")
        path.chmod(0o755)

    def run_launcher(self, *args):
        path = str(self.commands) + os.pathsep + os.environ["PATH"]
        return subprocess.run(["bash", str(self.launcher), *args], cwd=self.root,
                              env={**os.environ, "PATH": path}, capture_output=True,
                              encoding="utf-8", timeout=15)

    def test_start_creates_log_directory_before_launching(self):
        self.executable(self.commands / "ss", "exit 0")
        self.executable(self.commands / "setsid", 'shift; exec "$@"')
        self.executable(self.root / "build/bin/ant-chrome",
                        'echo "test launch"\ntouch app-instance.lock')
        self.assertFalse(self.log_file.parent.exists())
        result = self.run_launcher()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.log_file.read_text(encoding="utf-8"), "test launch\n")

    def test_check_handles_unavailable_profile_api(self):
        self.executable(self.commands / "ss",
                        "echo 'State Recv-Q Send-Q Local Address:Port Peer Address:Port'\n"
                        "echo 'LISTEN 0 128 127.0.0.1:19876 0.0.0.0:*'")
        for reply in ('echo \'{"ok": false}\'', "exit 22"):
            with self.subTest(reply=reply):
                self.executable(self.commands / "curl", reply)
                result = self.run_launcher("--check")
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("被占用", result.stdout)
                self.assertFalse(self.log_file.parent.exists())
