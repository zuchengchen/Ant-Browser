"""Calibration for the precise old-large-catalog exception and new resource oracle."""
import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from evidence import (EvidenceError, compare_runs, validate_v2_contract,
                      verify_build_artifacts, verify_go_suites, sha256)
from registration import (BASELINE_SHA256, acceptance_contract, catalog_sizes,
                          expected_metrics, expected_checks, expected_commands)
from resource_evidence import resource_inventory, summarize_resources, verify_resources


def role_report(role):
    policies = expected_metrics(role)
    report = {'role': role, 'acceptance': acceptance_contract(role),
              'run_id': role, 'source_hash': BASELINE_SHA256 if role == 'baseline' else 'candidate',
              'status': 'complete', 'diagnostic': False, 'expected_metrics': policies,
              'expected_correctness_checks': expected_checks(role),
              'expected_commands': expected_commands(),
              'metrics': {name: {**policy, 'samples': [100 if role == 'baseline' else 90] *
                          policy['minimum_samples']} for name, policy in policies.items()}}
    for name in ('fixture_hash', 'scenario_hash', 'toolchain', 'toolchain_version',
                 'browser_hash', 'browser_version', 'kernel_hashes', 'settings'):
        report[name] = 'same'
    if role == 'baseline':
        report['pressure_reference'] = {'path': 'pressure/reference.json', 'summary': 'tested separately'}
    return report


def raw_resources(runtime_root='/isolated/runtime', identity=('100', 1000)):
    points = []
    for index in range(31):
        processes = {identity[0]: {'start': identity[1], 'category': 'app', 'state': 'S',
                     'cpu': 1 + index / 100, 'rss': 8192, 'pss': 4096, 'rss_hwm': 12288, 'fds': 12},
                     '101': {'start': 1010, 'category': 'webview', 'state': 'R',
                     'cpu': 2 + index / 100, 'rss': 16384 + index * 4096,
                     'pss': 8192 + index * 4096, 'rss_hwm': 200000, 'fds': 10}}
        points.append({'monotonic_ns': (15 + index) * 1_000_000_000 + 1,
                       'finished_monotonic_ns': (15 + index) * 1_000_000_000 + 100,
                       'goroutines': 20 + index % 2, 'processes': processes})
    return {'schema': 1, 'runtime_root': runtime_root, 'started_identity': list(identity),
            'started_ns': 100, 'finished_ns': 45_000_000_101, 'warmup_started_ns': 1,
            'warmup_seconds': 15, 'sample_seconds': 30, 'interval_seconds': 1,
            'expected_browsers': [], 'points': points}


class MatrixTests(unittest.TestCase):
    def test_only_baseline_large_catalog_is_exempt(self):
        self.assertEqual(catalog_sizes('baseline'), [100, 1000])
        self.assertEqual(catalog_sizes('candidate'), [100, 1000, 10000])
        self.assertEqual(catalog_sizes(), [100, 1000, 10000])
        for role in ('baseline', 'candidate'):
            validate_v2_contract(role_report(role))
        difference = expected_metrics('candidate').keys() - expected_metrics('baseline').keys()
        self.assertEqual(len(difference), 51)
        self.assertTrue(all(name.startswith('catalog.10000.') for name in difference))
        self.assertTrue(all(not expected_metrics('candidate')[name]['improvement_eligible']
                            for name in difference))

    def test_wrong_goal_source_role_or_matrix_cannot_claim_exception(self):
        mutations = [lambda r: r['acceptance'].update(goal_sha256='wrong'),
                     lambda r: r.update(source_hash='wrong'),
                     lambda r: r.update(role='unknown'),
                     lambda r: r['acceptance']['baseline_pressure_exception'].update(complete_before_exemption=True),
                     lambda r: r['expected_metrics'].pop('catalog.1000.profile_filter'),
                     lambda r: r['metrics'].pop('catalog.100.profile_filter'),
                     lambda r: r['expected_correctness_checks'].remove('go_download')]
        for mutate in mutations:
            report = role_report('baseline'); mutate(report)
            with self.subTest(mutation=mutate), self.assertRaises(EvidenceError):
                validate_v2_contract(report)

    def test_candidate_failure_and_invalid_large_results_remain_failures(self):
        for change in ({'status': 'failed'}, {'diagnostic': True}, {'pressure_reference': {}}):
            report = role_report('candidate'); report.update(change)
            with self.subTest(change=change), self.assertRaises(EvidenceError):
                validate_v2_contract(report)
        for values in ([1] * 29, [0] * 30, [float('nan')] * 30, [True] * 30,
                       [1_800_001] * 30):
            report = role_report('candidate')
            report['metrics']['catalog.10000.profile_filter']['samples'] = values
            with self.subTest(values=values[:1]), self.assertRaises(EvidenceError):
                validate_v2_contract(report)

    def test_candidate_only_distributions_have_no_fabricated_comparison(self):
        before, after = role_report('baseline'), role_report('candidate')
        with patch('evidence.load_run', side_effect=[before, after]):
            result = compare_runs('before', 'after')
        self.assertEqual(result['status'], 'pass')
        self.assertEqual(len(result['candidate_only']), 51)
        for name, metric in result['candidate_only'].items():
            self.assertNotIn(name, result['comparisons'])
            self.assertNotIn(name, result['stable_user_flow_improvements'])
            self.assertFalse(metric['comparable_baseline'])
            self.assertFalse(any('ci' in key or 'fraction' in key or 'before_' in key for key in metric))
        for role_reports in ([after, before], [before, before]):
            with patch('evidence.load_run', side_effect=role_reports), self.assertRaises(EvidenceError):
                compare_runs('left', 'right')

    def test_small_catalog_regression_is_not_hidden_by_large_catalog(self):
        before, after = role_report('baseline'), role_report('candidate')
        name = 'catalog.100.profile_filter'
        after['metrics'][name]['samples'] = [110] * 30
        with patch('evidence.load_run', side_effect=[before, after]):
            result = compare_runs('before', 'after')
        self.assertEqual(result['status'], 'fail')
        self.assertIn(name, result['failures'])


