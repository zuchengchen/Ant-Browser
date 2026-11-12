"""Authenticate one approved historical failure, never a successful benchmark.

The immutable reference anchor is deliberately specific to np-large. Supplying
another directory, a self-consistent forged manifest, or a candidate result is
not an alternative way to qualify for the baseline-only exception.
"""
import hashlib
import json
import math
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat

GOAL_SHA256 = '9d3cb85cfbdd0d52a76f2b519a99b7b47c44edc71b54d8727d07a02d19cfd571'
BASELINE_SOURCE_SHA256 = 'b62741030294e2481b33c4350d6bfd72187e6f05fa66237c924a5e8c75785389'
APPROVED_REFERENCE_SHA256 = 'f32cbbdcd17757cd7792e55f9a59257e14b5c1c7ab5a961d57be046244adb8ea'
RUN_ID = 'native-168f96d0f8b44328b016f2efc474bc66'
OLD_GOAL_SHA256 = '6d64e45f1624a5f79b3b00376a99f4349f486a9d1985a7ae4e22a8a5b1337f72'
REVIEWED_WORKSPACE_SHA256 = 'a6bff362fb74147a6c61ca72dd64a9d3d2d10360836b4148b03f22b6fe75f991'
SCOPE = {'role': 'baseline', 'catalog_size': 10000, 'branch': 'catalog_gui',
         'existing_failure_only': True, 'whole_before_exempt': False,
         'candidate_exempt': False, 'numeric_comparison_available': False}
JSON_MEMBERS = ('approval', 'run', 'producer', 'lifecycle', 'parent_review', 'build',
                'build_baseline-source-manifest.json',
                'build_instrumented-source-manifest.json',
                'build_postbuild-source-manifest.json',
                'build_frozen-fixture-manifest.json',
                'build_diagnostic-fixture-manifest.json')
JSONL_MEMBERS = ('events', 'operations', 'monitor', 'host_memory', 'threads')
TEXT_MEMBERS = ('producer_stdout', 'producer_stderr', 'build_go-version.stdout')


class PressureEvidenceError(ValueError):
    """Historical pressure evidence is unavailable, altered, or misapplied."""


def require(condition, message):
    if not condition:
        raise PressureEvidenceError(message)


def _integer(value, label, minimum=0):
    require(type(value) is int and value >= minimum, 'Invalid integer: ' + label)
    return value


def _number(value, label, minimum=0):
    require(type(value) in (int, float) and math.isfinite(value) and value >= minimum,
            'Invalid numeric field: ' + label)
    return value


def _finite_tree(value):
    if isinstance(value, dict):
        for item in value.values():
            _finite_tree(item)
    elif isinstance(value, list):
        for item in value:
            _finite_tree(item)
    elif isinstance(value, float):
        require(math.isfinite(value), 'Nonfinite JSON number')


def _json(text):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, 'Duplicate JSON key: ' + key)
            result[key] = value
        return result
    def invalid(value):
        raise PressureEvidenceError('Nonfinite JSON constant: ' + value)
    try:
        result = json.loads(text, object_pairs_hook=pairs, parse_constant=invalid)
    except (ValueError, UnicodeError) as error:
        if isinstance(error, PressureEvidenceError):
            raise
        raise PressureEvidenceError('Invalid or truncated JSON') from error
    _finite_tree(result)
    return result


def _decode(raw, label, lines=False):
    require(raw and raw.endswith(b'\n') and b'\x00' not in raw,
            'Empty or truncated evidence: ' + label)
    try:
        text = raw.decode('utf-8')
    except UnicodeError as error:
        raise PressureEvidenceError('Non-UTF-8 evidence: ' + label) from error
    if lines:
        require(all(line.strip() for line in text.splitlines()), 'Blank JSONL record: ' + label)
        return [_json(line) for line in text.splitlines()]
    return _json(text)


def _object_hash(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False).encode()).hexdigest()


def _path(root, relative):
    require(type(relative) is str and relative and '\\' not in relative and '\x00' not in relative,
            'Invalid reference member path')
    parts = relative.split('/')
    require(not PurePosixPath(relative).is_absolute() and
            all(part not in ('', '.', '..') for part in parts), 'Escaping reference member path')
    target = root.joinpath(*parts)
    for path in (target, *target.parents):
        require(not path.is_symlink(), 'Symlink in evidence path: ' + str(path))
    return target


