package main

import (
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
