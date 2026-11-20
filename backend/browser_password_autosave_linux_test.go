//go:build linux

package backend

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestBrowserPasswordActionsUseBoundedIndexedNames(t *testing.T) {
	for _, count := range []any{int32(2), uint32(2)} {
		var indices []int32
		names, err := browserPasswordActionNames(count, func(index int32) (string, error) {
			indices = append(indices, index)
			return []string{"showContextMenu", "press"}[index], nil
		})
		if err != nil || !reflect.DeepEqual(names, []string{"showContextMenu", "press"}) || !reflect.DeepEqual(indices, []int32{0, 1}) {
			t.Fatalf("indexed names lost action ordering: names=%v indices=%v error=%v", names, indices, err)
		}
	}
	for _, count := range []any{int32(-1), int32(17), uint32(17), ^uint32(0), "2", nil} {
		names, err := browserPasswordActionNames(count, func(int32) (string, error) {
			t.Fatal("queried a native action with an invalid or excessive count")
			return "press", nil
		})
		if err == nil || names != nil {
			t.Fatalf("accepted action count %#v", count)
		}
	}
	if names, err := browserPasswordActionNames(int32(0), func(int32) (string, error) {
		t.Fatal("queried a missing action")
		return "", nil
	}); err != nil || len(names) != 0 {
		t.Fatalf("zero count rejected: names=%v error=%v", names, err)
	}
	if names, err := browserPasswordActionNames(int32(2), func(index int32) (string, error) {
		if index == 1 {
			return "", fmt.Errorf("native object disappeared")
		}
		return "press", nil
	}); err == nil || names != nil {
		t.Fatalf("accepted incomplete native actions: names=%v error=%v", names, err)
	}
}

