"""Bind reported GUI latency to host receipts and actual Wails observation records."""
from pathlib import Path
import hashlib
import json
import math
import statistics

from evidence import EvidenceError, _artifact_path, _json_loads, digest_object


def _records(path):
    try:
        text = path.read_text(encoding='utf-8')
        if not text or not text.endswith('\n'):
            raise EvidenceError('Missing or truncated GUI journal')
        result = [_json_loads(line) for line in text.splitlines()]
    except (OSError, ValueError) as error:
        raise EvidenceError('Unreadable GUI journal') from error
    if any(not isinstance(item, dict) for item in result):
        raise EvidenceError('Malformed GUI journal record')
    return result


def gui_metric_names(report):
    return {name for name in report['expected_metrics']
            if (name.startswith('catalog.') and '.resources.' not in name)
            or (name.startswith('runtime.') and name.endswith('.application_ready'))}


def _milliseconds(received, started):
    if type(received) is not int or type(started) is not int or not 0 < started < received:
        raise EvidenceError('Invalid GUI host clock')
    return (received - started) / 1e6


def verify_operation(host, backend, sample):
    from runtime import validate_render_proof
    token = sample.get('token')
    if not isinstance(token, str) or not token:
        raise EvidenceError('Missing GUI metric token')
    host = [item for item in host if item.get('token') == token]
    backend = [item for item in backend if item.get('token') == token]
    starts = [item for item in host if item.get('event') == 'start']
    completes = [item for item in host if item.get('event') == 'complete']
    requests = [item for item in backend if item.get('event') == 'request']
    if (len(starts) != 1 or len(completes) != 1 or len(requests) != 1 or
            any(item.get('event') == 'failed' for item in host) or
            any(item.get('event') in ('timeout', 'cancelled') for item in backend)):
        raise EvidenceError('GUI sample lacks a unique successful host/backend operation')
    start, complete, request = starts[0], completes[0], requests[0]
    if (start.get('operation') != 'timed_gui' or request.get('stream') is not True or
            sample.get('operation_id') != start.get('operation_id') or
            complete.get('operation_id') != start.get('operation_id') or
            complete.get('pid') != start.get('pid') or
            request.get('code_sha256') != start.get('code_sha256') or
            complete.get('code_sha256') != start.get('code_sha256') or
            complete.get('request_id') != request.get('request_id')):
        raise EvidenceError('GUI host/backend identity or code differs')
    elapsed = _milliseconds(complete.get('received_monotonic_ns'), start.get('started_monotonic_ns'))
    timeout = start.get('timeout_seconds')
    if (type(timeout) not in (int, float) or not math.isfinite(timeout) or not 0 < timeout <= 1800
            or complete.get('elapsed_ms') != elapsed or not 0 < elapsed < timeout * 1000):
        raise EvidenceError('GUI elapsed time excludes or exceeds the host observation window')
    raw = [item for item in backend if item.get('event') == 'message']
    if any(item.get('request_id') != request['request_id'] or not isinstance(item.get('payload'), dict)
           for item in raw):
        raise EvidenceError('Malformed GUI backend result')
    messages = [item['payload'] for item in raw]
    if any(item.get('token') != token or item.get('ok') is not True or
           type(item.get('sequence')) is not int or item.get('kind') not in ('phase', 'result')
           for item in messages):
        raise EvidenceError('Failed or mismatched GUI backend result')
    final = [item for item in messages if item.get('kind') == 'result']
    if (len(final) != 1 or sorted(item['sequence'] for item in messages) != list(range(1, len(messages) + 1))
            or final[0]['sequence'] != len(messages) or complete.get('terminal_sequence') != len(messages)
            or digest_object(final[0].get('value')) != complete.get('value_sha256')):
        raise EvidenceError('GUI terminal result, sequence or hash differs')
    if start.get('require_render') is True:
        validate_render_proof(final[0]['value'])
    phases = [item for item in host if item.get('event') == 'phase']
    raw_phases = sorted((item for item in messages if item.get('kind') == 'phase'), key=lambda item: item['sequence'])
    names = start.get('expected_phases')
    if (not isinstance(names, list) or set(item.get('phase') for item in raw_phases) != set(names)
            or len(raw_phases) != len(names) or len(phases) != len(names) or len(names) != len(set(names))):
        raise EvidenceError('Missing or unexpected GUI phases')
    phase_ms = {}
    for raw_phase in raw_phases:
        matches = [item for item in phases if item.get('phase') == raw_phase['phase']]
        if len(matches) != 1:
            raise EvidenceError('Duplicate or missing GUI phase receipt')
        phase = matches[0]
        value = _milliseconds(phase.get('received_monotonic_ns'), start['started_monotonic_ns'])
        if (phase.get('sequence') != raw_phase['sequence'] or phase.get('value') != raw_phase.get('value')
                or phase.get('elapsed_ms') != value or phase['received_monotonic_ns'] > complete['received_monotonic_ns']):
            raise EvidenceError('GUI phase receipt differs from the raw callback')
        validate_render_proof(phase['value'])
        phase_ms[phase['phase']] = value
    if phase_ms != complete.get('phase_ms'):
        raise EvidenceError('GUI phase timing cache differs')
    if sample.get('application_ready'):
        ready = [item for item in host if item.get('event') == 'application_ready']
        if len(ready) != 1 or sample.get('phase') is not None:
            raise EvidenceError('Initial render lacks its host launch origin')
        ready = ready[0]
        if (ready.get('received_monotonic_ns') != complete['received_monotonic_ns'] or
                ready.get('application_started_monotonic_ns', 0) > start['started_monotonic_ns']):
            raise EvidenceError('Initial readiness receipt differs')
        value = _milliseconds(ready.get('received_monotonic_ns'), ready.get('application_started_monotonic_ns'))
        if ready.get('elapsed_ms') != value or value >= 1_800_000:
            raise EvidenceError('Initial render exceeded its original limit')
        return value
    if sample.get('phase') is not None:
        if sample['phase'] not in phase_ms:
            raise EvidenceError('GUI metric references an absent phase')
        return phase_ms[sample['phase']]
    return elapsed