def _read_regular(path, retain=False):
    require(not path.is_symlink(), 'Symlink evidence file: ' + str(path))
    try:
        # O_NONBLOCK prevents a forged FIFO from hanging before fstat rejects it.
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, 'rb') as source:
            start = os.fstat(source.fileno())
            require(stat.S_ISREG(start.st_mode), 'Non-regular evidence file')
            digest, count, chunks, header = hashlib.sha256(), 0, [], b''
            for data in iter(lambda: source.read(1024 * 1024), b''):
                digest.update(data)
                count += len(data)
                if len(header) < 20:
                    header += data[:20 - len(header)]
                if retain:
                    chunks.append(data)
            end = os.fstat(source.fileno())
            require((start.st_dev, start.st_ino, start.st_size, start.st_mtime_ns) ==
                    (end.st_dev, end.st_ino, end.st_size, end.st_mtime_ns) and count == end.st_size,
                    'Evidence changed while reading')
    except OSError as error:
        raise PressureEvidenceError('Cannot read evidence file: ' + str(path)) from error
    return {'sha256': digest.hexdigest(), 'bytes': count, 'header': header}, b''.join(chunks)


def _load_reference(path):
    path = Path(path).absolute()
    for part in (path, *path.parents):
        require(not part.is_symlink(), 'Symlink reference path')
    info, raw = _read_regular(path, True)
    require(info['sha256'] == APPROVED_REFERENCE_SHA256, 'Unapproved pressure reference anchor')
    reference = _decode(raw, 'reference')
    require(type(reference['schema']) is int and reference['schema'] == 1 and
            reference['kind'] == 'approved_linux_baseline_catalog_pressure', 'Wrong reference kind')
    require(reference['scope'] == SCOPE and reference['run_id'] == RUN_ID,
            'Pressure exception scope or run differs')
    require(reference['goal_sha256'] == GOAL_SHA256 and
            reference['baseline_source_sha256'] == BASELINE_SOURCE_SHA256, 'Reference target differs')
    files = reference['files']
    require(isinstance(files, dict) and len(files) == 118, 'Incomplete reference members')
    paths, origins = set(), set()
    for identity, entry in files.items():
        require(type(identity) is str and set(entry) == {'path', 'sha256', 'bytes', 'origin'},
                'Invalid reference member')
        _path(path.parent, entry['path'])
        require(entry['path'] not in paths and entry['origin'] not in origins,
                'Duplicate reference member path or origin')
        paths.add(entry['path']); origins.add(entry['origin'])
        require(type(entry['sha256']) is str and re.fullmatch('[0-9a-f]{64}', entry['sha256']),
                'Invalid reference member digest')
        _integer(entry['bytes'], 'member bytes')
    return path, reference


def _load_evidence(path, reference):
    documents, observed = {}, {}
    retained = set(JSON_MEMBERS + JSONL_MEMBERS + TEXT_MEMBERS)
    for identity, entry in reference['files'].items():
        info, raw = _read_regular(_path(path.parent, entry['path']), identity in retained)
        require(info['sha256'] == entry['sha256'] and info['bytes'] == entry['bytes'],
                'Evidence hash/size mismatch: ' + identity)
        observed[identity] = info
        if identity in JSON_MEMBERS or identity in JSONL_MEMBERS:
            documents[identity] = _decode(raw, identity, identity in JSONL_MEMBERS)
        elif identity in TEXT_MEMBERS:
            documents[identity] = raw.decode('utf-8')
    return documents, observed


def _identity(value):
    require(isinstance(value, list) and len(value) == 2 and type(value[0]) is str and
            value[0].isdecimal() and int(value[0]) > 0, 'Invalid process identity')
    _integer(value[1], 'process start ticks', 1)
    return tuple(value)


def _clock(value):
    require(isinstance(value, dict) and set(value) == {'wall_ns', 'monotonic_ns'}, 'Invalid clock pair')
    return (_integer(value['wall_ns'], 'wall clock', 1),
            _integer(value['monotonic_ns'], 'monotonic clock', 1))


