"""Actual Linux application process control; all state is local to the run."""
import contextlib
import ctypes
import hashlib
import http.client
import json
import math
import os
import shutil
import signal
import socket
import sqlite3
import subprocess
import threading
import time
from pathlib import Path

from evidence import EvidenceError, _json_loads

PROC_ROOT = Path('/proc')
SHUTDOWN_TIMEOUT = 10
RECLAMATION_TIMEOUT = 5
_LIBC = ctypes.CDLL(None, use_errno=True)


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path, timeout=1830):
        super().__init__('localhost', timeout=timeout)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def call_socket(path, endpoint, value=None):
    connection = UnixHTTP(path)
    try:
        connection.request('POST' if value is not None else 'GET', endpoint,
                           json.dumps(value) if value is not None else None,
                           {'Content-Type': 'application/json'})
        response = connection.getresponse()
        content = response.read()
        if response.status != 200:
            raise EvidenceError(f'GUI observer HTTP {response.status}: {content[:1000]!r}')
        return _json_loads(content)
    finally:
        connection.close()


def stream_evaluation(path, request, started_ns, timeout):
    """Receive complete observer messages under one host deadline, including entry queueing."""
    if not 0 < timeout <= 1800:
        raise EvidenceError('GUI timeout must be in (0, 1800] seconds')
    deadline_ns = started_ns + int(timeout * 1_000_000_000)
    connection = UnixHTTP(path, timeout=timeout)
    expired = threading.Event()

    def expire():
        expired.set()
        if connection.sock is not None:
            with contextlib.suppress(OSError):
                connection.sock.shutdown(socket.SHUT_RDWR)

    timer = threading.Timer(max(0, (deadline_ns - time.monotonic_ns()) / 1e9), expire)
    timer.daemon = True
    timer.start()
    try:
        connection.request('POST', '/eval', json.dumps(request), {'Content-Type': 'application/json'})
        response = connection.getresponse()
        if response.status != 200:
            raise EvidenceError(f'GUI observer HTTP {response.status}: {response.read(1000)!r}')
        if response.getheader('Content-Type', '').split(';')[0] != 'application/x-ndjson':
            raise EvidenceError('Timed GUI observer did not return the calibrated stream protocol')
        while True:
            line = response.readline((8 << 20) + 1)
            received_ns = time.monotonic_ns()
            if expired.is_set() or received_ns >= deadline_ns:
                raise EvidenceError('GUI operation exceeded host observation deadline')
            if not line:
                return
            if len(line) > 8 << 20 or not line.endswith(b'\n'):
                raise EvidenceError('Truncated or oversized GUI observer message')
            yield _json_loads(line), received_ns
    except (OSError, http.client.HTTPException) as error:
        if expired.is_set() or time.monotonic_ns() >= deadline_ns:
            raise EvidenceError('GUI operation exceeded host observation deadline') from error
        raise
    finally:
        timer.cancel()
        connection.close()