def verify_gui(directory, report):
    directory = Path(directory).resolve()
    names = gui_metric_names(report)
    records = report.get('gui_samples')
    if not isinstance(records, dict) or records.keys() != names:
        raise EvidenceError('GUI metric provenance coverage differs')
    cache, used, count = {}, set(), 0
    for name in sorted(names):
        entries = records[name]
        if not isinstance(entries, list) or len(entries) != len(report['metrics'][name]['samples']):
            raise EvidenceError('GUI metric receipt count differs')
        actual = []
        for index, sample in enumerate(entries):
            if not isinstance(sample, dict):
                raise EvidenceError('Invalid GUI metric provenance')
            journal = _artifact_path(directory, sample.get('journal'))
            expected_journal, code = metric_operation(name, index)
            if sample.get('journal') != expected_journal:
                raise EvidenceError('Wrong GUI scale, round or host journal')
            if journal not in cache:
                cache[journal] = (_records(journal), _records(journal.parent / 'runtime/linuxperf-gui-events.jsonl'))
            identity = (sample['journal'], sample.get('token'), sample.get('phase'), sample.get('application_ready', False))
            if identity in used:
                raise EvidenceError('One GUI receipt reused as multiple metric samples')
            used.add(identity)
            initial = name.endswith(('.initial_render', '.application_ready'))
            if sample.get('application_ready', False) != initial:
                raise EvidenceError('GUI latency used the wrong host origin')
            phase = {'keyword_save_visible': 'keyword_visible', 'keyword_save_closed': 'keyword_closed'}.get(name.split('.')[-1])
            if sample.get('phase') != phase:
                raise EvidenceError('GUI metric used the wrong completion phase')
            host, backend = cache[journal]
            actual.append(verify_operation(host, backend, sample))
            start = next(item for item in host if item.get('token') == sample['token']
                         and item.get('event') == 'start')
            if start['code_sha256'] != hashlib.sha256(code.encode()).hexdigest():
                raise EvidenceError('GUI metric used a different business operation')
            if start.get('require_render') is not (not name.endswith('_api')):
                raise EvidenceError('GUI metric omitted its required rendered completion')
            if initial:
                try:
                    lifecycle = _json_loads((journal.parent / 'lifecycle.json').read_text())
                except (OSError, ValueError) as error:
                    raise EvidenceError('Missing GUI launch lifecycle') from error
                first = lifecycle[0] if isinstance(lifecycle, list) and lifecycle else {}
                ready = next(item for item in host if item.get('token') == sample['token']
                             and item.get('event') == 'application_ready')
                if (first.get('event') != 'start' or first.get('pid') != start['pid']
                        or ready.get('pid') != start['pid']
                        or ready['application_started_monotonic_ns'] != first.get('host_started_monotonic_ns')):
                    raise EvidenceError('GUI initial metric did not use the independent first launch')
            count += 1
        if actual != report['metrics'][name]['samples']:
            raise EvidenceError('GUI metric differs from raw host receipts: ' + name)
    for name in names:
        if name.endswith('.keyword_save_visible'):
            closed = records[name.removesuffix('visible') + 'closed']
            for visible, dismissed in zip(records[name], closed):
                if any(visible.get(key) != dismissed.get(key) for key in ('journal', 'token', 'operation_id')):
                    raise EvidenceError('Keyword visibility and closure did not observe the same save')
    return {'metrics': len(names), 'samples': count, 'journals': len(cache),
            'clock': 'host dispatch/launch through validated result receipt'}