def _validate_build(reference, doc, observed):
    files, build = reference['files'], doc['build']
    require(build['status'] == 'complete' and build['diagnostic'] is True and
            build['gui_started'] is False and build['performance_executed'] is False, 'Invalid diagnostic build')
    require(build['product_source_hash'] == BASELINE_SOURCE_SHA256 and
            build['toolchain'] == 'go1.24.13' and
            build['toolchain_version'] == 'go version go1.24.13 linux/amd64', 'Build target/toolchain differs')
    require(doc['build_go-version.stdout'].strip() == build['toolchain_version'], 'Build Go output differs')
    require(build['unexpected_prebuild_source_changes'] == build['unexpected_postbuild_source_changes'] ==
            build['build_generated_source_changes'] == {}, 'Unexpected product transformation')
    baseline = doc['build_baseline-source-manifest.json']
    instrumented = doc['build_instrumented-source-manifest.json']
    postbuild = doc['build_postbuild-source-manifest.json']
    require(_object_hash(baseline) == BASELINE_SOURCE_SHA256 and
            _object_hash(instrumented) == build['instrumented_source_hash'] and
            _object_hash(postbuild) == build['postbuild_source_hash'] and instrumented == postbuild,
            'Build source manifests differ')
    transformed = dict(baseline)
    for name, change in build['allowed_prepare_transformations'].items():
        require(transformed.get(name) == change['before'], 'Invalid prebuild transformation input')
        transformed[name] = change['after']
    require(transformed == instrumented, 'Instrumented source not derived from baseline')
    frozen = doc['build_frozen-fixture-manifest.json']
    fixture = doc['build_diagnostic-fixture-manifest.json']
    require(_object_hash(frozen) == build['frozen_fixture_hash'] and
            _object_hash(fixture) == build['diagnostic_fixture_hash'], 'Build fixture digest differs')
    amended = dict(frozen)
    for name, change in build['fixture_overrides'].items():
        require(amended.get(name) == change['before'], 'Invalid fixture override input')
        amended[name] = change['after']
    require(amended == fixture, 'Unexpected fixture change')
    require(build['seed_matches_frozen_source'] is True and
            files['seed_source']['sha256'] == build['seed_source_sha256'], 'Seed source differs')
    for kind in ('binary', 'seed'):
        require(files[kind]['origin'] == build[kind] and files[kind]['sha256'] == build[kind + '_sha256'],
                'Build binary identity differs')
        header = observed[kind]['header']
        require(header[:6] == b'\x7fELF\x02\x01' and header[18:20] == b'\x3e\x00', 'Non-amd64 ELF')
    require(len(build['commands']) == 12 and len(build['artifacts']) == 31, 'Incomplete build producers')
    names = set()
    for command in build['commands']:
        require(command['name'] not in names, 'Duplicate build producer'); names.add(command['name'])
        require(type(command['exit_code']) is int and command['exit_code'] == 0, 'Failed build producer')
        for stream in ('stdout', 'stderr'):
            entry = files['build_' + command[stream]]
            require(entry['sha256'] == command[stream + '_sha256'] and
                    entry['bytes'] == command[stream + '_bytes'], 'Build producer log differs')
    for artifact in build['artifacts']:
        entry = files['seed' if artifact['path'] == 'seed' else 'build_' + artifact['path']]
        require(entry['sha256'] == artifact['sha256'] and entry['bytes'] == artifact['bytes'], 'Build artifact differs')
    wails = next(command for command in build['commands'] if command['name'] == 'wails-build')
    require(wails['argv'] == ['go', 'run', 'github.com/wailsapp/wails/v2/cmd/wails@v2.12.0',
                             'build', '-s', '-platform', 'linux/amd64', '-tags', 'webkit2_41', '-o', 'ant-chrome'],
            'Wrong Wails build command')


