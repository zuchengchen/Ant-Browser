"""Strict, standalone validation and statistical comparison for Linux runs."""
import hashlib
import json
import math
import random
import re
import statistics
from pathlib import Path


class EvidenceError(RuntimeError):
    pass


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def source_manifest(root):
    root = Path(root)
    if not root.is_dir():
        raise EvidenceError('Missing source root')
    result = {}
    directories = ('backend', 'internal', 'frontend/src', 'frontend/scripts',
                   'frontend/public', 'frontend/wailsjs', 'build')
    paths = [*root.glob('*.go'), *(root / name for name in (
        'go.mod', 'go.sum', 'wails.json', 'bin/linux-amd64/xray', 'bin/linux-amd64/sing-box'))]
    for pattern in ('tsconfig*', 'tailwind*', 'postcss*', 'vite*', 'index*', 'package*'):
        paths.extend((root / 'frontend').glob(pattern))
    for name in directories:
        paths.extend((root / name).rglob('*'))
    for path in sorted(set(paths)):
        relative = path.relative_to(root)
        if relative.parts[0] == 'build' and any(part in ('bin', 'dist') for part in relative.parts[1:]):
            continue
        if path.is_symlink() and path.is_dir():
            raise EvidenceError('Untracked symlinked source directory: ' + str(path))
        if path.is_file():
            result[relative.as_posix()] = sha256(path)
    return result


def digest_object(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False).encode()).hexdigest()


def _json_loads(text):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('Duplicate JSON key: ' + key)
            result[key] = value
        return result

    def nonfinite(value):
        raise ValueError('Nonfinite JSON value: ' + value)

    return json.loads(text, object_pairs_hook=pairs, parse_constant=nonfinite)


def _go_run(path, required, exit_code):
    """Validate machine events, never infer success from human log words."""
    if type(exit_code) is not int or exit_code != 0:
        raise EvidenceError(f'Go producer failed: exit {exit_code}')
    if not isinstance(required, (list, tuple, set, dict)) or not required:
        raise EvidenceError('Required test names must be explicitly registered')
    wanted = list(required)
    for key in wanted:
        if isinstance(key, str) and key:
            continue
        if isinstance(key, tuple) and len(key) == 2 and all(isinstance(x, str) and x for x in key):
            continue
        raise EvidenceError('Invalid required test identity')
    if len(set(wanted)) != len(wanted):
        raise EvidenceError('Duplicate required test identity')
    packages, states, outputs = {}, {}, {}
    try:
        stream = Path(path).read_text(encoding='utf-8')
        if not stream or not stream.endswith('\n'):
            raise EvidenceError('Empty or truncated Go JSON')
        for line in stream.splitlines():
            try:
                event = _json_loads(line)
            except (ValueError, TypeError) as error:
                raise EvidenceError('Invalid or truncated Go JSON') from error
            if not isinstance(event, dict):
                raise EvidenceError('Go event must be an object')
            action, test, package = event.get('Action'), event.get('Test'), event.get('Package')
            if action in ('fail', 'build-fail'):
                raise EvidenceError(f'Go run failed: {package} {test}')
            if action == 'build-output':
                if not isinstance(event.get('Output'), str) or not event.get('ImportPath'):
                    raise EvidenceError('Invalid Go build output')
                continue
            if not isinstance(package, str) or not package or (test is not None and (not isinstance(test, str) or not test)):
                raise EvidenceError('Missing Go package/test identity')
            key = (package, test)
            if action == 'start' and test is None:
                if package in packages:
                    raise EvidenceError('Duplicate Go package start: ' + package)
                packages[package] = 'active'
                continue
            if packages.get(package) != 'active':
                raise EvidenceError('Go event outside an active package: ' + package)
            if action == 'output':
                if not isinstance(event.get('Output'), str) or (test is not None and key not in states):
                    raise EvidenceError('Invalid or unattributed Go output')
                outputs.setdefault(key, []).append(event['Output'])
            elif test is not None:
                state = states.get(key)
                if action == 'run':
                    if state and state['active']:
                        raise EvidenceError('Duplicate Go test start: ' + str(key))
                    states.setdefault(key, {'passed': 0, 'skipped': 0})['active'] = True
                elif action in ('pause', 'cont'):
                    if not state or not state['active']:
                        raise EvidenceError('Inactive Go test: ' + str(key))
                elif action in ('pass', 'skip'):
                    if not state or not state['active']:
                        raise EvidenceError('Go test completed without start: ' + str(key))
                    state['active'] = False
                    state['passed' if action == 'pass' else 'skipped'] += 1
                else:
                    raise EvidenceError('Unexpected Go test action: ' + str(action))
            elif action in ('pass', 'skip'):
                if any(k[0] == package and value['active'] for k, value in states.items()):
                    raise EvidenceError('Go package ended with unfinished tests: ' + package)
                packages[package] = action
            else:
                raise EvidenceError('Unexpected Go package action: ' + str(action))
    except (OSError, UnicodeError) as error:
        raise EvidenceError('Missing or unreadable Go JSON') from error
    if not packages or any(state == 'active' for state in packages.values()):
        raise EvidenceError('Unfinished Go package')
    resolved = []
    for required_key in wanted:
        matches = [key for key in states if key[1] == required_key] if isinstance(required_key, str) else [required_key]
        if len(matches) != 1:
            raise EvidenceError('Missing or ambiguous required Go test: ' + str(required_key))
        key = matches[0]
        state = states.get(key)
        if not state or not state['passed'] or state['skipped'] or packages[key[0]] != 'pass':
            raise EvidenceError('Required Go test missing, failed or skipped: ' + str(key))
        if any(value['skipped'] for child, value in states.items()
               if child[0] == key[0] and child[1].startswith(key[1] + '/')):
            raise EvidenceError('Required Go subtest skipped: ' + str(key))
        resolved.append(key)
    return states, outputs, resolved


