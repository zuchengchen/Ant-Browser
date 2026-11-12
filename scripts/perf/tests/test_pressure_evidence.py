"""Calibrate authentication separately from structured historical-failure checks.

The 2.9 MB fixture contains verified original structured evidence, no executable.
Semantic mutations enter after file authentication through mocked readers; they
do not weaken the public verifier or change its fixed trust anchor. A separately
recorded actual-bundle verification covers every original byte and copy helper.
"""
import copy
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import pressure_evidence as pe

FIXTURE_SHA256 = '2d6dd11c2bb5a03c019cba247887996fd9814f8901e636dbc878c980ea08b904'


def set_at(container, path, value):
    for key in path[:-1]:
        container = container[key]
    container[path[-1]] = value


def change(path, value):
    return lambda ref, doc, obs: set_at(doc, path, value)


def remove(path):
    def mutate(ref, doc, obs):
        value = doc
        for key in path[:-1]:
            value = value[key]
        del value[path[-1]]
    return mutate


def edit_native(stage, field, value):
    def mutate(ref, doc, obs):
        token = pe.RUN_ID + ':0:filter'
        for events in (doc['events'], doc['run']['native_events']):
            event = next(e for e in events if e['op_id'] == token and e['stage'] == stage)
            set_at(event, field, value)
    return mutate