def validate_render_proof(value):
    proof = value.get('render') if isinstance(value, dict) else None
    if (not isinstance(proof, dict) or type(proof.get('render_proof_version')) is not int
            or proof['render_proof_version'] != 2):
        raise EvidenceError('GUI result lacks a v2 rendered-frame proof')
    if type(proof.get('frame_count')) is not int or proof['frame_count'] != 2:
        raise EvidenceError('GUI result lacks two rendered frames')

    def valid_time(item):
        if type(item) not in (int, float):
            return False
        try:
            return math.isfinite(item) and item >= 0
        except OverflowError:
            return False

    # performance.now() may have equal values at separate RAF callbacks. The
    # native callback timestamp identifies frames at a different observation point.
    keys = ('predicate_ms', 'first_frame_ms', 'second_frame_ms', 'completed_ms', 'deadline_ms')
    times = [proof.get(key) for key in keys]
    if not all(valid_time(item) for item in times):
        raise EvidenceError('GUI rendered-frame clocks are invalid')
    if not (times[0] <= times[1] <= times[2] <= times[3] < times[4]):
        raise EvidenceError('GUI rendered-frame callback order or deadline is invalid')
    attempts = proof.get('frame_attempts')
    if not isinstance(attempts, list) or len(attempts) < 2:
        raise EvidenceError('GUI result lacks native RAF attempts')
    for attempt in attempts:
        if (not isinstance(attempt, dict) or set(attempt) != {'raf_ms', 'callback_ms'}
                or not all(valid_time(attempt[key]) for key in ('raf_ms', 'callback_ms'))):
            raise EvidenceError('GUI native RAF attempt clocks are invalid')
    callbacks = [attempt['callback_ms'] for attempt in attempts]
    if (callbacks[0] != times[1] or callbacks[-1] != times[2]
            or any(left > right for left, right in zip(callbacks, callbacks[1:]))):
        raise EvidenceError('GUI RAF callbacks differ from the recorded frame bounds')
    native = [attempt['raf_ms'] for attempt in attempts]
    # Return at the first distinguishable native frame. Equal timestamps are
    # retried under the original deadline and retained, never invented or lost.
    # Do not require native timestamps to be >= predicate/performance.now().
    if any(stamp != native[0] for stamp in native[:-1]) or native[-1] <= native[0]:
        raise EvidenceError('GUI native RAF sequence lacks its first distinct final frame')
    if proof.get('predicate_before') is not True or proof.get('predicate_after') is not True:
        raise EvidenceError('GUI business result did not hold across two frames')
    return proof


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class Display:
    def __init__(self, directory):
        self.directory = Path(directory)

    def __enter__(self):
        self.log = (self.directory / 'xvfb.log').open('wb')
        self.started_ns = time.time_ns()
        self.process = subprocess.Popen(['Xvfb', '-displayfd', '1', '-screen', '0', '1920x1080x24',
                                         '-s', '0', '-nolisten', 'tcp'],
                                        stdout=subprocess.PIPE, stderr=self.log)
        item = _read_stat(self.process.pid)
        self.identity = (str(self.process.pid), item['start'])
        line = self.process.stdout.readline().decode().strip()
        if not line.isdigit():
            self.__exit__(None, None, None)
            raise EvidenceError('Xvfb did not provide a display')
        self.name = ':' + line
        return self.name

    def __exit__(self, *args):
        forced = False
        _signal_owned(self.identity, signal.SIGTERM)
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            forced = True
            _signal_owned(self.identity, signal.SIGKILL)
            self.process.wait()
        finally:
            self.log.close()
        remaining = remaining_owned([self.identity])
        record = {'started_ns': self.started_ns, 'finished_ns': time.time_ns(),
                  'identity': list(self.identity), 'display': getattr(self, 'name', None),
                  'returncode': self.process.returncode, 'forced_shutdown': forced,
                  'remaining_owned': [list(item) for item in remaining]}
        (self.directory / 'display-lifecycle.json').write_text(json.dumps(record, indent=2) + '\n')
        if self.process.returncode != 0 or forced or remaining:
            raise EvidenceError('Xvfb did not exit normally with complete identity reclamation')


def verify_display(directory):
    try:
        record = _json_loads((Path(directory) / 'display-lifecycle.json').read_text())
    except (OSError, ValueError) as error:
        raise EvidenceError('Missing display lifecycle') from error
    identity = record.get('identity')
    if (not isinstance(identity, list) or len(identity) != 2 or
            not isinstance(identity[0], str) or not identity[0].isdigit() or int(identity[0]) < 1 or
            type(identity[1]) is not int or identity[1] < 1 or
            type(record.get('started_ns')) is not int or type(record.get('finished_ns')) is not int or
            not 0 < record['started_ns'] < record['finished_ns'] or
            not isinstance(record.get('display'), str) or not record['display'].startswith(':') or
            not record['display'][1:].isdigit() or type(record.get('returncode')) is not int or
            record['returncode'] != 0 or record.get('forced_shutdown') is not False or
            record.get('remaining_owned') != []):
        raise EvidenceError('Incomplete or failed display lifecycle')
    return record


def _read_stat(pid):
    path = PROC_ROOT / str(pid)
    text = (path / 'stat').read_text()
    tail = text[text.rfind(')') + 2:].split()
    try:
        return {'pid': str(pid), 'parent': int(tail[1]), 'group': int(tail[2]),
                'session': int(tail[3]), 'state': tail[0],
                'ticks': int(tail[11]) + int(tail[12]), 'start': int(tail[19]),
                'uid': path.stat().st_uid, 'path': path}
    except (ValueError, IndexError) as error:
        raise EvidenceError(f'Invalid process identity for PID {pid}') from error


def _references_runtime(arguments, runtime_root):
    """Match argv path components, never a substring or a neighbouring directory."""
    for argument in arguments:
        value = argument if argument.startswith('/') else argument.split('=', 1)[-1]
        if not value.startswith('/'):
            continue
        path = Path(os.path.normpath(value))
        if path.is_relative_to(runtime_root):
            return True
    return False


