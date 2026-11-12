"""Process/lifecycle calibration with fake procfs and short isolated children."""
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import runtime
from evidence import EvidenceError


class OperationJournalTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.app = object.__new__(runtime.Application)
        self.app.output = Path(self.temporary.name)
        self.app.socket = self.app.output / 'observer.sock'
        self.app.process = SimpleNamespace(pid=123)
        self.app._operation_id = 0

    def events(self):
        return [json.loads(line) for line in
                (self.app.output / 'operations.log').read_text().splitlines()]

    def test_success_and_harmless_error_text_are_recorded_as_complete(self):
        code = 'return await antPerfGui.filterProfiles("FAIL/error", []);'
        with mock.patch.object(runtime, 'call_socket', return_value={'ok': True, 'value': 'error is data'}):
            self.assertEqual(self.app.evaluate(code), 'error is data')
            self.app.evaluate('return true;')
        events = self.events()
        self.assertEqual([item['event'] for item in events], ['start', 'complete', 'start', 'complete'])
        self.assertEqual([item['operation_id'] for item in events], [1, 1, 2, 2])
        self.assertEqual(events[0]['operation'], 'filterProfiles')
        self.assertEqual(events[0]['code_sha256'], hashlib.sha256(code.encode()).hexdigest())
        self.assertNotIn('code', events[0])
        self.assertGreaterEqual(events[1]['elapsed_ms'], 0)

    def test_transport_failure_is_recorded_before_cleanup(self):
        failure = TimeoutError('original observer timeout')
        with mock.patch.object(runtime, 'call_socket', side_effect=failure):
            with self.assertRaises(TimeoutError) as caught:
                self.app.evaluate('return true;')
        self.assertIs(caught.exception, failure)
        events = self.events()
        self.assertEqual([item['event'] for item in events], ['start', 'failed'])
        self.assertEqual(events[1]['error'], 'TimeoutError: original observer timeout')

    def test_gui_failure_cannot_be_logged_as_complete(self):
        with mock.patch.object(runtime, 'call_socket', return_value={'ok': False, 'error': 'wrong rows'}):
            with self.assertRaisesRegex(EvidenceError, 'wrong rows'):
                self.app.evaluate('return true;')
        self.assertEqual([item['event'] for item in self.events()], ['start', 'failed'])


class ProcFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.proc = self.base / 'proc'
        self.proc.mkdir()
        self.root = self.base / 'run/runtime'
        self.root.mkdir(parents=True)
        self.patch = mock.patch.object(runtime, 'PROC_ROOT', self.proc)
        self.patch.start()
        self.addCleanup(self.patch.stop)
        self.process(100, parent=1, session=100, argv=['/owned/app'])
        self.process(101, parent=100, session=100, argv=['/usr/bin/WebKitWebProcess'])

    def process(self, pid, parent=1, session=None, start=None, state='S', argv=None):
        path = self.proc / str(pid)
        path.mkdir(exist_ok=True)
        tail = ['0'] * 20
        tail[0:4] = [state, str(parent), str(session or pid), str(session or pid)]
        tail[11:13] = ['100', '20']
        tail[19] = str(start or pid * 10)
        (path / 'stat').write_text(f'{pid} (fixture with ) parens) ' + ' '.join(tail))
        (path / 'cmdline').write_bytes('\0'.join(argv or ['/owned/helper']).encode() + b'\0')
        (path / 'smaps_rollup').write_text('Rss: 20 kB\nPss: 10 kB\n')
        (path / 'status').write_text('VmHWM: 32 kB\n')
        (path / 'fd').mkdir(exist_ok=True)
        return path

    def test_reparented_runtime_crashpad_and_descendants_are_included(self):
        self.process(102, argv=['/usr/lib/chromium/chrome_crashpad_handler', '--database='+str(self.root/'cache/Crash Reports')])
        self.process(103, parent=102, argv=['/owned/helper'])
        result = runtime.proc_snapshot(100, self.root)
        self.assertEqual(set(result), {'100', '101', '102', '103'})
        self.assertEqual(result['102']['category'], 'browser')
        self.assertEqual(result['102']['pss'], 10240)
        self.assertEqual(result['102']['rss_hwm'], 32768)
        self.assertEqual(result['101']['category'], 'webview')
        self.assertEqual(set(runtime.proc_snapshot(100)), {'100', '101'})

    def test_same_session_reparented_helper_is_included(self):
        self.process(102, parent=1, session=100)
        self.assertIn('102', runtime.proc_snapshot(100))

    def test_neighbour_prefix_and_embedded_prose_are_never_owned(self):
        for index, argument in enumerate((str(self.root)+'-neighbour/cache',
                                          '--database='+str(self.root)+'2/cache',
                                          'script mentions '+str(self.root),
                                          '--annotation=path='+str(self.root),
                                          str(self.root/'../unrelated'))):
            self.process(110+index, argv=['/usr/lib/chromium/chrome_crashpad_handler', argument])
        self.assertEqual(set(runtime.proc_snapshot(100, self.root)), {'100', '101'})
        self.assertTrue(runtime._references_runtime(['--database='+str(self.root)], self.root))
        self.assertTrue(runtime._references_runtime([str(self.root/'a=b')], self.root))

    def test_associated_foreign_uid_is_excluded(self):
        self.process(102, argv=['/usr/bin/chromium', '--user-data-dir='+str(self.root)])
        original = runtime._read_stat
        def read(pid):
            result = original(pid)
            if str(pid) == '102':
                result['uid'] += 1
            return result
        with mock.patch.object(runtime, '_read_stat', side_effect=read):
            self.assertEqual(set(runtime.proc_snapshot(100, self.root)), {'100', '101'})

    def test_known_child_uid_change_fails(self):
        original = runtime._read_stat
        def read(pid):
            result = original(pid)
            if str(pid) == '101':
                result['uid'] += 1
            return result
        with mock.patch.object(runtime, '_read_stat', side_effect=read):
            with self.assertRaises(EvidenceError):
                runtime.proc_snapshot(100, self.root)
            with self.assertRaises(EvidenceError):
                runtime.remaining_owned([('101', 1010)])

    def test_missing_live_resource_does_not_become_zero(self):
        for missing in ('smaps_rollup', 'cmdline', 'fd', 'status'):
            self.process(101, parent=100, session=100)
            path = self.proc/'101'/missing
            path.rmdir() if path.is_dir() else path.unlink()
            with self.subTest(resource=missing), self.assertRaises(EvidenceError):
                runtime.proc_snapshot(100)

    def test_invalid_or_missing_high_water_is_not_zero(self):
        for content in ('Name: fixture\n', 'VmHWM: -1 kB\n', 'VmHWM: 32 bytes\n',
                        'VmHWM: 32 kB\nVmHWM: 64 kB\n'):
            with self.subTest(content=content):
                (self.proc/'101/status').write_text(content)
                with self.assertRaises(EvidenceError):
                    runtime.proc_snapshot(100)

    def test_zombie_high_water_is_explicitly_zero(self):
        self.process(101, parent=100, state='Z')
        (self.proc/'101/status').unlink()
        self.assertEqual(runtime.proc_snapshot(100)['101']['rss_hwm'], 0)

    def test_incomplete_or_denied_live_memory_fails(self):
        path = self.proc/'101/smaps_rollup'
        path.write_text('Rss: 20 kB\n')
        with self.assertRaises(EvidenceError):
            runtime.proc_snapshot(100)
        original = Path.read_text
        def read(target, *args, **kwargs):
            if target == path:
                raise PermissionError('fixture denied')
            return original(target, *args, **kwargs)
        with mock.patch.object(Path, 'read_text', read), self.assertRaises(EvidenceError):
            runtime.proc_snapshot(100)

    def test_disappearing_child_is_an_exit_race(self):
        original = Path.read_text
        def read(target, *args, **kwargs):
            if target == self.proc/'101/smaps_rollup':
                shutil.rmtree(self.proc/'101')
                raise FileNotFoundError('fixture exited')
            return original(target, *args, **kwargs)
        with mock.patch.object(Path, 'read_text', read):
            self.assertEqual(set(runtime.proc_snapshot(100)), {'100'})

    def test_pid_reuse_during_sampling_does_not_mix_resources(self):
        original = Path.read_text
        def read(target, *args, **kwargs):
            if target == self.proc/'101/smaps_rollup':
                self.process(101, start=9999)
            return original(target, *args, **kwargs)
        with mock.patch.object(Path, 'read_text', read):
            self.assertEqual(set(runtime.proc_snapshot(100)), {'100'})

    def test_zombies_remain_owned_and_are_explicit_in_snapshot(self):
        self.process(101, parent=100, session=100, state='Z')
        (self.proc/'101/smaps_rollup').unlink()
        self.assertEqual(runtime.owned_states([('101', 1010)]), {('101', 1010): 'Z'})
        self.assertEqual(runtime.remaining_owned([('101', 1010)]), {('101', 1010)})
        result = runtime.proc_snapshot(100)
        self.assertEqual(result['101']['state'], 'Z')
        self.assertEqual(result['101']['pss'], 0)
        with mock.patch.object(os, 'kill') as kill:
            self.assertFalse(runtime._signal_owned(('101', 1010), signal.SIGKILL))
            kill.assert_not_called()

    def test_reused_or_missing_pid_is_not_signalled(self):
        self.process(101, start=9999)
        self.assertEqual(runtime.remaining_owned([('101', 1010), ('404', 4)]), set())
        with mock.patch.object(runtime, '_open_pidfd') as opened, mock.patch.object(os, 'kill') as kill:
            self.assertFalse(runtime._signal_owned(('101', 1010), signal.SIGKILL))
            opened.assert_not_called()
            kill.assert_not_called()

    def test_pidfd_signal_rechecks_identity_after_open(self):
        original = runtime.owned_states
        calls = 0
        def states(identities):
            nonlocal calls
            calls += 1
            if calls == 2:
                self.process(101, start=9999)
            return original(identities)
        with mock.patch.object(runtime, 'owned_states', side_effect=states), \
             mock.patch.object(runtime, '_open_pidfd', return_value=9876), \
             mock.patch.object(runtime, '_send_pidfd') as send, \
             mock.patch.object(os, 'close') as close:
            self.assertFalse(runtime._signal_owned(('101', 1010), signal.SIGKILL))
            send.assert_not_called()
            close.assert_called_once_with(9876)

    def test_reused_application_pid_does_not_capture_new_users_family(self):
        self.process(100, start=9999)
        self.assertEqual(runtime._process_family(100, self.root, root_started=1000), {})

    def test_missing_or_zombie_application_cannot_prove_sample(self):
        self.process(100, state='Z')
        with self.assertRaises(EvidenceError):
            runtime.proc_snapshot(100)
        shutil.rmtree(self.proc/'100')
        with self.assertRaises(EvidenceError):
            runtime.proc_snapshot(100)


