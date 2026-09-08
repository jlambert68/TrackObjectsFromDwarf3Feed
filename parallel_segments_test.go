package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessRawSegmentsParallelReturnsWhenAlreadyCancelled(t *testing.T) {
	rawSegmentDir := t.TempDir()
	manifest := RawSegmentManifest{
		FPS:    30,
		Width:  16,
		Height: 16,
		Segments: []RawVideoSegment{
			{Index: 1, File: "segment_000001.avi", StartFrame: 1, EndFrame: 30, Frames: 30},
		},
	}
	if err := writeJSONFile(filepath.Join(rawSegmentDir, "manifest.json"), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	stopCh := make(chan struct{})
	close(stopCh)
	done := make(chan error, 1)
	go func() {
		_, err := processRawSegmentsParallel(rawSegmentDir, DefaultTrackingSettings(), 30, stopCh)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrStopTracking) {
			t.Fatalf("expected ErrStopTracking, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled parallel segment processing did not return")
	}
}
