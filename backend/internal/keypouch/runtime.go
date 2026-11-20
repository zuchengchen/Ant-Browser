package keypouch

import (
	"context"
	"time"
)

const namespace = "/api/ant/device/instance-runtime/"

type binding struct {
	Ticket    string `json:"ticket"`
	ProfileID string `json:"profile_id"`
}

type observation struct {
	binding
	State string `json:"state"`
}

// Post signs with the same device identity that completed the open request.
type Reporter struct {
	Post     func(ctx context.Context, path string, body, response any) error
	Snapshot func(ctx context.Context, profiles []string) (map[string]bool, error)
}

// Run sends a fresh snapshot at startup, on an instance transition, and every ten
// seconds while the desktop is running. Each pass finishes before another starts.
func (r Reporter) Run(ctx context.Context, changed <-chan struct{}) {
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for ctx.Err() == nil {
		sample, cancel := context.WithTimeout(ctx, 25*time.Second)
		_ = r.Sample(sample)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-heartbeat.C:
		}
	}
}

// Sample discovers current bindings before checking the local process manager. Retrying
// starts a new sample so a delayed request cannot extend an old observation's lifetime.
func (r Reporter) Sample(ctx context.Context) error {
	var bindings struct {
		SampleMS int64     `json:"sample_ms"`
		Items    []binding `json:"instances"`
	}
	if err := r.Post(ctx, namespace+"bindings", struct{}{}, &bindings); err != nil {
		return err
	}
	if len(bindings.Items) == 0 {
		return nil
	}
	profiles := make([]string, 0, len(bindings.Items))
	seen := make(map[string]bool)
	for _, b := range bindings.Items {
		if !seen[b.ProfileID] {
			profiles = append(profiles, b.ProfileID)
			seen[b.ProfileID] = true
		}
	}
	states, inspectionErr := r.Snapshot(ctx, profiles)
	for start := 0; start < len(bindings.Items); start += 24 {
		part := bindings.Items[start:min(start+24, len(bindings.Items))]
		reports := make([]observation, 0, len(part))
		for _, b := range part {
			if err := ctx.Err(); err != nil {
				return err
			}
			state := "unknown"
			if running, ok := states[b.ProfileID]; ok && inspectionErr == nil {
				if running {
					state = "open"
				} else {
					state = "closed"
				}
			}
			reports = append(reports, observation{b, state})
		}
		body := struct {
			SampleMS int64         `json:"sample_ms"`
			Items    []observation `json:"instances"`
		}{bindings.SampleMS, reports}
		if err := r.Post(ctx, namespace+"report", body, nil); err != nil {
			return err
		}
	}
	return inspectionErr
}
