package singleinstance

import (
	"testing"
	"time"
)

func TestSecondaryAntForwardsOneTicketBeforePrimaryWindowIsReady(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("XDG_STATE_HOME", root)
	g, primary, err := Acquire(root, "")
	if err != nil || !primary {
		t.Fatal(primary, err)
	}
	defer g.Close()
	const uri = "ant-browser://keypouch/create-open/abc_abc_abc_abc_abc_abc_abc_abc_"
	done := make(chan error, 1)
	go func() {
		_, primary, err := Acquire(root, uri)
		if primary {
			t.Error("started second primary")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sender waited for the primary startup")
	}
	select {
	case req := <-g.Activations():
		if req.URI != uri {
			t.Fatal("lost desktop link")
		}
		req.Complete()
	case <-time.After(time.Second):
		t.Fatal("no activation")
	}
	if len(g.activation) != 0 {
		t.Fatal("ticket queued more than once")
	}
}