class ResourceTests(unittest.TestCase):
    def test_display_exit_requires_owned_identity_and_normal_reclamation(self):
        from runtime import verify_display
        raw = {'started_ns': 100, 'finished_ns': 200, 'identity': ['123', 456],
               'display': ':99', 'returncode': 0, 'forced_shutdown': False, 'remaining_owned': []}
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'display-lifecycle.json'
            path.write_text(json.dumps(raw))
            self.assertEqual(verify_display(temporary), raw)
            for fields in ({'returncode': -9}, {'forced_shutdown': True},
                           {'remaining_owned': [['123', 456]]}, {'identity': ['123', 0]},
                           {'finished_ns': 99}, {'display': 'wrong'}):
                path.write_text(json.dumps({**raw, **fields}))
                with self.subTest(fields=fields), self.assertRaises(EvidenceError):
                    verify_display(temporary)

    def test_actual_windows_peaks_and_empty_categories_are_explicit(self):
        raw = raw_resources()
        result = summarize_resources(raw)
        self.assertAlmostEqual(result['app.cpu'], .3)
        self.assertEqual(result['webview.observed_rss_peak'], 139264)
        self.assertEqual(result['webview.rss_hwm_upper_bound'], 200000)
        self.assertEqual(result['browser.cpu'], 0)
        self.assertEqual(result['browser.processes'], 0)
        self.assertEqual(len(resource_inventory('baseline')), 40)
        self.assertEqual(len(resource_inventory('candidate')), 50)

    def test_missing_zero_nonfinite_truncated_or_wrong_process_rejected(self):
        mutations = [lambda r: r['points'].pop(),
                     lambda r: r.update(warmup_seconds=14),
                     lambda r: r['points'][0].update(monotonic_ns=14_000_000_001),
                     lambda r: r['points'][1].update(monotonic_ns=15_000_000_002),
                     lambda r: r['points'][0].update(goroutines=0),
                     lambda r: r['points'][0]['processes'].pop('101'),
                     lambda r: r['points'][0]['processes']['100'].update(state='Z'),
                     lambda r: r['points'][1]['processes']['100'].update(cpu=0),
                     lambda r: r['points'][0]['processes']['101'].update(pss=float('nan')),
                     lambda r: r['points'][0]['processes']['101'].pop('rss_hwm'),
                     lambda r: r.update(started_identity=['999', 1000]),
                     lambda r: r.update(finished_ns=1)]
        for mutate in mutations:
            raw = raw_resources(); mutate(raw)
            with self.subTest(mutation=mutate), self.assertRaises(EvidenceError):
                summarize_resources(raw)

    def test_longer_collection_window_does_not_invent_cpu_regression(self):
        raw = raw_resources()
        for index, point in enumerate(raw['points']):
            point['monotonic_ns'] += index * 100_000_000
            point['finished_monotonic_ns'] += index * 100_000_000
        raw['finished_ns'] += 3_000_000_000
        result = summarize_resources(raw)
        self.assertEqual(result['sampling_elapsed_seconds'], 33)
        self.assertAlmostEqual(result['app.raw_cpu'], .3)
        self.assertAlmostEqual(result['app.cpu'], .3 * 30 / 33)

    def test_started_browser_cannot_disappear_or_reuse_pid_during_warmup(self):
        raw = raw_resources(); raw['expected_browsers'] = [['200', 2000]]
        with self.assertRaisesRegex(EvidenceError, 'Started browser'):
            summarize_resources(raw)
        for point in raw['points']:
            point['processes']['200'] = {'start': 2000, 'category': 'browser', 'state': 'S',
                'cpu': 1, 'rss': 8192, 'pss': 4096, 'rss_hwm': 12288, 'fds': 5}
        self.assertEqual(summarize_resources(raw)['browser.processes'], 1)
        raw['points'][15]['processes']['200']['start'] = 2001
        with self.assertRaisesRegex(EvidenceError, 'Started browser'):
            summarize_resources(raw)

    def test_cached_resource_values_and_lifecycles_cannot_replace_raw(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); directory = root / 'c10000-0'; directory.mkdir()
            raw = raw_resources(str(directory / 'runtime'))
            resource = directory / 'resources.json'; resource.write_text(json.dumps(raw))
            lifecycle = [{'event': 'start', 'time_ns': 50, 'started_identity': raw['started_identity']},
                         {'event': 'close', 'time_ns': 46_000_000_000}]
            (directory / 'lifecycle.json').write_text(json.dumps(lifecycle))
            name = 'c10000-0/resources.json'; summary = summarize_resources(raw)
            metric = 'catalog.10000.resources.app.pss'
            report = {'role': 'candidate', 'resource_evidence': {name: summary},
                      'expected_metrics': {metric: {}}, 'metrics': {metric: {'samples': [summary['app.pss']]}}}
            with patch('resource_evidence.resource_inventory', return_value={name: 'catalog.10000.resources'}):
                self.assertEqual(verify_resources(root, report)['rounds'], 1)
                report['metrics'][metric]['samples'] = [0]
                with self.assertRaisesRegex(EvidenceError, 'raw samples'):
                    verify_resources(root, report)
                report['metrics'][metric]['samples'] = [summary['app.pss']]
                raw['started_identity'] = ['100', 2000]; resource.write_text(json.dumps(raw))
                with self.assertRaises(EvidenceError):
                    verify_resources(root, report)


