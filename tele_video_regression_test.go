package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const teleSmallObjectRegressionVideo = "Videos/drive-download-20260812T181808Z-1-001/DWARF3_TELE_2026-08-12-20-04-00-273_20260812200537458.mp4"

func TestTELERecordingRetainsSmallSlowObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("video regression test disabled in short mode")
	}
	if _, err := os.Stat(teleSmallObjectRegressionVideo); errors.Is(err, os.ErrNotExist) {
		t.Skipf("regression video is not available: %s", teleSmallObjectRegressionVideo)
	} else if err != nil {
		t.Fatalf("stat regression video: %v", err)
	}

	settings := DefaultTrackingSettingsForProfile(trackingProfileGeneral)
	settings.MinArea = 6
	settings.SlowMinSpeed = 10
	settings.MinSpeed = 40
	settings.MinHits = 2
	settings.ForegroundThreshold = 200
	settings.MOG2VarThreshold = 16

	trackFrames := make(map[int][]int)
	helicopterFrames := make(map[int][]int)
	engine := TrackerEngine{
		Config: TrackerConfig{
			Input:            teleSmallObjectRegressionVideo,
			InputLabel:       "video file",
			OutputDir:        filepath.Join(t.TempDir(), "events"),
			FallbackFPS:      30,
			AnalysisMaxWidth: defaultAnalysisMaxWidth,
			Settings:         settings,
		},
		Hooks: TrackerHooks{OnFrame: func(update *FrameUpdate) error {
			for _, track := range update.Metadata.Tracks {
				trackFrames[track.ID] = append(trackFrames[track.ID], update.SourceFrame)
				if track.X >= 2500 && track.X <= 3500 && track.Y >= 900 && track.Y <= 1500 {
					helicopterFrames[track.ID] = append(helicopterFrames[track.ID], update.SourceFrame)
				}
			}
			return nil
		}},
	}
	if err := engine.Run(); err != nil {
		t.Fatalf("track TELE regression video: %v", err)
	}

	longTracks := 0
	for _, frames := range trackFrames {
		if len(frames) >= 15 && frames[len(frames)-1]-frames[0] >= 20 {
			longTracks++
		}
	}
	t.Logf("TELE small-object tracks: stable_tracks=%d classified_ids=%d", longTracks, len(trackFrames))
	t.Logf("dark-helicopter region coverage: %v", helicopterFrames)
	if longTracks < 4 {
		t.Fatalf("only %d small slow objects retained stable tracks, want at least 4", longTracks)
	}
	if len(trackFrames) > 100 {
		t.Fatalf("tiny-object detector produced %d classified IDs, want at most 100", len(trackFrames))
	}
}
