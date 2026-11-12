#!/usr/bin/env python3
"""Reproducible Linux Wails performance runs. See the accepted goal and README."""
import argparse
import contextlib
import datetime
import http.server
import json
import os
import shutil
import sqlite3
import statistics
import subprocess
import sys
import threading
import time
import traceback
import urllib.request
import uuid
from pathlib import Path

from evidence import EvidenceError, compare_runs, digest_object, sha256, source_manifest, verify_go_suites
from runtime import Application, Display, call_socket, proc_snapshot, remaining_owned, _read_stat, verify_display
from resource_evidence import capture_resources, summarize_resources, verify_resources
from gui_evidence import verify_gui, verify_instance_actions, verify_catalog_observations
from pressure_evidence import prepare_pressure_reference as copy_verified_pressure
from benchmark_evidence import (PACKAGE_LIST_ARGV, BENCHMARK_ARGV, collect_package_inventory,
                                collect_benchmark_report, compare_benchmark_reports)

HERE = Path(__file__).resolve().parent
from registration import (SCENARIOS, V2, GOAL_SHA256, BASELINE_SHA256, ROLES,
                          catalog_sizes, go_registration, metric_policy, go_policy,
                          expected_metrics, expected_checks, expected_commands, acceptance_contract)
GO_VERSION = 'go1.24.13'
GUI = (HERE / 'fixtures/gui-operations.js').read_text()


class ProductFailure(EvidenceError):
    """The real application or producer returned an incorrect business result."""


class ProducerFailure(EvidenceError):
    """A build/test command failed; its retained diagnostics identify the cause."""


def failure_chain(error):
    """Record every visible cause without attributing cleanup to the operation."""
    result, seen = [], set()
    while error is not None and id(error) not in seen:
        seen.add(id(error))
        kind = ('product_behavior' if isinstance(error, ProductFailure) else
                'producer_failure_requires_diagnostic_review' if isinstance(error, ProducerFailure) else
                'measurement_or_verifier_failure')
        result.append({'type': type(error).__name__, 'message': str(error), 'kind': kind})
        error = (error.__cause__ if error.__cause__ is not None else
                 None if error.__suppress_context__ else error.__context__)
    return result


def application_lifecycles(directory, smoke=False, role=None):
    """Verify every independently launched application, including restarts."""
    from runtime import load_lifecycle
    directory = Path(directory).resolve()
    rounds = 1 if smoke else SCENARIOS['independent_rounds']
    sizes = [100] if smoke else catalog_sizes(role)
    counts = [0, 1] if smoke else SCENARIOS['runtime_counts']
    expected = {f'c{size}-{index}/lifecycle.json': 2 if index == 0 else 1
                for size in sizes for index in range(rounds)}
    expected.update({f'r{count}-{index}/lifecycle.json': 1
                     for count in counts for index in range(rounds)})
    actual = {str(path.relative_to(directory)) for path in directory.glob('*/lifecycle.json')}
    if actual != expected.keys():
        raise EvidenceError('Application lifecycle coverage differs from registered rounds')
    return {name: load_lifecycle(directory / name, expected_starts=starts)
            for name, starts in expected.items()}


