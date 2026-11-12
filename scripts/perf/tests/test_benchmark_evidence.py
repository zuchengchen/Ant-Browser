"""Synthetic calibration: these tests never execute Go or a timed benchmark."""
import copy
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from benchmark_evidence import (BENCHMARK_ARGV, BENCHMARK_REGISTRY, METRIC_UNITS,
    PACKAGE_LIST_ARGV, collect_benchmark_report, collect_package_inventory,
    compare_benchmark_reports)
from evidence import EvidenceError

PACKAGES = sorted((*BENCHMARK_REGISTRY, 'ant-chrome/backend/internal/config', 'ant-chrome/internal/paths'))
FIRST_PACKAGE = next(iter(BENCHMARK_REGISTRY))
FIRST_NAME = BENCHMARK_REGISTRY[FIRST_PACKAGE][0]
FIRST_TOKEN = FIRST_NAME + '-16'
FIRST_ROW = FIRST_TOKEN + '\t1000\t100 ns/op\t1000 B/op\t10 allocs/op\n'
LOG = '[2026-11-10 08:00:00.123] [INFO] [Browser] synthetic operation\n'


def fixture(count=10, multiplier=1, split=False, cpus=16):
    lines = []
    for package, names in BENCHMARK_REGISTRY.items():
        lines.extend(('goos: linux', 'goarch: amd64', 'pkg: ' + package, 'cpu: synthetic fixed CPU'))
        for name in names:
            # Go may print nonnumeric announcement lines for parents and leaves.
            for i in range(1, len(name.split('/')) + 1):
                lines.append('/'.join(name.split('/')[:i]))
            for sample in range(count):
                suffix = '' if cpus == 1 else '-' + str(cpus)
                token = name + suffix
                values = f'{1000+sample}\t{(100+sample)*multiplier:g} ns/op\t{1000+sample} B/op\t{10+sample%3} allocs/op'
                if split:
                    lines.extend((token + '\t' + LOG.rstrip(), LOG.rstrip(), values))
                else:
                    lines.append(token + '\t' + values)
        lines.extend(('PASS', 'ok  \t' + package + '\t12.345s'))
    lines.extend(('PASS', 'ok\tant-chrome/backend/internal/config\t0.003s [no tests to run]',
                  '?   \tant-chrome/internal/paths\t[no test files]'))
    return '\n'.join(lines) + '\n'


class BenchmarkEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.output = Path(self.directory.name) / 'bench.stdout'
        self.good = fixture()

    def collect(self, text=None, exit_code=0, packages=PACKAGES, argv=BENCHMARK_ARGV):
        self.output.write_text(self.good if text is None else text, encoding='utf-8')
        return collect_benchmark_report(self.output, exit_code, packages, argv=argv)

    def reject(self, text, **kwargs):
        with self.assertRaises(EvidenceError):
            self.collect(text, **kwargs)

    def test_fixed_registry_and_complete_results(self):
        self.assertEqual({p: len(n) for p,n in BENCHMARK_REGISTRY.items()}, {
            'ant-chrome/backend': 3, 'ant-chrome/backend/internal/browser': 15,
            'ant-chrome/backend/internal/logger': 1, 'ant-chrome/backend/internal/proxy': 13})
        result = self.collect()
        self.assertEqual(len(result['benchmarks']), 32)
        self.assertEqual(set(result['packages']), set(PACKAGES))
        self.assertTrue(result['auxiliary'])
        self.assertFalse(result['improvement_eligible'])
        for row in result['benchmarks'].values():
            self.assertEqual(len(row['iterations']), 10)
            self.assertEqual(set(row['metrics']), set(METRIC_UNITS))
            self.assertEqual(row['medians']['ns/op'], 104.5)
            self.assertEqual(row['medians']['B/op'], 1004.5)
            self.assertEqual(row['gomaxprocs'], 16)

    def test_actual_go_stdout_split_records(self):
        direct = self.collect()
        split = self.collect(fixture(split=True))
        self.assertEqual(direct['benchmarks'], split['benchmarks'])

    def test_single_cpu_and_scientific_time_notation(self):
        result = self.collect(fixture(cpus=1).replace('\t100 ns/op', '\t1e2 ns/op'))
        self.assertTrue(all(r['gomaxprocs'] == 1 for r in result['benchmarks'].values()))

    def test_at_least_ten_not_just_matching_name(self):
        self.reject(fixture(count=9))
        self.collect(fixture(count=11))
        self.reject(self.good.replace(FIRST_ROW, '', 1))
        bare = self.good.replace(FIRST_ROW, FIRST_TOKEN + '\n', 1)
        self.reject(bare)
        prefixed = self.good.replace(FIRST_ROW, 'benign message ' + FIRST_ROW, 1)
        self.reject(prefixed)

    def test_empty_whitespace_missing_or_truncated(self):
        for text in ('', '\n', self.good.rstrip(), self.good.rsplit('?', 1)[0],
                     self.good + 'partial output\n', self.good + 'PASS\n'):
            with self.subTest(text=text[-60:]): self.reject(text)
        with self.assertRaises(EvidenceError):
            collect_benchmark_report(Path(self.directory.name)/'absent', 0, PACKAGES, argv=BENCHMARK_ARGV)

    def test_producer_must_succeed_with_exact_command(self):
        for code in (1, -9, False, '0', None):
            with self.subTest(code=code): self.reject(self.good, exit_code=code)
        for argv in (None, [], (*BENCHMARK_ARGV, '-benchtime=1x'),
                     tuple('-count=1' if x == '-count=10' else x for x in BENCHMARK_ARGV),
                     tuple('^BenchmarkBenign' if x == '^BenchmarkLinuxPerf' else x for x in BENCHMARK_ARGV)):
            with self.subTest(argv=argv): self.reject(self.good, argv=argv)

    def test_required_packages_and_benchmark_package_pass(self):
        for bad in (
            self.good.replace('PASS\nok  \t'+FIRST_PACKAGE, 'ok  \t'+FIRST_PACKAGE, 1),
            self.good.replace('ok  \t'+FIRST_PACKAGE+'\t12.345s', '? '+FIRST_PACKAGE+' [no test files]', 1),
            self.good.replace('ok  \t'+FIRST_PACKAGE+'\t12.345s', 'ok '+FIRST_PACKAGE+' (cached)', 1),
            self.good.replace('ok\tant-chrome/backend/internal/config\t0.003s [no tests to run]\n', '', 1),
            self.good + '? ant-chrome/internal/extra [no test files]\n',
            self.good + '? ant-chrome/internal/paths [no test files]\n',
        ):
            with self.subTest(tail=bad[-120:]): self.reject(bad)
        for packages in ([], [FIRST_PACKAGE], [*PACKAGES, PACKAGES[0]], [*PACKAGES, 'unrelated/package']):
            with self.subTest(packages=packages): self.reject(self.good, packages=packages)

    def test_failing_skipping_and_zero_matching_runs(self):
        for marker in ('FAIL', '--- FAIL: BenchmarkLinuxPerfX', '--- SKIP: BenchmarkLinuxPerfX',
                       'panic: synthetic', 'fatal error: synthetic', 'exit status 2'):
            self.reject(self.good.replace('PASS\n', marker+'\n', 1))
        benign = 'PASS\nok\tant-chrome/backend\t0.002s [no tests to run]\n'
        self.reject(benign)

    def test_numeric_fields_must_be_complete_finite_and_positive(self):
        for field, replacement in (
            ('\t1000\t', '\t0\t'), ('100 ns/op', '0 ns/op'),
            ('100 ns/op', '-2 ns/op'), ('100 ns/op', 'NaN ns/op'),
            ('100 ns/op', '+Inf ns/op'), ('100 ns/op', '1e999 ns/op'),
            ('1000 B/op', '9'*5000+' B/op'),
            ('1000 B/op', '0 B/op'), ('10 allocs/op', '0 allocs/op'),
            ('1000 B/op', '1.5 B/op'), ('10 allocs/op', '-1 allocs/op'),
            ('\t1000 B/op', ''), ('\t10 allocs/op', ''),
            ('ns/op', 'ms/op'), ('allocs/op', 'allocs/op trailing text'),
        ):
            with self.subTest(replacement=replacement):
                bad = FIRST_ROW.replace(field, replacement)
                self.reject(self.good.replace(FIRST_ROW, bad, 1))

    def test_registration_identity_and_cpu(self):
        for replacement in (FIRST_NAME+'/benign-16', 'BenchmarkLinuxPerfUnknown-16',
                            FIRST_NAME+'-0', FIRST_NAME+'-016'):
            self.reject(self.good.replace(FIRST_TOKEN, replacement, 1))
        self.reject(self.good.replace(FIRST_TOKEN, FIRST_NAME+'-8', 1))
        other = BENCHMARK_REGISTRY['ant-chrome/backend/internal/logger'][0]+'-16'
        self.reject(self.good.replace(FIRST_TOKEN, other, 1))
        self.reject(self.good.replace('pkg: '+FIRST_PACKAGE, 'pkg: ant-chrome/backend/internal/config', 1))
        self.reject(self.good.replace('goos: linux', 'goos: windows', 1))
        self.reject(self.good.replace('goarch: amd64', 'goarch: arm64', 1))

    def test_split_record_must_have_owned_prefix_and_complete_tail(self):
        split = FIRST_TOKEN+'\t'+LOG+'1000\t100 ns/op\t1000 B/op\t10 allocs/op\n'
        self.collect(self.good.replace(FIRST_ROW, split, 1))
        for bad in (split.replace(FIRST_TOKEN, 'benign '+FIRST_TOKEN),
                    split.replace(LOG, 'unrecognized preamble\n'),
                    split.replace('1000\t100 ns/op', '1000\t0 ns/op'),
                    split.replace('1000\t100 ns/op\t1000 B/op\t10 allocs/op\n', ''),
                    FIRST_ROW.split('\t', 1)[1]):
            with self.subTest(bad=bad): self.reject(self.good.replace(FIRST_ROW, bad, 1))

    def test_result_after_pass_does_not_count(self):
        bad = self.good.replace(FIRST_ROW, '', 1).replace('PASS\n', 'PASS\n'+FIRST_ROW, 1)
        self.reject(bad)

    def test_package_inventory_is_positive_evidence(self):
        self.output.write_text('\n'.join(PACKAGES)+'\n')
        result = collect_package_inventory(self.output, 0, argv=PACKAGE_LIST_ARGV)
        self.assertEqual(result['packages'], PACKAGES)
        for text in ('', '\n'.join(PACKAGES), '\n'.join(PACKAGES+[PACKAGES[0]])+'\n',
                     'benign ant-chrome/backend\n', '\n'.join(PACKAGES[1:])+'\n'):
            self.output.write_text(text)
            with self.assertRaises(EvidenceError):
                collect_package_inventory(self.output, 0, argv=PACKAGE_LIST_ARGV)
        self.output.write_text('\n'.join(PACKAGES)+'\n')
        with self.assertRaises(EvidenceError):
            collect_package_inventory(self.output, 1, argv=PACKAGE_LIST_ARGV)

    def test_comparison_is_auxiliary_and_recomputes_medians(self):
        before, after = self.collect(), self.collect(fixture(multiplier=.5))
        for row in after['benchmarks'].values(): row['medians']['ns/op'] = 999999
        comparison = compare_benchmark_reports(before, after, repetitions=50)
        self.assertTrue(comparison['auxiliary'])
        self.assertFalse(comparison['improvement_eligible'])
        self.assertEqual(comparison['stable_user_flow_improvements'], [])
        self.assertEqual(len(comparison['comparisons']), 32)
        for result in comparison['comparisons'].values():
            self.assertEqual(result['ns/op']['after_median'], 52.25)
            self.assertEqual(result['B/op']['before_median'], 1004.5)

    def test_comparison_revalidates_samples_and_policy(self):
        before = self.collect()
        first = FIRST_PACKAGE+'::'+FIRST_NAME
        mutations = [
            lambda x: x.update(improvement_eligible=True),
            lambda x: x['benchmarks'][first]['metrics']['ns/op'].clear(),
            lambda x: x['benchmarks'][first]['metrics']['B/op'].__setitem__(0, 0),
            lambda x: x['benchmarks'][first]['metrics']['ns/op'].__setitem__(0, float('nan')),
            lambda x: x['benchmarks'][first].update(gomaxprocs=8),
            lambda x: x['environments'].__setitem__(FIRST_PACKAGE, None),
            lambda x: x['benchmarks'].pop(first),
            lambda x: x['packages'].pop('ant-chrome/internal/paths'),
        ]
        for mutate in mutations:
            bad = copy.deepcopy(before); mutate(bad)
            with self.subTest(mutate=mutate), self.assertRaises(EvidenceError):
                compare_benchmark_reports(before, bad, repetitions=10)


if __name__ == '__main__':
    unittest.main()
