package main

import (
	"bytes"
	"io"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

type testReadWriteCloser struct {
	reader *strings.Reader
	writer bytes.Buffer
}

func (c *testReadWriteCloser) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *testReadWriteCloser) Write(p []byte) (int, error) {
	return c.writer.Write(p)
}

func (c *testReadWriteCloser) Close() error {
	return nil
}

var _ io.ReadWriteCloser = (*testReadWriteCloser)(nil)

func TestParsePASVResponse(t *testing.T) {
	host, port, err := parsePASVResponse("Entering Passive Mode (192,168,88,1,195,44)")
	if err != nil {
		t.Fatalf("parsePASVResponse returned error: %v", err)
	}
	if host != "192.168.88.1" {
		t.Fatalf("unexpected host: %s", host)
	}
	if port != 49964 {
		t.Fatalf("unexpected port: %d", port)
	}
}

func TestParseUnixFTPListLineFile(t *testing.T) {
	entry, ok := parseUnixFTPListLine("-rw-r--r-- 1 root root 1048576 Aug 21 14:35 DWARF_20260821143500.mp4")
	if !ok {
		t.Fatal("expected parseUnixFTPListLine to succeed")
	}
	if entry.IsDir {
		t.Fatal("expected regular file entry")
	}
	if entry.Name != "DWARF_20260821143500.mp4" {
		t.Fatalf("unexpected name: %s", entry.Name)
	}
	if entry.Size != 1048576 {
		t.Fatalf("unexpected size: %d", entry.Size)
	}
	if entry.ModTime.Month() != time.August || entry.ModTime.Day() != 21 {
		t.Fatalf("unexpected mod time: %v", entry.ModTime)
	}
}

func TestParseUnixFTPListLineDirectory(t *testing.T) {
	entry, ok := parseUnixFTPListLine("drwxr-xr-x 2 root root 4096 Aug 20 2026 videos")
	if !ok {
		t.Fatal("expected parseUnixFTPListLine to succeed")
	}
	if !entry.IsDir {
		t.Fatal("expected directory entry")
	}
	if entry.Name != "videos" {
		t.Fatalf("unexpected name: %s", entry.Name)
	}
	if entry.ModTime.Year() != 2026 {
		t.Fatalf("unexpected mod time year: %v", entry.ModTime)
	}
}

func TestNormalizeRemotePath(t *testing.T) {
	if got := normalizeRemotePath(" album/2026/../latest "); got != "/album/latest" {
		t.Fatalf("unexpected normalized path: %s", got)
	}
	if got := normalizeRemotePath(""); got != "/Videos" {
		t.Fatalf("unexpected normalized root path: %s", got)
	}
}

func TestDefaultDwarfControllerUsesVideosRoot(t *testing.T) {
	controller := DefaultDwarfController()
	if controller.FTPRoot != "/Videos" {
		t.Fatalf("unexpected default dwarf ftp root: %s", controller.FTPRoot)
	}
}

func TestDwarfProtoPhotographCmdUsesSelectedCamera(t *testing.T) {
	if got := dwarfProtoPhotographCmd(dwarfCameraTele); got != dwarfProtoTelePhotographCmd {
		t.Fatalf("tele photograph command = %d", got)
	}
	if got := dwarfProtoPhotographCmd(dwarfCameraWide); got != dwarfProtoWidePhotographCmd {
		t.Fatalf("wide photograph command = %d", got)
	}
}

func TestEncodeDwarfDevicePath(t *testing.T) {
	got := encodeDwarfDevicePath("/DWARF3/Photos/My still #1.jpg")
	want := "/DWARF3/Photos/My%20still%20%231.jpg"
	if got != want {
		t.Fatalf("encoded path = %q, want %q", got, want)
	}
}

func TestDwarfFTPReadResponseDoesNotMatchExpectedCodeInMessage(t *testing.T) {
	conn := &testReadWriteCloser{reader: strings.NewReader("500 331 login required\r\n")}

	client := &dwarfFTPClient{
		ctrl:    textproto.NewConn(conn),
		timeout: time.Second,
	}
	code, msg, err := client.readResponse("USER anonymous", 230, 331)
	if err == nil {
		t.Fatalf("readResponse accepted code %d with message %q", code, msg)
	}
	if code != 500 {
		t.Fatalf("unexpected response code: %d", code)
	}
}

func TestCommandInterface(t *testing.T) {
	if got := commandInterface(map[string]any{"interface": 10007}); got != 10007 {
		t.Fatalf("unexpected int interface: %d", got)
	}
	if got := commandInterface(map[string]any{"interface": float64(10009)}); got != 10009 {
		t.Fatalf("unexpected float interface: %d", got)
	}
}