def verified_application_lifecycles(directory):
    from evidence import load_run
    report = load_run(directory)
    events = application_lifecycles(directory, role=report.get('role'))
    if events != report.get('application_lifecycles'):
        raise EvidenceError('Cached lifecycle report differs from raw process evidence')
    return {'files': len(events), 'starts': sum(len(items) // 2 for items in events.values())}


def environment():
    result = os.environ.copy()
    result['GOTOOLCHAIN'] = GO_VERSION
    return result


def command(argv, cwd, out, name, report, extra_env=None):
    env = environment()
    if extra_env:
        env.update(extra_env)
    start = time.time_ns()
    stdout, stderr = out / (name + '.stdout'), out / (name + '.stderr')
    print('COMMAND', name, flush=True)
    with stdout.open('wb') as stream, stderr.open('wb') as errors:
        result = subprocess.run(argv, cwd=cwd, env=env, stdout=stream, stderr=errors)
    report['commands'].append({'name': name, 'argv': argv, 'cwd': str(cwd), 'exit_code': result.returncode,
                               'started_ns': start, 'finished_ns': time.time_ns(),
                               'stdout': stdout.name, 'stderr': stderr.name})
    if result.returncode:
        raise ProducerFailure(f'{name} failed with exit {result.returncode}; see {stdout} and {stderr}')
    return stdout


def fixture_hash():
    return digest_object({str(p.relative_to(HERE)): sha256(p) for p in sorted(HERE.rglob('*'))
                          if p.is_file() and '__pycache__' not in p.parts})


def copy_source(root, destination):
    """Copy build inputs without opening personal runtime directories or configs."""
    root, destination = Path(root), Path(destination)
    destination.mkdir(parents=True, exist_ok=False)
    ignored = shutil.ignore_patterns('.git', '.codex', 'node_modules', 'dist', 'dist_stub',
                                     '.vite', '__pycache__', '*.db', '*.sqlite', '*.sqlite3', '.env*')
    for name in ('backend', 'internal', 'frontend', 'scripts'):
        shutil.copytree(root / name, destination / name, ignore=ignored)
    shutil.copytree(root / 'build', destination / 'build',
                    ignore=shutil.ignore_patterns('bin', 'dist'))
    for path in [*root.glob('*.go'), *(root / name for name in ('go.mod', 'go.sum', 'wails.json'))]:
        shutil.copy2(path, destination / path.name)
    for name in ('xray', 'sing-box'):
        path = Path('bin/linux-amd64') / name
        (destination / path).parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(root / path, destination / path)
    (destination / 'chrome').mkdir()
    (destination / 'chrome/README.md').write_text('Performance fixture: browser binaries are external.\n')


def prepare(root, output, report):
    build = output / 'build'
    copy_source(root, build)
    hook = build / 'linux_perf_hook.go'
    hook.write_text((HERE / 'fixtures/main_hook.go.txt').read_text())
    main = build / 'main.go'
    text = main.read_text()
    needle = '\t\tOnStartup: func(ctx context.Context) {'
    if text.count(needle) != 1:
        raise EvidenceError('Cannot install the test-only GUI observer')
    main.write_text(text.replace(needle, '\t\tOnDomReady: linuxPerfOnDomReady,\n' + needle))
    seed = build / 'backend/cmd/linuxperf-seed/main.go'
    seed.parent.mkdir(parents=True, exist_ok=True)
    seed.write_text((HERE / 'fixtures/seed.go.txt').read_text())
    for fixture in (HERE / 'fixtures/go').rglob('*.go.txt'):
        target = build / fixture.relative_to(HERE / 'fixtures/go').with_suffix('')
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(fixture, target)
    command(['npm', 'ci'], build / 'frontend', output, 'npm-ci', report)
    command(['npm', 'run', 'build:clean'], build / 'frontend', output, 'frontend-build', report)
    command(['go', 'run', 'github.com/wailsapp/wails/v2/cmd/wails@v2.12.0', 'build', '-s', '-platform',
             'linux/amd64', '-tags', 'webkit2_41', '-o', 'ant-chrome'], build, output, 'wails-build', report)
    command(['go', 'build', '-o', str(output / 'seed'), './backend/cmd/linuxperf-seed'], build, output, 'seed-build', report)
    binary = build / 'build/bin/ant-chrome'
    if not binary.is_file() or binary.read_bytes()[:5] != b'\x7fELF\x02':
        raise EvidenceError('Missing fresh Linux ELF64 application')
    report['binary_sha256'] = sha256(binary)
    report['build_artifacts'] = {name: {'path': str(path.relative_to(output)),
        'sha256': sha256(path), 'bytes': path.stat().st_size}
        for name, path in (('application', binary), ('seed', output / 'seed'))}
    report['instrumentation'] = 'Identical OnDomReady Unix socket hook, isolated builds only'
    report['correctness_checks'].append('built_real_wails_elf64')
    return build, binary, output / 'seed'


def add(report, name, value, unit='ms', core=True, eligible=False, minimum=30, noise=0):
    policy = metric_policy(unit, core, eligible, minimum, noise)
    if report.get('expected_metrics') and report['expected_metrics'].get(name) != policy:
        raise EvidenceError('Unregistered metric or policy: ' + name)
    metric = report['metrics'].setdefault(name, {'samples': [], **policy})
    metric['samples'].append(value)


def add_registered(report, name, value):
    policy = report['expected_metrics'][name]
    add(report, name, value, policy['unit'], core=policy['core'],
        eligible=policy['improvement_eligible'], minimum=policy['minimum_samples'],
        noise=policy['absolute_noise'])


def record_gui_metric(report, name, timed, phase=None):
    journal = Path(timed['journal_path']).resolve().relative_to(Path(report['output']).resolve())
    sample = {'journal': str(journal), 'token': timed['token'],
              'operation_id': timed['operation_id'], 'phase': phase,
              'application_ready': timed.get('application_ready', False)}
    report.setdefault('gui_samples', {}).setdefault(name, []).append(sample)
    add_registered(report, name, timed['elapsed_ms'] if phase is None else timed['phase_ms'][phase])


def record_resources(report, path, resources, prefix):
    summary = summarize_resources(resources, formal=not report.get('diagnostic', False))
    name = str((Path(path) / 'resources.json').relative_to(report['output']))
    report.setdefault('resource_evidence', {})[name] = summary
    for key, value in summary.items():
        metric = prefix + '.' + key
        if metric in report['expected_metrics']:
            add_registered(report, metric, value)


def checkpoint(report, output):
    (output / 'progress.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))


def host_sample(report, stage):
    report.setdefault('host_load', []).append({'stage': stage, 'time_ns': time.time_ns(),
        'load_average': os.getloadavg(), 'cpu': Path('/proc/stat').read_text().splitlines()[0],
        'memory': Path('/proc/meminfo').read_text()})


def catalog_suite(binary, seed, output, report, display, chromium, smoke):
    sizes = [100] if smoke else catalog_sizes(report.get('role'))
    rounds = 1 if smoke else SCENARIOS['independent_rounds']
    samples = 2 if smoke else SCENARIOS['short_samples']
    for size in sizes:
        for round_index in range(rounds):
            print(f'CATALOG size={size} round={round_index+1}/{rounds}', flush=True)
            host_sample(report, f'catalog.{size}.{round_index}')
            path = output / f'c{size}-{round_index}'
            app = Application(binary, seed, path, display, chromium, size=size)
            try:
                app.start(); app.install_gui(GUI)
                elapsed = app.ready()
                record_gui_metric(report, f'catalog.{size}.initial_render', app.last_ready_timing)
                if report.get('role') == 'candidate' and size == 10000:
                    resources = capture_resources(app, warmup=15, duration=30)
                    record_resources(report, path, resources, 'catalog.10000.resources')
                    postcheck = app.evaluate_timed('return await antPerfGui.initialReady(10000,true);',
                                                  require_render=True)
                    (app.output / 'resource-gui.json').write_text(json.dumps(postcheck, ensure_ascii=False) + '\n')
                    report['correctness_checks'].append(f'catalog_10000_{round_index}_resources')
                if round_index:
                    continue
                ids = [f'perf-profile-{i:05d}' for i in range(size)]
                def gui(code, **options):
                    return app.evaluate_timed(code, **options)
                for _ in range(samples):
                    result=gui('return await antPerfGui.filterProfiles("性能实例 00042",["perf-profile-00042"]);',require_render=True)
                    record_gui_metric(report,f'catalog.{size}.profile_filter',result)
                    result=gui('return await antPerfGui.filterProfiles("",'+json.dumps(ids)+');',require_render=True)
                    record_gui_metric(report,f'catalog.{size}.profile_restore',result)
                gui('return await antPerfGui.filterProfiles("性能实例 00042",["perf-profile-00042"]);',require_render=True)
                for index in range(samples):
                    keyword=f'perf-updated-00042-{index%2}'
                    arguments='"perf-profile-00042","性能实例 00042",'+json.dumps(keyword)
                    gui('return await antPerfGui.prepareKeyword('+arguments+');',require_render=True)
                    result=gui('return await antPerfGui.savePreparedKeyword('+arguments+',linuxPerfPhase);',
                               expected_phases=('keyword_visible','keyword_closed'),require_render=True)
                    record_gui_metric(report,f'catalog.{size}.keyword_save_visible',result,phase='keyword_visible')
                    record_gui_metric(report,f'catalog.{size}.keyword_save_closed',result,phase='keyword_closed')
                    with sqlite3.connect(app.runtime/'data/app.db') as db:
                        actual=json.loads(db.execute('SELECT keywords FROM browser_profiles WHERE profile_id=?',('perf-profile-00042',)).fetchone()[0])
                    if actual != [keyword]:
                        raise RuntimeError('GUI keyword save differs from SQLite')
                for _ in range(samples):
                    for kind in ('profiles','groups','proxies'):
                        result=gui('return await antPerfGui.catalogAPI('+json.dumps(kind)+','+str(size)+',true);')
                        record_gui_metric(report,f'catalog.{size}.{kind}_api',result)
                gui('return await antPerfGui.catalogAPI("config",'+str(size)+',true);')
                gui('document.querySelector(\'a[href="/browser/proxy-pool"]\').click(); return {render:await antPerfGui.waitRendered(()=>location.pathname==="/browser/proxy-pool" && !!document.querySelector(\'input[placeholder="搜索名称或服务器..."]\'),"proxy route")};',require_render=True)
                names=[f'性能代理 {i:05d}' for i in range(size)]
                for _ in range(samples):
                    result=gui('return await antPerfGui.filterProxies("性能代理 00042",["性能代理 00042"]);',require_render=True)
                    record_gui_metric(report,f'catalog.{size}.proxy_filter',result)
                    result=gui('return await antPerfGui.filterProxies("性能代理",'+json.dumps(names,ensure_ascii=False)+');',require_render=True)
                    record_gui_metric(report,f'catalog.{size}.proxy_restore',result)
                gui('document.querySelector(\'a[href="/browser/list"]\').click(); await antPerfGui.waitRendered(()=>!!document.querySelector(\'input[placeholder="搜索名称/快捷码/关键字..."]\'),"profile route"); return await antPerfGui.filterProfiles("",'+json.dumps(ids)+');',require_render=True)
                report['correctness_checks'].append(f'catalog_{size}_exact_results_gui_save_sqlite')
                verify_catalog_events(app, size)
                report['correctness_checks'].append(f'catalog_{size}_backend_event_refresh')
                app.evaluate_timed('document.querySelector(\'a[href="/browser/list"]\').click(); await antPerfGui.waitRendered(()=>!!document.querySelector(\'input[placeholder="搜索名称/快捷码/关键字..."]\'),"profile route"); return await antPerfGui.filterProfiles("性能实例 00042",["perf-profile-00042"]);')
                app.evaluate_timed('return await antPerfGui.cancelKeyword("perf-profile-00042","性能实例 00042",' + json.dumps(keyword) + ');')
                database_path = path / 'runtime/data/app.db'
                with sqlite3.connect(database_path) as db:
                    db.execute("CREATE TRIGGER perf_reject_keyword BEFORE INSERT ON browser_profiles WHEN NEW.profile_id='perf-profile-00042' BEGIN SELECT RAISE(ABORT,'synthetic save failure'); END")
                try:
                    app.evaluate_timed('return await antPerfGui.rejectedKeyword("perf-profile-00042","性能实例 00042",' + json.dumps(keyword) + ');')
                finally:
                    with sqlite3.connect(database_path) as db:
                        db.execute('DROP TRIGGER perf_reject_keyword')
                with sqlite3.connect(database_path) as db:
                    actual = json.loads(db.execute('SELECT keywords FROM browser_profiles WHERE profile_id=?', ('perf-profile-00042',)).fetchone()[0])
                if actual != [keyword]:
                    raise ProductFailure('Cancelled or rejected GUI save changed persistence')
                report['correctness_checks'].append(f'catalog_{size}_cancel_and_error')
            finally:
                app.close()
                checkpoint(report, output)
            # Restart the same isolated state and verify persistence independently.
            if round_index == 0:
                try:
                    app.start(); app.install_gui(GUI)
                    # The UI deliberately remembers the previous search. Clear
                    # it through the real input before checking the full catalog.
                    app.evaluate_timed('await antPerfGui.waitRendered(()=>!!document.querySelector(\'input[placeholder="搜索名称/快捷码/关键字..."]\'),"restored route"); return await antPerfGui.filterProfiles("",' + json.dumps(ids) + ');')
                    app.ready()
                    persisted = app.evaluate_timed('return (await window.go.main.App.BrowserProfileList()).find(p=>p.profileId==="perf-profile-00042").keywords;')['value']
                    if persisted != [keyword]:
                        raise ProductFailure('Saved keyword changed across application restart')
                    report['correctness_checks'].append(f'catalog_{size}_restart_persistence')
                finally:
                    app.close()


class EventProbeProxy(http.server.BaseHTTPRequestHandler):
    def do_HEAD(self):
        time.sleep(.03)
        self.send_response(204)
        self.send_header('Content-Length', '0')
        self.end_headers()

    def log_message(self, *args):
        pass


def verify_catalog_events(app, size):
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 20042), EventProbeProxy)
    server.daemon_threads = True
    thread = threading.Thread(target=lambda: server.serve_forever(poll_interval=.01), daemon=True)
    thread.start()
    try:
        success = app.evaluate_timed('return await antPerfGui.verifyProxyEvent("perf-profile-00042",'
            + str(size) + ',true);', require_render=True)
    finally:
        server.shutdown(); server.server_close(); thread.join(timeout=2)
    if thread.is_alive():
        raise EvidenceError('Local event proxy server did not stop')
    failure = app.evaluate_timed('return await antPerfGui.verifyProxyEvent("perf-profile-00042",'
        + str(size) + ',false);', require_render=True)
    (app.output / 'event-refresh.json').write_text(json.dumps(
        {'success': success, 'failure': failure}, ensure_ascii=False, indent=2) + '\n')


class LocalPage(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        payload = b'<!doctype html><title>Ant Linux Performance Fixture</title><main id="ready">local deterministic page</main>'
        self.send_response(200); self.send_header('Content-Type', 'text/html; charset=utf-8')
        self.send_header('Content-Length', str(len(payload))); self.end_headers(); self.wfile.write(payload)

    def log_message(self, *args):
        pass


def wait_page(port, url):
    import websocket
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(f'http://127.0.0.1:{port}/json/list', timeout=2) as response:
                pages = json.load(response)
            for page in pages:
                if page.get('url') != url or page.get('title') != 'Ant Linux Performance Fixture':
                    continue
                connection = websocket.create_connection(page['webSocketDebuggerUrl'], timeout=2, suppress_origin=True)
                try:
                    connection.send(json.dumps({'id': 1729, 'method': 'Runtime.evaluate',
                        'params': {'expression': 'document.querySelector("#ready")?.textContent', 'returnByValue': True}}))
                    while True:
                        message = json.loads(connection.recv())
                        if message.get('id') == 1729:
                            if message.get('result', {}).get('result', {}).get('value') == 'local deterministic page':
                                return
                            break
                finally:
                    connection.close()
        except (OSError, ValueError):
            pass
        time.sleep(.02)
    raise ProductFailure('Real Chromium did not load the expected local page')


def runtime_suite(binary, seed, output, report, display, chromium, smoke):
    rounds = 1 if smoke else SCENARIOS['independent_rounds']
    counts = [0, 1] if smoke else SCENARIOS['runtime_counts']
    warmup = 1 if smoke else SCENARIOS['warmup_seconds']
    duration = 2 if smoke else SCENARIOS['resource_sample_seconds']
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), LocalPage)
    thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
    url = f'http://127.0.0.1:{server.server_port}/fixture'
    try:
        for count in counts:
            for round_index in range(rounds):
                print(f'RUNTIME instances={count} round={round_index+1}/{rounds}', flush=True)
                host_sample(report, f'runtime.{count}.{round_index}')
                app = Application(binary, seed, output / f'r{count}-{round_index}', display, chromium,
                                  proxies=False, start_url=url)
                try:
                    app.start(); app.install_gui(GUI)
                    app.ready()
                    record_gui_metric(report, f'runtime.{count}.application_ready', app.last_ready_timing)
                    startup_times, stop_times, browser_starts = [], [], []
                    instance_actions = []
                    ids = [f'perf-profile-{i:05d}' for i in range(count)]
                    for profile_id in ids:
                        timed = app.evaluate_timed('return await antPerfGui.instanceAction('
                            + json.dumps(profile_id) + ',true);', require_render=True)
                        profile = timed['value']
                        if not profile.get('running') or not profile.get('debugReady') or not profile.get('pid'):
                            raise ProductFailure('Real browser did not report running and debug-ready')
                        wait_page(profile['debugPort'], url)
                        page_ready_ns = time.monotonic_ns()
                        startup_times.append((page_ready_ns - timed['started_monotonic_ns']) / 1e6)
                        instance_actions.append({'action': 'start', 'profile_id': profile_id,
                            'timed': timed, 'page_ready_monotonic_ns': page_ready_ns,
                            'elapsed_ms': startup_times[-1], 'verified_url': url,
                            'title': 'Ant Linux Performance Fixture', 'content': 'local deterministic page'})
                        identity = _read_stat(profile['pid'])
                        if identity['uid'] != os.geteuid() or identity['state'] in ('Z', 'X'):
                            raise ProductFailure('Started browser identity is not live and owned')
                        browser_starts.append({'profile_id': profile_id, 'pid': str(profile['pid']),
                                               'start': identity['start'], 'debug_port': profile['debugPort'],
                                               'verified_page_url': url})
                    if startup_times:
                        add(report, f'runtime.{count}.instance_ready', statistics.median(startup_times), eligible=True, minimum=10)
                    (app.output / 'browser-starts.json').write_text(json.dumps(browser_starts, indent=2) + '\n')
                    resources = capture_resources(app, warmup=warmup, duration=duration,
                        expected_browsers=[(item['pid'], item['start']) for item in browser_starts])
                    record_resources(report, app.output, resources, f'runtime.{count}')
                    owned_browsers = {(pid, item['start'])
                        for pid, item in resources['points'][-1]['processes'].items()
                        if item['category'] == 'browser'}
                    for profile_id in ids:
                        timed = app.evaluate_timed('return await antPerfGui.instanceAction('
                            + json.dumps(profile_id) + ',false);', require_render=True)
                        stop_times.append(timed['elapsed_ms'])
                        instance_actions.append({'action': 'stop', 'profile_id': profile_id, 'timed': timed})
                    if stop_times:
                        add(report, f'runtime.{count}.stop', statistics.median(stop_times), minimum=10)
                    deadline = time.monotonic() + 10
                    while time.monotonic() < deadline:
                        remaining = remaining_owned(owned_browsers)
                        if not remaining:
                            break
                        time.sleep(.05)
                    else:
                        raise ProductFailure('Owned Chromium processes remained after stopping')
                    (app.output / 'instance-actions.json').write_text(
                        json.dumps(instance_actions, ensure_ascii=False, indent=2) + '\n')
                    report['correctness_checks'].append(f'runtime_{count}_{round_index}_real_page_states_reclamation')
                finally:
                    app.close(); checkpoint(report, output)
    finally:
        server.shutdown(); server.server_close(); thread.join()


