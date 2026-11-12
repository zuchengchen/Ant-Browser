package browser

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type coreArchiveTestEntry struct {
	name string
	body string
	link string
	dir  bool
}

func makeCoreTestArchive(t *testing.T, format string, entries []coreArchiveTestEntry) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "core."+format)
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	if format == "zip" {
		writer := zip.NewWriter(file)
		for _, entry := range entries {
			header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
			header.SetMode(0o755)
			body := entry.body
			if entry.dir {
				header.Name = strings.TrimSuffix(header.Name, "/") + "/"
				header.SetMode(os.ModeDir | 0o755)
			} else if entry.link != "" {
				header.SetMode(os.ModeSymlink | 0o777)
				body = entry.link
			}
			out, err := writer.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(out, body); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		var output io.Writer = file
		var compressed *gzip.Writer
		if format == "tar.gz" {
			compressed = gzip.NewWriter(file)
			output = compressed
		}
		writer := tar.NewWriter(output)
		for _, entry := range entries {
			header := &tar.Header{Name: entry.name, Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(entry.body))}
			if entry.dir {
				header.Typeflag = tar.TypeDir
			} else if entry.link != "" {
				header.Typeflag = tar.TypeSymlink
				header.Linkname = entry.link
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(writer, entry.body); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if compressed != nil {
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archive
}

func extractCoreTestArchive(t *testing.T, archive, dest string) error {
	t.Helper()
	return ExtractCoreArchiveAndStripRootForImport(archive, dest, func(int, string) {})
}

func assertCoreArchiveFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestCoreArchiveRejectsEscapingEntriesAndLinks(t *testing.T) {
	for _, format := range []string{"tar", "tar.gz", "zip"} {
		t.Run(format, func(t *testing.T) {
			for _, attack := range []string{"parent_entry", "absolute_entry", "absolute_link", "relative_link", "escape_after_strip", "forward_link_chain", "link_then_parent", "cycle"} {
				t.Run(attack, func(t *testing.T) {
					workspace := t.TempDir()
					outside := filepath.Join(workspace, "outside")
					if err := os.Mkdir(outside, 0o755); err != nil {
						t.Fatal(err)
					}
					sentinel := filepath.Join(outside, "sentinel")
					if err := os.WriteFile(sentinel, []byte("original"), 0o600); err != nil {
						t.Fatal(err)
					}
					entries := []coreArchiveTestEntry{{name: "root/chrome", body: "chrome"}}
					switch attack {
					case "parent_entry":
						entries = append(entries, coreArchiveTestEntry{name: "../outside/sentinel", body: "corrupt"})
					case "absolute_entry":
						entries = append(entries, coreArchiveTestEntry{name: sentinel, body: "corrupt"})
					case "absolute_link":
						entries = append(entries, coreArchiveTestEntry{name: "root/escape", link: outside}, coreArchiveTestEntry{name: "root/escape/sentinel", body: "corrupt"})
					case "relative_link":
						entries = append(entries, coreArchiveTestEntry{name: "root/escape", link: "../../outside"}, coreArchiveTestEntry{name: "root/escape/sentinel", body: "corrupt"})
					case "escape_after_strip":
						entries = append(entries, coreArchiveTestEntry{name: "root/escape", link: "../outside/sentinel"})
					case "forward_link_chain":
						entries = append(entries, coreArchiveTestEntry{name: "root/alias", link: "escape/sentinel"}, coreArchiveTestEntry{name: "root/escape", link: "../outside"})
					case "link_then_parent":
						entries = append(entries, coreArchiveTestEntry{name: "root/hop", link: "."}, coreArchiveTestEntry{name: "root/escape", link: "hop/../outside/sentinel"})
					case "cycle":
						entries = append(entries, coreArchiveTestEntry{name: "root/a", link: "b"}, coreArchiveTestEntry{name: "root/b", link: "a"})
					}
					archive := makeCoreTestArchive(t, format, entries)
					if err := extractCoreTestArchive(t, archive, filepath.Join(workspace, "dest")); err == nil {
						t.Fatal("unsafe archive unexpectedly accepted")
					}
					assertCoreArchiveFile(t, sentinel, "original")
					files, err := os.ReadDir(outside)
					if err != nil || len(files) != 1 {
						t.Fatalf("outside directory changed: entries=%v, err=%v", files, err)
					}
				})
			}
		})
	}
}

func TestCoreArchivePreservesSafeLinksAndStripsRoot(t *testing.T) {
	for _, format := range []string{"tar", "tar.gz", "zip"} {
		t.Run(format, func(t *testing.T) {
			entries := []coreArchiveTestEntry{
				{name: "root/", dir: true},
				{name: "root/launcher", link: "current/chrome"},
				{name: "root/current", link: "bin"},
				{name: "root/bin/chrome", body: "executable"},
				{name: "root/bin/config", link: "../assets/config"},
				{name: "root/assets/config", body: "settings"},
				{name: "root/future", link: "missing/data"},
			}
			dest := filepath.Join(t.TempDir(), "dest")
			if err := extractCoreTestArchive(t, makeCoreTestArchive(t, format, entries), dest); err != nil {
				t.Fatal(err)
			}
			for path, want := range map[string]string{"bin/chrome": "executable", "launcher": "executable", "current/chrome": "executable", "bin/config": "settings"} {
				assertCoreArchiveFile(t, filepath.Join(dest, path), want)
			}
			for path, want := range map[string]string{"launcher": "current/chrome", "current": "bin", "bin/config": "../assets/config", "future": "missing/data"} {
				got, err := os.Readlink(filepath.Join(dest, path))
				if err != nil || got != want {
					t.Fatalf("link %s = %q, err=%v; want %q", path, got, err, want)
				}
			}
			if _, err := os.Lstat(filepath.Join(dest, "root")); !os.IsNotExist(err) {
				t.Fatalf("top directory was not stripped: %v", err)
			}
			info, err := os.Stat(filepath.Join(dest, "bin/chrome"))
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("executable mode lost: info=%v, err=%v", info, err)
			}
		})
	}
}

func TestCoreArchivePreservesPlainFiles(t *testing.T) {
	for _, format := range []string{"tar", "zip"} {
		for _, layout := range []string{"single_file", "multiple_roots"} {
			t.Run(format+"/"+layout, func(t *testing.T) {
				entries := []coreArchiveTestEntry{{name: "chrome", body: "executable"}}
				if layout == "multiple_roots" {
					entries = append(entries, coreArchiveTestEntry{name: "assets/config", body: "settings"})
				}
				dest := t.TempDir()
				if err := extractCoreTestArchive(t, makeCoreTestArchive(t, format, entries), dest); err != nil {
					t.Fatal(err)
				}
				assertCoreArchiveFile(t, filepath.Join(dest, "chrome"), "executable")
				if layout == "multiple_roots" {
					assertCoreArchiveFile(t, filepath.Join(dest, "assets/config"), "settings")
				}
			})
		}
	}
}

func TestCoreArchiveRejectsExistingParentSymlinks(t *testing.T) {
	for _, format := range []string{"tar", "zip"} {
		for _, location := range []string{"destination", "parent"} {
			t.Run(format+"/"+location, func(t *testing.T) {
				workspace := t.TempDir()
				outside := filepath.Join(workspace, "outside")
				if err := os.Mkdir(outside, 0o755); err != nil {
					t.Fatal(err)
				}
				sentinel := filepath.Join(outside, "sentinel")
				if err := os.WriteFile(sentinel, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(workspace, "dest")
				link := dest
				name := "sentinel"
				if location == "parent" {
					if err := os.Mkdir(dest, 0o755); err != nil {
						t.Fatal(err)
					}
					link = filepath.Join(dest, "alias")
					name = "alias/sentinel"
				}
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
				archive := makeCoreTestArchive(t, format, []coreArchiveTestEntry{{name: name, body: "corrupt"}, {name: "marker", body: "marker"}})
				if err := extractCoreTestArchive(t, archive, dest); err == nil {
					t.Fatal("pre-existing parent symlink accepted")
				}
				assertCoreArchiveFile(t, sentinel, "original")
				files, err := os.ReadDir(outside)
				if err != nil || len(files) != 1 {
					t.Fatalf("outside directory changed: entries=%v, err=%v", files, err)
				}
			})
		}
	}
}

func TestCoreArchiveReplacesExistingFileLinksWithoutFollowing(t *testing.T) {
	for _, format := range []string{"tar", "zip"} {
		for _, kind := range []string{"symlink", "hardlink"} {
			t.Run(format+"/"+kind, func(t *testing.T) {
				workspace := t.TempDir()
				sentinel := filepath.Join(workspace, "sentinel")
				if err := os.WriteFile(sentinel, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(workspace, "dest")
				if err := os.Mkdir(dest, 0o755); err != nil {
					t.Fatal(err)
				}
				link := os.Symlink
				if kind == "hardlink" {
					link = os.Link
				}
				if err := link(sentinel, filepath.Join(dest, "chrome")); err != nil {
					t.Fatal(err)
				}
				archive := makeCoreTestArchive(t, format, []coreArchiveTestEntry{{name: "chrome", body: "executable"}})
				if err := extractCoreTestArchive(t, archive, dest); err != nil {
					t.Fatal(err)
				}
				assertCoreArchiveFile(t, sentinel, "original")
				assertCoreArchiveFile(t, filepath.Join(dest, "chrome"), "executable")
			})
		}
	}
}