def metric_operation(name, index):
    """Fixed business operations and independent round origins, not caller labels."""
    domain, amount, operation = name.split('.')
    size = int(amount) if domain == 'catalog' else 100
    if operation in ('initial_render', 'application_ready'):
        prefix = 'c' if domain == 'catalog' else 'r'
        proxies = 'true' if domain == 'catalog' else 'false'
        return f'{prefix}{amount}-{index}/operations.log', f'return await antPerfGui.initialReady({size},{proxies});'
    journal = f'c{size}-0/operations.log'
    if operation == 'profile_filter':
        code = 'return await antPerfGui.filterProfiles("性能实例 00042",["perf-profile-00042"]);'
    elif operation == 'profile_restore':
        ids = [f'perf-profile-{i:05d}' for i in range(size)]
        code = 'return await antPerfGui.filterProfiles("",' + json.dumps(ids) + ');'
    elif operation == 'proxy_filter':
        code = 'return await antPerfGui.filterProxies("性能代理 00042",["性能代理 00042"]);'
    elif operation == 'proxy_restore':
        names = [f'性能代理 {i:05d}' for i in range(size)]
        code = 'return await antPerfGui.filterProxies("性能代理",' + json.dumps(names, ensure_ascii=False) + ');'
    elif operation in ('keyword_save_visible', 'keyword_save_closed'):
        keyword = f'perf-updated-00042-{index % 2}'
        code = ('return await antPerfGui.savePreparedKeyword("perf-profile-00042","性能实例 00042",'
                + json.dumps(keyword) + ',linuxPerfPhase);')
    elif operation in ('profiles_api', 'groups_api', 'proxies_api'):
        kind = operation.removesuffix('_api')
        code = 'return await antPerfGui.catalogAPI(' + json.dumps(kind) + ',' + str(size) + ',true);'
    else:
        raise EvidenceError('Unknown GUI business metric: ' + name)
    return journal, code


