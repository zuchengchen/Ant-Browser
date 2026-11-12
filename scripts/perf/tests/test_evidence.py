"""Calibration cases for acceptance evidence; no product/performance workloads."""
import copy
import json
import os
from pathlib import Path
import sys
import tempfile
import time
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from evidence import (EvidenceError, check_go_json, collect_go_reports, compare_runs,
                      compare_samples, digest_object, load_run, sha256, source_manifest,
                      validate_metric)


PACKAGE = 'example/internal/fixture'
TEST = 'TestLinuxPerfFixture'
REGISTRATION = {(PACKAGE, TEST): {'suite': 'fixture', 'component': 'sample',
                                'metrics': {'latency': 'ms'}}}
POLICY = {'minimum_samples': 30, 'unit': 'ms', 'core': True,
          'improvement_eligible': True, 'absolute_noise': 0}


def payload(samples=30, diagnostic=False):
    return {'schema_version': 1, 'suite': 'fixture', 'component': 'sample',
            'samples': samples, 'diagnostic': diagnostic,
            'metrics': {'latency': [1.0] * samples}, 'metric_units': {'latency': 'ms'},
            'evidence': {'hash_match': True, 'resources_closed': True}}


def event(action, test=None, package=PACKAGE, **fields):
    result = {'Action': action, 'Package': package, **fields}
    if test is not None:
        result['Test'] = test
    return result


def go_events(report=None, package=PACKAGE, test=TEST, chunk_size=None):
    output = '    linux_perf_test.go:42: LINUXPERF_JSON:' + json.dumps(report or payload()) + '\n'
    chunks = [output] if chunk_size is None else [output[i:i+chunk_size] for i in range(0, len(output), chunk_size)]
    return [event('start', package=package), event('run', test, package),
            event('output', test, package, Output='benign FAIL test fixture; 0 errors\n'),
            *(event('output', test, package, Output=chunk) for chunk in chunks),
            event('pass', test, package), event('output', package=package, Output='PASS\n'),
            event('pass', package=package)]


class GoEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.path = Path(self.temporary.name) / 'go.stdout'

    def write(self, events):
        self.path.write_text(''.join(json.dumps(row) + '\n' for row in events))
        return self.path

    def collect(self, events, **options):
        return collect_go_reports(self.write(events), REGISTRATION, 0, **options)

    def test_real_success_with_benign_fail_words(self):
        result = check_go_json(self.write(go_events()), [TEST], 0)
        self.assertEqual(result['passed'], [TEST])
        self.assertEqual(self.collect(go_events()), [payload()])

    def test_output_fragments_are_joined_before_report_json_parsing(self):
        for size in (1, 7, 49, 1024):
            with self.subTest(size=size):
                self.assertEqual(self.collect(go_events(chunk_size=size)), [payload()])

    def test_interleaved_packages_with_same_test_name_stay_separate(self):
        second = payload()
        second['metrics']['latency'] = [2] * 30
        left = go_events(chunk_size=7)
        right = go_events(second, package='example/other', chunk_size=7)
        events = []
        for index in range(max(len(left), len(right))):
            if index < len(left):
                events.append(left[index])
            if index < len(right):
                events.append(right[index])
        registration = {**REGISTRATION, ('example/other', TEST): REGISTRATION[(PACKAGE, TEST)]}
        path = self.write(events)
        self.assertEqual(collect_go_reports(path, registration, 0), [payload(), second])
        check_go_json(path, list(registration), 0)
        with self.assertRaisesRegex(EvidenceError, 'ambiguous'):
            check_go_json(path, [TEST], 0)

    def test_real_fail_actions_and_nonzero_exit_are_failures(self):
        for failed_index in (-1, -3):
            with self.subTest(failed_index=failed_index):
                events = go_events()
                events[failed_index]['Action'] = 'fail'
                with self.assertRaises(EvidenceError):
                    self.collect(events)
        for code in (1, -9, True):
            with self.subTest(exit_code=code), self.assertRaises(EvidenceError):
                check_go_json(self.write(go_events()), [TEST], code)

    def test_zero_tests_or_unregistered_names_cannot_prove_success(self):
        events = [event('start'), event('output', Output='testing: warning: no tests to run\n'), event('pass')]
        for required in ([TEST], [], ['OtherTest']):
            with self.subTest(required=required), self.assertRaises(EvidenceError):
                check_go_json(self.write(events), required, 0)
        with self.assertRaises(EvidenceError):
            check_go_json(self.write(go_events()), ['OtherTest'], 0)
        with self.assertRaises(EvidenceError):
            collect_go_reports(self.write(go_events()), {TEST: REGISTRATION[(PACKAGE, TEST)]}, 0)

    def test_unfinished_package_test_and_repeat_are_rejected(self):
        for events in (go_events()[:-1], go_events()[:-3] + [event('pass')],
                       go_events()[:-1] + [event('run', TEST), event('pass')]):
            with self.subTest(events=events[-2:]), self.assertRaises(EvidenceError):
                self.collect(events)

    def test_required_skipped_test_or_subtest_is_rejected(self):
        events = go_events()
        events[-3]['Action'] = 'skip'
        with self.assertRaises(EvidenceError):
            self.collect(events)
        events = go_events()
        events[2:2] = [event('run', TEST+'/needed'), event('skip', TEST+'/needed')]
        with self.assertRaises(EvidenceError):
            self.collect(events)

    def test_unstarted_pass_and_events_after_package_end_are_rejected(self):
        for events in ([event('start'), event('pass', TEST), event('pass')],
                       go_events() + [event('output', Output='trailing\n')]):
            with self.subTest(events=events[-2:]), self.assertRaises(EvidenceError):
                self.collect(events)

    def test_subtest_failure_and_unfinished_subtest_are_rejected(self):
        for action in ('fail', None):
            events = go_events()
            extra = [event('run', TEST+'/child')]
            if action:
                extra.append(event(action, TEST+'/child'))
            events[2:2] = extra
            with self.subTest(action=action), self.assertRaises(EvidenceError):
                self.collect(events)

    def test_parallel_subtest_lifecycle_and_build_diagnostics_are_valid(self):
        events = go_events()
        events[2:2] = [event('run', TEST+'/parallel'), event('pause', TEST+'/parallel'),
                       event('cont', TEST+'/parallel'), event('pass', TEST+'/parallel')]
        events.insert(0, {'Action': 'build-output', 'ImportPath': PACKAGE, 'Output': '0 errors\n'})
        self.assertEqual(self.collect(events), [payload()])
        events[0]['Action'] = 'build-fail'
        with self.assertRaises(EvidenceError):
            self.collect(events)

    def test_missing_empty_invalid_and_truncated_go_artifacts(self):
        for data in ('', '{"Action":', '[]\n', '{"Action":"start","Action":"pass"}\n'):
            self.path.write_text(data)
            with self.subTest(data=data), self.assertRaises(EvidenceError):
                check_go_json(self.path, [TEST], 0)
        self.path.unlink()
        with self.assertRaises(EvidenceError):
            check_go_json(self.path, [TEST], 0)
        self.write(go_events())
        self.path.write_text(self.path.read_text().rstrip('\n'))
        with self.assertRaises(EvidenceError):
            check_go_json(self.path, [TEST], 0)

    def test_missing_duplicate_or_truncated_report(self):
        for output in ('ordinary successful log\n', 'LINUXPERF_JSON:{\n',
                       'LINUXPERF_JSON:' + json.dumps(payload()),
                       ('LINUXPERF_JSON:' + json.dumps(payload()) + '\n') * 2):
            events = go_events()
            events[3]['Output'] = output
            with self.subTest(output=output[:40]), self.assertRaises(EvidenceError):
                self.collect(events)

    def test_marker_in_unrelated_prose_does_not_count(self):
        events = go_events()
        events[3]['Output'] = 'printed source containing LINUXPERF_JSON:' + json.dumps(payload()) + '\n'
        with self.assertRaises(EvidenceError):
            self.collect(events)

    def test_report_suite_component_schema_and_diagnostic_must_match(self):
        for field, value in (('suite', 'different'), ('component', 'different'),
                             ('schema_version', True), ('schema_version', 2),
                             ('diagnostic', True), ('diagnostic', 'false'), ('evidence', {})):
            report = payload()
            report[field] = value
            with self.subTest(field=field, value=value), self.assertRaises(EvidenceError):
                self.collect(go_events(report))
        report = payload(1, True)
        self.assertEqual(self.collect(go_events(report), expected_samples=1, allow_diagnostic=True), [report])
        with self.assertRaises(EvidenceError):
            self.collect(go_events(report), expected_samples=1)

    def test_report_metric_units_names_samples_and_finiteness(self):
        reports = []
        for count in (0, 29, 31, True):
            report = payload()
            report['samples'] = count
            reports.append(report)
        for values in ([1] * 29, [1] * 31, [float('nan')] * 30, [float('inf')] * 30,
                       [-1] * 30, [True] * 30, ['1'] * 30):
            report = payload()
            report['metrics']['latency'] = values
            reports.append(report)
        for field, value in (('metrics', {}), ('metrics', {'other': [1] * 30}),
                             ('metric_units', {'latency': 'seconds'}),
                             ('metric_units', {'latency': 'ms', 'extra': 'ms'})):
            report = payload()
            report[field] = value
            reports.append(report)
        for report in reports:
            with self.subTest(report=report), self.assertRaises(EvidenceError):
                self.collect(go_events(report))


class RunEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.directory, self.report = self.run_fixture('before', 100)

    def run_fixture(self, name, value):
        directory = self.base / name
        directory.mkdir()
        root = self.base / (name + '-source')
        root.mkdir()
        (root / 'main.go').write_text('package main // ' + name + '\n')
        start = time.time_ns() - 1_000_000_000
        stdout = directory / 'seed-build.stdout'
        stderr = directory / 'seed-build.stderr'
        gui_log = directory / 'xvfb.log'
        stdout.write_text('')
        stderr.write_text('')
        gui_log.write_text('')
        result_file = directory / 'gui.json'
        result_file.write_text('{"assertion":true}\n')
        manifest = source_manifest(root)
        report = {'schema': 1, 'run_id': name, 'root': str(root), 'output': str(directory),
                  'started_ns': start, 'finished_ns': time.time_ns() + 1_000_000,
                  'status': 'complete', 'diagnostic': False,
                  'source_manifest': manifest, 'source_hash': digest_object(manifest),
                  'fixture_hash': 'fixture-hash', 'scenario_hash': 'scenario-hash',
                  'toolchain': 'go1.24.13', 'toolchain_version': 'go version go1.24.13 linux/amd64',
                  'browser_hash': 'browser-hash', 'browser_version': 'Chromium fixture',
                  'kernel_hashes': {'xray': 'xray-hash', 'sing-box': 'sing-hash', 'mihomo': 'mihomo-hash'},
                  'settings': {'size': 100, 'samples': 30},
                  'expected_metrics': {'latency': dict(POLICY)},
                  'metrics': {'latency': {'samples': [value] * 30, **POLICY}},
                  'expected_commands': ['seed-build'],
                  'commands': [{'name': 'seed-build', 'argv': ['go', 'build', './fixture'],
                                'cwd': str(root), 'exit_code': 0,
                                'started_ns': start + 1, 'finished_ns': time.time_ns(),
                                'stdout': stdout.name, 'stderr': stderr.name}],
                  'expected_correctness_checks': ['result_hash_and_cleanup'],
                  'correctness_checks': ['result_hash_and_cleanup'],
                  'artifacts': [{'path': path.name, 'sha256': sha256(path),
                                 'allow_empty': path != result_file}
                                for path in (stdout, stderr, gui_log, result_file)]}
        self.seal(directory, report)
        return directory, report

    def seal(self, directory=None, report=None):
        directory = self.directory if directory is None else directory
        report = self.report if report is None else report
        (directory / 'results.json').write_text(json.dumps(report))
        (directory / 'complete.json').write_text(json.dumps({'run_id': report.get('run_id'),
            'results_sha256': sha256(directory / 'results.json')}))

    def fails(self):
        self.seal()
        with self.assertRaises(EvidenceError):
            load_run(self.directory)

    def test_complete_run_accepts_explicit_empty_build_stdout_stderr_and_xvfb_log(self):
        self.assertEqual(load_run(self.directory)['run_id'], 'before')

    def test_missing_truncated_or_wrong_seal_and_report(self):
        for filename, value in (('results.json', '{'), ('complete.json', '{'),
                                ('complete.json', '{}'), ('results.json', '[]'),
                                ('complete.json', '[]')):
            with self.subTest(filename=filename, value=value):
                self.seal()
                (self.directory / filename).write_text(value)
                with self.assertRaises(EvidenceError):
                    load_run(self.directory)
        for filename in ('results.json', 'complete.json'):
            self.seal()
            (self.directory / filename).unlink()
            with self.subTest(missing=filename), self.assertRaises(EvidenceError):
                load_run(self.directory)

    def test_missing_truncated_stale_and_post_run_artifact(self):
        path = self.directory / 'gui.json'
        original = path.read_bytes()
        original_time = path.stat().st_mtime_ns
        for modification in ('missing', 'truncated', 'stale', 'late'):
            with self.subTest(modification=modification):
                path.write_bytes(original)
                os.utime(path, ns=(original_time, original_time))
                if modification == 'missing':
                    path.unlink()
                elif modification == 'truncated':
                    path.write_bytes(original[:3])
                else:
                    stamp = self.report['started_ns'] - 1 if modification == 'stale' else self.report['finished_ns'] + 1
                    os.utime(path, ns=(stamp, stamp))
                with self.assertRaises(EvidenceError):
                    load_run(self.directory)

    def test_empty_data_cannot_be_authorized_as_success(self):
        path = self.directory / 'gui.json'
        path.write_text('')
        record = self.report['artifacts'][-1]
        record.update(sha256=sha256(path), allow_empty=True)
        self.fails()

    def test_empty_go_test_stdout_is_rejected(self):
        self.report['commands'][0]['argv'] = ['go', 'test', '-json', './fixture']
        self.fails()

    def test_empty_log_requires_explicit_allow_empty(self):
        self.report['artifacts'][0].pop('allow_empty')
        self.fails()

    def test_artifact_path_traversal_absolute_and_symlink_escape(self):
        original = copy.deepcopy(self.report)
        outside = self.base / 'outside.log'
        outside.write_text('positive evidence\n')
        (self.directory / 'escape.log').symlink_to(outside)
        for name in ('../outside.log', str(outside), 'escape.log', './xvfb.log', 'sub/../xvfb.log'):
            self.report = copy.deepcopy(original)
            self.report['artifacts'].append({'path': name, 'sha256': sha256(outside)})
            with self.subTest(name=name):
                self.fails()

    def test_duplicate_artifact_and_missing_command_logs(self):
        original = copy.deepcopy(self.report)
        self.report['artifacts'].append(self.report['artifacts'][0].copy())
        self.fails()
        self.report = original
        self.report['artifacts'].pop(1)
        self.fails()

    def test_failed_missing_duplicate_or_unfinished_commands(self):
        original = copy.deepcopy(self.report)
        for change in ('exit', 'missing', 'duplicate', 'unfinished', 'outside', 'log_reused', 'argv', 'cwd'):
            self.report = copy.deepcopy(original)
            command = self.report['commands'][0]
            if change == 'exit':
                command['exit_code'] = 2
            elif change == 'missing':
                self.report['commands'] = []
            elif change == 'duplicate':
                self.report['commands'].append(command.copy())
            elif change == 'unfinished':
                command.pop('finished_ns')
            elif change == 'outside':
                command['finished_ns'] = self.report['finished_ns'] + 1
            elif change == 'log_reused':
                command['stderr'] = command['stdout']
            elif change == 'argv':
                command['argv'] = []
            elif change == 'cwd':
                command['cwd'] = 'relative/path'
            with self.subTest(change=change):
                self.fails()

    def test_correctness_checks_require_exact_unique_registered_ids(self):
        original = copy.deepcopy(self.report)
        for field in ('expected_correctness_checks', 'correctness_checks'):
            for value in ([], ['other'], ['result_hash_and_cleanup'] * 2, [True]):
                self.report = copy.deepcopy(original)
                self.report[field] = value
                with self.subTest(field=field, value=value):
                    self.fails()

    def test_metric_coverage_and_policy_require_preregistration(self):
        original = copy.deepcopy(self.report)
        for change in ('no_registry', 'no_metric', 'extra_metric', 'changed_policy', 'incomplete_policy'):
            self.report = copy.deepcopy(original)
            if change == 'no_registry':
                self.report.pop('expected_metrics')
            elif change == 'no_metric':
                self.report['metrics'] = {}
            elif change == 'extra_metric':
                self.report['metrics']['surprise'] = copy.deepcopy(self.report['metrics']['latency'])
            elif change == 'changed_policy':
                self.report['metrics']['latency']['core'] = False
            else:
                self.report['expected_metrics']['latency'].pop('absolute_noise')
            with self.subTest(change=change):
                self.fails()

    def test_incomplete_diagnostic_and_invalid_run_timestamps(self):
        original = copy.deepcopy(self.report)
        for key, value in (('status', 'failed'), ('status', 'running'), ('diagnostic', True),
                           ('diagnostic', 0), ('finished_ns', self.report['started_ns']),
                           ('started_ns', False), ('run_id', ''), ('schema', True)):
            self.report = copy.deepcopy(original)
            self.report[key] = value
            with self.subTest(key=key, value=value):
                self.fails()

    def test_changed_source_is_rejected(self):
        (Path(self.report['root']) / 'main.go').write_text('package changed\n')
        with self.assertRaises(EvidenceError):
            load_run(self.directory)

    def test_sample_count_type_unit_classification_and_noise_are_strict(self):
        for key, value in (('samples', [1] * 29), ('samples', [True] * 30),
                           ('samples', [float('nan')] * 30), ('samples', [-1] * 30),
                           ('minimum_samples', 10.0), ('unit', 'ns/op'), ('core', 1),
                           ('improvement_eligible', 'yes'), ('absolute_noise', True),
                           ('absolute_noise', float('inf')), ('samples', [10 ** 400] * 30),
                           ('absolute_noise', 10 ** 400)):
            metric = {'samples': [1] * 30, **POLICY, key: value}
            with self.subTest(key=key, value=value), self.assertRaises(EvidenceError):
                validate_metric('calibration', metric)

    def test_comparison_success_and_real_regression(self):
        after, report = self.run_fixture('after', 90)
        result = compare_runs(self.directory, after)
        self.assertEqual(result['status'], 'pass')
        self.assertEqual(result['stable_user_flow_improvements'], ['latency'])
        report['metrics']['latency']['samples'] = [110] * 30
        self.seal(after, report)
        result = compare_runs(self.directory, after)
        self.assertEqual(result['status'], 'fail')
        self.assertEqual(result['comparisons']['latency']['status'], 'regression')

    def test_unchanged_has_no_stable_user_flow_gain(self):
        after, _ = self.run_fixture('unchanged', 100)
        result = compare_runs(self.directory, after)
        self.assertEqual(result['status'], 'fail')
        self.assertIn('no_stable_user_flow_improvement', result['failures'])

    def test_comparison_rejects_different_inputs(self):
        after, report = self.run_fixture('after', 90)
        original = copy.deepcopy(report)
        for field in ('fixture_hash', 'scenario_hash', 'toolchain', 'toolchain_version',
                      'browser_hash', 'browser_version', 'kernel_hashes', 'settings'):
            report = copy.deepcopy(original)
            report[field] = 'different'
            self.seal(after, report)
            with self.subTest(field=field), self.assertRaises(EvidenceError):
                compare_runs(self.directory, after)

    def test_comparison_rejects_same_run_or_same_sources(self):
        with self.assertRaises(EvidenceError):
            compare_runs(self.directory, self.directory)
        after, report = self.run_fixture('after', 90)
        report.update(root=self.report['root'], source_manifest=self.report['source_manifest'],
                      source_hash=self.report['source_hash'])
        self.seal(after, report)
        with self.assertRaises(EvidenceError):
            compare_runs(self.directory, after)

    def test_self_consistent_changed_registration_cannot_change_comparison_policy(self):
        after, report = self.run_fixture('after', 90)
        report['expected_metrics']['latency']['core'] = False
        report['metrics']['latency']['core'] = False
        self.seal(after, report)
        load_run(after)
        with self.assertRaisesRegex(EvidenceError, 'expected_metrics'):
            compare_runs(self.directory, after)

    def test_one_improvement_cannot_hide_another_core_regression(self):
        after, report = self.run_fixture('after', 90)
        for current, value in ((self.report, 100), (report, 110)):
            current['expected_metrics']['other'] = {**POLICY, 'improvement_eligible': False}
            current['metrics']['other'] = {'samples': [value] * 30, **current['expected_metrics']['other']}
        self.seal()
        self.seal(after, report)
        result = compare_runs(self.directory, after)
        self.assertEqual(result['status'], 'fail')
        self.assertEqual(result['stable_user_flow_improvements'], ['latency'])
        self.assertEqual(result['failures'], ['other'])


