"""Calibration of the full GUI run's required lifecycle coverage."""
import sys
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from evidence import EvidenceError
from linux_perf import application_lifecycles
import linux_perf


class FailureChainTests(unittest.TestCase):
    def test_operation_and_cleanup_failures_both_survive_in_final_evidence(self):
        self.check_failure_chain(EvidenceError)

    def test_product_failure_is_not_reclassified_as_cleanup_failure(self):
        self.check_failure_chain(linux_perf.ProductFailure)

    def test_direct_failure_and_benign_error_words_keep_their_type(self):
        for error, kind in [
                (linux_perf.ProductFailure('incorrect saved row'), 'product_behavior'),
                (linux_perf.ProducerFailure('exit 1'), 'producer_failure_requires_diagnostic_review'),
                (EvidenceError('message contains ProductFailure and FAIL'), 'measurement_or_verifier_failure')]:
            with self.subTest(kind=kind):
                self.assertEqual(linux_perf.failure_chain(error), [
                    {'type': type(error).__name__, 'message': str(error), 'kind': kind}])

    def test_explicit_cause_and_suppressed_context_follow_traceback_semantics(self):
        original = linux_perf.ProductFailure('wrong result')
        wrapper = EvidenceError('wrapper')
        wrapper.__cause__ = original
        wrapper.__context__ = RuntimeError('unrelated context')
        self.assertEqual([item['type'] for item in linux_perf.failure_chain(wrapper)],
                         ['EvidenceError', 'ProductFailure'])
        wrapper.__cause__ = None
        wrapper.__suppress_context__ = True
        self.assertEqual(len(linux_perf.failure_chain(wrapper)), 1)

    def check_failure_chain(self, operation_error):
        def fail_with_cleanup(*args):
            try:
                raise operation_error('original GUI timeout')
            finally:
                raise EvidenceError('cleanup exit -9')

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            output = root / 'failed-run'
            args = SimpleNamespace(root=str(root), output=str(output),
                                   chromium='/synthetic/chromium', smoke=False)
            with patch.multiple(linux_perf,
                    source_manifest=lambda root: {'main.go': 'synthetic'},
                    fixture_hash=lambda: 'fixture', sha256=lambda path: 'hash',
                    prepare=fail_with_cleanup), \
                    patch.dict(linux_perf.os.environ, {'LINUXPERF_MIHOMO_BINARY': '/synthetic/mihomo'}), \
                    patch.object(linux_perf.subprocess, 'check_output', return_value='synthetic version'):
                with self.assertRaisesRegex(EvidenceError, 'cleanup exit -9'):
                    linux_perf.measure(args)
            report = json.loads((output / 'results.json').read_text())
            self.assertEqual(report['status'], 'failed')
            self.assertIn('original GUI timeout', report['exception_traceback'])
            self.assertIn('cleanup exit -9', report['exception_traceback'])
            self.assertEqual(report['failure_kind'], 'chained_failures_require_diagnostic_review')
            self.assertEqual([item['type'] for item in report['exception_chain']],
                             ['EvidenceError', operation_error.__name__])
            self.assertEqual(report['exception_chain'][1]['kind'],
                'product_behavior' if operation_error is linux_perf.ProductFailure else
                'measurement_or_verifier_failure')
            self.assertFalse((output / 'complete.json').exists())


class LifecycleCoverageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.output = Path(self.temporary.name)
        self.expected = {f'c{size}-{index}': 2 if index == 0 else 1
                         for size in (100, 1000, 10000) for index in range(10)}
        self.expected.update({f'r{count}-{index}': 1
                              for count in (0, 1, 5, 10) for index in range(10)})
        for name in self.expected:
            path = self.output / name / 'lifecycle.json'
            path.parent.mkdir()
            path.write_text('[]')

    def test_all_70_rounds_and_73_launches_are_required(self):
        calls = {}

        def verify(path, expected_starts):
            calls[path.parent.name] = expected_starts
            return list(range(expected_starts * 2))

        with patch('runtime.load_lifecycle', side_effect=verify, create=True):
            evidence = application_lifecycles(self.output)
        self.assertEqual(calls, self.expected)
        self.assertEqual(len(evidence), 70)
        self.assertEqual(sum(len(events) // 2 for events in evidence.values()), 73)

    def test_missing_round_is_rejected(self):
        (self.output / 'r10-9/lifecycle.json').unlink()
        with patch('runtime.load_lifecycle', create=True), self.assertRaises(EvidenceError):
            application_lifecycles(self.output)

    def test_v2_baseline_requires_exactly_60_rounds_and_62_starts(self):
        # Unregistered large-baseline lifecycles are not silently discarded.
        with self.assertRaises(EvidenceError):
            application_lifecycles(self.output, role='baseline')
        for index in range(10):
            (self.output / f'c10000-{index}/lifecycle.json').unlink()
        with patch('runtime.load_lifecycle', side_effect=lambda path, expected_starts:
                   list(range(expected_starts * 2))):
            result = application_lifecycles(self.output, role='baseline')
        self.assertEqual(len(result), 60)
        self.assertEqual(sum(len(events) // 2 for events in result.values()), 62)
        with self.assertRaises(EvidenceError):
            application_lifecycles(self.output, role='candidate')

    def test_unregistered_round_is_rejected(self):
        extra = self.output / 'r10-10/lifecycle.json'
        extra.parent.mkdir()
        extra.write_text('[]')
        with patch('runtime.load_lifecycle', create=True), self.assertRaises(EvidenceError):
            application_lifecycles(self.output)

    def test_failed_process_verification_is_propagated(self):
        with patch('runtime.load_lifecycle', side_effect=EvidenceError('exit 1'), create=True):
            with self.assertRaisesRegex(EvidenceError, 'exit 1'):
                application_lifecycles(self.output)


if __name__ == '__main__':
    unittest.main()