def go_suites(build, output, report, smoke):
    from evidence import collect_go_reports
    for name, package, test, registration in go_registration():
        host_sample(report, 'go.' + name)
        extra = {'LINUXPERF_PROXY_SAMPLES': '1' if smoke else '30', 'LINUXPERF_IO_SAMPLES': '1' if smoke else '30'}
        path = command(['go', 'test', '-count=1', '-json', '-tags', 'webkit2_41 linuxperf', './'+package.removeprefix('ant-chrome/'), '-run', '^'+test+'$'],
                       build, output, 'go-' + name, report, extra)
        records = collect_go_reports(path, {(package, test): registration}, 0,
                                     expected_samples=1 if smoke and name != 'logger' else 30,
                                     allow_diagnostic=smoke)
        for record in records:
            for key, values in record['metrics'].items():
                policy, multiplier = go_policy(name, key, registration['metrics'][key])
                for value in values:
                    add(report, name + '.' + key, value * multiplier, policy['unit'],
                        core=policy['core'], eligible=policy['improvement_eligible'], noise=policy['absolute_noise'])
            report.setdefault('go_evidence', {})[name] = record.get('evidence')
        report['correctness_checks'].append('go_' + name)


def benchmark_suite(build, output, report):
    host_sample(report, 'microbenchmarks')
    path = command(list(PACKAGE_LIST_ARGV), build, output, 'benchmark-packages', report)
    inventory = collect_package_inventory(path, 0, argv=list(PACKAGE_LIST_ARGV))
    report['benchmark_inventory'] = inventory
    path = command(list(BENCHMARK_ARGV), build, output, 'benchmarks', report)
    report['benchmarks'] = collect_benchmark_report(path, 0, inventory['packages'], argv=list(BENCHMARK_ARGV))
    report['correctness_checks'].append('all_registered_microbenchmarks')


