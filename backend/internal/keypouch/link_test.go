package keypouch

import (
	"strings"
	"testing"
)

func TestLinksOnlyAcceptTheTwoNativeActionsAndAnOpaqueTicket(t *testing.T) {
	id := strings.Repeat("ab-_", 8)
	for _, action := range []string{"pair", "create-open"} {
		op, ticket, ok := ParseLink("ant-browser://keypouch/" + action + "/" + id)
		if !ok || op != action || ticket != id {
			t.Fatal(action, ticket)
		}
	}
	for _, raw := range []string{
		"ant-browser://other/pair/" + id, "ant-browser://keypouch/pair/" + id + "?site=elsewhere",
		"ant-browser://keypouch/create-open/" + id + "#more", "ant-browser://keypouch/pair/../" + id,
		"ant-browser://keypouch/run/" + id, "ant-browser://keypouch/pair/" + id[:30],
	} {
		if _, _, ok := ParseLink(raw); ok {
			t.Errorf("invalid deep link accepted: %s", raw)
		}
	}
}
