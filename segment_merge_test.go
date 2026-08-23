package main

import (
	"testing"
	"time"
)

func TestMergeSegmentTrackingResultsMergesBoundaryTracks(t *testing.T) {
	settings := DefaultTrackingSettings()
	manifest := RawSegmentManifest{
		CreatedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		Segments: []RawVideoSegment{
			{Index: 1, File: "segment_000001.avi", StartFrame: 1, EndFrame: 12, StartTimeMS: 0, EndTimeMS: 1100, Frames: 12, OverlapAfterFrames: 2},
			{Index: 2, File: "segment_000002.avi", StartFrame: 11, EndFrame: 22, StartTimeMS: 1000, EndTimeMS: 2100, Frames: 12, OverlapBeforeFrames: 2},
		},
	}

	left := EventMetadata{
		FPS:    10,
		Width:  1920,
		Height: 1080,
		Frames: []FrameMetadata{
			frameWithTrack(10, 900, trackAt(1, 100, 100, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(11, 1000, trackAt(1, 110, 100, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(12, 1100, trackAt(1, 120, 100, 20, 20, 10, 0, trackTypeFast)),
		},
	}
	right := EventMetadata{
		FPS:    10,
		Width:  1920,
		Height: 1080,
		Frames: []FrameMetadata{
			frameWithTrack(1, 0, trackAt(7, 110, 100, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(2, 100, trackAt(7, 120, 100, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(3, 200, trackAt(7, 130, 100, 20, 20, 10, 0, trackTypeFast)),
		},
	}

	merged, err := MergeSegmentTrackingResults(manifest, []EventMetadata{left, right}, settings)
	if err != nil {
		t.Fatalf("MergeSegmentTrackingResults returned error: %v", err)
	}

	if len(merged.BoundaryMatch) != 1 {
		t.Fatalf("expected one boundary match, got %d", len(merged.BoundaryMatch))
	}
	if merged.BoundaryMatch[0].LeftTrackID != 1 || merged.BoundaryMatch[0].RightTrackID != 7 {
		t.Fatalf("unexpected boundary match: %+v", merged.BoundaryMatch[0])
	}

	if len(merged.Metadata.Frames) != 4 {
		t.Fatalf("expected 4 merged frames after overlap collapse, got %d", len(merged.Metadata.Frames))
	}

	globalIDBySegmentTrack := make(map[[2]int]int)
	for _, assignment := range merged.Assignments {
		globalIDBySegmentTrack[[2]int{assignment.SegmentIndex, assignment.LocalTrackID}] = assignment.GlobalTrackID
	}
	if globalIDBySegmentTrack[[2]int{0, 1}] != globalIDBySegmentTrack[[2]int{1, 7}] {
		t.Fatalf("expected merged global IDs, assignments=%+v", merged.Assignments)
	}
}

func TestMergeSegmentTrackingResultsKeepsDistinctTracksSeparate(t *testing.T) {
	settings := DefaultTrackingSettings()
	manifest := RawSegmentManifest{
		CreatedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		Segments: []RawVideoSegment{
			{Index: 1, File: "segment_000001.avi", StartFrame: 1, EndFrame: 12, StartTimeMS: 0, EndTimeMS: 1100, Frames: 12, OverlapAfterFrames: 2},
			{Index: 2, File: "segment_000002.avi", StartFrame: 11, EndFrame: 22, StartTimeMS: 1000, EndTimeMS: 2100, Frames: 12, OverlapBeforeFrames: 2},
		},
	}

	left := EventMetadata{
		FPS:    10,
		Width:  1920,
		Height: 1080,
		Frames: []FrameMetadata{
			frameWithTrack(11, 1000, trackAt(1, 110, 100, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(12, 1100, trackAt(1, 120, 100, 20, 20, 10, 0, trackTypeFast)),
		},
	}
	right := EventMetadata{
		FPS:    10,
		Width:  1920,
		Height: 1080,
		Frames: []FrameMetadata{
			frameWithTrack(1, 0, trackAt(7, 600, 300, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(2, 100, trackAt(7, 620, 300, 20, 20, 10, 0, trackTypeFast)),
		},
	}

	merged, err := MergeSegmentTrackingResults(manifest, []EventMetadata{left, right}, settings)
	if err != nil {
		t.Fatalf("MergeSegmentTrackingResults returned error: %v", err)
	}

	if len(merged.BoundaryMatch) != 0 {
		t.Fatalf("expected no boundary matches, got %+v", merged.BoundaryMatch)
	}
	if len(merged.Assignments) != 2 {
		t.Fatalf("expected two assignments, got %d", len(merged.Assignments))
	}
	if merged.Assignments[0].GlobalTrackID == merged.Assignments[1].GlobalTrackID {
		t.Fatalf("expected distinct global IDs, assignments=%+v", merged.Assignments)
	}
}

func frameWithTrack(sourceFrame int, timeMS int64, track TrackMetadata) FrameMetadata {
	return FrameMetadata{
		SourceFrame: sourceFrame,
		TimeMS:      timeMS,
		Tracks:      []TrackMetadata{track},
	}
}

func trackAt(id, x, y, w, h int, vx, vy float64, trackType string) TrackMetadata {
	return TrackMetadata{
		ID:        id,
		Type:      trackType,
		X:         x,
		Y:         y,
		BoxX:      x - w/2,
		BoxY:      y - h/2,
		BoxWidth:  w,
		BoxHeight: h,
		VX:        vx,
		VY:        vy,
		Speed:     vx,
	}
}
