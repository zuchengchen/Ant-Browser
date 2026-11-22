package browser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnpackedExtensionPathID(t *testing.T) {
	for _, test := range []struct {
		path    string
		windows bool
		want    string
	}{
		{"/opt/Ant Example/扩展", false, "odhoacfnlegjkamopplhoajlejeelaak"},
		{`c:\Ant\扩展`, true, "cjpikhkkfockbehgkdbjidmnmhebdbmd"},
		{`C:\Ant\扩展`, true, "cjpikhkkfockbehgkdbjidmnmhebdbmd"},
	} {
		if got := unpackedExtensionPathID(test.path, test.windows); got != test.want {
			t.Errorf("Chromium ID for %q = %q, want %q", test.path, got, test.want)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"key":"YWJj"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := UnpackedExtensionID(dir); err != nil || got != extensionIDFromPublicKey([]byte("abc")) {
		t.Fatalf("manifest key identity = %q, %v", got, err)
	}
}
