"""Predeclared measurement matrix; roles may differ only by the approved catalog exception."""
import json
import os
from pathlib import Path

HERE = Path(__file__).resolve().parent
SCENARIOS = json.loads((HERE / 'scenarios.json').read_text())
V2 = 'linux-performance-v2'
GOAL_SHA256 = '9d3cb85cfbdd0d52a76f2b519a99b7b47c44edc71b54d8727d07a02d19cfd571'
BASELINE_SHA256 = 'b62741030294e2481b33c4350d6bfd72187e6f05fa66237c924a5e8c75785389'
ROLES = ('baseline', 'candidate')


def catalog_sizes(role=None):
    if role not in (None, *ROLES):
        raise ValueError('Unknown measurement role')
    return [100, 1000] if role == 'baseline' else list(SCENARIOS['catalog_sizes'])


def go_registration():
    proxy_metrics = {engine + '.' + metric: ('KiB' if metric.endswith('_kib') else 'ms')
        for engine in ('xray', 'sing-box', 'mihomo')
        for metric in ('cold_ready_ms', 'cold_request_ms', 'reuse_ready_ms', 'reuse_request_ms',
                       'cold_parent_cpu_ms', 'cold_kernel_cpu_ms', 'cold_kernel_pss_kib')}
    io_metrics = lambda prefix, operations: {
        f'{prefix}_{content}_{operation}_{metric}': ('bytes' if metric == 'alloc_bytes' else 'ms')
        for content in ('compressible', 'incompressible') for operation in operations
        for metric in ('wall_ms', 'cpu_ms', 'alloc_bytes')}
    return [
        ('proxy', 'ant-chrome/backend/internal/proxy', 'TestLinuxPerfProxyLocalPaths',
         {'suite': 'proxy_local_paths', 'metrics': proxy_metrics}),
        ('download', 'ant-chrome/backend/internal/browser', 'TestLinuxPerfCoreDownload',
         {'suite': 'file_io', 'component': 'core_download', 'metrics': io_metrics('download', ('range', 'stream'))}),
        ('archive', 'ant-chrome/backend/internal/snapshot', 'TestLinuxPerfSnapshotArchive',
         {'suite': 'file_io', 'component': 'snapshot_archive', 'metrics': io_metrics('snapshot', ('zip', 'unzip'))}),
        ('logger', 'ant-chrome/backend/internal/logger', 'TestLinuxPerfLoggerMetrics',
         {'suite': 'memory_logger', 'metrics': {'write_ns_per_op': 'ns/op', 'cpu_ns_per_op': 'ns/op',
                                              'allocated_bytes_per_op': 'B/op'}}),
    ]


def metric_policy(unit='ms', core=True, eligible=False, minimum=30, noise=0):
    return {'unit': unit, 'core': core, 'improvement_eligible': eligible,
            'minimum_samples': minimum, 'absolute_noise': noise}


def go_policy(name, key, raw_unit):
    unit, multiplier = {'KiB': ('bytes', 1024), 'ns/op': ('ms', 1e-6),
                         'B/op': ('bytes', 1)}.get(raw_unit, (raw_unit, 1))
    core = 'alloc' not in key
    return metric_policy(unit=unit, core=core, eligible=name == 'proxy' and 'cold_ready' in key,
                         noise=4096 if key.endswith('pss_kib') else 0), multiplier


