package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const wideBallRegressionVideo = "dwarf_downloads/2026-09-09_195736/Processed/DWARF3_WIDE_2026-09-09-19-57-48-506.mp4"

func TestWideBallRecordingTracksCompleteThrows(t *testing.T) {
	if testing.Short() {
		t.Skip("video regression test disabled in short mode")
	}
	if _, err := os.Stat(wideBallRegressionVideo); errors.Is(err, os.ErrNotExist) {
		t.Skipf("regression video is not available: %s", wideBallRegressionVideo)
	} else if err != nil {
		t.Fatalf("stat regression video: %v", err)
	}

	framesWithDetections := 0
	framesWithTracks := 0
	trackIDs := make(map[int]struct{})
	trackFrames := make(map[int][]int)
	engine := TrackerEngine{
		Config: TrackerConfig{
			Input:            wideBallRegressionVideo,
			InputLabel:       "video file",
			OutputDir:        filepath.Join(t.TempDir(), "events"),
			FallbackFPS:      30,
			AnalysisMaxWidth: defaultAnalysisMaxWidth,
			Settings:         DefaultTrackingSettingsForProfile(trackingProfileBall),
		},
		Hooks: TrackerHooks{OnFrame: func(update *FrameUpdate) error {
			if update.DetectionCount > 0 {
				framesWithDetections++
			}
			if len(update.Metadata.Tracks) > 0 {
				framesWithTracks++
			}
			for _, track := range update.Metadata.Tracks {
				trackIDs[track.ID] = struct{}{}
				trackFrames[track.ID] = append(trackFrames[track.ID], update.SourceFrame)
			}
			return nil
		}},
	}
	if err := engine.Run(); err != nil {
		t.Fatalf("track regression video: %v", err)
	}
	t.Logf("ball regression: detection_frames=%d track_frames=%d unique_track_ids=%d",
		framesWithDetections, framesWithTracks, len(trackIDs))
	for id, frames := range trackFrames {
		t.Logf("track %d: %d frames, %d..%d", id, len(frames), frames[0], frames[len(frames)-1])
	}

	if framesWithDetections < 170 {
		t.Fatalf("motion-blurred ball was rejected too often: detections on %d frames, want at least 170", framesWithDetections)
	}
	if framesWithTracks < 165 {
		t.Fatalf("ball tracks were not retained: tracks on %d frames, want at least 165", framesWithTracks)
	}
	longTracks := 0
	for _, frames := range trackFrames {
		if len(frames) >= 45 && frames[len(frames)-1]-frames[0] >= 45 {
			longTracks++
		}
	}
	t.Logf("latest General settings: long_tracks=%d all_spans=%v", longTracks, trackFrames)
	if longTracks != 3 {
		t.Fatalf("got %d continuous ball trajectories, want exactly 3; spans=%v", longTracks, trackFrames)
	}
	if len(trackIDs) > 7 {
		t.Fatalf("ball trajectories were fragmented across %d IDs, want at most 7", len(trackIDs))
	}
}

func TestWideBallRecordingWithLatestGeneralSettings(t *testing.T) {
	settings := DefaultTrackingSettingsForProfile(trackingProfileGeneral)
	settings.MinArea = 6
	settings.SlowMinSpeed = 10
	settings.MinSpeed = 40
	settings.MinHits = 2
	settings.ForegroundThreshold = 200
	settings.MOG2VarThreshold = 16

	trackFrames := make(map[int][]int)
	engine := TrackerEngine{
		Config: TrackerConfig{
			Input:            wideBallRegressionVideo,
			InputLabel:       "video file",
			OutputDir:        filepath.Join(t.TempDir(), "events"),
			FallbackFPS:      30,
			AnalysisMaxWidth: defaultAnalysisMaxWidth,
			Settings:         settings,
		},
		Hooks: TrackerHooks{OnFrame: func(update *FrameUpdate) error {
			for _, track := range update.Metadata.Tracks {
				trackFrames[track.ID] = append(trackFrames[track.ID], update.SourceFrame)
			}
			return nil
		}},
	}
	if err := engine.Run(); err != nil {
		t.Fatalf("track regression video with saved general settings: %v", err)
	}

	longTracks := 0
	for _, frames := range trackFrames {
		if len(frames) >= 45 && frames[len(frames)-1]-frames[0] >= 45 {
			longTracks++
		}
	}
	t.Logf("saved General settings: long_tracks=%d all_spans=%v", longTracks, trackFrames)
	if longTracks != 3 {
		t.Fatalf("latest app settings produced %d continuous ball trajectories, want exactly 3; spans=%v", longTracks, trackFrames)
	}
}
