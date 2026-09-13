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
	type helicopterObservation struct {
		frame int
		x     int
		y     int
	}
	helicopterTracks := make(map[int][]helicopterObservation)
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
				// The known helicopter travels left-to-right from about (1895,1150)
				// to (3835,1050) during frames 6..293 in the reference event.
				expectedX := 1850 + update.SourceFrame*7
				if track.X >= expectedX-180 && track.X <= expectedX+180 && track.Y >= 1000 && track.Y <= 1200 {
					helicopterTracks[track.ID] = append(helicopterTracks[track.ID], helicopterObservation{
						frame: update.SourceFrame,
						x:     track.X,
						y:     track.Y,
					})
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
	continuousHelicopterTracks := 0
	for _, observations := range helicopterTracks {
		if len(observations) >= 200 &&
			observations[len(observations)-1].frame-observations[0].frame >= 250 &&
			observations[len(observations)-1].x-observations[0].x >= 1500 {
			continuousHelicopterTracks++
		}
	}
	t.Logf("TELE small-object tracks: stable_tracks=%d classified_ids=%d", longTracks, len(trackFrames))
	t.Logf("dark-helicopter path coverage: continuous_tracks=%d tracks=%v", continuousHelicopterTracks, helicopterTracks)
	if longTracks < 4 {
		t.Fatalf("only %d small slow objects retained stable tracks, want at least 4", longTracks)
	}
	// Full-resolution TELE analysis intentionally retains many tiny foreground
	// contours. Keep a generous ceiling to catch an unbounded-noise regression;
	// the known-good 2026-08-31 event contained 1,623 unique objects.
	if len(trackFrames) > 1000 {
		t.Fatalf("tiny-object detector produced %d classified IDs, want at most 1000", len(trackFrames))
	}
	if continuousHelicopterTracks < 1 {
		t.Fatalf("helicopter was not retained as one sustained track; path coverage=%v", helicopterTracks)
	}
}
