//go:build linux && cgo

package backend

import (
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileWindowMarkerLinuxTitlePreservesLongInstanceNames(t *testing.T) {
	name := "bingining+lowfish@gmail.com 中文实例"
	title := profileWindowMarkerLinuxTitle("Example page - Google Chrome", "2", name)
	if !strings.Contains(title, name) || !strings.HasSuffix(title, "Example page - Google Chrome") {
		t.Fatalf("instance name or original page title lost: %q", title)
	}
	updated := profileWindowMarkerLinuxTitle(title, "2", "新名字")
	if updated != "[2] 新名字 · Example page - Google Chrome" {
		t.Fatalf("rename duplicated or retained old marker: %q", updated)
	}
}

func TestProfileWindowMarkerI3FindsOnlyRequestedWindow(t *testing.T) {
	tree := profileWindowMarkerI3Node{Nodes: []profileWindowMarkerI3Node{
		{Window: 1, Border: "normal", Width: 2},
		{FloatingNodes: []profileWindowMarkerI3Node{{Window: 42, Border: "pixel", Width: 3}}},
	}}
	if border, width, found := tree.findBorder(42); !found || border != "pixel" || width != 3 {
		t.Fatalf("wrong window border: %q, %d, %v", border, width, found)
	}
	if _, _, found := tree.findBorder(7); found {
		t.Fatal("unknown window matched")
	}
}

func TestProfileWindowMarkerI3CommandsUseExactWindowAndPreserveBorder(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "i3.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			commands <- "accept failed"
			return
		}
		defer connection.Close()
		header := make([]byte, 14)
		if _, err := io.ReadFull(connection, header); err != nil {
			commands <- "header failed"
			return
		}
		payload := make([]byte, binary.LittleEndian.Uint32(header[6:10]))
		if _, err := io.ReadFull(connection, payload); err != nil {
			commands <- "payload failed"
			return
		}
		commands <- string(payload)
		response := `[{"success":true}]`
		binary.LittleEndian.PutUint32(header[6:10], uint32(len(response)))
		_, _ = connection.Write(append(header, response...))
	}()
	if !profileWindowMarkerI3SetBorder(socket, 42, "pixel", 3) {
		t.Fatal("border command failed")
	}
	if got := <-commands; got != `[id="42"] border pixel 3` {
		t.Fatalf("unsafe window criteria: %q", got)
	}
	if profileWindowMarkerI3SetBorder(socket, 0, "normal", 1) || profileWindowMarkerI3SetBorder(socket, 42, "normal; exec bad", 1) {
		t.Fatal("invalid criteria accepted")
	}
}
