//go:build linux && linuxperf

package logger

import (
	"encoding/json"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestLinuxPerfLoggerMetrics measures steady logging after the viewer's bounded
// buffer is full. Setup, GC and snapshot assertions are excluded from timings.
// Its complete current-run JSON is consumed by the Linux performance harness.
func TestLinuxPerfLoggerMetrics(t *testing.T) {
	const samples, writesPerSample = 30, 20000
	writer := &MemoryWriter{entries: make([]MemoryLogEntry, 0, defaultMemoryBufferSize), maxSize: defaultMemoryBufferSize}
	entry := &LogEntry{Timestamp: time.Unix(1700000000, 0), Level: INFO, Component: "LinuxPerf", Message: "synthetic background event", Fields: map[string]interface{}{"synthetic": true}}
	for i := 0; i < defaultMemoryBufferSize; i++ {
		if err := writer.Write(entry); err != nil {
			t.Fatal(err)
		}
	}
	metrics := map[string][]float64{"write_ns_per_op": {}, "cpu_ns_per_op": {}, "allocated_bytes_per_op": {}}
	for sample := 0; sample < samples; sample++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		cpuBefore := linuxPerfLoggerCPU(t)
		startedAt := time.Now()
		for i := 0; i < writesPerSample; i++ {
			if err := writer.Write(entry); err != nil {
				t.Fatal(err)
			}
		}
		elapsed := time.Since(startedAt)
		cpuElapsed := linuxPerfLoggerCPU(t) - cpuBefore
		runtime.ReadMemStats(&after)
		metrics["write_ns_per_op"] = append(metrics["write_ns_per_op"], float64(elapsed)/writesPerSample)
		metrics["cpu_ns_per_op"] = append(metrics["cpu_ns_per_op"], cpuElapsed/writesPerSample)
		metrics["allocated_bytes_per_op"] = append(metrics["allocated_bytes_per_op"], float64(after.TotalAlloc-before.TotalAlloc)/writesPerSample)
		got := writer.GetEntries()
		if len(got) != defaultMemoryBufferSize || got[len(got)-1].Message != entry.Message || got[0].Component != "LinuxPerf" {
			t.Fatal("bounded memory log lost expected entries")
		}
	}
	encoded, err := json.Marshal(map[string]interface{}{
		"schema_version": 1, "suite": "memory_logger", "samples": samples, "writes_per_sample": writesPerSample,
		"metrics":      metrics,
		"metric_units": map[string]string{"write_ns_per_op": "ns/op", "cpu_ns_per_op": "ns/op", "allocated_bytes_per_op": "B/op"},
		"evidence":     map[string]interface{}{"retained_entries": defaultMemoryBufferSize, "timestamp": "2023-11-14T22:13:20Z", "cpu_resolution_ns": 1000, "cpu_source": "getrusage RUSAGE_SELF"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LINUXPERF_JSON:%s", encoded)
}

func linuxPerfLoggerCPU(t *testing.T) float64 {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return float64(usage.Utime.Sec+usage.Stime.Sec)*1e9 + float64(usage.Utime.Usec+usage.Stime.Usec)*1000
}