def check_go_json(path, required, exit_code):
    states, _, _ = _go_run(path, required, exit_code)
    return {'started': sorted({key[1] for key in states}),
            'passed': sorted({key[1] for key, state in states.items() if state['passed']}),
            'skipped': sorted({key[1] for key, state in states.items() if state['skipped']}),
            'test_identities': sorted(states)}


def collect_go_reports(path, required_reports, exit_code, expected_samples=30, allow_diagnostic=False):
    """Read registered reports after joining Go's arbitrarily fragmented Output fields.

    required_reports maps explicit (Package, Test) pairs to {suite, component?,
    metrics: {name: raw_unit}}. Each registered test must emit exactly one report.
    """
    if not isinstance(required_reports, dict) or not required_reports or any(
        not isinstance(key, tuple) or len(key) != 2 for key in required_reports
    ):
        raise EvidenceError('Report producers require explicit (Package, Test) identities')
    if type(expected_samples) is not int or expected_samples < 1 or type(allow_diagnostic) is not bool or (expected_samples < 30 and not allow_diagnostic):
        raise EvidenceError('Invalid expected Go sample policy')
    _, outputs, resolved = _go_run(path, required_reports, exit_code)
    result = []
    marker = re.compile(r'^\s*(?:[^\s]+\.go:\d+:\s*)?LINUXPERF_JSON:(.*)$')
    for key in resolved:
        registration = required_reports[key]
        if not isinstance(registration, dict) or not isinstance(registration.get('suite'), str) or not registration['suite']:
            raise EvidenceError('Invalid Go report registration: ' + str(key))
        expected = registration.get('metrics')
        if not isinstance(expected, dict) or not expected or any(
            not isinstance(name, str) or not name or not isinstance(unit, str) or not unit
            for name, unit in expected.items()
        ):
            raise EvidenceError('Missing Go metric registration: ' + str(key))
        reports = []
        for line in ''.join(outputs.get(key, [])).splitlines(keepends=True):
            match = marker.fullmatch(line.rstrip('\r\n'))
            if not match:
                continue
            if not line.endswith('\n'):
                raise EvidenceError('Truncated Go report line: ' + str(key))
            try:
                report = _json_loads(match.group(1))
            except (ValueError, TypeError) as error:
                raise EvidenceError('Invalid or truncated Go report: ' + str(key)) from error
            reports.append(report)
        if len(reports) != 1 or not isinstance(reports[0], dict):
            raise EvidenceError('Required exactly one Go report: ' + str(key))
        report = reports[0]
        if type(report.get('schema_version')) is not int or report['schema_version'] != 1:
            raise EvidenceError('Unsupported Go report schema: ' + str(key))
        if any(report.get(field) != registration.get(field) for field in ('suite', 'component')):
            raise EvidenceError('Unexpected Go suite/component: ' + str(key))
        diagnostic = report.get('diagnostic', False)
        if type(diagnostic) is not bool or (diagnostic and not allow_diagnostic):
            raise EvidenceError('Diagnostic Go report is not acceptance evidence: ' + str(key))
        if type(report.get('samples')) is not int or report['samples'] != expected_samples:
            raise EvidenceError('Wrong Go sample count: ' + str(key))
        metrics, units = report.get('metrics'), report.get('metric_units')
        if not isinstance(metrics, dict) or metrics.keys() != expected.keys() or units != expected:
            raise EvidenceError('Go metric coverage or units differ: ' + str(key))
        for name, values in metrics.items():
            if not isinstance(values, list) or len(values) != expected_samples or not _valid_samples(values):
                raise EvidenceError('Invalid Go metric samples: ' + name)
        if not isinstance(report.get('evidence'), dict) or not report['evidence']:
            raise EvidenceError('Missing Go correctness evidence: ' + str(key))
        result.append(report)
    return result