func TestBrowserPasswordSingletonUsesLiveUnixListenerOwnership(t *testing.T) {
	directory := t.TempDir()
	socketDir, err := os.MkdirTemp("", "ant-password-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	socketPath := filepath.Join(socketDir, "singleton socket")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{filepath.Join(directory, "SingletonLock"): host + "-" + strconv.Itoa(os.Getpid()), filepath.Join(directory, "SingletonSocket"): socketPath} {
		if err := os.Symlink(value, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := browserPasswordSingletonOwner("/proc", directory, os.Getpid()); err != nil {
		t.Fatalf("owned live listener rejected: %v", err)
	}
	if err := browserPasswordSingletonOwner("/proc", directory, os.Getppid()); err == nil {
		t.Fatal("accepted listener as another process")
	}
}

func TestBrowserPasswordHiddenCommandLineRequiresOwnedSingleton(t *testing.T) {
	for _, variant := range []string{"hidden", "empty", "flat_argv", "flat_directory_spaces", "flat_display_directory", "flat_renderer", "different_directory", "empty_directory_flag", "renderer", "wrong_lock_pid", "wrong_host", "socket_owned_by_other_pid", "not_listener", "missing_socket"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "profile")
			if variant == "flat_directory_spaces" {
				directory = filepath.Join(root, "profile with spaces")
			}
			process := filepath.Join(root, "123")
			for _, path := range []string{directory, filepath.Join(process, "fd"), filepath.Join(root, "net")} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			socketDir, err := os.MkdirTemp("", "ant-password-singleton-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(socketDir)
			socketPath := filepath.Join(socketDir, "socket with spaces")
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			host, _ := os.Hostname()
			lock := host + "-123"
			inode := "234567"
			fdLink := "socket:[234567]"
			flags := "00010000"
			command := "/bin/chrome\x00\x00\x00"
			switch variant {
			case "empty":
				command = ""
			case "flat_argv":
				command = "/bin/chrome --fingerprint=123 --user-data-dir=\"" + directory + "\" --force-renderer-accessibility=basic\x00\x00"
			case "flat_directory_spaces":
				command = "/bin/chrome --user-data-dir=" + directory + " --fingerprint=123\x00"
			case "flat_display_directory":
				command = "/bin/chrome --user-data-dir=\"" + root + "\" --fingerprint=123\x00"
			case "flat_renderer":
				command = "/bin/chrome --type=renderer --fingerprint=123\x00"
			case "different_directory":
				command = "/bin/chrome\x00--user-data-dir=" + root + "\x00"
			case "empty_directory_flag":
				command = "/bin/chrome\x00--user-data-dir=\x00"
			case "renderer":
				command = "/bin/chrome\x00--type=renderer\x00"
			case "wrong_lock_pid":
				lock = host + "-456"
			case "wrong_host":
				lock = "another-host-123"
			case "socket_owned_by_other_pid":
				fdLink = "socket:[345678]"
			case "not_listener":
				flags = "00000000"
			case "missing_socket":
				socketPath = filepath.Join(socketDir, "not-present")
			}
			fields := append([]string{"S"}, strings.Fields(strings.Repeat("0 ", 18))...)
			fields = append(fields, "777")
			for path, value := range map[string]string{filepath.Join(process, "cmdline"): command, filepath.Join(process, "stat"): "123 (chrome) " + strings.Join(fields, " "), filepath.Join(root, "net", "unix"): "Num RefCount Protocol Flags Type St Inode Path\n0000000000000000: 00000002 00000000 " + flags + " 0001 01 " + inode + " " + socketPath + "\n"} {
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for path, value := range map[string]string{filepath.Join(directory, "SingletonLock"): lock, filepath.Join(directory, "SingletonSocket"): socketPath, filepath.Join(process, "fd", "5"): fdLink} {
				if err := os.Symlink(value, path); err != nil {
					t.Fatal(err)
				}
			}
			identity, err := browserPasswordProcessIdentity(root, directory, 123)
			want := variant == "hidden" || variant == "empty" || variant == "flat_argv" || variant == "flat_directory_spaces" || variant == "flat_display_directory"
			if (err == nil) != want {
				t.Fatalf("identity=%#v error=%v", identity, err)
			}
			if want && (identity.PID != 123 || identity.StartTime != "777") {
				t.Fatalf("wrong identity: %#v", identity)
			}
		})
	}
}

func TestBrowserPasswordAccessibilityLaunchFlags(t *testing.T) {
	args := []string{"--lang=zh-CN", "--enable-features=KeepThis"}
	got := browserPasswordAutosaveLaunchArgs(args)
	if !reflect.DeepEqual(got, []string{"--lang=zh-CN", "--enable-features=KeepThis", "--force-renderer-accessibility=basic"}) {
		t.Fatalf("args=%#v", got)
	}
	for _, flag := range []string{"--force-renderer-accessibility=complete", "--disable-renderer-accessibility"} {
		if got := browserPasswordAutosaveLaunchArgs([]string{flag}); !reflect.DeepEqual(got, []string{flag}) {
			t.Fatalf("overrode user setting: %#v", got)
		}
	}
}

func TestBrowserPasswordProcessIdentityBindsMainProfileAndStartTime(t *testing.T) {
	for _, variant := range []string{"valid", "wrong_profile", "renderer", "invalid_start"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "profile")
			process := filepath.Join(root, "123")
			if err := os.MkdirAll(process, 0700); err != nil {
				t.Fatal(err)
			}
			args := []string{"/bin/chrome", "--user-data-dir=" + directory}
			start := "456"
			if variant == "wrong_profile" {
				args[1] = "--user-data-dir=" + root
			}
			if variant == "renderer" {
				args = append(args, "--type=renderer")
			}
			if variant == "invalid_start" {
				start = "0"
			}
			fields := append([]string{"S"}, strings.Fields(strings.Repeat("0 ", 18))...)
			fields = append(fields, start)
			for path, value := range map[string]string{filepath.Join(process, "cmdline"): strings.Join(args, "\x00") + "\x00", filepath.Join(process, "stat"): "123 (chrome with space) " + strings.Join(fields, " ")} {
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			identity, err := browserPasswordProcessIdentity(root, directory, 123)
			if (err == nil) != (variant == "valid") {
				t.Fatalf("identity=%#v error=%v", identity, err)
			}
			if err == nil && (identity.PID != 123 || identity.StartTime != "456") {
				t.Fatalf("wrong identity: %#v", identity)
			}
		})
	}
}
