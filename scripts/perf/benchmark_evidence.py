"""Fail-closed collection of Linux microbenchmarks; never user-flow evidence.

The producer must run BENCHMARK_ARGV, after collecting every selected package
with PACKAGE_LIST_ARGV under the same toolchain, build tags and environment.
Go's untagged benchmark prefix can be interrupted by the application's stdout
logger. Only an exact registered leaf prefix plus a recognized log line may
start that split record; free-standing numbers and substring matches do not.
"""
import math
import re
import statistics
from pathlib import Path

from evidence import EvidenceError, compare_samples, sha256

BENCHMARK_ARGV = ('go', 'test', '-run', '^$', '-bench', '^BenchmarkLinuxPerf',
                  '-benchmem', '-count=10', '-tags', 'webkit2_41',
                  './backend/...', './internal/...')
PACKAGE_LIST_ARGV = ('go', 'list', '-tags', 'webkit2_41', './backend/...', './internal/...')
MINIMUM_SAMPLES = 10
METRIC_UNITS = ('ns/op', 'B/op', 'allocs/op')
DIRECTORY_SIZES = (100, 1000, 10000)
PROFILE_OPERATIONS = ('SetKeywords', 'Update', 'List', 'ListByTag', 'DAOList')
PROXY_CASES = ('vless_yaml_xray', 'anytls_yaml_xray', 'vless_yaml_mihomo', 'http_auth_uri')
BENCHMARK_REGISTRY = {
    'ant-chrome/backend': tuple(f'BenchmarkLinuxPerfGroupList/n={n}' for n in DIRECTORY_SIZES),
    'ant-chrome/backend/internal/browser': tuple(
        f'BenchmarkLinuxPerfProfiles/n={n}/{operation}'
        for n in DIRECTORY_SIZES for operation in PROFILE_OPERATIONS),
    'ant-chrome/backend/internal/logger': ('BenchmarkLinuxPerfMemoryLogWrite',),
    'ant-chrome/backend/internal/proxy': tuple(
        f'BenchmarkLinuxPerfProxyResolve/{case}/{n}'
        for n in DIRECTORY_SIZES for case in PROXY_CASES) + ('BenchmarkLinuxPerfSingBoxOutbound',),
}

_NUMBER = r'(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?'
_VALUES = re.compile(r'(?P<iterations>[0-9]+)[ \t]+(?P<time>' + _NUMBER +
                     r')[ \t]+ns/op[ \t]+(?P<bytes>[0-9]+)[ \t]+B/op[ \t]+'
                     r'(?P<allocs>[0-9]+)[ \t]+allocs/op[ \t]*')
_NAME = re.compile(r'(Benchmark[^\s]+?)(?:-([0-9]+))?')
_OK = re.compile(r'ok[ \t]+(\S+)[ \t]+(' + _NUMBER + r')s(?:[ \t]+\[no tests to run\])?[ \t]*')
_NO_TESTS = re.compile(r'\?[ \t]+(\S+)[ \t]+\[no test files\][ \t]*')
_PACKAGE = re.compile(r'ant-chrome/(?:backend|internal)(?:/[A-Za-z0-9_.-]+)*')
_LOG = re.compile(r'(?:\[\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d{3}\] '
                  r'\[(?:DEBUG|INFO|WARN|ERROR|FATAL)\] \[[^\]\r\n]+\] '
                  r'|\d{4}/\d\d/\d\d \d\d:\d\d:\d\d ).*')
_FAILURE = re.compile(r'(?:FAIL(?:\s|$)|--- (?:FAIL|SKIP):|panic:|fatal error:|exit status )')


def _read_complete(path):
    try:
        text = Path(path).read_text(encoding='utf-8')
    except (OSError, UnicodeError) as error:
        raise EvidenceError('Unreadable benchmark producer output') from error
    if not text or not text.endswith('\n') or '\x00' in text:
        raise EvidenceError('Empty or truncated benchmark producer output')
    return text