def verified_benchmarks(directory):
    from evidence import load_run
    directory = Path(directory)
    report = load_run(directory)
    commands = {item['name']: item for item in report['commands']}
    inventory_command, benchmark_command = commands['benchmark-packages'], commands['benchmarks']
    inventory = collect_package_inventory(directory / inventory_command['stdout'],
        inventory_command['exit_code'], argv=inventory_command['argv'])
    result = collect_benchmark_report(directory / benchmark_command['stdout'],
        benchmark_command['exit_code'], inventory['packages'], argv=benchmark_command['argv'])
    if inventory != report.get('benchmark_inventory') or result != report.get('benchmarks'):
        raise EvidenceError('Cached benchmark report differs from raw producer evidence')
    return result


def prepare_pressure_reference(reference, output, source_hash):
    if not reference or source_hash != BASELINE_SHA256:
        raise EvidenceError('Baseline role requires its approved portable pressure reference')
    try:
        return copy_verified_pressure(reference, output / 'pressure')
    except ValueError as error:
        raise EvidenceError('Cannot verify approved pressure reference') from error


def measure(args):
    root, output = Path(args.root).resolve(), Path(args.output).resolve()
    role = getattr(args, 'role', None)
    if role and sha256(HERE.parents[1] / '.codex/goals/2026-11-10-linux-performance-v2.md') != GOAL_SHA256:
        raise EvidenceError('Approved v2 goal is missing or changed')
    output.mkdir(parents=True, exist_ok=False)
    source = source_manifest(root)
    if role == 'baseline' and digest_object(source) != BASELINE_SHA256:
        raise EvidenceError('Baseline role must use the approved original source')
    report = {'schema': 1, 'run_id': str(uuid.uuid4()), 'root': str(root), 'output': str(output),
              'started_ns': time.time_ns(), 'status': 'running', 'diagnostic': args.smoke,
              'source_manifest': source, 'source_hash': digest_object(source), 'fixture_hash': fixture_hash(),
              'scenario_hash': sha256(HERE / 'scenarios.json'), 'toolchain': GO_VERSION,
              'browser_hash': sha256(args.chromium), 'settings': SCENARIOS,
              'commands': [], 'artifacts': [], 'correctness_checks': [], 'metrics': {},
              'expected_metrics': expected_metrics(role), 'expected_correctness_checks': expected_checks(role),
              'expected_commands': expected_commands(),
              'platform': dict(zip(('system', 'node', 'release', 'version', 'machine'), os.uname())),
              'kernel_hashes': {name: sha256(root / 'bin/linux-amd64' / name) for name in ('xray', 'sing-box')}}
    if role:
        report.update(role=role, acceptance=acceptance_contract(role))
    mihomo = os.environ.get('LINUXPERF_MIHOMO_BINARY')
    if not mihomo or not Path(mihomo).is_absolute():
        raise EvidenceError('Set LINUXPERF_MIHOMO_BINARY to the pinned, verified Mihomo executable')
    report['kernel_hashes']['mihomo'] = sha256(mihomo)
    report['browser_version'] = subprocess.check_output([args.chromium, '--version'], text=True).strip()
    report['toolchain_version'] = subprocess.check_output(['go', 'version'], env=environment(), text=True).strip()
    checkpoint(report, output)
    try:
        if role == 'baseline':
            # Copy and independently verify only the explicitly approved old
            # 10,000-entry failure. Other incomplete evidence remains a failure.
            reference = getattr(args, 'pressure_reference', None) or os.environ.get('LINUXPERF_PRESSURE_REFERENCE')
            report['pressure_reference'] = prepare_pressure_reference(reference, output, report['source_hash'])
            report['correctness_checks'].append('baseline_10000_pressure_reference')
        build, binary, seed = prepare(root, output, report)
        with Display(output) as display:
            catalog_suite(binary, seed, output, report, display, args.chromium, args.smoke)
            runtime_suite(binary, seed, output, report, display, args.chromium, args.smoke)
        report['display_lifecycle'] = verify_display(output)
        report['application_lifecycles'] = application_lifecycles(output, args.smoke, role)
        report['correctness_checks'].append('all_application_lifecycles')
        if role and not args.smoke:
            report['resource_verification'] = verify_resources(output, report)
            report['correctness_checks'].append('registered_resource_samples')
            report['gui_verification'] = verify_gui(output, report)
            report['instance_action_verification'] = verify_instance_actions(output, report)
            report['catalog_observation_verification'] = verify_catalog_observations(output, report)
            report['correctness_checks'].append('registered_gui_receipts')
        go_suites(build, output, report, args.smoke)
        if role and not args.smoke:
            report['go_verification'] = verify_go_suites(output, report)
        if not args.smoke:
            benchmark_suite(build, output, report)
        if source_manifest(root) != source or fixture_hash() != report['fixture_hash']:
            raise EvidenceError('Inputs changed during measurement')
        report['status'] = 'complete'
    except BaseException as error:
        report['status'] = 'failed'; report['error'] = f'{type(error).__name__}: {error}'
        # A cleanup error can replace an in-flight operation exception. Preserve
        # Python's complete exception chain so the original cause remains visible.
        report['exception_traceback'] = traceback.format_exc()
        report['exception_chain'] = failure_chain(error)
        report['failure_kind'] = (report['exception_chain'][0]['kind']
            if len(report['exception_chain']) == 1 else 'chained_failures_require_diagnostic_review')
        raise
    finally:
        report['finished_ns'] = time.time_ns()
        for path in sorted(output.rglob('*')):
            relative = path.relative_to(output)
            is_pressure = relative.parts[0] == 'pressure'
            is_gui_journal = relative.parts[-2:] == ('runtime', 'linuxperf-gui-events.jsonl')
            is_binary = relative.as_posix() in ('build/build/bin/ant-chrome', 'seed')
            if (path.is_file() and (is_pressure or is_gui_journal or is_binary or ('build' not in relative.parts and 'runtime' not in relative.parts))
                    and (is_pressure or is_binary or path.suffix in ('.stdout', '.stderr', '.json', '.jsonl', '.log'))
                    and (path.parent != output or path.name not in ('progress.json', 'results.json', 'complete.json'))):
                digest = sha256(path)
                historical = report.get('pressure_reference', {}).get('summary', {}).get('artifacts', {}).get(
                    relative.as_posix().removeprefix('pressure/')) if is_pressure else None
                historical_empty = (isinstance(historical, dict) and historical.get('bytes') == 0
                                    and historical.get('sha256') == digest)
                report['artifacts'].append({'path': str(relative), 'sha256': digest,
                    'allow_empty': historical_empty or path.suffix in ('.stdout', '.stderr', '.log')})
        (output / 'results.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
        if report['status'] == 'complete':
            (output / 'complete.json').write_text(json.dumps({'run_id': report['run_id'], 'results_sha256': sha256(output / 'results.json')}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    measure_parser = commands.add_parser('measure')
    measure_parser.add_argument('--root', required=True)
    measure_parser.add_argument('--output', required=True)
    measure_parser.add_argument('--chromium', default='/usr/lib/chromium/chromium')
    measure_parser.add_argument('--role', choices=ROLES,
                                help='Use the approved v2 baseline/candidate matrix; omit for the original matrix')
    measure_parser.add_argument('--pressure-reference',
                                help='Verified portable old 10,000-entry failure reference (baseline only)')
    measure_parser.add_argument('--smoke', action='store_true', help='Diagnostic only; never accepted by compare')
    compare_parser = commands.add_parser('compare')
    compare_parser.add_argument('--before', required=True); compare_parser.add_argument('--after', required=True)
    compare_parser.add_argument('--output', required=True)
    args = parser.parse_args()
    try:
        if args.command == 'measure':
            measure(args)
        else:
            output = Path(args.output).resolve(); output.mkdir(parents=True, exist_ok=False)
            lifecycle_evidence = {name: verified_application_lifecycles(directory)
                for name, directory in (('before', args.before), ('after', args.after))}
            result = compare_runs(args.before, args.after)
            result['application_lifecycles'] = lifecycle_evidence
            result['microbenchmarks'] = compare_benchmark_reports(verified_benchmarks(args.before), verified_benchmarks(args.after))
            (output / 'comparison.json').write_text(json.dumps(result, ensure_ascii=False, indent=2))
            print(json.dumps({'status': result['status'], 'failures': result['failures'], 'stable_improvements': result['stable_user_flow_improvements']}))
            if result['status'] != 'pass':
                return 1
    except (EvidenceError, OSError, ValueError, subprocess.SubprocessError) as error:
        print(f'PERFORMANCE VERIFICATION FAILED: {error}', file=sys.stderr)
        traceback.print_exception(error, file=sys.stderr)
        return 2
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