def _validate_evidence(reference, doc, observed):
    """Validate structured facts after provenance authentication (also calibrated independently)."""
    files, run, producer, review = reference['files'], doc['run'], doc['producer'], doc['parent_review']
    approval = doc['approval']
    require(approval['sha256'] == GOAL_SHA256 and approval['bytes'] == files['goal']['bytes'] and
            files['goal']['sha256'] == GOAL_SHA256 and approval['exact_readback'] is True and
            approval['execution_intent'] == 'save_and_start' and approval['approved_response'] == '1' and
            approval['predecessor_sha256'] == OLD_GOAL_SHA256, 'Goal approval receipt differs')
    _validate_build(reference, doc, observed)
    require(run['diagnostic'] is True and run['status'] == 'failed' and run['run_id'] == RUN_ID and
            run['product_source_hash'] == BASELINE_SOURCE_SHA256, 'Not the approved failed baseline diagnostic')
    require(type(run['size']) is int and run['size'] == 10000 and type(run['pairs']) is int and
            run['pairs'] == 6 and type(run['timeout_seconds']) is int and run['timeout_seconds'] == 1800,
            'Wrong catalog size, pair count, or timeout')
    require(producer['diagnostic'] is True and type(producer['exit_code']) is int and
            producer['exit_code'] == 1, 'Pressure producer did not naturally exit 1')
    require(not any(k in producer for k in ('wrapper_error', 'wrapper_traceback', 'interrupted', 'signal')),
            'Interrupted wrapper is not a natural timeout')
    require(producer['argv'] == ['/home/czc/opt/miniconda3/bin/python3', '-u', '-B', files['driver']['origin'],
                                '--size', '10000', '--pairs', '6', '--timeout-seconds', '1800',
                                '--output', '/tmp/ant-linux-perf-cntr19l1/np-large'], 'Wrong pressure producer command')
    expected_inputs = {files[k]['origin']: files[k]['sha256'] for k in ('binary', 'seed', 'build', 'observer', 'driver', 'runtime')}
    require(run['inputs'] == expected_inputs, 'Runtime inputs do not match baseline build and tools')
    driver_inputs = {files[k]['origin']: files[k]['sha256'] for k in ('driver', 'observer')}
    require(producer['inputs'] == producer['inputs_after'] == driver_inputs and
            producer['wrapper_sha256'] == files['wrapper']['sha256'], 'Producer inputs changed')
    for stream in ('stdout', 'stderr'):
        require(producer[stream + '_sha256'] == files['producer_' + stream]['sha256'], 'Producer log digest differs')
    expected_output = 'NATIVE READY 10000\n' + ''.join(
        f'NATIVE {name} COMPLETE {pair}\n' for pair in range(1, 4) for name in ('FILTER', 'RESTORE'))
    require(doc['producer_stdout'] == expected_output and doc['producer_stderr'] == '', 'Producer completion output differs')
    start, finish = _clock(run['started']), _clock(run['finished'])
    require(start[0] < finish[0] and start[1] < finish[1], 'Reversed run clocks')
    require(_integer(producer['started_ns'], 'producer start', 1) <= start[0] < finish[0] <=
            _integer(producer['finished_ns'], 'producer finish', 1), 'Producer/run clocks differ')
    token = RUN_ID + ':3:filter'
    error = 'EvidenceError: Native stage timeout without retry: ' + token + '/native_paste'
    require(len(run['errors']) == 1 and run['errors'][0]['error'] == error, 'Wrong primary failure')
    traceback = run['errors'][0]['traceback']
    require(type(traceback) is str and traceback.endswith(error + '\n') and
            'raise EvidenceError' in traceback and 'KeyboardInterrupt' not in traceback and
            'SystemExit' not in traceback, 'No unambiguous natural timeout traceback')
    cleanup_error = 'application: EvidenceError: Application exceeded graceful shutdown timeout; Forced cleanup of controlled processes; Application exit code was -9'
    require(run['cleanup_errors'] == [cleanup_error], 'Cleanup failure was omitted or changed')
    require(len(run['commands']) == 31, 'Missing or repeated native helper')
    names = ['screen-saver-reset', 'screen-saver-off', 'display-state', 'window-search', 'window-owner',
             'window-geometry', 'clipboard-readback', 'window-focus', 'focus-query', 'click-input']
    for index in range(7):
        names += ['verify-window-owner', 'verify-focus', 'paste-once' if index % 2 == 0 else 'clear-once']
    command_identities = set()
    previous = start[1]
    for index, command in enumerate(run['commands']):
        require(command['name'] == names[index] and type(command['returncode']) is int and
                command['returncode'] == 0 and command['reaped'] is True and
                'forced_kill' not in command and 'error' not in command, 'Native helper failed or changed')
        begin, end = _clock(command['start']), _clock(command['end'])
        require(start[0] <= begin[0] <= end[0] <= finish[0] and
                previous <= begin[1] <= end[1] <= finish[1], 'Invalid native helper clock')
        previous = end[1]
        command_identities.add((str(_integer(command['pid'], 'helper pid', 1)),
                                _integer(command['process_start_ticks'], 'helper start', 1)))
        for stream in ('stdout', 'stderr'):
            require(command[stream] == files[f'command_{index:02d}_{stream}']['origin'], 'Native command output identity differs')
        if index >= 10 and (index - 10) % 3 == 2:
            operation_index = (index - 12) // 3
            name = 'filter' if operation_index % 2 == 0 else 'restore'
            require(command['token'] == RUN_ID + f':{operation_index // 2}:{name}', 'Native operation target differs')
            expected_argv = ['xdotool', 'key', '--clearmodifiers'] + (['ctrl+v'] if name == 'filter' else ['ctrl+a', 'BackSpace'])
            require(command['argv'] == expected_argv, 'Native edit was retried, redirected, or changed')
    last = run['commands'][-1]
    require(last['token'] == token, 'Wrong final operation')
    close_clock = _clock(run['clipboard_pre_close']['clock'])
    elapsed = (close_clock[1] - last['start']['monotonic_ns']) / 1e9
    elapsed_wall = (close_clock[0] - last['start']['wall_ns']) / 1e9
    require(1800 <= elapsed < 1801 and abs(elapsed_wall - elapsed) < .1,
            'Timeout window shortened, extended, interrupted, or inconsistent')
    require(run['clipboard_pre_close']['returncode'] is None and
            run['clipboard_pre_close']['owner'] == run['clipboard_owner'] and
            run['clipboard_close']['returncode'] == -15 and run['clipboard_close']['owner_after'] == 0,
            'Clipboard ownership/cleanup evidence differs')
    require(run['display_close']['returncode'] == 0 and run['display_close']['reaped'] is True and
            run['display_close']['forced_shutdown'] is False, 'Display cleanup failed')
    raw = doc['events']
    require(len(raw) == 66 and [e['sequence'] for e in raw] == list(range(1, 67)) and
            all(type(e['sequence']) is int for e in raw), 'Raw event stream missing or noncontiguous')
    require(not any(e['op_id'] == token for e in raw), 'Timed-out operation has unexpected completion/activity')
    native = [e for e in raw if e['stage'].startswith('native_')]
    require(len(native) == 58 and len(run['native_events']) == 58, 'Incomplete native event evidence')
    observed_events = [{k: v for k, v in e.items() if k != 'host_observed'} for e in run['native_events']]
    require(observed_events == native, 'Copied events differ from raw event journal')
    require(sorted(e['details']['local_sequence'] for e in native) == list(range(1, 59)) and
            all(type(e['details']['local_sequence']) is int for e in native), 'Native JS sequence gap/duplicate')
    for event in raw:
        _integer(event['backend_time_ns'], 'backend time', 1)
        _integer(event['backend_elapsed_ns'], 'backend elapsed')
        require(start[0] <= event['backend_time_ns'] <= finish[0], 'Event time outside diagnostic')
        for key in ('performance_now_ms', 'performance_time_origin_ms'):
            if key in event:
                _number(event[key], key)
    for event in run['native_events']:
        require(event['details']['run_id'] == RUN_ID and event['details']['host_token'] == event['op_id'],
                'Native event target differs')
        observed_clock = _clock(event['host_observed'])
        require(start[1] <= observed_clock[1] <= close_clock[1], 'Event received outside live window')
    ids = [f'perf-profile-{i:05d}' for i in range(10000)]
    ready = [e for e in native if e['stage'] == 'native_catalog_ready']
    require(len(ready) == 1 and ready[0]['op_id'] == RUN_ID + ':setup' and
            sorted(ready[0]['details']['ids']) == ids and ready[0]['details']['input']['value'] == '',
            'No complete 10000-profile ready state')
    require(len(run['operations']) == 6, 'Expected precisely six completed operations')
    for index, operation in enumerate(run['operations']):
        pair, name = index // 2, ('filter' if index % 2 == 0 else 'restore')
        op_token, value = RUN_ID + f':{pair}:{name}', ('性能实例 00042' if name == 'filter' else '')
        expected_ids = ['perf-profile-00042'] if name == 'filter' else ids
        require(type(operation['pair_index']) is int and operation['pair_index'] == pair and
                operation['name'] == name and operation['host_token'] == op_token and
                operation['run_id'] == RUN_ID and sorted(operation['ids']) == expected_ids,
                'Completed operation target or complete result differs')
        stages = ['native_beforeinput', 'native_input', 'native_predicate_true',
                  'native_first_frame', 'native_second_frame', 'native_complete']
        if name == 'filter':
            stages.insert(0, 'native_paste')
        found = {}
        for stage in stages:
            matches = [e for e in native if e['op_id'] == op_token and e['stage'] == stage]
            require(len(matches) == 1, 'Missing/duplicate operation stage')
            found[stage] = matches[0]
        ordered = [found[stage]['details']['local_sequence'] for stage in stages]
        require(ordered == sorted(ordered), 'Reordered native operation stages')
        require({k: v for k, v in operation.items() if k != 'diagnostic_timing'} ==
                found['native_complete']['details'], 'Completed operation differs from raw result')
        for field, expected in (('beforeinput_count', 1), ('input_count', 1), ('paste_count', 1 if name == 'filter' else 0)):
            require(type(operation[field]) is int and operation[field] == expected, 'Wrong input execution count')
        for stage in ('native_beforeinput', 'native_input'):
            details = found[stage]['details']
            require(details['is_trusted'] is True and details['input_type'] ==
                    ('insertFromPaste' if name == 'filter' else 'deleteContentBackward'), 'Untrusted or wrong input')
            require(all(details['input'][k] is True for k in ('connected', 'same_current_input', 'active_target')),
                    'Wrong input focus/identity')
        require(found['native_input']['details']['input']['value'] == value and
                all(operation['input'][k] == value for k in ('value', 'defaultValue', 'keyword')) and
                operation['input']['visibility'] == 'visible', 'Wrong controlled input result')
        if name == 'filter':
            require(found['native_paste']['details']['is_trusted'] is True and
                    found['native_paste']['details']['text'] == value, 'Wrong trusted Unicode paste')
        _number(operation['input_to_render_ms'], 'input_to_render_ms')
        for field, timing in operation['diagnostic_timing'].items():
            if field.endswith('_ms') and not isinstance(timing, dict):
                _number(timing, field)
    life = doc['lifecycle']
    require(len(life) == 2 and [entry['event'] for entry in life] == ['start', 'close'], 'Incomplete application lifecycle')
    opened, closed = life
    identity = _identity(run['application_identity'])
    require(_identity(opened['started_identity']) == _identity(closed['started_identity']) == identity and
            opened['pid'] == closed['pid'] == int(identity[0]), 'Application lifecycle identity differs')
    expected_root = '/tmp/ant-linux-perf-cntr19l1/np-large/app/runtime'
    require(opened['root'] == closed['root'] == expected_root and
            files['runtime_marker']['origin'] == expected_root + '/.linux-perf-runtime', 'Wrong isolated runtime ownership')
    require(opened['returncode'] is None and opened['forced_shutdown'] is False and
            closed['returncode'] == -9 and closed['forced_shutdown'] is True,
            'Forced application failure was rewritten')
    require(closed['remaining_owned'] == closed['remaining_live'] == closed['remaining_zombies'] == [],
            'Controlled processes were not reaped')
    expected_errors = ['Application exceeded graceful shutdown timeout', 'Forced cleanup of controlled processes',
                       'Application exit code was -9']
    require(closed['errors'] == expected_errors and
            {'identity': list(identity), 'signal': 9} in closed['signals'], 'Missing forced cleanup details')
    owned = {_identity(x) for x in closed['owned']}
    require(len(owned) == len(closed['owned']) == 6 and identity in owned, 'Wrong owned process inventory')
    all_identities = owned | command_identities | {_identity(producer[k]) for k in ('wrapper_identity', 'producer_identity')}
    all_identities |= {_identity(run['display_identity']), _identity(run['clipboard_start']['identity'])}
    parent_ids = {_identity(entry['identity']) for entry in review['identities']}
    require(len(review['identities']) == len(parent_ids) == len(all_identities) == 41 and
            parent_ids == all_identities and all(entry['original_identity_gone'] is True and
                (entry['current'] is None or entry['current']['start'] != entry['identity'][1])
                for entry in review['identities']), 'Independent cleanup audit incomplete')
    require(review['diagnostic_status'] == 'failed' and review['goal_pass'] is False and
            review['producer_exit_code'] == 1 and review['completed_operations'] == 6 and
            review['registered_operations'] == 12 and review['failure_token'] == token and
            review['failure_stage'] == 'native_paste', 'Independent review target/failure differs')
    require(review['primary_errors'] == run['errors'] and review['cleanup_errors'] == run['cleanup_errors'] and
            review['native_event_count'] == 58 and review['native_sequences_contiguous'] is True and
            review['frozen_inputs_unchanged'] is True, 'Independent review omitted failure evidence')
    require(review['product_source_hash'] == REVIEWED_WORKSPACE_SHA256 and
            review['goal_hash'] == OLD_GOAL_SHA256, 'Historical parent workspace audit differs')
    require(_number(review['command_to_cleanup_seconds'], 'review timeout') == elapsed,
            'Independent timeout duration differs')
    require(review['files'] == {files[k]['origin']: files[k]['sha256'] for k in ('run', 'producer', 'lifecycle', 'events')},
            'Independent review not bound to raw artifacts')
    require(finish[0] <= _integer(closed['time_ns'], 'lifecycle close', 1) + 100000000 and
            closed['time_ns'] <= finish[0] <= producer['finished_ns'] < review['reviewed_ns'] < approval['saved_ns'],
            'Lifecycle/review/approval chronology differs')
    require(len(doc['operations']) == 2 and [x['event'] for x in doc['operations']] == ['start', 'complete'] and
            all(x['pid'] == int(identity[0]) and x['operation_id'] == 1 for x in doc['operations']),
            'Observer install journal incomplete')
    resources = _validate_resources(doc, owned, start[0], finish[0])
    return {'run_id': RUN_ID, 'status': 'failed', 'producer_exit_code': 1,
            'completed_operations': 6, 'registered_operations': 12, 'failure_token': token,
            'failure_stage': 'native_paste', 'timeout_seconds': 1800,
            'observed_wait_seconds': elapsed, 'application_exit_code': -9,
            'forced_shutdown': True, 'cleaned_owned_identity_count': len(owned),
            'independently_audited_identity_count': len(all_identities),
            'primary_errors': run['errors'], 'cleanup_errors': run['cleanup_errors'],
            'resource_observations': resources}