def _finite_nonnegative(value):
    try:
        return type(value) in (int, float) and math.isfinite(value) and value >= 0
    except OverflowError:
        return False


def _valid_samples(values):
    return all(_finite_nonnegative(value) for value in values)


def validate_metric(name, metric):
    if not isinstance(metric, dict):
        raise EvidenceError(f'{name}: metric must be an object')
    values = metric.get('samples')
    minimum = metric.get('minimum_samples')
    if type(minimum) is not int or minimum not in (10, 30) or not isinstance(values, list) or len(values) < minimum:
        raise EvidenceError(f'{name}: insufficient samples')
    if not _valid_samples(values):
        raise EvidenceError(f'{name}: nonfinite or invalid sample')
    if metric.get('unit') not in ('ms', 'seconds', 'bytes', 'count'):
        raise EvidenceError(f'{name}: unknown unit')
    if not isinstance(metric.get('core'), bool) or not isinstance(metric.get('improvement_eligible'), bool):
        raise EvidenceError(f'{name}: missing classification')
    floor = metric.get('absolute_noise', 0)
    if not _finite_nonnegative(floor):
        raise EvidenceError(f'{name}: invalid noise calibration')
    return values


def percentile(values, fraction):
    ordered = sorted(values)
    position = (len(ordered) - 1) * fraction
    lower, upper = math.floor(position), math.ceil(position)
    return ordered[lower] + (ordered[upper] - ordered[lower]) * (position - lower)


def compare_samples(before, after, noise=0, seed=1729, repetitions=4000):
    if not before or not after or not _valid_samples(before) or not _valid_samples(after):
        raise EvidenceError('Invalid statistical samples')
    if not _finite_nonnegative(noise):
        raise EvidenceError('Invalid statistical noise floor')
    if type(repetitions) is not int or repetitions < 1:
        raise EvidenceError('Invalid bootstrap count')
    rng = random.Random(seed)
    center_before, center_after = statistics.median(before), statistics.median(after)
    differences, ratios = [], []
    for _ in range(repetitions):
        a = statistics.median(rng.choices(before, k=len(before)))
        b = statistics.median(rng.choices(after, k=len(after)))
        differences.append(b - a)
        if a > 0:
            ratios.append((b - a) / a)
    interval = [percentile(differences, .025), percentile(differences, .975)]
    # A calibrated absolute floor applies only when both medians approach that floor.
    near_zero = max(center_before, center_after) <= noise
    limit = noise if near_zero else center_before * .05
    if interval[1] <= limit:
        status = 'pass'
    elif interval[0] > limit:
        status = 'regression'
    else:
        status = 'inconclusive'
    return {'before_median': center_before, 'after_median': center_after,
            'before_p95': percentile(before, .95), 'after_p95': percentile(after, .95),
            'difference_ci95': interval,
            'change_fraction': (center_after / center_before - 1) if center_before else None,
            'change_fraction_ci95': [percentile(ratios, .025), percentile(ratios, .975)] if len(ratios) == repetitions else None,
            'absolute_noise': noise, 'near_zero': near_zero, 'limit': limit,
            'stable_improvement': interval[1] < -noise,
            'status': status}


def _names(value, label):
    if not isinstance(value, list) or not value or any(not isinstance(x, str) or not x for x in value):
        raise EvidenceError('Missing or invalid ' + label)
    if len(set(value)) != len(value):
        raise EvidenceError('Duplicate ' + label)
    return set(value)