class LifecycleTests(unittest.TestCase):
    def setUp(self):
        # Short paths also exercise the production Unix socket path guard.
        self.temporary = tempfile.TemporaryDirectory(prefix='ant-life-')
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.patch = mock.patch.multiple(runtime, SHUTDOWN_TIMEOUT=.1, RECLAMATION_TIMEOUT=.15)
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def application(self, mode='normal', name='app'):
        output = self.base/name
        (output/'runtime/data').mkdir(parents=True, exist_ok=True)
        (output/'runtime/data/app.db').touch()
        binary = self.base/(name+'.py')
        binary.write_text('#!'+sys.executable+'\n'
            'import os, signal, sys, time\nfrom pathlib import Path\n'
            + ('signal.signal(signal.SIGTERM, signal.SIG_IGN)\n' if mode == 'ignore' else
               'signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))\n')
            + 'signal.signal(signal.SIGUSR1, lambda *_: sys.exit('+('0' if mode == 'early_zero' else '7')+'))\n'
            'Path(os.environ["LINUX_PERF_SOCKET"]).touch()\n'
            'while True: time.sleep(.01)\n')
        binary.chmod(0o700)
        app = runtime.Application(binary, 'unused-seed', output, ':test', 'unused-chromium', size=0, reusable=True)
        self.addCleanup(self.kill_if_needed, app)
        return app

    @staticmethod
    def kill_if_needed(app):
        if app.process is not None:
            if app.process.poll() is None:
                app.process.kill()
            app.process.wait(timeout=2)
        if hasattr(app, 'log'):
            app.log.close()

    def start(self, app):
        with mock.patch.object(runtime, 'call_socket', side_effect=lambda *args: {'root':str(app.runtime), 'pid':app.process.pid}):
            app.start(timeout=2)

    def test_normal_sigterm_zero_exit_has_complete_lifecycle(self):
        app = self.application()
        self.start(app)
        record = app.close()
        self.assertEqual(record['returncode'], 0)
        self.assertFalse(record['forced_shutdown'])
        self.assertEqual(record['remaining_owned'], [])
        events = runtime.load_lifecycle(app.lifecycle_path)
        self.assertEqual([row['event'] for row in events], ['start', 'close'])
        self.assertEqual(runtime.remaining_owned(app.owned), set())
        self.assertTrue(app.log.closed)
        self.assertEqual(app.close(), record)
        self.assertEqual(len(runtime.load_lifecycle(app.lifecycle_path)), 2)

    def test_reused_directory_appends_two_start_close_pairs(self):
        app = self.application()
        self.start(app)
        app.close()
        restarted = self.application()
        self.start(restarted)
        restarted.close()
        self.assertEqual(len(runtime.load_lifecycle(app.lifecycle_path, expected_starts=2)), 4)
        with self.assertRaises(EvidenceError):
            runtime.load_lifecycle(app.lifecycle_path, expected_starts=1)

    def test_abnormal_and_unrequested_zero_exit_are_failures(self):
        for mode in ('normal', 'early_zero'):
            app = self.application(mode, name=mode)
            self.start(app)
            app.process.send_signal(signal.SIGUSR1)
            app.process.wait(timeout=2)
            with self.subTest(mode=mode), self.assertRaises(EvidenceError):
                app.close()
            events = json.loads(app.lifecycle_path.read_text())
            self.assertEqual(events[-1]['returncode'], 0 if mode == 'early_zero' else 7)
            self.assertTrue(events[-1]['errors'])
            self.assertEqual(events[-1]['remaining_owned'], [])
            with self.assertRaises(EvidenceError):
                runtime.load_lifecycle(app.lifecycle_path)

    def test_forced_shutdown_is_failure_even_after_successful_cleanup(self):
        app = self.application('ignore')
        self.start(app)
        with self.assertRaises(EvidenceError):
            app.close()
        record = json.loads(app.lifecycle_path.read_text())[-1]
        self.assertTrue(record['forced_shutdown'])
        self.assertEqual(record['returncode'], -signal.SIGKILL)
        self.assertEqual(record['remaining_owned'], [])
        self.assertEqual(runtime.remaining_owned(app.owned), set())
        with self.assertRaises(EvidenceError):
            app.close()

    def test_real_libc_pidfd_term_and_kill_without_python_wrappers(self):
        for mode in ('normal', 'ignore'):
            app = self.application(mode, name='libc-'+mode)
            self.start(app)
            def open_pidfd(pid):
                return runtime._libc_pidfd('pidfd_open', (pid, 0),
                                          (runtime.ctypes.c_int, runtime.ctypes.c_uint))
            def send_pidfd(descriptor, sig):
                return runtime._libc_pidfd('pidfd_send_signal', (descriptor, int(sig), None, 0),
                    (runtime.ctypes.c_int, runtime.ctypes.c_int, runtime.ctypes.c_void_p, runtime.ctypes.c_uint))
            with mock.patch.object(runtime, '_open_pidfd', side_effect=open_pidfd), \
                 mock.patch.object(runtime, '_send_pidfd', side_effect=send_pidfd), \
                 mock.patch.object(runtime, '_libc_pidfd', wraps=runtime._libc_pidfd) as native:
                if mode == 'normal':
                    app.close()
                else:
                    with self.assertRaises(EvidenceError):
                        app.close()
                sent = [call.args[1][1] for call in native.call_args_list if call.args[0] == 'pidfd_send_signal']
                self.assertIn(signal.SIGTERM, sent)
                if mode == 'ignore':
                    self.assertIn(signal.SIGKILL, sent)
            self.assertEqual(app.process.returncode, 0 if mode == 'normal' else -signal.SIGKILL)
            self.assertEqual(runtime.remaining_owned(app.owned), set())

    def test_discovery_error_still_cleans_up_and_records_failure(self):
        app = self.application()
        self.start(app)
        with mock.patch.object(runtime, '_process_family', side_effect=EvidenceError('calibration discovery failure')):
            with self.assertRaises(EvidenceError):
                app.close()
        self.assertEqual(app.process.returncode, 0)
        self.assertEqual(runtime.remaining_owned(app.owned), set())
        self.assertTrue(json.loads(app.lifecycle_path.read_text())[-1]['errors'])

    def test_remaining_zombie_is_recorded_and_not_signalled(self):
        app = self.application()
        self.start(app)
        zombie = ('900000001', 123)
        app.owned.add(zombie)
        original = runtime.owned_states
        def states(identities):
            result = original([identity for identity in identities if identity != zombie])
            if zombie in identities:
                result[zombie] = 'Z'
            return result
        with mock.patch.object(runtime, 'owned_states', side_effect=states), \
             mock.patch.object(runtime, '_signal_owned', wraps=runtime._signal_owned) as send:
            with self.assertRaises(EvidenceError):
                app.close()
            self.assertFalse(any(call.args[0] == zombie for call in send.call_args_list))
        record = json.loads(app.lifecycle_path.read_text())[-1]
        self.assertEqual(record['remaining_zombies'], [list(zombie)])
        self.assertEqual(record['remaining_owned'], [list(zombie)])
        self.assertFalse(record['forced_shutdown'])
        with self.assertRaises(EvidenceError):
            runtime.load_lifecycle(app.lifecycle_path)

    def test_startup_failure_cannot_be_hidden_by_normal_close(self):
        app = self.application()
        with mock.patch.object(runtime, 'call_socket', return_value={'root':'wrong', 'pid':-1}):
            with self.assertRaises(EvidenceError):
                app.start(timeout=2)
        with self.assertRaises(EvidenceError):
            app.close()
        self.assertEqual(app.process.returncode, 0)
        with self.assertRaises(EvidenceError):
            runtime.load_lifecycle(app.lifecycle_path)

    def test_loader_rejects_missing_truncated_incomplete_or_wrong_evidence(self):
        app = self.application()
        self.start(app)
        app.close()
        original = json.loads(app.lifecycle_path.read_text())
        for field, value in (('returncode', 7), ('returncode', False), ('forced_shutdown', True),
                             ('root', '/wrong/root'), ('remaining_owned', [['123', 123]]),
                             ('remaining_zombies', [['123', 123]]), ('errors', ['error']),
                             ('started_identity', ['123', 1]), ('owned', None)):
            events = copy.deepcopy(original)
            events[-1][field] = value
            app.lifecycle_path.write_text(json.dumps(events))
            with self.subTest(field=field), self.assertRaises(EvidenceError):
                runtime.load_lifecycle(app.lifecycle_path)
        for content in ('{', '[]', '{}', json.dumps(original[:1]),
                        json.dumps(original).replace('"returncode": 0', '"returncode": 7, "returncode": 0'),
                        json.dumps(original).replace('"returncode": 0', '"returncode": NaN')):
            app.lifecycle_path.write_text(content)
            with self.subTest(content=content), self.assertRaises(EvidenceError):
                runtime.load_lifecycle(app.lifecycle_path)
        missing = copy.deepcopy(original)
        missing[0].pop('returncode')
        app.lifecycle_path.write_text(json.dumps(missing))
        with self.assertRaises(EvidenceError):
            runtime.load_lifecycle(app.lifecycle_path)
        app.lifecycle_path.unlink()
        with self.assertRaises(EvidenceError):
            runtime.load_lifecycle(app.lifecycle_path)

    def test_reuse_rejects_duplicate_json_keys_and_nonfinite_constants(self):
        app = self.application()
        self.start(app)
        app.close()
        original = app.lifecycle_path.read_text()
        for invalid in (original.replace('"returncode": 0', '"returncode": 7, "returncode": 0'),
                        original.replace('"returncode": 0', '"returncode": NaN')):
            app.lifecycle_path.write_text(invalid)
            with self.assertRaises(EvidenceError):
                self.application()


if __name__ == '__main__':
    unittest.main()