def _command(argv, expected, exit_code):
    if type(exit_code) is not int or exit_code != 0:
        raise EvidenceError(f'Benchmark producer failed: exit {exit_code!r}')
    if not isinstance(argv, (list, tuple)) or tuple(argv) != expected:
        raise EvidenceError('Benchmark producer command differs from the registered command')


def _packages(packages):
    if not isinstance(packages, (list, tuple)) or not packages:
        raise EvidenceError('Missing expected package inventory')
    if any(not isinstance(p, str) or not _PACKAGE.fullmatch(p) for p in packages):
        raise EvidenceError('Invalid expected package inventory')
    if len(set(packages)) != len(packages) or not set(BENCHMARK_REGISTRY).issubset(packages):
        raise EvidenceError('Duplicate or missing benchmark package in inventory')
    return set(packages)


def collect_package_inventory(path, exit_code, *, argv):
    """Validate the complete output of the fixed go-list producer before timing."""
    _command(argv, PACKAGE_LIST_ARGV, exit_code)
    packages = _read_complete(path).splitlines()
    _packages(packages)
    return {'argv': list(argv), 'producer_exit_code': exit_code,
            'packages': sorted(packages), 'stdout_sha256': sha256(path)}


def _name(token):
    match = _NAME.fullmatch(token)
    if not match:
        raise EvidenceError('Malformed benchmark name: ' + token)
    name, suffix = match.groups()
    if suffix is not None and (not suffix.isdecimal() or int(suffix) <= 0 or suffix.startswith('0')):
        raise EvidenceError('Invalid benchmark CPU suffix: ' + token)
    return name, int(suffix) if suffix is not None else 1


def _positive(value, label, integral=False):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or value <= 0:
        raise EvidenceError('Non-positive or nonfinite benchmark ' + label)
    try:
        finite = math.isfinite(value)
    except OverflowError:
        finite = False
    if not finite:
        raise EvidenceError('Non-positive or nonfinite benchmark ' + label)
    if integral and type(value) is not int:
        raise EvidenceError('Non-integral benchmark ' + label)
    return value