def _timestamps(record, label, start=None, finish=None):
    first, last = record.get('started_ns'), record.get('finished_ns')
    if type(first) is not int or type(last) is not int or not 0 < first < last:
        raise EvidenceError('Invalid ' + label + ' timestamps')
    if start is not None and (first < start or last > finish):
        raise EvidenceError(label + ' outside run timestamps')
    return first, last


def _artifact_path(directory, name):
    if not isinstance(name, str) or not name or '\\' in name:
        raise EvidenceError('Invalid artifact path')
    relative = Path(name)
    if relative.is_absolute() or any(part in ('.', '..') for part in name.split('/')) or relative.as_posix() != name:
        raise EvidenceError('Artifact path must be canonical and relative: ' + name)
    path = directory / relative
    if not path.resolve().is_relative_to(directory):
        raise EvidenceError('Artifact escapes run directory: ' + name)
    return path


METRIC_POLICY = ('minimum_samples', 'unit', 'core', 'improvement_eligible', 'absolute_noise')


def load_run(directory):
    directory = Path(directory).resolve()
    try:
        report = _json_loads((directory / 'results.json').read_text(encoding='utf-8'))
        seal = _json_loads((directory / 'complete.json').read_text(encoding='utf-8'))
    except (OSError, ValueError, UnicodeError) as error:
        raise EvidenceError('Missing, unreadable or incomplete run') from error
    if not isinstance(report, dict) or not isinstance(seal, dict):
        raise EvidenceError('Run report and seal must be objects')
    if not isinstance(report.get('run_id'), str) or not report['run_id'] or seal.get('run_id') != report['run_id'] or seal.get('results_sha256') != sha256(directory / 'results.json'):
        raise EvidenceError('Run seal mismatch')
    if report.get('schema') != 1 or type(report.get('schema')) is not int:
        raise EvidenceError('Unsupported run schema')
    if report.get('status') != 'complete' or report.get('diagnostic') is not False or not isinstance(report.get('metrics'), dict) or not report['metrics']:
        raise EvidenceError('Run is not a complete acceptance run')
    start, finish = _timestamps(report, 'run')
    manifest = report.get('source_manifest')
    if not isinstance(manifest, dict) or not manifest or digest_object(manifest) != report.get('source_hash'):
        raise EvidenceError('Source provenance mismatch')
    if not isinstance(report.get('root'), str) or not report['root']:
        raise EvidenceError('Missing source root')
    if source_manifest(report['root']) != report['source_manifest']:
        raise EvidenceError('Source changed after measurement')
    expected_metrics = report.get('expected_metrics')
    if not isinstance(expected_metrics, dict) or expected_metrics.keys() != report['metrics'].keys():
        raise EvidenceError('Missing or different registered metric coverage')
    for name, metric in report['metrics'].items():
        if not isinstance(name, str) or not name:
            raise EvidenceError('Invalid metric name')
        validate_metric(name, metric)
        policy = expected_metrics[name]
        if not isinstance(policy, dict) or set(policy) != set(METRIC_POLICY) or any(
            metric.get(key) != policy[key] or type(metric.get(key)) is not type(policy[key])
            for key in METRIC_POLICY
        ):
            raise EvidenceError('Metric differs from registered policy: ' + name)
    if _names(report.get('expected_correctness_checks'), 'registered correctness checks') != _names(report.get('correctness_checks'), 'correctness checks'):
        raise EvidenceError('Correctness check coverage differs')
    expected_commands = _names(report.get('expected_commands'), 'registered commands')
    commands = report.get('commands')
    if not isinstance(commands, list) or any(not isinstance(item, dict) for item in commands):
        raise EvidenceError('Invalid command evidence')
    if _names([item.get('name') for item in commands], 'commands') != expected_commands:
        raise EvidenceError('Command coverage differs')
    streams = {}
    for command in commands:
        if type(command.get('exit_code')) is not int or command['exit_code'] != 0:
            raise EvidenceError('Command failed: ' + command['name'])
        _timestamps(command, 'command ' + command['name'], start, finish)
        argv = command.get('argv')
        if not isinstance(argv, list) or not argv or any(not isinstance(arg, str) for arg in argv) or not argv[0]:
            raise EvidenceError('Missing command arguments: ' + command['name'])
        if not isinstance(command.get('cwd'), str) or not Path(command['cwd']).is_absolute():
            raise EvidenceError('Missing command working directory: ' + command['name'])
        for stream in ('stdout', 'stderr'):
            name = command.get(stream)
            _artifact_path(directory, name)
            if name in streams:
                raise EvidenceError('Reused command log: ' + name)
            streams[name] = (command, stream)
    artifacts = report.get('artifacts')
    if not isinstance(artifacts, list) or not artifacts or any(not isinstance(item, dict) for item in artifacts):
        raise EvidenceError('Missing positive artifact evidence')
    seen = set()
    for record in artifacts:
        name = record.get('path')
        path = _artifact_path(directory, name)
        if name in seen or name in ('results.json', 'complete.json'):
            raise EvidenceError('Duplicate or self-referential artifact: ' + name)
        seen.add(name)
        allow_empty = record.get('allow_empty', False)
        if type(allow_empty) is not bool:
            raise EvidenceError('Invalid empty-artifact policy: ' + name)
        try:
            if not path.is_file() or sha256(path) != record.get('sha256'):
                raise EvidenceError('Missing or changed artifact: ' + name)
            info = path.stat()
        except OSError as error:
            raise EvidenceError('Unreadable artifact: ' + name) from error
        if not start <= info.st_mtime_ns <= finish:
            raise EvidenceError('Stale or post-run artifact: ' + name)
        if info.st_size == 0:
            # Some successful commands (go build) and quiet Xvfb/GUI runs emit
            # nothing. Empty data artifacts or go-test stdout cannot prove work.
            command_stream = streams.get(name)
            is_go_test = command_stream and command_stream[1] == 'stdout' and Path(command_stream[0]['argv'][0]).name == 'go' and 'test' in command_stream[0]['argv'][1:]
            historical = report.get('pressure_reference', {}).get('summary', {}).get('artifacts', {}).get(
                name.removeprefix('pressure/')) if report.get('role') == 'baseline' and name.startswith('pressure/') else None
            verified_empty_history = (isinstance(historical, dict) and historical.get('bytes') == 0
                                      and historical.get('sha256') == record.get('sha256'))
            # The pinned historical verifier below must authenticate this exact
            # empty log. It cannot exempt a current producer or an arbitrary file.
            if (not allow_empty or (not command_stream and path.suffix != '.log' and
                                   not verified_empty_history) or is_go_test):
                raise EvidenceError('Empty artifact is not execution evidence: ' + name)
    if not set(streams).issubset(seen):
        raise EvidenceError('Missing command stdout/stderr artifact')
    if 'role' in report or 'acceptance' in report:
        validate_v2_run(directory, report)
    return report