def expected_metrics(role=None):
    result = {}
    for size in catalog_sizes(role):
        for operation in ('initial_render', 'profile_filter', 'profile_restore', 'proxy_filter',
                          'proxy_restore', 'keyword_save_visible', 'keyword_save_closed',
                          'profiles_api', 'groups_api', 'proxies_api'):
            result[f'catalog.{size}.{operation}'] = metric_policy(
                eligible=(role != 'candidate' or size != 10000) and operation in (
                    'profile_filter', 'proxy_filter', 'keyword_save_visible'),
                core=(role != 'candidate' or size != 10000) and operation != 'keyword_save_closed',
                minimum=10 if operation == 'initial_render' else 30)
    if role == 'candidate':
        result.update(catalog_resource_policies())
    for count in SCENARIOS['runtime_counts']:
        result[f'runtime.{count}.application_ready'] = metric_policy(eligible=True, minimum=10)
        if count:
            result[f'runtime.{count}.instance_ready'] = metric_policy(eligible=True, minimum=10)
            result[f'runtime.{count}.stop'] = metric_policy(minimum=10)
        result[f'runtime.{count}.goroutines'] = metric_policy('count', core=False, minimum=10)
        for category in SCENARIOS['runtime']['categories']:
            prefix = f'runtime.{count}.{category}.'
            result[prefix+'cpu'] = metric_policy('seconds', minimum=10, noise=2/os.sysconf('SC_CLK_TCK'))
            result[prefix+'pss'] = metric_policy('bytes', minimum=10, noise=4096)
            for metric, unit in (('rss', 'bytes'), ('fds', 'count'), ('processes', 'count')):
                result[prefix+metric] = metric_policy(unit, core=False, minimum=10)
    for name, _, _, registration in go_registration():
        for key, unit in registration['metrics'].items():
            result[name+'.'+key] = go_policy(name, key, unit)[0]
    return result


def expected_checks(role=None):
    return ['built_real_wails_elf64'] + [f'catalog_{size}_{check}'
        for size in catalog_sizes(role)
        for check in ('exact_results_gui_save_sqlite', 'restart_persistence', 'cancel_and_error',
                      'backend_event_refresh')] + [
        f'runtime_{count}_{index}_real_page_states_reclamation'
        for count in SCENARIOS['runtime_counts'] for index in range(SCENARIOS['independent_rounds'])
    ] + (['baseline_10000_pressure_reference'] if role == 'baseline' else []) + ([
        f'catalog_10000_{index}_resources' for index in range(SCENARIOS['independent_rounds'])
    ] if role == 'candidate' else []) + (['registered_resource_samples', 'registered_gui_receipts'] if role else []) + ['go_'+name for name, *_ in go_registration()] + [
        'all_registered_microbenchmarks', 'all_application_lifecycles']


def catalog_resource_policies():
    result = {'catalog.10000.resources.goroutines': metric_policy('count', core=False, minimum=10)}
    for category in SCENARIOS['runtime']['categories']:
        for metric, unit in (('cpu', 'seconds'), ('pss', 'bytes'), ('rss', 'bytes'),
                             ('fds', 'count'), ('processes', 'count'),
                             ('observed_pss_peak', 'bytes'), ('observed_rss_peak', 'bytes'),
                             ('rss_hwm_upper_bound', 'bytes')):
            result[f'catalog.10000.resources.{category}.{metric}'] = metric_policy(
                unit, core=False, minimum=10)
    return result


def expected_commands():
    return ['npm-ci', 'frontend-build', 'wails-build', 'seed-build'] + [
        'go-'+x[0] for x in go_registration()] + ['benchmark-packages', 'benchmarks']


def acceptance_contract(role):
    if role not in ROLES:
        raise ValueError('Explicit baseline/candidate role required')
    paired = expected_metrics('baseline')
    only_candidate = {name: policy for name, policy in expected_metrics('candidate').items()
                      if name not in paired}
    return {'version': V2, 'goal_sha256': GOAL_SHA256, 'role': role,
            'paired_catalog_sizes': [100, 1000], 'candidate_only_catalog_sizes': [10000],
            'baseline_pressure_exception': {'catalog_size': 10000,
                'complete_before_exemption': False, 'candidate_exemption': False},
            'paired_metrics': paired, 'candidate_only_metrics': only_candidate,
            'gui_clock': 'host dispatch before queue through verified business result, two frames and receipt',
            'resource_cpu': 'raw owned-process CPU delta scaled to 30 seconds using actual snapshot span; raw delta retained',
            'sample_registration': {'short_samples': 30, 'independent_rounds': 10,
                                    'warmup_seconds': 15, 'sample_seconds': 30},
            'observation_timeout_seconds': 1800}