def _validate_resources(doc, owned, start, finish):
    monitor = doc['monitor']
    require(len(monitor) == 38 and len(doc['host_memory']) == 2 and len(doc['threads']) == 1,
            'Missing diagnostic resource observations')
    peaks, previous = {}, start
    for sample in monitor:
        moment = _integer(sample['time_ns'], 'resource time', 1)
        require(previous < moment < finish, 'Resource times outside diagnostic or unordered'); previous = moment
        require(len(sample['load_average']) == 3 and sample['owned'], 'Missing load/owned observations')
        for value in sample['load_average']:
            _number(value, 'load average')
        for process in sample['owned']:
            identity = (process['pid'], _integer(process['start'], 'resource identity', 1))
            require(identity in owned, 'Resource observation belongs to another process')
            _integer(process['ticks'], 'resource CPU ticks')
            for key in ('VmRSS', 'VmHWM'):
                text = process['memory'][key]
                require(type(text) is str and re.fullmatch(r'[0-9]+ kB', text), 'Invalid memory quantity')
                value = int(text.split()[0])
                peaks[process['comm'] + ':' + key] = max(peaks.get(process['comm'] + ':' + key, 0), value)
    for sample in doc['host_memory']:
        require(start < _integer(sample['time_ns'], 'host memory time', 1) < finish, 'Host memory time outside diagnostic')
        require(sample['memory'], 'Missing host memory values')
        for value in sample['memory'].values():
            require(type(value) is str and re.fullmatch(r'[0-9]+ kB', value), 'Invalid host memory quantity')
    threads = doc['threads'][0]
    require((str(threads['identity'][0]), threads['identity'][1]) in owned and
            start < _integer(threads['time_ns'], 'thread sample time', 1) < finish,
            'Thread snapshot belongs to another process/window')
    _integer(threads['clock_ticks'], 'clock tick rate', 1)
    for thread in threads['threads']:
        for key in ('tid', 'start', 'utime', 'stime'):
            _integer(thread[key], 'thread ' + key)
    return {'samples': 38, 'host_memory_samples': 2, 'thread_snapshots': 1,
            'observed_peak_kib_by_comm': peaks, 'cpu_observation_unit': 'cumulative process ticks',
            'formal_resource_samples': False, 'pss_available': False,
            'scope': 'Historical diagnostic observations only; no substituted formal CPU/PSS distribution.'}


