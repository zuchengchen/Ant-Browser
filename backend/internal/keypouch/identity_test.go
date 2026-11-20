package keypouch

import (
	"errors"
	"os"
	"sync"
	"testing"
)

func TestDeviceIdentitySurvivesRestartAndConcurrentPairing(t *testing.T) {
	dir := t.TempDir()
	const site = "https://keypouch.example"
	if _, err := OpenDevice(dir, "first-install", site, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unpaired start should leave identity absent: %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("unpaired startup wrote a device identity", err)
	}
	keys := make(chan string, 6)
	var wg sync.WaitGroup
	for range cap(keys) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := OpenDevice(dir, "first-install", site, true)
			if err != nil {
				t.Error(err)
				return
			}
			keys <- d.id
		}()
	}
	wg.Wait()
	close(keys)
	d, err := OpenDevice(dir, "first-install", site, false)
	if err != nil {
		t.Fatal(err)
	}
	for id := range keys {
		if id != d.id {
			t.Fatal("concurrent pairing generated conflicting device identities")
		}
	}
	other, err := OpenDevice(dir, "second-install", site, true)
	if err != nil || other.id == d.id {
		t.Fatal("installations must carry their own identity", err)
	}
}
