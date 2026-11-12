"""Collect and revalidate fixed-duration resources from owned Linux processes."""
import json
import statistics
import time
from pathlib import Path

from evidence import EvidenceError, _finite_nonnegative, _json_loads
from registration import SCENARIOS
from runtime import call_socket, proc_snapshot

CATEGORIES = tuple(SCENARIOS['runtime']['categories'])


def capture_resources(app, warmup=15, duration=30, expected_browsers=()):
    record = {'schema': 1, 'runtime_root': str(app.runtime),
              'started_identity': list(app.started_identity),
              'started_ns': time.time_ns(), 'warmup_started_ns': time.monotonic_ns(),
              'warmup_seconds': warmup, 'sample_seconds': duration,
              'interval_seconds': 1, 'expected_browsers': [list(x) for x in expected_browsers],
              'points': []}
    time.sleep(warmup)
    try:
        origin = time.monotonic_ns()
        for index in range(duration + 1):
            remaining = (origin + index * 1_000_000_000 - time.monotonic_ns()) / 1e9
            if remaining > 0:
                time.sleep(remaining)
            sampled_at = time.monotonic_ns()
            if index == 0:
                origin = sampled_at
            snapshot = proc_snapshot(app.process.pid, app.runtime)
            app.owned.update((pid, item['start']) for pid, item in snapshot.items())
            state = call_socket(app.socket, '/state')
            if state.get('pid') != app.process.pid or state.get('root') != str(app.runtime):
                raise EvidenceError('Resource sample came from a different application')
            record['points'].append({'monotonic_ns': sampled_at,
                                     'finished_monotonic_ns': time.monotonic_ns(),
                                     'processes': snapshot, 'goroutines': state['goroutines']})
    finally:
        record['finished_ns'] = time.time_ns()
        (app.output / 'resources.json').write_text(json.dumps(record, indent=2) + '\n')
    summarize_resources(record, formal=(warmup == 15 and duration == 30))
    return record


def summarize_resources(record, formal=True):
    if not isinstance(record, dict) or record.get('schema') != 1:
        raise EvidenceError('Invalid resource schema')
    warmup, duration = record.get('warmup_seconds'), record.get('sample_seconds')
    if (type(warmup) is not int or type(duration) is not int or warmup < 1 or duration < 1
            or record.get('interval_seconds') != 1 or
            (formal and (warmup != 15 or duration != 30))):
        raise EvidenceError('Resource window differs from registration')
    identity = record.get('started_identity')
    if (not isinstance(identity, list) or len(identity) != 2 or
            not isinstance(identity[0], str) or not identity[0].isdigit() or int(identity[0]) < 1
            or type(identity[1]) is not int or identity[1] < 1):
        raise EvidenceError('Invalid resource application identity')
    browsers = record.get('expected_browsers')
    if (not isinstance(browsers, list) or any(not isinstance(item, list) or len(item) != 2 or
            not isinstance(item[0], str) or not item[0].isdigit() or int(item[0]) < 1 or
            type(item[1]) is not int or item[1] < 1 for item in browsers) or
            len({tuple(item) for item in browsers}) != len(browsers)):
        raise EvidenceError('Invalid expected browser identities')
    root = record.get('runtime_root')
    if not isinstance(root, str) or not Path(root).is_absolute():
        raise EvidenceError('Invalid resource runtime root')
    first, last = record.get('started_ns'), record.get('finished_ns')
    start = record.get('warmup_started_ns')
    if (any(type(x) is not int or x < 1 for x in (first, last, start)) or first >= last
            or last - first < (warmup + duration) * 1_000_000_000):
        raise EvidenceError('Invalid resource timestamps')
    points = record.get('points')
    if not isinstance(points, list) or len(points) != duration + 1:
        raise EvidenceError('Missing resource samples')
    previous_time, previous_finish, previous = start, start, {}
    for index, point in enumerate(points):
        if not isinstance(point, dict):
            raise EvidenceError('Invalid resource point')
        tick = point.get('monotonic_ns')
        done = point.get('finished_monotonic_ns')
        scheduled = start + warmup * 1_000_000_000 if index == 0 else points[0]['monotonic_ns'] + index * 1_000_000_000
        if (type(tick) is not int or tick < scheduled or tick <= previous_time or tick < previous_finish
                or type(done) is not int or done < tick):
            raise EvidenceError('Truncated resource warmup or sample interval')
        if type(point.get('goroutines')) is not int or point['goroutines'] < 1:
            raise EvidenceError('Missing goroutine sample')
        snapshot = point.get('processes')
        if not isinstance(snapshot, dict) or not snapshot:
            raise EvidenceError('Empty process sample')
        for pid, item in snapshot.items():
            if not isinstance(pid, str) or not pid.isdigit() or int(pid) < 1 or not isinstance(item, dict):
                raise EvidenceError('Invalid resource process identity')
            if (type(item.get('start')) is not int
                    or item['start'] < 1 or item.get('category') not in CATEGORIES
                    or not isinstance(item.get('state'), str) or len(item['state']) != 1):
                raise EvidenceError('Invalid resource process classification')
            for key in ('cpu', 'pss', 'rss', 'fds', 'rss_hwm'):
                if not _finite_nonnegative(item.get(key)):
                    raise EvidenceError('Missing or nonfinite resource field: ' + key)
            if type(item['fds']) is not int:
                raise EvidenceError('Invalid file descriptor count')
            old = previous.get(pid)
            if old and old['start'] == item['start']:
                if old['category'] != item['category'] or old['cpu'] > item['cpu']:
                    raise EvidenceError('Resource identity or CPU counter changed')
        application = snapshot.get(identity[0])
        if (not application or application['start'] != identity[1]
                or application['category'] != 'app' or application['state'] in ('Z', 'X')
                or application['rss'] <= 0 or application['pss'] <= 0):
            raise EvidenceError('Missing live application resource evidence')
        if not any(item['category'] == 'webview' and item['pss'] > 0 and
                   item['rss'] > 0 and item['state'] not in ('Z', 'X')
                   for item in snapshot.values()):
            raise EvidenceError('Missing real WebView resource evidence')
        for browser in browsers:
            item = snapshot.get(browser[0])
            if (not item or item['start'] != browser[1] or item['category'] != 'browser' or
                    item['state'] in ('Z', 'X') or item['pss'] <= 0 or item['rss'] <= 0):
                raise EvidenceError('Started browser did not survive the complete resource window')
        if not browsers and any(item['category'] == 'browser' and item['state'] not in ('Z', 'X')
                                for item in snapshot.values()):
            raise EvidenceError('Unexpected browser in zero-instance resource scenario')
        previous_time, previous_finish, previous = tick, done, snapshot
    elapsed = (points[-1]['monotonic_ns'] - points[0]['monotonic_ns']) / 1e9
    result = {'goroutines': statistics.median(point['goroutines'] for point in points),
              'sampling_elapsed_seconds': elapsed}
    for category in CATEGORIES:
        totals = {key: [] for key in ('pss', 'rss', 'fds', 'processes', 'rss_hwm')}
        cpu, previous = 0, points[0]['processes']
        for index, point in enumerate(points):
            snapshot = point['processes']
            items = [item for item in snapshot.values() if item['category'] == category]
            for key in totals:
                totals[key].append(len(items) if key == 'processes' else sum(item[key] for item in items))
            if index:
                for pid, item in snapshot.items():
                    old = previous.get(pid)
                    if item['category'] == category:
                        cpu += item['cpu'] - old['cpu'] if old and old['start'] == item['start'] else item['cpu']
            previous = snapshot
        result[category + '.raw_cpu'] = cpu
        result[category + '.cpu'] = cpu * duration / elapsed
        for key in ('pss', 'rss', 'fds', 'processes'):
            result[category + '.' + key] = statistics.median(totals[key][1:])
        result[category + '.observed_pss_peak'] = max(totals['pss'])
        result[category + '.observed_rss_peak'] = max(totals['rss'])
        # Per-process lifetime high-water marks need not occur simultaneously.
        # This sum is an explicit upper bound, not a simultaneous memory peak.
        result[category + '.rss_hwm_upper_bound'] = max(totals['rss_hwm'])
    return result