REJECTIONS = {
    'run_candidate_source': change(('run', 'product_source_hash'), pe.REVIEWED_WORKSPACE_SHA256),
    'run_claims_success': change(('run', 'status'), 'complete'),
    'run_not_diagnostic': change(('run', 'diagnostic'), False),
    'run_wrong_identity': change(('run', 'run_id'), 'native-forged'),
    'catalog_100': change(('run', 'size'), 100),
    'catalog_1000': change(('run', 'size'), 1000),
    'catalog_numeric_string': change(('run', 'size'), '10000'),
    'wrong_pair_count': change(('run', 'pairs'), 3),
    'timeout_extended': change(('run', 'timeout_seconds'), 1801),
    'timeout_nan': change(('run', 'timeout_seconds'), float('nan')),
    'timeout_numeric_string': change(('run', 'timeout_seconds'), '1800'),
    'producer_exit_zero': change(('producer', 'exit_code'), 0),
    'producer_exit_signal': change(('producer', 'exit_code'), -2),
    'producer_exit_boolean': change(('producer', 'exit_code'), True),
    'producer_manual_interrupt': change(('producer', 'wrapper_error'), 'KeyboardInterrupt: cancelled'),
    'producer_interrupt_flag': change(('producer', 'interrupted'), True),
    'producer_wrong_argv': change(('producer', 'argv', 5), '1000'),
    'producer_changed_driver': change(('producer', 'inputs_after'), {}),
    'producer_bad_wrapper_hash': change(('producer', 'wrapper_sha256'), '0' * 64),
    'producer_false_stdout_hash': change(('producer', 'stdout_sha256'), '0' * 64),
    'producer_benign_pass_not_completion': change(('producer_stdout',), 'PASS: all good\n'),
    'producer_omitted_last_completion': change(('producer_stdout',), 'NATIVE READY 10000\n'),
    'producer_missing_finish': remove(('producer', 'finished_ns')),
    'runtime_input_removed': change(('run', 'inputs'), {}),
    'primary_error_missing': change(('run', 'errors'), []),
    'primary_error_benign_substring': change(('run', 'errors', 0, 'error'), 'harmless text: Native stage timeout without retry'),
    'primary_error_manual_interrupt': change(('run', 'errors', 0, 'error'), 'KeyboardInterrupt: user stop'),
    'traceback_manual_interrupt': change(('run', 'errors', 0, 'traceback'), 'KeyboardInterrupt\n'),
    'cleanup_error_hidden': change(('run', 'cleanup_errors'), []),
    'helper_zero_execution': change(('run', 'commands'), []),
    'helper_nonzero': change(('run', 'commands', 30, 'returncode'), 1),
    'helper_not_reaped': change(('run', 'commands', 30, 'reaped'), False),
    'helper_killed': change(('run', 'commands', 30, 'forced_kill'), True),
    'helper_wrong_failure_token': change(('run', 'commands', 30, 'token'), pe.RUN_ID + ':5:filter'),
    'helper_synthetic_window_input': change(('run', 'commands', 30, 'argv'), ['xdotool', 'key', '--window', '2097168', 'ctrl+v']),
    'helper_non_numeric_clock': change(('run', 'commands', 30, 'start', 'monotonic_ns'), '1800'),
    'clipboard_lost_before_deadline': change(('run', 'clipboard_pre_close', 'returncode'), -15),
    'clipboard_wrong_owner': change(('run', 'clipboard_pre_close', 'owner'), 0),
    'clipboard_not_released': change(('run', 'clipboard_close', 'owner_after'), 4194305),
    'display_forced': change(('run', 'display_close', 'forced_shutdown'), True),
    'events_missing': change(('events',), []),
    'events_last_truncated': lambda ref, doc, obs: doc['events'].pop(),
    'events_duplicate_sequence': change(('events', -1, 'sequence'), 65),
    'events_non_numeric_clock': change(('events', 0, 'backend_time_ns'), 'NaN'),
    'events_infinite_js_clock': edit_native('native_input', ('performance_now_ms',), float('inf')),
    'native_sequence_duplicate': edit_native('native_input', ('details', 'local_sequence'), 1),
    'native_untrusted': edit_native('native_beforeinput', ('details', 'is_trusted'), False),
    'native_wrong_input_type': edit_native('native_beforeinput', ('details', 'input_type'), 'insertText'),
    'native_wrong_focus': edit_native('native_input', ('details', 'input', 'active_target'), False),
    'native_wrong_run': edit_native('native_input', ('details', 'run_id'), 'native-other'),
    'native_wrong_unicode_paste': edit_native('native_paste', ('details', 'text'), '性能实例 00043'),
    'native_result_copy_missing': change(('run', 'native_events'), []),
    'operation_zero_execution': change(('run', 'operations'), []),
    'operation_wrong_order': change(('run', 'operations', 0, 'name'), 'restore'),
    'operation_false_ids': change(('run', 'operations', 0, 'ids'), ['perf-profile-00043']),
    'operation_restore_sampled_ids': change(('run', 'operations', 1, 'ids'), ['perf-profile-00000']),
    'operation_boolean_execution_count': change(('run', 'operations', 0, 'input_count'), True),
    'operation_non_numeric_latency': change(('run', 'operations', 0, 'input_to_render_ms'), 'fast'),
    'operation_nan_timing': change(('run', 'operations', 0, 'diagnostic_timing', 'host_command_start_to_beforeinput_ms'), float('nan')),
    'lifecycle_missing_close': lambda ref, doc, obs: doc['lifecycle'].pop(),
    'lifecycle_false_success': change(('lifecycle', 1, 'returncode'), 0),
    'lifecycle_forced_flag_hidden': change(('lifecycle', 1, 'forced_shutdown'), False),
    'lifecycle_live_remaining': change(('lifecycle', 1, 'remaining_live'), [['751490', 6003625]]),
    'lifecycle_zombie_remaining': change(('lifecycle', 1, 'remaining_zombies'), [['751560', 6003640]]),
    'lifecycle_wrong_start_identity': change(('lifecycle', 1, 'started_identity'), ['751490', 6003626]),
    'lifecycle_missing_forced_signal': change(('lifecycle', 1, 'signals'), []),
    'lifecycle_missing_errors': change(('lifecycle', 1, 'errors'), []),
    'parent_review_missing_identities': change(('parent_review', 'identities'), []),
    'parent_review_unreaped': change(('parent_review', 'identities', 0, 'original_identity_gone'), False),
    'parent_review_original_still_alive': change(('parent_review', 'identities', 0, 'current'), {'start': 6003580, 'state': 'S'}),
    'parent_review_scope_success': change(('parent_review', 'goal_pass'), True),
    'parent_review_missing_file_bindings': change(('parent_review', 'files'), {}),
    'parent_review_false_timeout': change(('parent_review', 'command_to_cleanup_seconds'), 1800),
    'parent_review_boolean_timeout': change(('parent_review', 'command_to_cleanup_seconds'), True),
    'parent_review_primary_hidden': change(('parent_review', 'primary_errors'), []),
    'parent_review_wrong_failure_stage': change(('parent_review', 'failure_stage'), 'native_complete'),
    'build_wrong_source': change(('build', 'product_source_hash'), pe.REVIEWED_WORKSPACE_SHA256),
    'build_wrong_toolchain': change(('build', 'toolchain'), 'go1.25.0'),
    'build_source_changed': change(('build', 'unexpected_postbuild_source_changes'), {'backend/app.go': 'changed'}),
    'build_missing_source_manifest': remove(('build_baseline-source-manifest.json',)),
    'build_bad_instrumentation': change(('build', 'allowed_prepare_transformations'), {}),
    'build_failed_producer': change(('build', 'commands', 0, 'exit_code'), 1),
    'build_wrong_binary': change(('build', 'binary_sha256'), '0' * 64),
    'build_wrong_seed': change(('build', 'seed_sha256'), '0' * 64),
    'build_seed_not_frozen': change(('build', 'seed_matches_frozen_source'), False),
    'build_non_elf': lambda ref, doc, obs: obs['binary'].update(header=b'not an ELF'),
    'approval_other_goal': change(('approval', 'sha256'), pe.OLD_GOAL_SHA256),
    'approval_save_only': change(('approval', 'execution_intent'), 'save_only'),
    'approval_not_exact': change(('approval', 'exact_readback'), False),
    'approval_wrong_answer': change(('approval', 'approved_response'), '2'),
    'resources_missing': change(('monitor',), []),
    'resources_wrong_process': change(('monitor', 0, 'owned', 0, 'pid'), '1'),
    'resources_bad_cpu': change(('monitor', 0, 'owned', 0, 'ticks'), 'high'),
    'resources_non_numeric_rss': change(('monitor', 0, 'owned', 0, 'memory', 'VmRSS'), 'NaN kB'),
    'resources_nonfinite_load': change(('monitor', 0, 'load_average', 0), float('inf')),
    'resources_host_memory_missing': change(('host_memory',), []),
    'resources_thread_snapshot_missing': change(('threads',), []),
}


