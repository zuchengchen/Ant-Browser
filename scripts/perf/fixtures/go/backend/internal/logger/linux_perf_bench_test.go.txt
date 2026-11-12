package logger

import (
	"testing"
	"time"
)

// The log viewer keeps its newest 2000 entries. Warm the buffer before timing
// to measure sustained background/bridge activity, not only startup capacity.
func BenchmarkLinuxPerfMemoryLogWrite(b *testing.B) {
	writer := &MemoryWriter{entries: make([]MemoryLogEntry, 0, defaultMemoryBufferSize), maxSize: defaultMemoryBufferSize}
	entry := &LogEntry{Timestamp: time.Unix(1700000000, 0), Level: INFO, Component: "LinuxPerf", Message: "synthetic background event", Fields: map[string]interface{}{"synthetic": true}}
	for i := 0; i < defaultMemoryBufferSize; i++ {
		if err := writer.Write(entry); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := writer.Write(entry); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if got := writer.GetEntries(); len(got) != defaultMemoryBufferSize || got[len(got)-1].Message != entry.Message {
		b.Fatal("memory log lost expected entries")
	}
}