def verify_catalog_observations(directory, report):
    from registration import catalog_sizes
    directory = Path(directory).resolve()
    count = 0
    for size in catalog_sizes(report['role']):
        root = directory / f'c{size}-0'
        try:
            events = _json_loads((root / 'event-refresh.json').read_text())
        except (OSError, ValueError) as error:
            raise EvidenceError('Missing catalog event refresh observations') from error
        host, backend = _records(root / 'operations.log'), _records(root / 'runtime/linuxperf-gui-events.jsonl')
        tokens = set()
        for kind, ok in (('success', True), ('failure', False)):
            timed = events.get(kind) if isinstance(events, dict) else None
            if not isinstance(timed, dict) or timed.get('token') in tokens:
                raise EvidenceError('Missing or reused catalog event transition')
            tokens.add(timed.get('token'))
            sample = {key: timed.get(key) for key in ('token', 'operation_id')}
            elapsed = verify_operation(host, backend, sample)
            complete = next(x for x in host if x.get('token') == timed['token'] and x.get('event') == 'complete')
            start = next(x for x in host if x.get('token') == timed['token'] and x.get('event') == 'start')
            value = timed.get('value')
            if (not isinstance(value, dict) or digest_object(value) != complete['value_sha256'] or
                    timed.get('elapsed_ms') != elapsed or value.get('profileId') != 'perf-profile-00042' or
                    value.get('proxyId') != 'perf-proxy-00042' or value.get('before') == value.get('after') or
                    value.get('result', {}).get('ok') is not ok or value.get('event', {}).get('ok') is not ok):
                raise EvidenceError('Catalog event did not refresh the intended real result view')
            result, event = value['result'], value['event']
            code = 'return await antPerfGui.verifyProxyEvent("perf-profile-00042",' + str(size) + ',' + json.dumps(ok) + ');'
            after = str(result.get('latencyMs')) + 'ms' if ok else '失败'
            if (start['code_sha256'] != hashlib.sha256(code.encode()).hexdigest()
                    or start.get('require_render') is not True
                    or result.get('proxyId') != value['proxyId'] or event.get('proxyId') != value['proxyId']
                    or any(result.get(key) != event.get(key) for key in ('ok', 'testedAt', 'latencyMs', 'error'))
                    or not result.get('testedAt') or value.get('after') != after):
                raise EvidenceError('Catalog observation differs from the actual backend event')
            count += 1
    if report['role'] == 'candidate':
        for index in range(10):
            root = directory / f'c10000-{index}'
            try:
                timed = _json_loads((root / 'resource-gui.json').read_text())
                resources = _json_loads((root / 'resources.json').read_text())
            except (OSError, ValueError) as error:
                raise EvidenceError('Missing candidate GUI resource-window completion') from error
            host, backend = _records(root / 'operations.log'), _records(root / 'runtime/linuxperf-gui-events.jsonl')
            sample = {key: timed.get(key) for key in ('token', 'operation_id')}
            elapsed = verify_operation(host, backend, sample)
            complete = next(x for x in host if x.get('token') == timed.get('token') and x.get('event') == 'complete')
            start = next(x for x in host if x.get('token') == timed.get('token') and x.get('event') == 'start')
            if (timed.get('elapsed_ms') != elapsed or digest_object(timed.get('value')) != complete['value_sha256'] or
                    start['started_monotonic_ns'] < resources['points'][-1]['finished_monotonic_ns'] or
                    start.get('require_render') is not True or start['code_sha256'] != hashlib.sha256(
                        b'return await antPerfGui.initialReady(10000,true);').hexdigest()):
                raise EvidenceError('Candidate GUI proof precedes or differs from its resource window')
            value = timed.get('value', {})
            if value.get('profiles', {}).get('count') != 10000 or value.get('proxies', {}).get('count') != 10001:
                raise EvidenceError('Candidate GUI lost the complete large catalog')
            count += 1
    return {'observations': count, 'backend_event_transitions': len(catalog_sizes(report['role'])) * 2,
            'large_catalog_post_resource_checks': 10 if report['role'] == 'candidate' else 0}


