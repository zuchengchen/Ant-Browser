"""Reparse actual calibration receipts, then reject targeted corruptions."""
import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from evidence import EvidenceError
from gui_evidence import (_records, verify_operation, verify_gui,
                          verify_catalog_observations, verify_instance_actions)


class GuiReceiptTests(unittest.TestCase):
    def setUp(self):
        self.fixture = json.loads(Path(__file__).with_name('gui-receipt-calibration-v2.json').read_text())

    def test_historical_rendered_receipts_cannot_be_reused_as_v2(self):
        legacy = json.loads(Path(__file__).with_name('gui-receipt-calibration.json').read_text())
        checked = 0
        for sample in legacy['samples']:
            start = next(item for item in legacy['host']
                         if item.get('token') == sample['token'] and item.get('event') == 'start')
            if start.get('require_render') or start.get('expected_phases'):
                with self.subTest(sample=sample), self.assertRaisesRegex(EvidenceError, 'v2 rendered-frame proof'):
                    verify_operation(legacy['host'], legacy['backend'], sample)
                checked += 1
        self.assertEqual(checked, 4)

    def test_real_success_keyword_phases_and_launch_origin(self):
        for sample in self.fixture['samples']:
            value = verify_operation(self.fixture['host'], self.fixture['backend'], sample)
            self.assertGreater(value, 0)
            self.assertLess(value, 1_800_000)

    def test_missing_duplicate_failed_or_forged_terminal_cannot_pass(self):
        sample = self.fixture['samples'][1]
        token = sample['token']
        for mutation in ('missing_start', 'missing_complete', 'failed', 'duplicate_complete',
                         'wrong_code', 'wrong_clock', 'wrong_terminal_hash', 'wrong_sequence',
                         'failed_backend', 'missing_backend_request', 'missing_frames'):
            host, backend = copy.deepcopy((self.fixture['host'], self.fixture['backend']))
            start = next(x for x in host if x.get('token') == token and x['event'] == 'start')
            complete = next(x for x in host if x.get('token') == token and x['event'] == 'complete')
            request = next(x for x in backend if x.get('token') == token and x['event'] == 'request')
            final = next(x['payload'] for x in backend if x.get('token') == token and
                         x['event'] == 'message' and x['payload'].get('kind') == 'result')
            if mutation == 'missing_start': host.remove(start)
            elif mutation == 'missing_complete': host.remove(complete)
            elif mutation == 'failed': host.append({**complete, 'event': 'failed'})
            elif mutation == 'duplicate_complete': host.append(complete)
            elif mutation == 'wrong_code': request['code_sha256'] = 'wrong'
            elif mutation == 'wrong_clock': complete['elapsed_ms'] = 0
            elif mutation == 'wrong_terminal_hash': complete['value_sha256'] = 'wrong'
            elif mutation == 'wrong_sequence': final['sequence'] = 9
            elif mutation == 'failed_backend': final['ok'] = False
            elif mutation == 'missing_backend_request': backend.remove(request)
            elif mutation == 'missing_frames':
                final['value']['render']['frame_count'] = 1
                from evidence import digest_object
                complete['value_sha256'] = digest_object(final['value'])
            with self.subTest(mutation=mutation), self.assertRaises(EvidenceError):
                verify_operation(host, backend, sample)

    def test_phase_receipts_cannot_be_missing_or_measure_from_another_origin(self):
        sample = next(x for x in self.fixture['samples'] if x.get('phase'))
        for mutation in ('missing_phase', 'wrong_origin', 'wrong_value', 'unknown_phase'):
            host, backend = copy.deepcopy((self.fixture['host'], self.fixture['backend']))
            phase = next(x for x in host if x.get('token') == sample['token'] and
                         x['event'] == 'phase' and x['phase'] == sample['phase'])
            if mutation == 'missing_phase': host.remove(phase)
            elif mutation == 'wrong_origin': phase['elapsed_ms'] = .001
            elif mutation == 'wrong_value': phase['value']['keyword'] = 'different'
            elif mutation == 'unknown_phase': sample = {**sample, 'phase': 'different'}
            with self.subTest(mutation=mutation), self.assertRaises(EvidenceError):
                verify_operation(host, backend, sample)

    def test_launch_origin_missing_or_later_than_request_is_rejected(self):
        sample = next(x for x in self.fixture['samples'] if x.get('application_ready'))
        for mutation in ('missing', 'late'):
            host = copy.deepcopy(self.fixture['host'])
            ready = next(x for x in host if x.get('token') == sample['token'] and x['event'] == 'application_ready')
            if mutation == 'missing': host.remove(ready)
            else: ready['application_started_monotonic_ns'] = ready['received_monotonic_ns']
            with self.subTest(mutation=mutation), self.assertRaises(EvidenceError):
                verify_operation(host, self.fixture['backend'], sample)

    def test_truncated_zero_invalid_and_nonfinite_journals_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'operations.log'
            for content in ('', '{}', '{"token":\n', '[]\n', '{"n":NaN}\n', '{"n":1,"n":2}\n'):
                path.write_text(content)
                with self.subTest(content=content), self.assertRaises(EvidenceError):
                    _records(path)
            path.write_text('{"message":"FAIL and error are ordinary data"}\n')
            self.assertEqual(len(_records(path)), 1)

    def test_actual_metric_scale_round_and_business_bindings(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            app = root / 'c100-0'
            (app / 'runtime').mkdir(parents=True)
            for records, path in ((self.fixture['host'], app / 'operations.log'),
                    (self.fixture['backend'], app / 'runtime/linuxperf-gui-events.jsonl')):
                path.write_text(''.join(json.dumps(x) + '\n' for x in records))
            lifecycle = app / 'lifecycle.json'
            lifecycle.write_text(json.dumps([self.fixture['first_lifecycle']]))

            report = {'expected_metrics': dict.fromkeys(self.fixture['metric_samples']),
                      'metrics': {}, 'gui_samples': {}}
            for name, sample in self.fixture['metric_samples'].items():
                report['gui_samples'][name] = [sample]
                report['metrics'][name] = {'samples': [verify_operation(
                    self.fixture['host'], self.fixture['backend'], sample)]}
            self.assertEqual(verify_gui(root, report)['metrics'], 8)
            for mutation in ('wrong_scale', 'wrong_round', 'wrong_initial_round',
                             'swap_apis', 'swap_filter_restore', 'restart_pid', 'restart_origin'):
                bad = copy.deepcopy(report)
                first = copy.deepcopy(self.fixture['first_lifecycle'])
                if mutation in ('wrong_scale', 'wrong_round', 'wrong_initial_round'):
                    name = 'catalog.100.initial_render' if mutation == 'wrong_initial_round' else 'catalog.100.profile_filter'
                    target = 'c1000-0' if mutation == 'wrong_scale' else 'c100-1'
                    alternate = root / target
                    alternate.mkdir(exist_ok=True)
                    (alternate / 'operations.log').write_text((app / 'operations.log').read_text())
                    bad['gui_samples'][name][0]['journal'] = target + '/operations.log'
                elif mutation in ('swap_apis', 'swap_filter_restore'):
                    a, b = ('profiles_api', 'groups_api') if mutation == 'swap_apis' else ('profile_filter', 'profile_restore')
                    a, b = 'catalog.100.' + a, 'catalog.100.' + b
                    bad['gui_samples'][a], bad['gui_samples'][b] = bad['gui_samples'][b], bad['gui_samples'][a]
                    bad['metrics'][a], bad['metrics'][b] = bad['metrics'][b], bad['metrics'][a]
                elif mutation == 'restart_pid': first['pid'] += 1
                else: first['host_started_monotonic_ns'] -= 1
                lifecycle.write_text(json.dumps([first]))
                with self.subTest(mutation=mutation), self.assertRaises(EvidenceError):
                    verify_gui(root, bad)
            lifecycle.write_text(json.dumps([self.fixture['first_lifecycle']]))

    def test_real_backend_event_refresh_and_wrong_identity_rejection(self):
        from unittest.mock import patch
        events = self.fixture['event_observations']
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); app = root / 'c100-0'
            (app / 'runtime').mkdir(parents=True)
            for records, path in ((self.fixture['host'], app / 'operations.log'),
                    (self.fixture['backend'], app / 'runtime/linuxperf-gui-events.jsonl')):
                path.write_text(''.join(json.dumps(x) + '\n' for x in records))
            event_path = app / 'event-refresh.json'
            event_path.write_text(json.dumps(events))
            with patch('registration.catalog_sizes', return_value=[100]):
                self.assertEqual(verify_catalog_observations(root, {'role': 'baseline'})['observations'], 2)
                for mutation in ('missing', 'wrong_identity', 'swapped', 'wrong_value', 'reused'):
                    bad = copy.deepcopy(events)
                    if mutation == 'missing': del bad['failure']
                    elif mutation == 'wrong_identity': bad['success']['value']['proxyId'] = 'wrong'
                    elif mutation == 'swapped': bad['success'], bad['failure'] = bad['failure'], bad['success']
                    elif mutation == 'wrong_value': bad['success']['value']['after'] = 'forged'
                    else: bad['failure'] = bad['success']
                    event_path.write_text(json.dumps(bad))
                    with self.subTest(mutation=mutation), self.assertRaises(EvidenceError):
                        verify_catalog_observations(root, {'role': 'baseline'})

    def test_real_instance_cdp_timing_and_wrong_or_missing_action_rejected(self):
        from unittest.mock import patch
        import registration
        fixture = json.loads(Path(__file__).with_name('instance-receipt-calibration-v2.json').read_text())
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name, content in fixture['files'].items():
                path = root / name; path.parent.mkdir(parents=True, exist_ok=True); path.write_text(content)
            report = {'metrics': fixture['metrics']}
            action_path = root / 'r1-0/instance-actions.json'
            original = json.loads(action_path.read_text())
            with patch.dict(registration.SCENARIOS, {'runtime_counts': [1], 'independent_rounds': 1}):
                self.assertEqual(verify_instance_actions(root, report), {'rounds': 1, 'real_actions': 2, 'metrics': 2})
                for mutation in ('missing', 'reused', 'wrong_profile', 'wrong_content', 'wrong_clock', 'wrong_metric'):
                    actions, bad = copy.deepcopy((original, report))
                    if mutation == 'missing': actions.pop()
                    elif mutation == 'reused': actions[1]['timed'] = actions[0]['timed']
                    elif mutation == 'wrong_profile': actions[0]['profile_id'] = 'perf-profile-99999'
                    elif mutation == 'wrong_content': actions[0]['content'] = 'wrong'
                    elif mutation == 'wrong_clock': actions[0]['page_ready_monotonic_ns'] -= 1000000
                    else: bad['metrics']['runtime.1.stop']['samples'] = [0]
                    action_path.write_text(json.dumps(actions))
                    with self.subTest(mutation=mutation), self.assertRaises(EvidenceError):
                        verify_instance_actions(root, bad)


if __name__ == '__main__':
    unittest.main()
