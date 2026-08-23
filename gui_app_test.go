package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDwarfMediaMatchesRecordingByTimestamp(t *testing.T) {
	recordingStartedAt := time.Date(2026, time.August, 21, 14, 35, 0, 0, time.Local)
	file := DwarfMediaFile{
		Path: "/DWARF3_TELE_2026-08-21-14-35-04-321.mp4",
		Name: "DWARF3_TELE_2026-08-21-14-35-04-321.mp4",
	}

	if !dwarfMediaMatchesRecording(file, dwarfCameraTele, "", recordingStartedAt) {
		t.Fatal("expected recording to match by parsed timestamp near the recording start")
	}
}

func TestSelectDwarfMediaFileSkipsDownloadedAndUsesCameraFallback(t *testing.T) {
	files := []DwarfMediaFile{
		{
			Path:    "/DWARF3_TELE_2026-08-21-14-35-04-321.mp4",
			Name:    "DWARF3_TELE_2026-08-21-14-35-04-321.mp4",
			ModTime: time.Date(2026, time.August, 21, 14, 35, 4, 0, time.Local),
		},
		{
			Path:    "/DWARF3_TELE_2026-08-21-14-34-00-000.mp4",
			Name:    "DWARF3_TELE_2026-08-21-14-34-00-000.mp4",
			ModTime: time.Date(2026, time.August, 21, 14, 34, 0, 0, time.Local),
		},
	}
	downloaded := map[string]DwarfQueuedRecording{
		files[0].Path: {RemotePath: files[0].Path},
	}

	selected := selectDwarfMediaFile(files, downloaded, dwarfCameraTele, "", time.Time{})
	if selected == nil {
		t.Fatal("expected a fallback selection")
	}
	if selected.Path != files[1].Path {
		t.Fatalf("unexpected selection: %s", selected.Path)
	}
}

func TestFormatDwarfRecordStartReportIncludesRawAndNewFiles(t *testing.T) {
	report := DwarfRecordStartReport{
		Host:           "192.168.88.1",
		Camera:         dwarfCameraTele,
		RecordingName:  "DWARF_TEST_20260821150000",
		StartAckOK:     true,
		StartAckDetail: "start recording acknowledged",
		StartAckRaw:    `{"code":0}`,
		StopAckOK:      true,
		StopAckDetail:  "stop recording acknowledged",
		StopAckRaw:     `{"code":0}`,
		BeforeCount:    10,
		AfterCount:     11,
		NewFiles:       []string{"/DWARF3_TELE_2026-08-21-15-00-00-000.mp4"},
		WaitDuration:   4 * time.Second,
	}

	text := formatDwarfRecordStartReport(report)
	for _, want := range []string{
		"Host: 192.168.88.1",
		"Start Ack Raw: {\"code\":0}",
		"Stop Ack Raw: {\"code\":0}",
		"New Files: /DWARF3_TELE_2026-08-21-15-00-00-000.mp4",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected report to contain %q, got:\n%s", want, text)
		}
	}
}

func TestDwarfDownloadLocalPathUsesRecordingSubdirectory(t *testing.T) {
	got := dwarfDownloadLocalPath("dwarf_downloads", "DWARF_20260823160000", "/Videos/DWARF3_WIDE_2026-08-23-16-00-19-049.mp4")
	want := filepath.Join("dwarf_downloads", "DWARF_20260823160000", "DWARF3_WIDE_2026-08-23-16-00-19-049.mp4")
	if got != want {
		t.Fatalf("unexpected local path: got %s want %s", got, want)
	}
}

func TestDwarfDownloadLocalPathWithoutRecordingUsesBaseDirectory(t *testing.T) {
	got := dwarfDownloadLocalPath("dwarf_downloads", "", "/Videos/DWARF3_TELE_2026-08-23-16-00-19-049.mp4")
	want := filepath.Join("dwarf_downloads", "DWARF3_TELE_2026-08-23-16-00-19-049.mp4")
	if got != want {
		t.Fatalf("unexpected local path: got %s want %s", got, want)
	}
}

func TestDwarfDownloadLocalPathUsesQueueDirectoryDirectly(t *testing.T) {
	got := dwarfDownloadLocalPath(filepath.Join("dwarf_downloads", "2026-08-23_161622", dwarfQueueStageQueue), "DWARF_20260823161622", "/Videos/DWARF3_WIDE_2026-08-23-16-16-30-000.mp4")
	want := filepath.Join("dwarf_downloads", "2026-08-23_161622", dwarfQueueStageQueue, "DWARF3_WIDE_2026-08-23-16-16-30-000.mp4")
	if got != want {
		t.Fatalf("unexpected queue path: got %s want %s", got, want)
	}
}

func TestDwarfCaptureSessionDirUsesStartTime(t *testing.T) {
	startedAt := time.Date(2026, time.August, 23, 16, 16, 22, 0, time.Local)
	got := dwarfCaptureSessionDir("dwarf_downloads", startedAt)
	want := filepath.Join("dwarf_downloads", "2026-08-23_161622")
	if got != want {
		t.Fatalf("unexpected session dir: got %s want %s", got, want)
	}
}

func TestMoveDwarfQueuedRecordingToStage(t *testing.T) {
	root := t.TempDir()
	queueDir := filepath.Join(root, dwarfQueueStageQueue)
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatalf("create queue dir: %v", err)
	}
	initialPath := filepath.Join(queueDir, "clip.mp4")
	if err := os.WriteFile(initialPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	recording := DwarfQueuedRecording{
		Camera:     dwarfCameraWide,
		RemotePath: "/Videos/clip.mp4",
		LocalPath:  initialPath,
	}

	moved, err := moveDwarfQueuedRecordingToStage(recording, dwarfQueueStageUnderProcessing)
	if err != nil {
		t.Fatalf("move to under processing: %v", err)
	}
	wantUnderProcessing := filepath.Join(root, dwarfQueueStageUnderProcessing, "clip.mp4")
	if moved.LocalPath != wantUnderProcessing {
		t.Fatalf("unexpected under processing path: got %s want %s", moved.LocalPath, wantUnderProcessing)
	}
	if _, err := os.Stat(wantUnderProcessing); err != nil {
		t.Fatalf("expected moved file to exist: %v", err)
	}

	moved, err = moveDwarfQueuedRecordingToStage(moved, dwarfQueueStageProcessed)
	if err != nil {
		t.Fatalf("move to processed: %v", err)
	}
	wantProcessed := filepath.Join(root, dwarfQueueStageProcessed, "clip.mp4")
	if moved.LocalPath != wantProcessed {
		t.Fatalf("unexpected processed path: got %s want %s", moved.LocalPath, wantProcessed)
	}
	if _, err := os.Stat(wantProcessed); err != nil {
		t.Fatalf("expected processed file to exist: %v", err)
	}
}