def verify_instance_actions(directory, report):
    from registration import SCENARIOS
    directory = Path(directory).resolve()
    samples = {}
    executed = 0
    for count in SCENARIOS['runtime_counts']:
        for index in range(SCENARIOS['independent_rounds']):
            root = directory / f'r{count}-{index}'
            try:
                actions = _json_loads((root / 'instance-actions.json').read_text())
                launches = _json_loads((root / 'browser-starts.json').read_text())
            except (OSError, ValueError) as error:
                raise EvidenceError('Missing real instance action evidence') from error
            expected = [(action, f'perf-profile-{i:05d}')
                        for action in ('start', 'stop') for i in range(count)]
            if not isinstance(actions, list) or [(x.get('action'), x.get('profile_id')) for x in actions] != expected:
                raise EvidenceError('Incomplete real instance action coverage')
            if not isinstance(launches, list) or len(launches) != count:
                raise EvidenceError('Missing real instance launch identities')
            host, backend = _records(root / 'operations.log'), _records(root / 'runtime/linuxperf-gui-events.jsonl')
            values = {'start': [], 'stop': []}
            tokens = set()
            for action in actions:
                timed = action.get('timed')
                if not isinstance(timed, dict) or timed.get('token') in tokens:
                    raise EvidenceError('Reused or missing instance action')
                tokens.add(timed.get('token'))
                sample = {key: timed.get(key) for key in ('token', 'operation_id')}
                elapsed = verify_operation(host, backend, sample)
                start = next(x for x in host if x.get('token') == timed['token'] and x.get('event') == 'start')
                complete = next(x for x in host if x.get('token') == timed['token'] and x.get('event') == 'complete')
                if (timed.get('elapsed_ms') != elapsed or timed.get('started_monotonic_ns') != start['started_monotonic_ns']
                        or timed.get('received_monotonic_ns') != complete['received_monotonic_ns']
                        or digest_object(timed.get('value')) != complete['value_sha256']):
                    raise EvidenceError('Instance action cache differs from its host/backend receipts')
                profile = timed['value']
                code = ('return await antPerfGui.instanceAction(' + json.dumps(action['profile_id']) + ','
                        + json.dumps(action['action'] == 'start') + ');')
                if start.get('require_render') is not True or start['code_sha256'] != hashlib.sha256(code.encode()).hexdigest():
                    raise EvidenceError('Instance receipt used a different UI operation')
                if (profile.get('profileId') != action['profile_id'] or
                        profile.get('running') is not (action['action'] == 'start')):
                    raise EvidenceError('Instance action returned the wrong profile state')
                if action['action'] == 'start':
                    launch = launches[len(values['start'])]
                    if (profile.get('debugReady') is not True or str(profile.get('pid')) != launch.get('pid')
                            or profile.get('debugPort') != launch.get('debug_port')
                            or launch.get('profile_id') != action['profile_id'] or
                            action.get('verified_url') != launch.get('verified_page_url') or
                            not isinstance(action.get('verified_url'), str) or
                            not action['verified_url'].startswith('http://127.0.0.1:') or
                            action.get('title') != 'Ant Linux Performance Fixture' or
                            action.get('content') != 'local deterministic page'):
                        raise EvidenceError('CDP page evidence differs from the running UI/profile')
                    ready = action.get('page_ready_monotonic_ns')
                    if type(ready) is not int or ready < timed['received_monotonic_ns']:
                        raise EvidenceError('CDP readiness precedes instance action completion')
                    elapsed = _milliseconds(ready, timed['started_monotonic_ns'])
                    if action.get('elapsed_ms') != elapsed or elapsed >= 1_800_000:
                        raise EvidenceError('Instance readiness timing differs')
                values[action['action']].append(elapsed)
                executed += 1
            for action, metric in (('start', 'instance_ready'), ('stop', 'stop')):
                if values[action]:
                    samples.setdefault(f'runtime.{count}.{metric}', []).append(statistics.median(values[action]))
    for name, values in samples.items():
        if report['metrics'].get(name, {}).get('samples') != values:
            raise EvidenceError('Real instance metric differs from its raw actions: ' + name)
    return {'rounds': len(SCENARIOS['runtime_counts']) * SCENARIOS['independent_rounds'],
            'real_actions': executed, 'metrics': len(samples)}