def validate_v2_contract(report):
    """The asymmetric matrix is fixed by the approved goal, not by missing data."""
    from registration import (ROLES, BASELINE_SHA256, acceptance_contract,
                              expected_metrics, expected_checks, expected_commands)
    role = report.get('role')
    if role not in ROLES or report.get('acceptance') != acceptance_contract(role):
        raise EvidenceError('Missing or changed approved v2 role/goal contract')
    if report.get('status') != 'complete' or report.get('diagnostic') is not False:
        raise EvidenceError('Incomplete candidate/baseline cannot use the pressure exception')
    if (report.get('expected_metrics') != expected_metrics(role) or
            report.get('expected_correctness_checks') != expected_checks(role) or
            report.get('expected_commands') != expected_commands()):
        raise EvidenceError('V2 coverage differs from the preregistered role')
    if set(report.get('metrics', {})) != set(expected_metrics(role)):
        raise EvidenceError('V2 required metric is missing')
    if role == 'baseline' and report.get('source_hash') != BASELINE_SHA256:
        raise EvidenceError('V2 baseline source differs from approved pressure target')
    if role == 'candidate' and 'pressure_reference' in report:
        raise EvidenceError('Candidate cannot inherit the baseline pressure exception')
    for name, metric in report['metrics'].items():
        if name.startswith('catalog.') and '.resources.' not in name:
            values = validate_metric(name, metric)
            if any(value <= 0 or value > 1_800_000 for value in values):
                raise EvidenceError('Catalog operation did not complete within its original limit: ' + name)


