//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestPortableDesktopEntryKeepsExecutableAndURIAsSeparateArguments(t *testing.T) {
	path := "/opt/Ant 50%/special$ant/ant-chrome"
	entry, err := portableDesktopEntry(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entry, `Exec="/opt/Ant 50%%/special\\$ant/ant-chrome" %u`+"\n") ||
		!strings.Contains(entry, "MimeType=x-scheme-handler/ant-browser;\n") {
		t.Fatalf("invalid desktop entry: %s", entry)
	}
	for _, invalid := range []string{"relative/ant", "/opt/Ant\nExec=other"} {
		if _, err := portableDesktopEntry(invalid); err == nil {
			t.Fatalf("invalid executable accepted: %q", invalid)
		}
	}
}
