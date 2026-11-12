"""Low-load calibration of the real Unix HTTP timing boundary and GUI proof checks."""
import contextlib
import http.server
import json
from pathlib import Path
import socketserver
import sys
import tempfile
import threading
import time
import unittest
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import runtime
from evidence import EvidenceError


def rendered(**changes):
    proof = {'render_proof_version': 2, 'frame_count': 2,
             'predicate_before': True, 'predicate_after': True,
             'predicate_ms': 0, 'first_frame_ms': 1, 'second_frame_ms': 2,
             'completed_ms': 3, 'deadline_ms': 1000,
             'frame_attempts': [{'raf_ms': 0, 'callback_ms': 1},
                                {'raf_ms': 1, 'callback_ms': 2}]}
    proof.update(changes)
    return {'render': proof, 'rows': ['synthetic-row']}


@contextlib.contextmanager
def observer(app, messages, *, delay=0, linger=0, raw=None):
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            time.sleep(delay)
            try:
                self.send_response(200)
                self.send_header('Content-Type', 'application/x-ndjson')
                self.end_headers()
                if raw is not None:
                    self.wfile.write(raw)
                for item in messages:
                    if 'delay' in item:
                        time.sleep(item['delay'])
                        continue
                    value = {'token': request['token'], 'request_id': 'test-request', **item}
                    self.wfile.write(json.dumps(value).encode() + b'\n')
                    self.wfile.flush()
                time.sleep(linger)
            except (BrokenPipeError, ConnectionResetError):
                pass
    server = socketserver.ThreadingUnixStreamServer(str(app.socket), Handler)
    server.daemon_threads = True
    thread = threading.Thread(target=lambda: server.serve_forever(poll_interval=.01), daemon=True)
    thread.start()
    try:
        yield
    finally:
        server.shutdown(); server.server_close(); thread.join(timeout=2)
        app.socket.unlink(missing_ok=True)


class GuiTimingTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.app = object.__new__(runtime.Application)
        self.app.output = Path(self.temporary.name)
        self.app.socket = self.app.output / 'observer.sock'
        self.app.process = SimpleNamespace(pid=123)
        self.app._operation_id = 0

    def result(self, **changes):
        return {'sequence': 1, 'kind': 'result', 'ok': True, 'value': rendered(), **changes}

    def test_host_includes_real_entry_and_return_queue_delays(self):
        messages = [{'delay': .04}, self.result()]
        with observer(self.app, messages, delay=.05):
            result = self.app.evaluate_timed('return preparedResult;', require_render=True, timeout=1)
        self.assertGreaterEqual(result['elapsed_ms'], 85)
        self.assertLess(result['elapsed_ms'], 900)
        self.assertEqual(result['value']['render']['completed_ms'], 3)
        events = [json.loads(line) for line in (self.app.output/'operations.log').read_text().splitlines()]
        self.assertEqual([event['event'] for event in events], ['start', 'complete'])
        self.assertEqual(events[-1]['elapsed_ms'], result['elapsed_ms'])

    def test_phases_share_host_origin_despite_callback_arrival_order(self):
        messages = [
            {'sequence': 2, 'kind': 'phase', 'phase': 'closed', 'ok': True, 'value': rendered()},
            self.result(sequence=3), {'delay': .03},
            {'sequence': 1, 'kind': 'phase', 'phase': 'visible', 'ok': True, 'value': rendered()},
        ]
        with observer(self.app, messages, delay=.03):
            result = self.app.evaluate_timed('return preparedResult;', expected_phases=('visible','closed'), require_render=True, timeout=1)
        phases = result['phases']
        self.assertGreaterEqual(phases['closed']['elapsed_ms'], 25)
        self.assertGreaterEqual(phases['visible']['elapsed_ms'], 55)
        self.assertEqual(result['received_monotonic_ns'], phases['visible']['received_monotonic_ns'])
        for phase in phases.values():
            self.assertAlmostEqual(phase['elapsed_ms'], (phase['received_monotonic_ns']-result['started_monotonic_ns'])/1e6)

    def test_independent_phase_can_really_complete_first(self):
        messages = [
            {'sequence': 1, 'kind': 'phase', 'phase': 'closed', 'ok': True, 'value': rendered()},
            {'sequence': 2, 'kind': 'phase', 'phase': 'visible', 'ok': True, 'value': rendered()},
            self.result(sequence=3),
        ]
        with observer(self.app, messages):
            result = self.app.evaluate_timed('return preparedResult;', expected_phases=('visible','closed'), require_render=True, timeout=1)
        self.assertEqual(result['phases']['closed']['sequence'], 1)
        self.assertEqual(result['phases']['visible']['sequence'], 2)

    def test_invalid_identity_sequence_terminal_and_phase_fail(self):
        variants = [
            [self.result(token='stale-token')],
            [self.result(sequence=2)],
            [self.result(), self.result()],
            [{'sequence': 1, 'kind': 'phase', 'phase': 'unregistered', 'ok': True, 'value': rendered()}],
            [],
            [self.result(kind='error', ok=False, error='wrong full IDs')],
        ]
        for messages in variants:
            with self.subTest(messages=messages), observer(self.app, messages):
                with self.assertRaises(EvidenceError):
                    self.app.evaluate_timed('return preparedResult;', timeout=1)

    def test_missing_phase_cannot_pass(self):
        with observer(self.app, [self.result()]):
            with self.assertRaisesRegex(EvidenceError, 'Missing required'):
                self.app.evaluate_timed('return preparedResult;', expected_phases=('visible',), timeout=1)

    def test_missing_frames_or_business_result_cannot_pass(self):
        for value in ({}, rendered(frame_count=1), rendered(predicate_after=False),
                      rendered(second_frame_ms=1), rendered(completed_ms=-1)):
            with self.subTest(value=value), observer(self.app, [self.result(value=value)]):
                with self.assertRaises(EvidenceError):
                    self.app.evaluate_timed('return preparedResult;', require_render=True, timeout=1)

    def test_missing_callback_hits_single_host_deadline(self):
        with observer(self.app, [], linger=.2):
            started = time.monotonic()
            with self.assertRaisesRegex(EvidenceError, 'host observation deadline'):
                self.app.evaluate_timed('await neverCompletes;', timeout=.05)
            self.assertLess(time.monotonic()-started, .18)

    def test_truncated_json_and_duplicate_keys_fail(self):
        for raw in (b'{"ok":true}', b'{"ok":true,"ok":false}\n'):
            with self.subTest(raw=raw), observer(self.app, [], raw=raw):
                with self.assertRaises((EvidenceError, ValueError)):
                    self.app.evaluate_timed('return preparedResult;', timeout=1)

    def test_harmless_error_text_is_data(self):
        with observer(self.app, [self.result(value='0 errors; benign FAIL label')]):
            self.assertEqual(self.app.evaluate_timed('return text;', timeout=1)['value'], '0 errors; benign FAIL label')


if __name__ == '__main__':
    unittest.main()
