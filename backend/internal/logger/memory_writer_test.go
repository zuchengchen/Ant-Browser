package logger

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemoryWriterRetainsNewestEntriesInChronologicalOrder(t *testing.T) {
	for _, size := range []int{1, 3, 2000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			writer := &MemoryWriter{entries: make([]MemoryLogEntry, 0, size), maxSize: size}
			for i := 0; i < size*3+1; i++ {
				entry := &LogEntry{Timestamp: time.Unix(int64(i), 0), Level: INFO, Component: "MemoryTest", Message: fmt.Sprint(i)}
				if err := writer.Write(entry); err != nil {
					t.Fatal(err)
				}
				if i >= 6 && i%size != 0 && i%size != size-1 {
					continue
				}
				got := writer.GetEntries()
				first := i + 1 - size
				if first < 0 {
					first = 0
				}
				if len(got) != i+1-first {
					t.Fatalf("write %d: len=%d", i, len(got))
				}
				for j, item := range got {
					if item.Message != fmt.Sprint(first+j) {
						t.Fatalf("write %d slot %d: message=%q want=%d", i, j, item.Message, first+j)
					}
				}
			}
		})
	}
}

func TestMemoryWriterSnapshotAndClearAfterWrapping(t *testing.T) {
	writer := &MemoryWriter{maxSize: 2}
	for _, message := range []string{"one", "two", "three"} {
		if err := writer.Write(NewLogEntry(INFO, "MemoryTest", message)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := writer.GetEntries()
	writer.Clear()
	if got := writer.GetEntries(); len(got) != 0 {
		t.Fatalf("clear left %d entries", len(got))
	}
	if err := writer.Write(NewLogEntry(INFO, "MemoryTest", "four")); err != nil {
		t.Fatal(err)
	}
	got := writer.GetEntries()
	if len(got) != 1 || got[0].Message != "four" {
		t.Fatalf("clear did not reset insertion order: %+v", got)
	}
	if len(snapshot) != 2 || snapshot[0].Message != "two" || snapshot[1].Message != "three" {
		t.Fatalf("snapshot changed after clear/write: %+v", snapshot)
	}
}

func TestMemoryWriterKeepsExistingFiltering(t *testing.T) {
	writer := &MemoryWriter{maxSize: 3}
	for _, entry := range []*LogEntry{
		nil,
		NewLogEntry(INFO, "Xray", "复用 xray 桥接进程"),
		NewLogEntry(WARN, "Xray", "复用 xray 桥接进程"),
		NewLogEntry(INFO, "Other", "复用 xray 桥接进程"),
	} {
		if err := writer.Write(entry); err != nil {
			t.Fatal(err)
		}
	}
	got := writer.GetEntries()
	if len(got) != 2 || got[0].Level != "WARN" || got[1].Component != "Other" {
		t.Fatalf("memory log filtering changed: %+v", got)
	}
}

func TestMemoryWriterConcurrentWritesAndSnapshots(t *testing.T) {
	writer := &MemoryWriter{maxSize: 200}
	var writers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		writers.Add(1)
		go func(worker int) {
			defer writers.Done()
			for i := 0; i < 500; i++ {
				_ = writer.Write(NewLogEntry(INFO, "Concurrent", fmt.Sprintf("%d/%d", worker, i)))
			}
		}(worker)
	}
	done := make(chan struct{})
	go func() { writers.Wait(); close(done) }()
	for {
		got := writer.GetEntries()
		if len(got) > 200 {
			t.Fatalf("snapshot exceeded limit: %d", len(got))
		}
		for _, item := range got {
			if item.Message == "" || item.Component != "Concurrent" {
				t.Fatalf("incomplete concurrent entry: %+v", item)
			}
		}
		select {
		case <-done:
			if got := writer.GetEntries(); len(got) != 200 {
				t.Fatalf("completed snapshot count: %d", len(got))
			}
			return
		default:
		}
	}
}