class SourceAndStatisticsTests(unittest.TestCase):
    def test_source_manifest_covers_actual_build_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            included = ['main.go', 'config.go', 'go.mod', 'go.sum', 'wails.json',
                        'backend/core.go', 'internal/bridge.go', 'frontend/src/App.tsx',
                        'frontend/scripts/build.mjs', 'frontend/public/icon.png',
                        'frontend/wailsjs/go/main/App.js', 'frontend/tsconfig.node.json',
                        'frontend/tailwind.config.js', 'frontend/postcss.config.js',
                        'frontend/vite.config.ts', 'frontend/index.html',
                        'frontend/package.json', 'frontend/package-lock.json',
                        'build/appicon.png', 'build/linux/info.json',
                        'bin/linux-amd64/xray', 'bin/linux-amd64/sing-box']
            excluded = ['data/personal.db', 'config.yaml', 'frontend/node_modules/vendor/index.js',
                        'frontend/dist/app.js', 'build/bin/app', 'build/dist/release.zip',
                        'build/linux/bin/generated', 'bin/windows-amd64/xray.exe']
            for name in included + excluded:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(name)
            manifest = source_manifest(root)
            self.assertEqual(set(manifest), set(included))
            self.assertTrue(all(value == sha256(root / name) for name, value in manifest.items()))

    def test_symlinked_source_directory_cannot_hide_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'backend').mkdir()
            (root / 'external').mkdir()
            (root / 'backend/shared').symlink_to(root / 'external', target_is_directory=True)
            with self.assertRaises(EvidenceError):
                source_manifest(root)

    def test_bootstrap_is_reproducible_and_detects_ambiguous_limit(self):
        before, after = [100] * 30, [90] * 15 + [120] * 15
        result = compare_samples(before, after)
        self.assertEqual(result, compare_samples(before, after))
        self.assertEqual(result['status'], 'inconclusive')
        self.assertFalse(result['stable_improvement'])

    def test_near_zero_uses_absolute_noise_but_cannot_invent_gain(self):
        result = compare_samples([0] * 30, [1] * 30, noise=2)
        self.assertEqual(result['status'], 'pass')
        self.assertTrue(result['near_zero'])
        self.assertIsNone(result['change_fraction'])
        self.assertFalse(result['stable_improvement'])
        result = compare_samples([0] * 30, [3] * 30, noise=2)
        self.assertEqual(result['status'], 'regression')


if __name__ == '__main__':
    unittest.main()