def _process_family(root_pid, runtime_root=None, root_started=None):
    root_pid = int(root_pid)
    runtime_root = Path(runtime_root).resolve() if runtime_root is not None else None
    all_processes = {}
    associated = set()
    for path in PROC_ROOT.iterdir():
        if not path.name.isdigit():
            continue
        try:
            item = _read_stat(path.name)
        except (FileNotFoundError, ProcessLookupError):
            continue
        except (PermissionError, EvidenceError) as error:
            if int(path.name) == root_pid:
                raise EvidenceError('Cannot read application identity') from error
            continue
        pid = int(path.name)
        all_processes[pid] = item
        if runtime_root is not None and item['uid'] == os.geteuid():
            try:
                item['argv'] = (path / 'cmdline').read_bytes().decode(errors='replace').split('\0')
            except (FileNotFoundError, ProcessLookupError, PermissionError):
                continue
            if _references_runtime(item['argv'], runtime_root):
                associated.add(pid)
    # start_new_session=True also identifies reparented same-session helpers.
    # Runtime arguments identify crashpad daemons that created another session.
    reused = root_started is not None and root_pid in all_processes and all_processes[root_pid]['start'] != root_started
    family = associated - ({root_pid} if reused else set())
    if not reused:
        family |= {root_pid} | {pid for pid, item in all_processes.items()
            if item['session'] == root_pid and item['uid'] == os.geteuid()}
    while True:
        expanded = family | {pid for pid, item in all_processes.items() if item['parent'] in family}
        if expanded == family:
            break
        family = expanded
    selected = {pid: item for pid, item in all_processes.items() if pid in family}
    if any(item['uid'] != os.geteuid() for item in selected.values()):
        raise EvidenceError('Controlled process ownership changed')
    return selected


def owned_states(identities):
    """Global PID/start checks retain zombies and exclude reused PID identities."""
    result = {}
    for pid, started in identities:
        try:
            item = _read_stat(pid)
        except (FileNotFoundError, ProcessLookupError):
            continue
        except (OSError, EvidenceError) as error:
            raise EvidenceError(f'Cannot inspect controlled PID {pid}') from error
        if item['start'] == started:
            if item['uid'] != os.geteuid():
                raise EvidenceError(f'Controlled PID {pid} ownership changed')
            result[(str(pid), started)] = item['state']
    return result


def remaining_owned(identities):
    return set(owned_states(identities))


def _libc_pidfd(name, arguments, types):
    # Some Linux Python builds omit pidfd wrappers despite kernel/libc support.
    function = getattr(_LIBC, name, None)
    if function is None:
        raise EvidenceError('Linux pidfd support is required for safe cleanup')
    function.argtypes, function.restype = types, ctypes.c_int
    result = function(*arguments)
    if result < 0:
        error = ctypes.get_errno()
        raise OSError(error, os.strerror(error))
    return result


def _open_pidfd(pid):
    if hasattr(os, 'pidfd_open'):
        return os.pidfd_open(pid)
    return _libc_pidfd('pidfd_open', (pid, 0), (ctypes.c_int, ctypes.c_uint))


def _send_pidfd(descriptor, sig):
    if hasattr(signal, 'pidfd_send_signal'):
        return signal.pidfd_send_signal(descriptor, sig)
    return _libc_pidfd('pidfd_send_signal', (descriptor, int(sig), None, 0),
                      (ctypes.c_int, ctypes.c_int, ctypes.c_void_p, ctypes.c_uint))


def _signal_owned(identity, sig):
    """Bind signals to pidfds so PID reuse cannot target an unrelated process."""
    identity = (str(identity[0]), identity[1])
    if owned_states([identity]).get(identity) in (None, 'Z', 'X'):
        return False
    try:
        descriptor = _open_pidfd(int(identity[0]))
    except ProcessLookupError:
        return False
    try:
        # Recheck after pidfd_open: a recycled PID must never receive a signal.
        if owned_states([identity]).get(identity) in (None, 'Z', 'X'):
            return False
        _send_pidfd(descriptor, sig)
    except ProcessLookupError:
        return False
    finally:
        os.close(descriptor)
    return True