class ProducerBindingTests(unittest.TestCase):
    def test_application_and_seed_must_be_current_sealed_amd64_elfs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            report = {'build_artifacts': {}, 'artifacts': [], 'commands': []}
            for name, relative in (('application', 'build/build/bin/ant-chrome'), ('seed', 'seed')):
                path = root / relative; path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b'\x7fELF\x02\x01' + bytes(12) + b'\x3e\x00' + b'calibration')
                record = {'path': relative, 'sha256': sha256(path), 'bytes': path.stat().st_size}
                report['build_artifacts'][name] = record
                report['artifacts'].append({'path': relative, 'sha256': record['sha256']})
                mtime = path.stat().st_mtime_ns
                report['commands'].append({'name': 'wails-build' if name == 'application' else 'seed-build',
                                           'started_ns': mtime - 100, 'finished_ns': mtime + 100})
            report['binary_sha256'] = report['build_artifacts']['application']['sha256']
            self.assertEqual(verify_build_artifacts(root, report), report['build_artifacts'])
            for mutation in ('missing_seal', 'wrong_path', 'wrong_hash', 'wrong_size', 'wrong_binary', 'stale'):
                bad = copy.deepcopy(report)
                if mutation == 'missing_seal': bad['artifacts'].pop()
                elif mutation == 'wrong_path': bad['build_artifacts']['seed']['path'] = 'other'
                elif mutation == 'wrong_hash': bad['build_artifacts']['seed']['sha256'] = 'wrong'
                elif mutation == 'wrong_size': bad['build_artifacts']['seed']['bytes'] = 0
                elif mutation == 'wrong_binary': bad['binary_sha256'] = 'wrong'
                else: bad['commands'][0]['started_ns'] = bad['commands'][0]['finished_ns'] + 1
                with self.subTest(mutation=mutation), self.assertRaises(EvidenceError):
                    verify_build_artifacts(root, bad)
            (root / 'seed').write_bytes(b'not an ELF file')
            with self.assertRaises(EvidenceError):
                verify_build_artifacts(root, report)

    def test_reported_go_metrics_and_kernel_hashes_must_match_raw_producers(self):
        from registration import go_registration, go_policy
        from test_evidence import go_events
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            report = {'commands': [], 'metrics': {}, 'go_evidence': {},
                      'kernel_hashes': {'xray': '1' * 64, 'sing-box': '2' * 64, 'mihomo': '3' * 64}}
            for name, package, test, registration in go_registration():
                evidence = {'positive_assertion': True}
                if name == 'proxy':
                    evidence.update(xray_sha256=report['kernel_hashes']['xray'],
                                    singbox_sha256=report['kernel_hashes']['sing-box'],
                                    mihomo_sha256=report['kernel_hashes']['mihomo'])
                raw = {'schema_version': 1, 'suite': registration['suite'], 'samples': 30,
                       'diagnostic': False, 'metric_units': registration['metrics'],
                       'metrics': {key: [5] * 30 for key in registration['metrics']}, 'evidence': evidence}
                if 'component' in registration: raw['component'] = registration['component']
                path = root / (name + '.stdout')
                path.write_text(''.join(json.dumps(x) + '\n' for x in go_events(raw, package, test)))
                if name == 'proxy':
                    proxy_path, proxy_report = path, copy.deepcopy(raw)
                    proxy_package, proxy_test = package, test
                report['commands'].append({'name': 'go-' + name, 'stdout': path.name, 'exit_code': 0,
                    'argv': ['go', 'test', '-count=1', '-json', '-tags', 'webkit2_41 linuxperf',
                             './' + package.removeprefix('ant-chrome/'), '-run', '^' + test + '$']})
                report['go_evidence'][name] = evidence
                for key, unit in registration['metrics'].items():
                    policy, multiplier = go_policy(name, key, unit)
                    report['metrics'][name + '.' + key] = {**policy, 'samples': [5 * multiplier] * 30}
            self.assertEqual(verify_go_suites(root, report)['suites'], 4)
            for mutation in ('wrong_argv', 'failed_producer', 'wrong_value', 'wrong_evidence', 'wrong_kernel'):
                bad = copy.deepcopy(report)
                if mutation == 'wrong_argv': bad['commands'][0]['argv'][-1] = '^WrongTest$'
                elif mutation == 'failed_producer': bad['commands'][0]['exit_code'] = 1
                elif mutation == 'wrong_value': next(iter(bad['metrics'].values()))['samples'][0] = 0
                elif mutation == 'wrong_evidence': bad['go_evidence']['proxy']['positive_assertion'] = False
                else: bad['kernel_hashes']['xray'] = '4' * 64
                with self.subTest(mutation=mutation), self.assertRaises(EvidenceError):
                    verify_go_suites(root, bad)

            original_stdout = proxy_path.read_text()
            try:
                for engine, field in (('xray', 'xray_sha256'), ('sing-box', 'singbox_sha256'), ('mihomo', 'mihomo_sha256')):
                    for mutation, value in (('both_missing', None), ('both_null', None),
                                            ('both_non_hash', 'not-a-sha256')):
                        bad, raw = copy.deepcopy(report), copy.deepcopy(proxy_report)
                        if mutation == 'both_missing':
                            raw['evidence'].pop(field)
                            bad['kernel_hashes'].pop(engine)
                        else:
                            raw['evidence'][field] = value
                            bad['kernel_hashes'][engine] = value
                        # Keep the raw/cache evidence equal so only the kernel
                        # identity guard can reject this paired invalid value.
                        bad['go_evidence']['proxy'] = copy.deepcopy(raw['evidence'])
                        proxy_path.write_text(''.join(json.dumps(x) + '\n'
                            for x in go_events(raw, proxy_package, proxy_test)))
                        with self.subTest(engine=engine, mutation=mutation), self.assertRaisesRegex(
                                EvidenceError, 'registered proxy kernel: ' + engine):
                            verify_go_suites(root, bad)
            finally:
                proxy_path.write_text(original_stdout)


if __name__ == '__main__':
    unittest.main()