def verify_pressure(reference_path, baseline_source_sha256, goal_path_or_hash, *, role='baseline', catalog_size=10000):
    """Return a deterministic, sealable attestation; raise on any invalid evidence.

Callers must pass the actual measured role and source hash, and retain all other
required baseline checks. No success metrics, zero values, or improvement credit
are produced by this API. Historical timestamps deliberately stay historical.
"""
    require(role == 'baseline' and type(catalog_size) is int and catalog_size == 10000,
            'Pressure exception is only for the baseline 10000 catalog branch')
    require(type(baseline_source_sha256) is str and baseline_source_sha256 == BASELINE_SOURCE_SHA256,
            'Not the original approved baseline source')
    require(isinstance(goal_path_or_hash, (str, os.PathLike)), 'Invalid approved goal argument')
    if type(goal_path_or_hash) is str and re.fullmatch('[0-9a-f]{64}', goal_path_or_hash):
        actual_goal_hash = goal_path_or_hash
    else:
        path = Path(goal_path_or_hash).absolute()
        for component in (path, *path.parents):
            require(not component.is_symlink(), 'Symlink goal file')
        info, _ = _read_regular(path)
        actual_goal_hash = info['sha256']
    require(actual_goal_hash == GOAL_SHA256, 'Wrong approved goal revision')
    try:
        path, reference = _load_reference(reference_path)
        documents, observed = _load_evidence(path, reference)
        failure = _validate_evidence(reference, documents, observed)
    except (KeyError, TypeError, IndexError, StopIteration, OverflowError, UnicodeError) as error:
        raise PressureEvidenceError('Malformed or incomplete pressure evidence') from error
    manifest = {entry['path']: {'sha256': entry['sha256'], 'bytes': entry['bytes']}
                for entry in reference['files'].values()}
    return {'schema': 1, 'kind': 'approved_historical_baseline_pressure_failure',
            'status': 'verified_failure_reference', 'goal_sha256': GOAL_SHA256,
            'reference_sha256': APPROVED_REFERENCE_SHA256,
            'baseline_source_sha256': BASELINE_SOURCE_SHA256, 'scope': dict(SCOPE),
            'diagnostic': True, 'goal_pass': False, 'candidate_validation_pass': False,
            'user_flow_improvement_eligible': False, 'failure': failure,
            'numeric_comparison_metrics': {}, 'missing_baseline_metrics': 'unavailable_not_zero',
            'other_baseline_checks_required': True, 'candidate_10000_checks_required': True,
            'artifact_manifest_sha256': _object_hash(manifest), 'artifacts': manifest}


