package keypouch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSampleRediscoversBindingsAndReportsLocalOpenCloseAndFailure(t *testing.T) {
	var sample int64
	var states []string
	var inspections int
	open := true
	post := func(_ context.Context, path string, body, dst any) error {
		switch path {
		case namespace + "bindings":
			sample++
			items := []binding{}
			for i := 0; i < 25; i++ {
				items = append(items, binding{fmt.Sprint(i), "tracked-profile"})
			}
			items = append(items, binding{"25", "unavailable"})
			b, _ := json.Marshal(map[string]any{"sample_ms": sample, "instances": items})
			return json.Unmarshal(b, dst)
		case namespace + "report":
			b, err := json.Marshal(body)
			var got struct {
				Sample int64         `json:"sample_ms"`
				Items  []observation `json:"instances"`
			}
			if err != nil || json.Unmarshal(b, &got) != nil || got.Sample != sample || len(got.Items) == 0 || len(got.Items) > 24 {
				t.Fatal("invalid report", string(b), err)
			}
			for i, item := range got.Items {
				if item.Ticket == "" || item.ProfileID == "" {
					t.Fatalf("missing binding at index %d", i)
				}
				states = append(states, item.State)
			}
		default:
			t.Fatal("unexpected request", path)
		}
		return nil
	}
	r := Reporter{Post: post, Snapshot: func(_ context.Context, profiles []string) (map[string]bool, error) {
		inspections++
		if len(profiles) != 2 || profiles[0] != "tracked-profile" || profiles[1] != "unavailable" {
			t.Fatalf("expected only unique bound profiles, got %v", profiles)
		}
		return map[string]bool{"tracked-profile": open, "unbound": true}, nil
	}}
	for _, state := range []string{"open", "closed", "open"} {
		states = nil
		before := inspections
		if err := r.Sample(context.Background()); err != nil {
			t.Fatal(err)
		}
		if inspections != before+1 || len(states) != 26 || strings.Join(states, ",") != strings.Repeat(state+",", 25)+"unknown" {
			t.Fatal("unexpected sample", inspections-before, states)
		}
		open = !open
	}
}