def collect_benchmark_report(path, exit_code, expected_packages, *, argv):
    """Collect all 32 registered leaves, >=10 samples each, and package finishes.

    expected_packages must come from collect_package_inventory for this build.
    Allocations are strictly positive for this frozen fixture. A future truly
    allocation-free benchmark needs an explicit policy change, never an implicit
    missing/zero fallback.
    """
    _command(argv, BENCHMARK_ARGV, exit_code)
    expected_packages = _packages(expected_packages)
    rows, completed, environments = {}, {}, {}
    active, pending, passed = None, None, False
    header = {}
    last_terminal = False
    text = _read_complete(path)

    def record(token, values):
        name, procs = _name(token)
        if active is None or passed or name not in BENCHMARK_REGISTRY.get(active, ()):
            raise EvidenceError('Unregistered benchmark or incorrect package: ' + token)
        try:
            iterations = _positive(int(values['iterations']), 'iterations', True)
            sample = (_positive(float(values['time']), 'ns/op'),
                      _positive(int(values['bytes']), 'B/op', True),
                      _positive(int(values['allocs']), 'allocs/op', True))
        except (ValueError, OverflowError) as error:
            raise EvidenceError('Invalid benchmark numeric field') from error
        key = active + '::' + name
        row = rows.setdefault(key, {'package': active, 'benchmark': name, 'gomaxprocs': procs,
                                    'iterations': [], 'metrics': {unit: [] for unit in METRIC_UNITS}})
        if row['gomaxprocs'] != procs:
            raise EvidenceError('Mixed GOMAXPROCS samples: ' + name)
        row['iterations'].append(iterations)
        for unit, value in zip(METRIC_UNITS, sample):
            row['metrics'][unit].append(value)

    for number, line in enumerate(text.splitlines(), 1):
        if not line.strip():
            continue
        last_terminal = False
        if _FAILURE.match(line):
            raise EvidenceError(f'Failed or skipped Go benchmark at line {number}')
        # A pending prefix can only be finished by one exact numeric record.
        if pending:
            values = _VALUES.fullmatch(line.lstrip(' \t'))
            if values:
                record(pending, values.groupdict()); pending = None
            elif not _LOG.fullmatch(line):
                raise EvidenceError(f'Interrupted or truncated benchmark record at line {number}')
            continue
        if line.startswith(('goos:', 'goarch:', 'cpu:')):
            kind, value = line.split(':', 1); value = value.strip()
            if not value or kind in header or (kind != 'cpu' and active is not None):
                raise EvidenceError('Malformed or repeated benchmark environment header')
            if kind == 'goos' and value != 'linux':
                raise EvidenceError('Benchmark producer did not run on Linux')
            if kind == 'cpu' and active is None:
                raise EvidenceError('CPU header without benchmark package')
            header[kind] = value
            continue
        if line.startswith('pkg:'):
            package = line[4:].strip()
            if active is not None or package in completed or package not in BENCHMARK_REGISTRY:
                raise EvidenceError('Unexpected or duplicate benchmark package: ' + package)
            if header.get('goos') != 'linux' or not header.get('goarch'):
                raise EvidenceError('Missing benchmark environment headers')
            active = package
            environments[package] = header
            continue
        if line.startswith('Benchmark'):
            token, *tail = line.split(maxsplit=1)
            name, _ = _name(token)
            allowed = BENCHMARK_REGISTRY.get(active, ())
            if passed or not any(name == leaf or leaf.startswith(name + '/') for leaf in allowed):
                raise EvidenceError('Unknown benchmark/announcement: ' + token)
            if not tail:  # Go also prints parent/child announcements; never count them.
                continue
            values = _VALUES.fullmatch(tail[0])
            if values:
                record(token, values.groupdict())
            elif name in allowed and '\t' in line and _LOG.fullmatch(tail[0]):
                pending = token
            else:
                raise EvidenceError(f'Malformed benchmark record at line {number}')
            continue
        if line == 'PASS':
            if passed:
                raise EvidenceError('Duplicate package PASS marker')
            passed = True
            continue
        terminal, no_tests = _OK.fullmatch(line), _NO_TESTS.fullmatch(line)
        if terminal or no_tests:
            package = (terminal or no_tests).group(1)
            if package not in expected_packages or package in completed:
                raise EvidenceError('Unexpected or duplicate package completion: ' + package)
            if active is not None and package != active:
                raise EvidenceError('Package finished before active benchmark package')
            if package in BENCHMARK_REGISTRY and (no_tests or active != package or not passed):
                raise EvidenceError('Benchmark package lacks a completed PASS: ' + package)
            if no_tests and passed:
                raise EvidenceError('PASS marker cannot belong to a no-test package')
            completed[package] = {'status': 'no_test_files' if no_tests else 'ok'}
            if terminal:
                elapsed = float(terminal.group(2))
                if not math.isfinite(elapsed) or elapsed < 0:
                    raise EvidenceError('Invalid package completion duration')
                completed[package]['elapsed_seconds'] = elapsed
            active, passed, header = None, False, {}
            last_terminal = True
            continue
        if line.startswith(('ok ', 'ok\t', '? ', '?\t')) or 'ns/op' in line or 'allocs/op' in line:
            # Never recover numbers from prefixed/logged/partially printed results.
            raise EvidenceError(f'Unowned or malformed benchmark result at line {number}')
        # Ordinary product logs do not constitute benchmark evidence.

    if pending or active is not None or passed or header or not last_terminal:
        raise EvidenceError('Benchmark output ended before package completion')
    if set(completed) != expected_packages:
        raise EvidenceError('Missing package completion: ' + ', '.join(sorted(expected_packages - set(completed))))
    report = {'schema': 1, 'suite': 'linux_microbenchmarks', 'auxiliary': True,
              'improvement_eligible': False, 'minimum_samples': MINIMUM_SAMPLES,
              'argv': list(argv), 'producer_exit_code': exit_code,
              'stdout_sha256': sha256(path), 'expected_packages': sorted(expected_packages),
              'packages': completed, 'environments': environments, 'benchmarks': rows}
    _validate_report(report)
    for row in rows.values():
        row['medians'] = {unit: statistics.median(values) for unit, values in row['metrics'].items()}
    return report