def copy_pressure_reference(reference_path, destination):
    """Fresh-byte copy for a new baseline evidence directory; never copy2/link.

The original failure and timestamps remain in the bytes. Destination freshness
does not reclassify the historic diagnostic as a newly measured sample.
"""
    verify_pressure(reference_path, BASELINE_SOURCE_SHA256, GOAL_SHA256)
    source, reference = _load_reference(reference_path)
    destination = Path(destination).absolute()
    require(not destination.exists() and not destination.is_symlink(), 'Refusing to replace pressure evidence')
    for parent in destination.parents:
        require(not parent.is_symlink(), 'Symlink pressure destination')
    destination.mkdir(parents=True, exist_ok=False)
    for entry in reference['files'].values():
        target = _path(destination, entry['path'])
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(_path(source.parent, entry['path']), target)
    result = destination / 'reference.json'
    shutil.copyfile(source, result)
    verify_pressure(result, BASELINE_SOURCE_SHA256, GOAL_SHA256)
    return result


def prepare_pressure_reference(reference_path, destination):
    """Copy helper for output/pressure; return the run-relative report entry."""
    path = copy_pressure_reference(reference_path, destination)
    return {'path': path.relative_to(Path(destination).absolute().parent).as_posix(),
            'summary': verify_pressure(path, BASELINE_SOURCE_SHA256, GOAL_SHA256)}
