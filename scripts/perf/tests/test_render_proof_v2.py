"""Synthetic v2 clock/receipt calibration; never converts historical GUI runs."""
import copy
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from evidence import EvidenceError, digest_object
from gui_evidence import _records, verify_operation
from runtime import validate_render_proof


def rendered():
    return {'render': {'render_proof_version': 2, 'frame_count': 2,
                      'predicate_before': True, 'predicate_after': True,
                      'predicate_ms': 100, 'first_frame_ms': 101,
                      'second_frame_ms': 101, 'completed_ms': 102,
                      'deadline_ms': 110,
                      'frame_attempts': [{'raf_ms': 90, 'callback_ms': 101},
                                         {'raf_ms': 91, 'callback_ms': 101}]},
            'rows': ['synthetic-row']}


def receipts(value, *, phase=False):
    """Independent, explicitly synthetic host/backend records with valid hashes."""
    token = 'synthetic-v2-render-proof'
    code = hashlib.sha256(b'return syntheticRenderProof;').hexdigest()
    identity = {'token': token, 'operation_id': 7, 'pid': 123, 'code_sha256': code}
    start = {**identity, 'event': 'start', 'operation': 'timed_gui',
             'started_monotonic_ns': 1_000_000_000, 'timeout_seconds': 1,
             'expected_phases': ['visible'] if phase else [], 'require_render': True}
    complete = {**identity, 'event': 'complete', 'received_monotonic_ns': 1_020_000_000,
                'elapsed_ms': 20, 'request_id': '7', 'terminal_sequence': 2 if phase else 1,
                'value_sha256': digest_object(value), 'phase_ms': {'visible': 10} if phase else {}}
    host = [start, complete]
    backend = [{'event': 'request', 'token': token, 'request_id': '7',
                'stream': True, 'code_sha256': code}]
    if phase:
        host.insert(1, {**identity, 'event': 'phase', 'phase': 'visible', 'sequence': 1,
                        'elapsed_ms': 10, 'received_monotonic_ns': 1_010_000_000,
                        'value': copy.deepcopy(value)})
        backend.append({'event': 'message', 'token': token, 'request_id': '7',
                        'payload': {'token': token, 'ok': True, 'sequence': 1,
                                    'kind': 'phase', 'phase': 'visible', 'value': copy.deepcopy(value)}})
    backend.append({'event': 'message', 'token': token, 'request_id': '7',
                    'payload': {'token': token, 'ok': True, 'sequence': 2 if phase else 1,
                                'kind': 'result', 'value': copy.deepcopy(value)}})
    return host, backend, {'token': token, 'operation_id': 7,
                           **({'phase': 'visible'} if phase else {})}