def resource_inventory(role):
    expected = {f'r{count}-{index}/resources.json': f'runtime.{count}'
                for count in SCENARIOS['runtime_counts']
                for index in range(SCENARIOS['independent_rounds'])}
    if role == 'candidate':
        expected.update({f'c10000-{index}/resources.json': 'catalog.10000.resources'
                         for index in range(SCENARIOS['independent_rounds'])})
    return expected


def verify_resources(directory, report):
    directory = Path(directory).resolve()
    expected = resource_inventory(report.get('role'))
    actual = {str(path.relative_to(directory)) for path in directory.glob('*/resources.json')}
    if actual != expected.keys():
        raise EvidenceError('Resource file coverage differs from registered rounds')
    summaries, samples, identities = {}, {}, set()
    for name, prefix in expected.items():
        path = directory / name
        try:
            record = _json_loads(path.read_text())
            lifecycle = _json_loads((path.parent / 'lifecycle.json').read_text())
        except (OSError, ValueError) as error:
            raise EvidenceError('Missing or invalid resource evidence') from error
        values = summarize_resources(record)
        expected_count = int(path.parent.name.split('-')[0][1:]) if prefix.startswith('runtime.') else 0
        if len(record['expected_browsers']) != expected_count:
            raise EvidenceError('Resource browser count differs from registered scenario')
        if prefix.startswith('runtime.'):
            try:
                starts = _json_loads((path.parent / 'browser-starts.json').read_text())
            except (OSError, ValueError) as error:
                raise EvidenceError('Missing real browser launch identities') from error
            if (not isinstance(starts, list) or len(starts) != expected_count or
                    [item.get('profile_id') for item in starts] != [f'perf-profile-{i:05d}' for i in range(expected_count)] or
                    [[item.get('pid'), item.get('start')] for item in starts] != record['expected_browsers']):
                raise EvidenceError('Resource browser identities differ from the launched profiles')
        identity = tuple(record['started_identity'])
        if identity in identities:
            raise EvidenceError('Resource rounds reused an application identity')
        identities.add(identity)
        if (record['runtime_root'] != str(path.parent / 'runtime') or
                not isinstance(lifecycle, list) or len(lifecycle) < 2 or
                lifecycle[0].get('event') != 'start' or lifecycle[1].get('event') != 'close' or
                lifecycle[0].get('started_identity') != record['started_identity'] or
                not lifecycle[0]['time_ns'] <= record['started_ns'] < record['finished_ns'] <= lifecycle[1]['time_ns']):
            raise EvidenceError('Resource window does not match its application lifecycle')
        summaries[name] = values
        for key, value in values.items():
            metric = prefix + '.' + key
            if metric in report['expected_metrics']:
                samples.setdefault(metric, []).append(value)
    if report.get('resource_evidence') != summaries:
        raise EvidenceError('Cached resource summary differs from raw evidence')
    for metric, values in samples.items():
        if report['metrics'].get(metric, {}).get('samples') != values:
            raise EvidenceError('Resource metric differs from raw samples: ' + metric)
    return {'files': len(summaries), 'rounds': len(identities),
            'warmup_seconds': 15, 'sample_seconds': 30, 'points_per_round': 31}