def proc_snapshot(root_pid, runtime_root=None):
    root_pid = int(root_pid)
    family = _process_family(root_pid, runtime_root)
    result = {}
    for pid, item in family.items():
        identity = (str(pid), item['start'])
        path = item['path']
        category = 'other'
        try:
            command = ' '.join(item.get('argv') or (path / 'cmdline').read_bytes().decode(errors='replace').split('\0'))
            if pid == root_pid:
                category = 'app'
            elif 'WebKit' in command or 'bwrap' in command:
                category = 'webview'
            elif 'chromium' in command or 'chrome_crashpad' in command:
                category = 'browser'
            elif any(name in command for name in ('/xray', '/sing-box', '/mihomo')):
                category = 'proxy'
            else:
                category = 'other'
            if item['state'] in ('Z', 'X'):
                memory, fds = {'pss': 0, 'rss': 0, 'rss_hwm': 0}, 0
            else:
                memory = {}
                for line in (path / 'smaps_rollup').read_text().splitlines():
                    if line.startswith(('Pss:', 'Rss:')):
                        key, value, unit = line.split()
                        if unit != 'kB' or int(value) < 0:
                            raise ValueError('Invalid memory value')
                        memory[key[:-1].lower()] = int(value) * 1024
                if set(memory) != {'pss', 'rss'}:
                    raise ValueError('Missing PSS/RSS')
                high_water = [line for line in (path / 'status').read_text().splitlines() if line.startswith('VmHWM:')]
                if len(high_water) != 1:
                    raise ValueError('Missing or duplicate VmHWM')
                key, value, unit = high_water[0].split()
                if unit != 'kB' or int(value) < 0:
                    raise ValueError('Invalid VmHWM value')
                memory['rss_hwm'] = int(value) * 1024
                fds = len(list((path / 'fd').iterdir()))
            states = owned_states([identity])
            if identity not in states:
                continue
            result[str(pid)] = {'start': item['start'], 'cpu': item['ticks'] / os.sysconf('SC_CLK_TCK'),
                                'category': category, 'pss': memory['pss'], 'rss': memory['rss'],
                                'rss_hwm': memory['rss_hwm'],
                                'fds': fds, 'state': states[identity]}
        except (OSError, ValueError) as error:
            # A vanished identity is a normal exit race. Missing data for an
            # identity that is still alive must never become zero usage.
            state = owned_states([identity]).get(identity)
            if state is None:
                continue
            if state in ('Z', 'X'):
                result[str(pid)] = {'start': item['start'], 'cpu': item['ticks'] / os.sysconf('SC_CLK_TCK'),
                                    'category': category,
                                    'pss': 0, 'rss': 0, 'rss_hwm': 0, 'fds': 0, 'state': state}
                continue
            raise EvidenceError(f'Cannot read resource data for controlled PID {pid}') from error
    if str(root_pid) not in result or result[str(root_pid)]['state'] in ('Z', 'X'):
        raise EvidenceError('Application process disappeared')
    return result


def load_lifecycle(path, expected_starts=1, runtime_root=None):
    """Require every registered start to have a normal, fully reclaimed close."""
    path = Path(path)
    root = str(Path(runtime_root).resolve() if runtime_root is not None else (path.parent / 'runtime').resolve())
    if type(expected_starts) is not int or expected_starts < 1:
        raise EvidenceError('Invalid expected lifecycle count')
    try:
        events = _json_loads(path.read_text())
    except (OSError, ValueError) as error:
        raise EvidenceError('Missing or invalid lifecycle evidence') from error
    if not isinstance(events, list) or len(events) != expected_starts * 2:
        raise EvidenceError('Incomplete application lifecycle count')
    previous_time = 0
    identities = set()
    for index in range(0, len(events), 2):
        start, close = events[index:index+2]
        if not isinstance(start, dict) or not isinstance(close, dict):
            raise EvidenceError('Invalid application lifecycle event')
        identity = start.get('started_identity')
        if type(start.get('pid')) is not int or start['pid'] < 1 or not isinstance(identity, list) or len(identity) != 2 or identity[0] != str(start['pid']) or type(identity[1]) is not int or identity[1] < 1:
            raise EvidenceError('Missing started process identity')
        if tuple(identity) in identities:
            raise EvidenceError('Reused application lifecycle identity')
        identities.add(tuple(identity))
        for event, name in ((start, 'start'), (close, 'close')):
            if event.get('event') != name or type(event.get('pid')) is not int or event['pid'] != start['pid'] or event.get('root') != root or event.get('started_identity') != identity or 'returncode' not in event:
                raise EvidenceError('Mismatched application lifecycle identity')
            if type(event.get('time_ns')) is not int or event['time_ns'] <= previous_time:
                raise EvidenceError('Invalid application lifecycle timestamps')
            previous_time = event['time_ns']
            if event.get('forced_shutdown') is not False or event.get('remaining_owned') != []:
                raise EvidenceError('Application required forced shutdown or left owned processes')
        if start.get('returncode') is not None or type(close.get('returncode')) is not int or close['returncode'] != 0:
            raise EvidenceError('Application did not exit normally')
        if close.get('errors') != [] or close.get('remaining_live') != [] or close.get('remaining_zombies') != [] or not isinstance(close.get('owned'), list) or identity not in close['owned']:
            raise EvidenceError('Application lifecycle cleanup is incomplete')
    return events