class RenderProofV2Tests(unittest.TestCase):
    def test_distinct_native_frames_allow_equal_callback_clock(self):
        value = rendered()
        self.assertEqual(validate_render_proof(value), value['render'])
        self.assertLess(value['render']['frame_attempts'][0]['raf_ms'], value['render']['predicate_ms'])

    def test_equal_native_attempts_are_retained_until_first_distinct_frame(self):
        value = rendered()
        value['render']['frame_attempts'].insert(1, {'raf_ms': 90, 'callback_ms': 101})
        self.assertEqual(len(validate_render_proof(value)['frame_attempts']), 3)

    def test_native_clock_is_not_ordered_against_callback_clock(self):
        value = rendered()
        value['render']['frame_attempts'] = [{'raf_ms': 900, 'callback_ms': 101},
                                            {'raf_ms': 901, 'callback_ms': 101}]
        validate_render_proof(value)

    def test_all_callback_clock_readings_may_equal(self):
        value = rendered()
        value['render'].update(predicate_ms=101, completed_ms=101)
        validate_render_proof(value)

    def test_legacy_missing_or_wrong_version_is_rejected(self):
        for version in (None, 1, True, 2.0, '2', 3):
            value = rendered()
            if version is None:
                del value['render']['render_proof_version']
            else:
                value['render']['render_proof_version'] = version
            with self.subTest(version=version), self.assertRaises(EvidenceError):
                validate_render_proof(value)

    def test_clock_fields_reject_missing_bool_nonfinite_negative_and_wrong_types(self):
        fields = ('predicate_ms', 'first_frame_ms', 'second_frame_ms', 'completed_ms', 'deadline_ms')
        for field in fields:
            for invalid in (None, True, False, '1', float('nan'), float('inf'), -float('inf'), -1, 10**400):
                value = rendered(); value['render'][field] = invalid
                with self.subTest(field=field, invalid=repr(invalid)), self.assertRaises(EvidenceError):
                    validate_render_proof(value)
        for field in ('raf_ms', 'callback_ms'):
            for invalid in (None, True, '1', float('nan'), float('inf'), -1):
                value = rendered(); value['render']['frame_attempts'][0][field] = invalid
                with self.subTest(attempt_field=field, invalid=repr(invalid)), self.assertRaises(EvidenceError):
                    validate_render_proof(value)

    def test_missing_or_malformed_native_attempts_are_rejected(self):
        for attempts in (None, [], {}, [{}], [None, {}],
                         [{'raf_ms': 90}, {'raf_ms': 91, 'callback_ms': 101}],
                         [{'raf_ms': 90, 'callback_ms': 101, 'invented': True},
                          {'raf_ms': 91, 'callback_ms': 101}]):
            value = rendered(); value['render']['frame_attempts'] = attempts
            with self.subTest(attempts=attempts), self.assertRaises(EvidenceError):
                validate_render_proof(value)

    def test_equal_regressing_or_late_distinct_native_sequences_are_rejected(self):
        for stamps in ([90, 90], [90, 89], [90, 89, 91], [90, 91, 92], [90, 91, 91]):
            value = rendered()
            value['render']['frame_attempts'] = [{'raf_ms': x, 'callback_ms': 101} for x in stamps]
            with self.subTest(stamps=stamps), self.assertRaises(EvidenceError):
                validate_render_proof(value)

    def test_callback_bounds_predicates_and_deadline_are_enforced(self):
        mutations = [
            lambda p: p.update(frame_count=True), lambda p: p.update(frame_count=3),
            lambda p: p.update(predicate_before=False), lambda p: p.update(predicate_after=1),
            lambda p: p.update(predicate_ms=102), lambda p: p.update(first_frame_ms=100),
            lambda p: p.update(second_frame_ms=102), lambda p: p.update(completed_ms=100),
            lambda p: p.update(deadline_ms=102), lambda p: p.update(deadline_ms=101),
            lambda p: p.update(frame_attempts=[{'raf_ms': 90, 'callback_ms': 101},
                                              {'raf_ms': 90, 'callback_ms': 102},
                                              {'raf_ms': 91, 'callback_ms': 101}]),
        ]
        for index, mutate in enumerate(mutations):
            value = rendered(); mutate(value['render'])
            with self.subTest(mutation=index), self.assertRaises(EvidenceError):
                validate_render_proof(value)

    def test_raw_terminal_and_phase_receipts_reparse_v2_proofs(self):
        for phase in (False, True):
            host, backend, sample = receipts(rendered(), phase=phase)
            with self.subTest(phase=phase):
                self.assertEqual(verify_operation(host, backend, sample), 10 if phase else 20)
            with tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                for name, values in [('host', host), ('backend', backend)]:
                    (root/name).write_text(''.join(json.dumps(x) + '\n' for x in values))
                self.assertEqual(verify_operation(_records(root/'host'), _records(root/'backend'), sample),
                                 10 if phase else 20)

    def test_recomputed_terminal_hash_cannot_bypass_invalid_raw_proof(self):
        for mutation in ('native_equal', 'legacy', 'deadline', 'wrong_first', 'extra_distinct'):
            value = rendered()
            if mutation == 'native_equal': value['render']['frame_attempts'][-1]['raf_ms'] = 90
            elif mutation == 'legacy': del value['render']['render_proof_version']
            elif mutation == 'deadline': value['render']['deadline_ms'] = 102
            elif mutation == 'wrong_first': value['render']['first_frame_ms'] = 100
            else: value['render']['frame_attempts'].append({'raf_ms': 92, 'callback_ms': 101})
            host, backend, sample = receipts(value)
            with self.subTest(mutation=mutation), self.assertRaises(EvidenceError):
                verify_operation(host, backend, sample)

    def test_matching_host_and_backend_phase_cannot_bypass_invalid_phase_proof(self):
        host, backend, sample = receipts(rendered(), phase=True)
        for record in host:
            if record['event'] == 'phase': record['value']['render']['frame_attempts'][-1]['raf_ms'] = 90
        for record in backend:
            if record.get('payload', {}).get('kind') == 'phase':
                record['payload']['value']['render']['frame_attempts'][-1]['raf_ms'] = 90
        with self.assertRaises(EvidenceError):
            verify_operation(host, backend, sample)

    def test_raw_legacy_success_and_failed_operations_cannot_be_reclassified(self):
        value = rendered()
        for key in ('render_proof_version', 'frame_attempts', 'deadline_ms'):
            del value['render'][key]
        host, backend, sample = receipts(value)
        with self.assertRaises(EvidenceError):
            verify_operation(host, backend, sample)
        host, backend, sample = receipts(rendered())
        host.append({**host[-1], 'event': 'failed', 'error': 'Expected two distinct rendered frames'})
        with self.assertRaises(EvidenceError):
            verify_operation(host, backend, sample)


if __name__ == '__main__':
    unittest.main()