def validate_v2_run(directory, report):
    from registration import GOAL_SHA256
    from resource_evidence import verify_resources
    from gui_evidence import verify_gui, verify_instance_actions, verify_catalog_observations
    from pressure_evidence import verify_pressure
    validate_v2_contract(report)
    verify_build_artifacts(directory, report)
    if verify_go_suites(directory, report) != report.get('go_verification'):
        raise EvidenceError('Go verification differs from its raw producers')
    from runtime import verify_display
    if verify_display(directory) != report.get('display_lifecycle'):
        raise EvidenceError('Display lifecycle differs from its raw evidence')
    if report['role'] == 'baseline':
        reference = report.get('pressure_reference')
        if not isinstance(reference, dict) or reference.get('path') != 'pressure/reference.json':
            raise EvidenceError('Missing approved baseline pressure reference')
        try:
            summary = verify_pressure(directory / reference['path'], report['source_hash'], GOAL_SHA256)
        except ValueError as error:
            raise EvidenceError('Invalid historical pressure reference') from error
        if reference.get('summary') != summary:
            raise EvidenceError('Cached pressure evidence differs from verified raw evidence')
        expected_artifacts = {'pressure/' + name: item['sha256']
                              for name, item in summary['artifacts'].items()}
        expected_artifacts[reference['path']] = summary['reference_sha256']
        actual_artifacts = {item['path']: item['sha256'] for item in report['artifacts']
                            if item['path'].startswith('pressure/')}
        if actual_artifacts != expected_artifacts:
            raise EvidenceError('Historical pressure bundle is not completely sealed in this run')
    if verify_resources(directory, report) != report.get('resource_verification'):
        raise EvidenceError('Resource verification differs from raw evidence')
    if verify_gui(directory, report) != report.get('gui_verification'):
        raise EvidenceError('GUI receipt verification differs from raw evidence')
    if verify_instance_actions(directory, report) != report.get('instance_action_verification'):
        raise EvidenceError('Instance action verification differs from raw evidence')
    if verify_catalog_observations(directory, report) != report.get('catalog_observation_verification'):
        raise EvidenceError('Catalog observation verification differs from raw evidence')
    # The standalone comparator must enforce normal exits too; callers cannot
    # bypass lifecycle checks by skipping the CLI wrapper.
    from linux_perf import application_lifecycles
    if application_lifecycles(directory, role=report['role']) != report.get('application_lifecycles'):
        raise EvidenceError('Cached application lifecycle evidence differs')


def verify_build_artifacts(directory, report):
    expected = {'application': 'build/build/bin/ant-chrome', 'seed': 'seed'}
    records = report.get('build_artifacts')
    if not isinstance(records, dict) or records.keys() != expected.keys():
        raise EvidenceError('Missing built application or seed identity')
    sealed = {item['path']: item['sha256'] for item in report.get('artifacts', [])}
    commands = {item['name']: item for item in report.get('commands', [])}
    for name, relative in expected.items():
        record = records[name]
        if not isinstance(record, dict) or record.get('path') != relative:
            raise EvidenceError('Wrong application or seed build path')
        path = Path(directory) / relative
        try:
            digest = sha256(path)
            with path.open('rb') as stream:
                header = stream.read(20)
            size = path.stat().st_size
            mtime = path.stat().st_mtime_ns
        except OSError as error:
            raise EvidenceError('Missing built application or seed') from error
        if (len(header) != 20 or header[:6] != b'\x7fELF\x02\x01' or header[18:20] != b'\x3e\x00'
                or digest != record.get('sha256') or digest != sealed.get(relative)
                or size != record.get('bytes') or size <= 20):
            raise EvidenceError('Unsealed, changed or invalid Linux amd64 build artifact')
        command = commands.get('wails-build' if name == 'application' else 'seed-build')
        if not command or not command['started_ns'] <= mtime <= command['finished_ns']:
            raise EvidenceError('Build artifact was not created by its current producer')
    if report.get('binary_sha256') != records['application']['sha256']:
        raise EvidenceError('Application hash differs from its build artifact')
    return records