def seed_catalog(runtime_root, size, fixture_time, proxies=True):
    db = sqlite3.connect(str(Path(runtime_root) / 'data/app.db'))
    db.execute('PRAGMA foreign_keys=ON')
    rows, proxy_rows, codes = [], [], []
    for index in range(size):
        suffix = f'{index:05d}'
        profile_id = f'perf-profile-{suffix}'
        rows.append((profile_id, f'性能实例 {suffix}', suffix, 'perf-chromium',
                     '[]', f'perf-proxy-{suffix}' if proxies else '__direct__', '', 'fixed',
                     json.dumps(['--disable-sync', '--no-first-run']),
                     json.dumps([f'性能标签 {index % 5}']), json.dumps([f'perf-keyword-{suffix}']),
                     fixture_time, fixture_time))
        proxy_rows.append((f'perf-proxy-{suffix}', f'性能代理 {suffix}', f'http://127.0.0.1:{20000+index}',
                           f'性能分组 {index % 10}', index))
        codes.append((profile_id, 'P' + suffix))
    with db:
        db.executemany('INSERT INTO browser_profiles(profile_id,profile_name,user_data_dir,core_id,fingerprint_args,proxy_id,proxy_config,proxy_mode,launch_args,tags,keywords,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)', rows)
        if proxies:
            db.executemany('INSERT INTO browser_proxies(proxy_id,proxy_name,proxy_config,group_name,sort_order) VALUES (?,?,?,?,?)', proxy_rows)
        db.executemany('INSERT INTO launch_codes(profile_id,code) VALUES (?,?)', codes)
    assert db.execute('SELECT COUNT(*) FROM browser_profiles').fetchone()[0] == size
    db.close()


