package backend

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"ant-chrome/backend/internal/config"
)

type launchGateTestResult struct {
	profileID string
	release   func()
	queued    bool
}

func launchGateTestState(app *App) (occupied, waiting int) {
	gate := app.launchGate()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.occupied(), gate.waiting
}

func assertLaunchGateTestState(t *testing.T, app *App, wantOccupied, wantWaiting int) {
	t.Helper()
	occupied, waiting := launchGateTestState(app)
	if occupied != wantOccupied || waiting != wantWaiting {
		t.Fatalf("gate state: occupied=%d waiting=%d; want occupied=%d waiting=%d", occupied, waiting, wantOccupied, wantWaiting)
	}
}

func waitForLaunchGateTestQueue(t *testing.T, app *App, want int) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		_, waiting := launchGateTestState(app)
		if waiting == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("waiting requests=%d, want %d", waiting, want)
		case <-tick.C:
		}
	}
}

func receiveLaunchGateTestResult(t *testing.T, results <-chan launchGateTestResult) launchGateTestResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("waiting request was not admitted after release")
		return launchGateTestResult{}
	}
}

func TestInstanceLaunchGateMultipleWaiters(t *testing.T) {
	app := &App{config: &config.Config{}}
	app.config.Browser.MaxConcurrentInstances = 2
	first, _ := app.acquireInstanceLaunchSlot("first")
	defer first()
	second, _ := app.acquireInstanceLaunchSlot("second")
	defer second()
	const waiterCount = 6
	results := make(chan launchGateTestResult, waiterCount)
	for i := 0; i < waiterCount; i++ {
		profileID := fmt.Sprintf("waiter-%d", i)
		go func() {
			release, queued := app.acquireInstanceLaunchSlot(profileID)
			results <- launchGateTestResult{profileID: profileID, release: release, queued: queued}
		}()
	}
	waitForLaunchGateTestQueue(t, app, waiterCount)
	assertLaunchGateTestState(t, app, 2, waiterCount)
	first()
	seen := make(map[string]bool, waiterCount)
	for i := 0; i < waiterCount; i++ {
		result := receiveLaunchGateTestResult(t, results)
		if !result.queued || seen[result.profileID] {
			t.Fatalf("unexpected admission: profile=%s queued=%t duplicate=%t", result.profileID, result.queued, seen[result.profileID])
		}
		seen[result.profileID] = true
		assertLaunchGateTestState(t, app, 2, waiterCount-i-1)
		result.release()
	}
	second()
	assertLaunchGateTestState(t, app, 0, 0)
}

func TestInstanceLaunchGateQueuedDuplicatesDrainWaiting(t *testing.T) {
	app := &App{config: &config.Config{}}
	app.config.Browser.MaxConcurrentInstances = 1
	blocker, _ := app.acquireInstanceLaunchSlot("blocker")
	defer blocker()
	results := make(chan launchGateTestResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			release, queued := app.acquireInstanceLaunchSlot("same-profile")
			results <- launchGateTestResult{release: release, queued: queued}
		}()
	}
	waitForLaunchGateTestQueue(t, app, 2)
	blocker()
	first := receiveLaunchGateTestResult(t, results)
	second := receiveLaunchGateTestResult(t, results)
	if !first.queued || !second.queued {
		t.Fatal("both duplicate requests must retain their queued=true result")
	}
	assertLaunchGateTestState(t, app, 1, 0)
	first.release()
	assertLaunchGateTestState(t, app, 1, 0)
	second.release()
	assertLaunchGateTestState(t, app, 0, 0)
}

func TestInstanceLaunchGateDuplicateFailureKeepsOtherRequest(t *testing.T) {
	app := &App{config: &config.Config{}}
	app.config.Browser.MaxConcurrentInstances = 1
	first, _ := app.acquireInstanceLaunchSlot("same-profile")
	second, queued := app.acquireInstanceLaunchSlot("same-profile")
	if queued {
		t.Fatal("duplicate request should share the profile's existing slot")
	}
	assertLaunchGateTestState(t, app, 1, 0)
	// The first caller may fail after the second has begun its own start.
	// Repeating its failure cleanup must not withdraw the second caller's slot.
	first()
	first()
	assertLaunchGateTestState(t, app, 1, 0)
	second()
	assertLaunchGateTestState(t, app, 0, 0)
}

func TestInstanceLaunchGateStaleReleaseDoesNotReleaseNewAdmission(t *testing.T) {
	for _, lifecycleRelease := range []bool{false, true} {
		t.Run(fmt.Sprintf("lifecycle_release=%t", lifecycleRelease), func(t *testing.T) {
			app := &App{config: &config.Config{}}
			app.config.Browser.MaxConcurrentInstances = 1
			oldRelease, _ := app.acquireInstanceLaunchSlot("same-profile")
			if lifecycleRelease {
				app.releaseInstanceLaunchSlot("same-profile")
			} else {
				oldRelease()
			}
			newRelease, queued := app.acquireInstanceLaunchSlot("same-profile")
			if queued {
				t.Fatal("new start should acquire the released slot")
			}
			oldRelease()
			assertLaunchGateTestState(t, app, 1, 0)
			newRelease()
			assertLaunchGateTestState(t, app, 0, 0)
		})
	}
}

func TestInstanceLaunchGateConcurrentInitializationAndRelease(t *testing.T) {
	for i := 0; i < 64; i++ {
		app := &App{config: &config.Config{}}
		app.config.Browser.MaxConcurrentInstances = 1
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			release, _ := app.acquireInstanceLaunchSlot("profile")
			release()
		}()
		go func() {
			defer workers.Done()
			<-start
			// A stop for a different profile must be safe while the first
			// acquisition initializes the gate.
			app.releaseInstanceLaunchSlot("already-stopped-profile")
		}()
		close(start)
		workers.Wait()
		assertLaunchGateTestState(t, app, 0, 0)
	}
}

func TestInstanceLaunchGateConcurrentRepeatedRelease(t *testing.T) {
	app := &App{config: &config.Config{}}
	app.config.Browser.MaxConcurrentInstances = 1
	first, _ := app.acquireInstanceLaunchSlot("profile")
	second, _ := app.acquireInstanceLaunchSlot("profile")
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			first()
		}()
	}
	workers.Wait()
	assertLaunchGateTestState(t, app, 1, 0)
	app.releaseInstanceLaunchSlot("profile")
	app.releaseInstanceLaunchSlot("profile")
	second()
	assertLaunchGateTestState(t, app, 0, 0)
}