class PressureCalibration(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = Path(__file__).with_name('pressure-calibration.json')
        raw = path.read_bytes()
        if hashlib.sha256(raw).hexdigest() != FIXTURE_SHA256:
            raise AssertionError('Original structured calibration evidence changed')
        cls.fixture = pe._decode(raw, 'calibration fixture')

    def payload(self):
        ref = copy.deepcopy(self.fixture['reference'])
        doc = copy.deepcopy(self.fixture['documents'])
        obs = {k: {'header': bytes.fromhex(v)} for k, v in self.fixture['elf_headers'].items()}
        return ref, doc, obs

    def verify_payload(self, ref, doc, obs, **kwargs):
        with mock.patch.object(pe, '_load_reference', return_value=(Path('authenticated-reference.json'), ref)), \
             mock.patch.object(pe, '_load_evidence', return_value=(doc, obs)):
            return pe.verify_pressure('authenticated-reference.json', pe.BASELINE_SOURCE_SHA256, pe.GOAL_SHA256, **kwargs)

    def test_original_structured_evidence_positive(self):
        result = self.verify_payload(*self.payload())
        self.assertEqual(result['status'], 'verified_failure_reference')
        self.assertEqual(result['failure']['completed_operations'], 6)
        self.assertEqual(result['failure']['registered_operations'], 12)
        self.assertEqual(result['failure']['observed_wait_seconds'], 1800.196318694)
        self.assertEqual(result['failure']['application_exit_code'], -9)
        self.assertFalse(result['goal_pass'])
        self.assertFalse(result['candidate_validation_pass'])
        self.assertFalse(result['user_flow_improvement_eligible'])
        self.assertFalse(result['scope']['whole_before_exempt'])
        self.assertTrue(result['other_baseline_checks_required'])
        self.assertEqual(result['numeric_comparison_metrics'], {})

    def test_historical_parent_workspace_is_not_running_product(self):
        ref, doc, obs = self.payload()
        self.assertEqual(doc['parent_review']['product_source_hash'], pe.REVIEWED_WORKSPACE_SHA256)
        self.assertEqual(doc['run']['product_source_hash'], pe.BASELINE_SOURCE_SHA256)
        self.assertEqual(self.verify_payload(ref, doc, obs)['baseline_source_sha256'], pe.BASELINE_SOURCE_SHA256)

    def test_benign_fail_error_text_not_new_failure(self):
        ref, doc, obs = self.payload()
        doc['run']['annotation'] = 'Benign FAIL/error/KeyboardInterrupt quoted in unrelated commentary'
        result = self.verify_payload(ref, doc, obs)
        self.assertEqual(result['failure']['failure_stage'], 'native_paste')

    def test_summary_deterministic_and_does_not_mutate_input(self):
        ref, doc, obs = self.payload()
        original = copy.deepcopy(doc)
        a, b = self.verify_payload(ref, doc, obs), self.verify_payload(ref, doc, obs)
        self.assertEqual(a, b)
        self.assertEqual(doc, original)

    def test_candidate_role_never_qualifies(self):
        with self.assertRaises(pe.PressureEvidenceError):
            self.verify_payload(*self.payload(), role='candidate')

    def test_other_role_never_qualifies(self):
        with self.assertRaises(pe.PressureEvidenceError):
            self.verify_payload(*self.payload(), role='before')

    def test_entire_before_catalog_alias_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            self.verify_payload(*self.payload(), catalog_size='all')

    def test_other_catalog_never_qualifies(self):
        with self.assertRaises(pe.PressureEvidenceError):
            self.verify_payload(*self.payload(), catalog_size=1000)

    def test_candidate_source_argument_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe.verify_pressure('unused', pe.REVIEWED_WORKSPACE_SHA256, pe.GOAL_SHA256)

    def test_other_goal_argument_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe.verify_pressure('unused', pe.BASELINE_SOURCE_SHA256, pe.OLD_GOAL_SHA256)

    def test_typo_goal_argument_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe.verify_pressure('unused', pe.BASELINE_SOURCE_SHA256, '9d3cb85cfbdd0d52a76f2b519a99b7b47c44edc71b54d8727d07a02d19ec216e3')

    def test_missing_goal_file_rejected(self):
        with tempfile.TemporaryDirectory() as root, self.assertRaises(pe.PressureEvidenceError):
            pe.verify_pressure('unused', pe.BASELINE_SOURCE_SHA256, Path(root) / 'missing.md')

    def test_invalid_goal_argument_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe.verify_pressure('unused', pe.BASELINE_SOURCE_SHA256, None)

    def test_real_reference_anchor_survives_relocation(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'renamed.json'
            path.write_text(self.fixture['reference_raw'])
            observed_path, ref = pe._load_reference(path)
            self.assertEqual(observed_path, path)
            self.assertEqual(ref, self.fixture['reference'])

    def test_fake_reference_with_recomputed_hashes_rejected(self):
        ref = copy.deepcopy(self.fixture['reference'])
        ref['files']['run']['sha256'] = hashlib.sha256(b'{"status":"failed"}\n').hexdigest()
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'reference.json'
            path.write_text(json.dumps(ref) + '\n')
            with self.assertRaisesRegex(pe.PressureEvidenceError, 'anchor'):
                pe._load_reference(path)

    def test_entire_before_scope_expansion_rejected(self):
        ref = copy.deepcopy(self.fixture['reference']); ref['scope']['whole_before_exempt'] = True
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'reference.json'; path.write_text(json.dumps(ref) + '\n')
            with self.assertRaises(pe.PressureEvidenceError):
                pe._load_reference(path)

    def test_missing_reference_rejected(self):
        with tempfile.TemporaryDirectory() as root, self.assertRaises(pe.PressureEvidenceError):
            pe._load_reference(Path(root) / 'missing.json')

    def test_truncated_reference_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'reference.json'; path.write_text(self.fixture['reference_raw'][:-10])
            with self.assertRaises(pe.PressureEvidenceError):
                pe._load_reference(path)

    def test_symlink_reference_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'actual.json'; path.write_text(self.fixture['reference_raw'])
            link = Path(root) / 'reference.json'; link.symlink_to(path)
            with self.assertRaises(pe.PressureEvidenceError):
                pe._load_reference(link)

    def test_missing_artifact_rejected(self):
        with tempfile.TemporaryDirectory() as root, self.assertRaises(pe.PressureEvidenceError):
            pe._load_evidence(Path(root) / 'reference.json', self.fixture['reference'])

    def test_altered_artifact_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            member = Path(root) / self.fixture['reference']['files']['goal']['path']
            member.parent.mkdir(parents=True); member.write_text('wrong goal\n')
            with self.assertRaisesRegex(pe.PressureEvidenceError, 'hash/size'):
                pe._load_evidence(Path(root) / 'reference.json', self.fixture['reference'])

    def test_symlink_member_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / 'outside'; target.write_text('outside')
            link = Path(root) / 'member'; link.symlink_to(target)
            with self.assertRaises(pe.PressureEvidenceError):
                pe._path(Path(root), 'member')

    def test_symlink_member_directory_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / 'outside'; target.mkdir()
            link = Path(root) / 'member'; link.symlink_to(target, target_is_directory=True)
            with self.assertRaises(pe.PressureEvidenceError):
                pe._path(Path(root), 'member/file')

    def test_absolute_member_path_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe._path(Path('/tmp'), '/etc/passwd')

    def test_parent_member_path_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe._path(Path('/tmp'), '../outside')

    def test_backslash_member_path_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe._path(Path('/tmp'), 'folder\\outside')

    def test_non_regular_file_rejected(self):
        with tempfile.TemporaryDirectory() as root, self.assertRaises(pe.PressureEvidenceError):
            pe._read_regular(Path(root))

    def test_fifo_rejected_without_waiting_for_writer(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'fake-evidence'; os.mkfifo(path)
            with self.assertRaises(pe.PressureEvidenceError):
                pe._read_regular(path)

    def test_json_missing_final_newline_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe._decode(b'{"status":"failed"}', 'truncated')

    def test_json_duplicate_key_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe._decode(b'{"size":10000,"size":100}\n', 'duplicate')

    def test_json_nan_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe._decode(b'{"timeout":NaN}\n', 'nan')

    def test_json_overflow_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe._decode(b'{"timeout":1e999}\n', 'overflow')

    def test_jsonl_truncated_record_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe._decode(b'{"sequence":1}\n{"sequence":\n', 'journal', True)

    def test_jsonl_blank_record_rejected(self):
        with self.assertRaises(pe.PressureEvidenceError):
            pe._decode(b'{"sequence":1}\n\n', 'journal', True)

    def test_json_benign_failure_words_are_data(self):
        self.assertEqual(pe._decode(b'{"message":"FAIL error NaN"}\n', 'benign'), {'message': 'FAIL error NaN'})

    def test_no_numeric_baseline_substitution(self):
        result = self.verify_payload(*self.payload())
        self.assertEqual(result['missing_baseline_metrics'], 'unavailable_not_zero')
        self.assertEqual(result['numeric_comparison_metrics'], {})
        self.assertFalse(result['failure']['resource_observations']['formal_resource_samples'])
        self.assertFalse(result['failure']['resource_observations']['pss_available'])


def semantic_test(mutator):
    def test(self):
        ref, doc, obs = self.payload()
        mutator(ref, doc, obs)
        with self.assertRaises(pe.PressureEvidenceError):
            self.verify_payload(ref, doc, obs)
    return test


for _name, _mutator in REJECTIONS.items():
    setattr(PressureCalibration, 'test_reject_' + _name, semantic_test(_mutator))


if __name__ == '__main__':
    unittest.main()