def _validate_report(report):
    if not isinstance(report, dict) or report.get('schema') != 1 or report.get('suite') != 'linux_microbenchmarks':
        raise EvidenceError('Invalid benchmark report')
    if report.get('auxiliary') is not True or report.get('improvement_eligible') is not False or report.get('minimum_samples') != MINIMUM_SAMPLES:
        raise EvidenceError('Benchmark evidence cannot count as user-flow improvement')
    _command(report.get('argv'), BENCHMARK_ARGV, report.get('producer_exit_code'))
    packages = _packages(report.get('expected_packages'))
    if not isinstance(report.get('packages'), dict) or set(report['packages']) != packages:
        raise EvidenceError('Benchmark report has incomplete package coverage')
    for package, result in report['packages'].items():
        if not isinstance(result, dict) or result.get('status') not in ('ok', 'no_test_files'):
            raise EvidenceError('Benchmark report has failed package')
        if package in BENCHMARK_REGISTRY and result['status'] != 'ok':
            raise EvidenceError('Benchmark package was not tested')
    env = report.get('environments')
    if not isinstance(env, dict) or set(env) != set(BENCHMARK_REGISTRY):
        raise EvidenceError('Missing benchmark package environment')
    if any(not isinstance(e, dict) or e.get('goos') != 'linux' or not isinstance(e.get('goarch'), str) or not e['goarch'] for e in env.values()):
        raise EvidenceError('Invalid benchmark environment')
    if len({(e.get('goos'), e.get('goarch'), e.get('cpu')) for e in env.values()}) != 1:
        raise EvidenceError('Mixed benchmark environments')
    expected = {package + '::' + name: (package, name)
                for package, names in BENCHMARK_REGISTRY.items() for name in names}
    rows = report.get('benchmarks')
    if not isinstance(rows, dict) or set(rows) != set(expected):
        raise EvidenceError('Missing or unexpected registered benchmark')
    for key, (package, name) in expected.items():
        row = rows[key]
        if not isinstance(row, dict) or row.get('package') != package or row.get('benchmark') != name:
            raise EvidenceError('Benchmark report identity mismatch: ' + key)
        _positive(row.get('gomaxprocs'), 'GOMAXPROCS', True)
        counts = row.get('iterations')
        if not isinstance(counts, list) or len(counts) < MINIMUM_SAMPLES:
            raise EvidenceError('Insufficient benchmark samples: ' + key)
        for value in counts:
            _positive(value, 'iterations', True)
        metrics = row.get('metrics')
        if not isinstance(metrics, dict) or set(metrics) != set(METRIC_UNITS):
            raise EvidenceError('Missing benchmark allocation/time metrics: ' + key)
        for unit, values in metrics.items():
            if not isinstance(values, list) or len(values) != len(counts):
                raise EvidenceError('Mismatched benchmark metric samples: ' + key)
            for value in values:
                _positive(value, unit, unit != 'ns/op')
    if len({row['gomaxprocs'] for row in rows.values()}) != 1:
        raise EvidenceError('Mixed GOMAXPROCS across benchmark leaves')


def compare_benchmark_reports(before, after, *, repetitions=4000):
    """Return auxiliary medians/CIs for time and allocations, never goal success."""
    _validate_report(before); _validate_report(after)
    for key in ('expected_packages', 'environments'):
        if before[key] != after[key]:
            raise EvidenceError('Incomparable benchmark reports: ' + key)
    comparisons = {}
    for key, left in before['benchmarks'].items():
        right = after['benchmarks'][key]
        if left['gomaxprocs'] != right['gomaxprocs']:
            raise EvidenceError('Incomparable benchmark GOMAXPROCS: ' + key)
        comparisons[key] = {unit: compare_samples(left['metrics'][unit], right['metrics'][unit],
                                                 repetitions=repetitions)
                            for unit in METRIC_UNITS}
    return {'schema': 1, 'suite': 'linux_microbenchmarks', 'auxiliary': True,
            'improvement_eligible': False, 'comparisons': comparisons,
            'stable_user_flow_improvements': []}