class Application:
    def __init__(self, binary, seed, output, display, chromium, size=100, proxies=True, fixture_time='2026-11-10T00:00:00Z', reusable=False, start_url=None):
        self.output = Path(output).resolve()
        self.output.mkdir(parents=True, exist_ok=reusable)
        self.runtime = self.output / 'runtime'
        self.runtime.mkdir(exist_ok=reusable)
        (self.runtime / '.linux-perf-runtime').touch()
        self.socket = self.output / 'observer.sock'
        if len(str(self.socket).encode()) >= 104:
            raise EvidenceError('Unix socket path too long')
        self.binary, self.display = Path(binary), display
        self.size, self.proxies = size, proxies
        if not (self.runtime / 'data/app.db').exists():
            with (self.output / 'seed.log').open('wb') as stream:
                command = [str(seed), str(self.runtime), chromium, str(free_port())]
                if start_url:
                    command.append(start_url)
                subprocess.run(command, check=True,
                               stdout=stream, stderr=subprocess.STDOUT)
            seed_catalog(self.runtime, size, fixture_time, proxies)
        self.process = None
        self.owned = set()
        self.started_identity = None
        self._close_result = None
        self._startup_error = None
        self.lifecycle_path = self.output / 'lifecycle.json'
        self.lifecycle = []
        self._operation_id = 0
        if self.lifecycle_path.exists():
            try:
                previous = _json_loads(self.lifecycle_path.read_text())
            except (OSError, ValueError) as error:
                raise EvidenceError('Cannot reuse invalid lifecycle evidence') from error
            if not isinstance(previous, list) or not previous or len(previous) % 2:
                raise EvidenceError('Cannot reuse incomplete lifecycle evidence')
            self.lifecycle = load_lifecycle(self.lifecycle_path, len(previous) // 2, self.runtime)

    def _record_lifecycle(self, event, **fields):
        record = {'event': event, 'time_ns': time.time_ns(), 'root': str(self.runtime),
                  'pid': self.process.pid if self.process else None,
                  'started_identity': list(self.started_identity) if self.started_identity else None,
                  'returncode': None, 'forced_shutdown': False, 'remaining_owned': [], **fields}
        self.lifecycle.append(record)
        temporary = self.lifecycle_path.with_suffix('.json.tmp')
        temporary.write_text(json.dumps(self.lifecycle, indent=2) + '\n')
        temporary.replace(self.lifecycle_path)
        return record

    def _capture_owned(self):
        family = _process_family(self.process.pid, self.runtime,
                                 self.started_identity[1] if self.started_identity else None)
        self.owned.update((str(pid), item['start']) for pid, item in family.items())

    def start(self, timeout=180):
        if self.process is not None and self.process.poll() is None:
            raise EvidenceError('Application is already running')
        self._close_result = None
        self._startup_error = None
        self.owned = set()
        self.log = (self.output / 'application.log').open('ab')
        env = os.environ.copy()
        env.update({'DISPLAY': self.display, 'LINUX_PERF_RUNTIME': str(self.runtime),
                    'LINUX_PERF_SOCKET': str(self.socket), 'XDG_CACHE_HOME': str(self.runtime / 'cache'),
                    'XDG_DATA_HOME': str(self.runtime / 'xdg-data'), 'XDG_CONFIG_HOME': str(self.runtime / 'xdg-config'),
                    'ANT_BROWSER_DEBUG_STARTUP': '1'})
        env.pop('WAYLAND_DISPLAY', None)
        self.started = time.perf_counter()
        self.started_monotonic_ns = time.monotonic_ns()
        self.process = subprocess.Popen([str(self.binary)], cwd=self.runtime, env=env,
                                        stdout=self.log, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            item = _read_stat(self.process.pid)
            self.started_identity = (str(self.process.pid), item['start'])
            self.owned.add(self.started_identity)
            self._record_lifecycle('start', host_started_monotonic_ns=self.started_monotonic_ns)
            while time.perf_counter() - self.started < timeout:
                if self.process.poll() is not None:
                    raise EvidenceError(f'Application exited with {self.process.returncode}: {self.output / "application.log"}')
                if self.socket.exists():
                    try:
                        state = call_socket(self.socket, '/state')
                        if state.get('root') != str(self.runtime) or state.get('pid') != self.process.pid:
                            raise EvidenceError('Runtime isolation or PID mismatch')
                        self.state = state
                        self._capture_owned()
                        return
                    except (ConnectionRefusedError, FileNotFoundError):
                        pass
                time.sleep(.02)
            raise EvidenceError('Wails GUI did not become ready')
        except BaseException as error:
            self._startup_error = f'{type(error).__name__}: {error}'
            raise

    def evaluate(self, code):
        self._operation_id += 1
        identity = {'operation_id': self._operation_id,
                    'pid': self.process.pid if self.process else None,
                    'code_sha256': hashlib.sha256(code.encode()).hexdigest(),
                    'operation': next((name for name in (
                        'filterProfiles', 'filterProxies', 'saveKeyword',
                        'cancelKeyword', 'rejectedKeyword', 'instanceAction',
                        'waitRendered') if 'antPerfGui.' + name + '(' in code), 'javascript')}

        def record(event, **details):
            with (self.output / 'operations.log').open('a') as stream:
                stream.write(json.dumps({**identity, 'event': event,
                    'time_ns': time.time_ns(), **details}, ensure_ascii=False) + '\n')

        record('start')
        started = time.perf_counter()
        try:
            response = call_socket(self.socket, '/eval', {'code': code})
            if not response.get('ok'):
                raise EvidenceError('GUI operation failed: ' + json.dumps(response, ensure_ascii=False))
        except BaseException as error:
            record('failed', error=f'{type(error).__name__}: {error}',
                   elapsed_ms=(time.perf_counter() - started) * 1000)
            raise
        record('complete', elapsed_ms=(time.perf_counter() - started) * 1000)
        return response.get('value')

    def install_gui(self, script):
        self.evaluate(script + '\nreturn true;')

    def evaluate_timed(self, code, *, expected_phases=(), require_render=False, timeout=1800):
        """Time one host action through the final validated GUI result receipt.

        Phase timestamps share this same host origin. JS clocks are proof and
        diagnostic data only; they never replace these elapsed milliseconds.
        """
        if len(set(expected_phases)) != len(expected_phases) or any(not isinstance(x, str) or not x for x in expected_phases):
            raise EvidenceError('Invalid expected GUI phases')
        self._operation_id += 1
        started_ns = time.monotonic_ns()
        token = f'{self.process.pid}:{self._operation_id}:{started_ns}'
        identity = {'operation_id': self._operation_id, 'pid': self.process.pid,
                    'token': token, 'code_sha256': hashlib.sha256(code.encode()).hexdigest(),
                    'operation': 'timed_gui', 'started_monotonic_ns': started_ns}

        def record(event, **details):
            with (self.output / 'operations.log').open('a') as stream:
                stream.write(json.dumps({**identity, 'event': event, 'time_ns': time.time_ns(),
                                         **details}, ensure_ascii=False) + '\n')

        record('start', expected_phases=list(expected_phases), require_render=require_render,
               timeout_seconds=timeout)
        messages, phases, terminal, request_id = {}, {}, None, None
        request = {'code': code, 'stream': True, 'token': token, 'timeout_ms': int(timeout * 1000)}
        try:
            for message, received_ns in stream_evaluation(self.socket, request, started_ns, timeout):
                if not isinstance(message, dict) or message.get('token') != token:
                    raise EvidenceError('Timed GUI response token mismatch')
                seq = message.get('sequence')
                if type(seq) is not int or seq <= 0 or seq in messages:
                    raise EvidenceError('Duplicate or invalid GUI response sequence')
                current_id = message.get('request_id')
                if not isinstance(current_id, str) or not current_id or (request_id is not None and request_id != current_id):
                    raise EvidenceError('Timed GUI request identity mismatch')
                request_id = current_id
                messages[seq] = message
                elapsed_ms = (received_ns - started_ns) / 1e6
                if message.get('ok') is not True:
                    raise EvidenceError('GUI operation failed: ' + json.dumps(message, ensure_ascii=False))
                kind = message.get('kind')
                value = message.get('value')
                if kind == 'phase':
                    phase = message.get('phase')
                    if phase not in expected_phases or phase in phases:
                        raise EvidenceError('Unexpected or duplicate GUI phase')
                    validate_render_proof(value)
                    phases[phase] = {'elapsed_ms': elapsed_ms, 'received_monotonic_ns': received_ns,
                                     'sequence': seq, 'value': value}
                    record('phase', phase=phase, sequence=seq, elapsed_ms=elapsed_ms,
                           received_monotonic_ns=received_ns, value=value)
                elif kind == 'result':
                    if terminal is not None:
                        raise EvidenceError('Duplicate GUI terminal result')
                    if require_render:
                        validate_render_proof(value)
                    terminal = {'sequence': seq, 'value': value, 'received_monotonic_ns': received_ns}
                else:
                    raise EvidenceError('Unknown timed GUI message kind')
            if terminal is None or sorted(messages) != list(range(1, terminal['sequence'] + 1)):
                raise EvidenceError('Missing GUI terminal callback or incomplete sequence')
            if set(phases) != set(expected_phases):
                raise EvidenceError('Missing required GUI phases')
            # A final callback may arrive before an earlier phase callback. Count
            # the receipt of every required result, without using JS clock math.
            received_ns = max([terminal['received_monotonic_ns']] + [x['received_monotonic_ns'] for x in phases.values()])
            elapsed_ms = (received_ns - started_ns) / 1e6
            if elapsed_ms >= timeout * 1000:
                raise EvidenceError('GUI operation exceeded host observation deadline')
            value_hash = hashlib.sha256(json.dumps(terminal['value'], sort_keys=True, ensure_ascii=False).encode()).hexdigest()
            phase_ms = {name: phase['elapsed_ms'] for name, phase in phases.items()}
            record('complete', elapsed_ms=elapsed_ms, received_monotonic_ns=received_ns,
                   request_id=request_id, terminal_sequence=terminal['sequence'], value_sha256=value_hash,
                   phase_ms=phase_ms)
            return {'value': terminal['value'], 'elapsed_ms': elapsed_ms, 'phases': phases,
                    'operation_id': identity['operation_id'], 'phase_ms': phase_ms,
                    'journal_path': str(self.output / 'operations.log'),
                    'token': token, 'request_id': request_id, 'started_monotonic_ns': started_ns,
                    'received_monotonic_ns': received_ns}
        except BaseException as error:
            record('failed', error=f'{type(error).__name__}: {error}',
                   elapsed_ms=(time.monotonic_ns() - started_ns) / 1e6,
                   received_phases=list(phases))
            raise

    def ready(self):
        remaining = 1800 - (time.monotonic_ns() - self.started_monotonic_ns) / 1e9
        if remaining <= 0:
            raise EvidenceError('Initial GUI readiness exceeded host observation deadline')
        result = self.evaluate_timed('return await antPerfGui.initialReady('
                                     + json.dumps(self.size) + ',' + json.dumps(self.proxies) + ');',
                                     require_render=True, timeout=remaining)
        elapsed_ms = (result['received_monotonic_ns'] - self.started_monotonic_ns) / 1e6
        if elapsed_ms >= 1800000:
            raise EvidenceError('Initial GUI readiness exceeded host observation deadline')
        self.last_ready_timed = result
        self.last_ready_elapsed_ms = elapsed_ms
        self.last_ready_timing = {**result, 'elapsed_ms': elapsed_ms,
            'request_elapsed_ms': result['elapsed_ms'], 'application_ready': True,
            'request_started_monotonic_ns': result['started_monotonic_ns'],
            'started_monotonic_ns': self.started_monotonic_ns}
        with (self.output / 'operations.log').open('a') as stream:
            stream.write(json.dumps({'event': 'application_ready', 'operation_id': result['operation_id'],
                'token': result['token'], 'pid': self.process.pid,
                'application_started_monotonic_ns': self.started_monotonic_ns,
                'received_monotonic_ns': result['received_monotonic_ns'], 'elapsed_ms': elapsed_ms}) + '\n')
        return elapsed_ms

    def close(self):
        if self.process is None:
            return
        if self._close_result is not None:
            if self._close_result['errors']:
                raise EvidenceError('; '.join(self._close_result['errors']))
            return self._close_result
        errors, forced, signals = [], False, []
        remaining = {}

        def capture():
            try:
                self._capture_owned()
            except (OSError, EvidenceError) as error:
                errors.append('Cannot collect owned identities: ' + str(error))

        def inspect():
            states = {}
            for identity in self.owned:
                try:
                    states.update(owned_states([identity]))
                except EvidenceError as error:
                    states[identity] = 'unreadable'
                    message = str(error)
                    if message not in errors:
                        errors.append(message)
            return states

        def send(identity, sig):
            try:
                if _signal_owned(identity, sig):
                    signals.append({'identity': list(identity), 'signal': int(sig)})
            except (OSError, EvidenceError) as error:
                errors.append('Cannot signal controlled identity: ' + str(error))

        try:
            capture()
            if self._startup_error:
                errors.append(self._startup_error)
            if self.process.poll() is not None:
                errors.append(f'Application exited before requested shutdown: {self.process.returncode}')
            else:
                if self.started_identity is None:
                    errors.append('Application started identity is missing')
                else:
                    send(self.started_identity, signal.SIGTERM)
                try:
                    self.process.wait(timeout=SHUTDOWN_TIMEOUT)
                except subprocess.TimeoutExpired:
                    forced = True
                    errors.append('Application exceeded graceful shutdown timeout')
                    if self.started_identity is not None:
                        send(self.started_identity, signal.SIGKILL)
                    try:
                        self.process.wait(timeout=RECLAMATION_TIMEOUT)
                    except subprocess.TimeoutExpired:
                        errors.append('Application did not exit after SIGKILL')
        except BaseException as error:
            errors.append(f'Application shutdown failed: {type(error).__name__}: {error}')
        finally:
            # Complete cleanup even after failed sampling/startup or shutdown.
            # No blind killpg: every signal rechecks the recorded PID/start pair.
            capture()
            remaining = inspect()
            for identity, state in remaining.items():
                if state not in ('Z', 'X', 'unreadable'):
                    send(identity, signal.SIGTERM)
            deadline = time.monotonic() + RECLAMATION_TIMEOUT
            force_at = deadline - min(1, RECLAMATION_TIMEOUT / 2)
            killed = set()
            while remaining and time.monotonic() < deadline:
                if time.monotonic() >= force_at:
                    for identity, state in remaining.items():
                        if state not in ('Z', 'X', 'unreadable') and identity not in killed:
                            forced = True
                            killed.add(identity)
                            send(identity, signal.SIGKILL)
                self.process.poll()  # Reap the application if it exited meanwhile.
                time.sleep(.05)
                remaining = inspect()
            capture()
            remaining = inspect()
            for identity, state in remaining.items():
                if state not in ('Z', 'X', 'unreadable'):
                    forced = True
                    send(identity, signal.SIGKILL)
            if forced:
                # Forced termination is a failure even when cleanup succeeds.
                if 'Forced cleanup of controlled processes' not in errors:
                    errors.append('Forced cleanup of controlled processes')
            remaining = inspect()
            if remaining:
                errors.append('Controlled identities remained after shutdown')
            returncode = self.process.poll()
            if returncode != 0:
                errors.append(f'Application exit code was {returncode}')
            if hasattr(self, 'log'):
                self.log.close()
            with contextlib.suppress(FileNotFoundError):
                self.socket.unlink()
            self._close_result = self._record_lifecycle(
                'close', returncode=returncode, forced_shutdown=forced,
                owned=[list(identity) for identity in sorted(self.owned)],
                remaining_owned=[list(identity) for identity in sorted(remaining)],
                remaining_live=[list(identity) for identity, state in sorted(remaining.items()) if state not in ('Z', 'X')],
                remaining_zombies=[list(identity) for identity, state in sorted(remaining.items()) if state in ('Z', 'X')],
                signals=signals, errors=errors)
        if errors:
            raise EvidenceError('; '.join(errors))
        return self._close_result

    def __enter__(self):
        self.start()
        return self

    def __exit__(self, *args):
        self.close()