def verify_go_suites(directory, report):
    from registration import go_registration, go_policy
    commands = {item['name']: item for item in report['commands']}
    evidence, metrics = {}, {}
    for name, package, test, registration in go_registration():
        command = commands.get('go-' + name)
        argv = ['go', 'test', '-count=1', '-json', '-tags', 'webkit2_41 linuxperf',
                './' + package.removeprefix('ant-chrome/'), '-run', '^' + test + '$']
        if not command or command.get('argv') != argv:
            raise EvidenceError('Go suite command differs from registration: ' + name)
        records = collect_go_reports(Path(directory) / command['stdout'],
            {(package, test): registration}, command['exit_code'], expected_samples=30)
        if len(records) != 1:
            raise EvidenceError('Go suite lacks one complete result: ' + name)
        record = records[0]
        evidence[name] = record.get('evidence')
        for key, values in record['metrics'].items():
            policy, multiplier = go_policy(name, key, registration['metrics'][key])
            metric = name + '.' + key
            expected = {'samples': [value * multiplier for value in values], **policy}
            if report['metrics'].get(metric) != expected:
                raise EvidenceError('Go metric differs from raw producer: ' + metric)
            metrics[metric] = len(values)
    if report.get('go_evidence') != evidence:
        raise EvidenceError('Go correctness evidence differs from raw producers')
    for engine, field in (('xray', 'xray_sha256'), ('sing-box', 'singbox_sha256'), ('mihomo', 'mihomo_sha256')):
        actual_hash = evidence['proxy'].get(field)
        registered_hash = report.get('kernel_hashes', {}).get(engine)
        if (not isinstance(actual_hash, str) or re.fullmatch('[0-9a-f]{64}', actual_hash) is None
                or not isinstance(registered_hash, str) or re.fullmatch('[0-9a-f]{64}', registered_hash) is None
                or actual_hash != registered_hash):
            raise EvidenceError('Go traffic used a different registered proxy kernel: ' + engine)
    return {'suites': len(evidence), 'metrics': len(metrics), 'samples': sum(metrics.values())}


def compare_runs(before_directory, after_directory):
    before, after = load_run(before_directory), load_run(after_directory)
    v2 = 'role' in before or 'role' in after
    if v2 and (before.get('role') != 'baseline' or after.get('role') != 'candidate'):
        raise EvidenceError('V2 comparison requires baseline then candidate')
    for key in ('fixture_hash', 'scenario_hash', 'toolchain', 'toolchain_version',
                'browser_hash', 'browser_version', 'kernel_hashes', 'settings',
                'expected_commands') + (() if v2 else ('expected_metrics', 'expected_correctness_checks')):
        if not before.get(key) or before[key] != after.get(key):
            raise EvidenceError('Incomparable runs: ' + key)
    if before['run_id'] == after['run_id'] or before['source_hash'] == after['source_hash']:
        raise EvidenceError('Comparison requires distinct runs and product versions')
    if not v2 and before['metrics'].keys() != after['metrics'].keys():
        raise EvidenceError('Scenario coverage differs')
    comparisons = {}
    eligible_improvements = []
    failures = []
    for name, left in before['metrics'].items():
        right = after['metrics'][name]
        for key in METRIC_POLICY:
            if left.get(key) != right.get(key):
                raise EvidenceError(f'Metric policy changed: {name} {key}')
        result = compare_samples(left['samples'], right['samples'], left.get('absolute_noise', 0))
        comparisons[name] = result
        if left['core'] and result['status'] != 'pass':
            failures.append(name)
        if left['improvement_eligible'] and result['stable_improvement']:
            eligible_improvements.append(name)
    if not eligible_improvements:
        failures.append('no_stable_user_flow_improvement')
    result = {'status': 'pass' if not failures else 'fail', 'before_run': before['run_id'],
            'after_run': after['run_id'], 'comparisons': comparisons,
            'stable_user_flow_improvements': eligible_improvements, 'failures': failures}
    if v2:
        result['acceptance_version'] = before['acceptance']['version']
        result['goal_sha256'] = before['acceptance']['goal_sha256']
        result['baseline_pressure_reference'] = before['pressure_reference']
        result['candidate_only'] = {
            name: {'samples': len(metric['samples']), 'unit': metric['unit'],
                   'median': statistics.median(metric['samples']),
                   'p95': percentile(metric['samples'], .95),
                   'minimum': min(metric['samples']), 'maximum': max(metric['samples']),
                   'comparable_baseline': False, 'completion_and_correctness': 'pass'}
            for name, metric in after['metrics'].items() if name not in before['metrics']}
        result['candidate_only_limitation'] = (
            'Old 10000-entry catalog timed out. No comparable baseline, percentage change, '
            'CI, stable improvement or 5% regression conclusion is available for this branch.')
    return result
