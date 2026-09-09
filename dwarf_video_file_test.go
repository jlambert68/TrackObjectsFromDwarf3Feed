package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestMP4(t *testing.T, localPath string, boxTypes ...string) {
	t.Helper()
	var content bytes.Buffer
	for _, boxType := range boxTypes {
		if len(boxType) != 4 {
			t.Fatalf("invalid test MP4 box type %q", boxType)
		}
		if err := binary.Write(&content, binary.BigEndian, uint32(8)); err != nil {
			t.Fatalf("encode test MP4 box: %v", err)
		}
		content.WriteString(boxType)
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("create test MP4 directory: %v", err)
	}
	if err := os.WriteFile(localPath, content.Bytes(), 0o644); err != nil {
		t.Fatalf("write test MP4: %v", err)
	}
}

func TestValidateDwarfVideoFileAcceptsCompleteMP4(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "complete.mp4")
	writeTestMP4(t, localPath, "ftyp", "mdat", "moov")

	if err := validateDwarfVideoFile(localPath); err != nil {
		t.Fatalf("validate complete MP4: %v", err)
	}
}

func TestValidateDwarfVideoFileRejectsMP4WithoutMoov(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "incomplete.mp4")
	writeTestMP4(t, localPath, "ftyp", "mdat")

	err := validateDwarfVideoFile(localPath)
	if err == nil || !strings.Contains(err.Error(), "moov") {
		t.Fatalf("expected missing moov error, got %v", err)
	}
}

func TestDownloadValidatedDwarfVideoRetriesIncompleteMP4Atomically(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "queue", "recording.mp4")
	downloadCalls := 0
	waitCalls := 0
	err := downloadValidatedDwarfVideoWithRetry(
		"/Videos/recording.mp4",
		localPath,
		3,
		time.Millisecond,
		func(_, destination string) error {
			downloadCalls++
			if downloadCalls == 1 {
				writeTestMP4(t, destination, "ftyp", "mdat")
			} else {
				writeTestMP4(t, destination, "ftyp", "mdat", "moov")
			}
			return nil
		},
		func(time.Duration) { waitCalls++ },
	)
	if err != nil {
		t.Fatalf("download complete MP4: %v", err)
	}
	if downloadCalls != 2 || waitCalls != 1 {
		t.Fatalf("unexpected retry counts: downloads=%d waits=%d", downloadCalls, waitCalls)
	}
	if err := validateDwarfVideoFile(localPath); err != nil {
		t.Fatalf("published file is invalid: %v", err)
	}
	if _, err := os.Stat(localPath + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file remains after successful download: %v", err)
	}
}

func TestQuarantineDwarfQueuedRecordingPreservesReason(t *testing.T) {
	root := t.TempDir()
	localPath := filepath.Join(root, dwarfQueueStageQueue, "damaged.mp4")
	writeTestMP4(t, localPath, "ftyp", "mdat")
	recording := DwarfQueuedRecording{LocalPath: localPath, RemotePath: "/Videos/damaged.mp4"}
	if err := writeDwarfRecordingMetadata(recording); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	cause := errors.New("MP4 is missing its moov metadata box")
	failed, err := quarantineDwarfQueuedRecording(recording, cause)
	if err != nil {
		t.Fatalf("quarantine recording: %v", err)
	}
	wantPath := filepath.Join(root, dwarfQueueStageFailed, "damaged.mp4")
	if failed.LocalPath != wantPath {
		t.Fatalf("unexpected failed path: got %s want %s", failed.LocalPath, wantPath)
	}
	stored, err := readDwarfRecordingMetadata(wantPath)
	if err != nil {
		t.Fatalf("read failed metadata: %v", err)
	}
	if stored.FailureReason != cause.Error() {
		t.Fatalf("failure reason = %q, want %q", stored.FailureReason, cause.Error())
	}
	if _, err := os.Stat(localPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("damaged file remains in queue: %v", err)
	}
}
