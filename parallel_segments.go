package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type segmentProcessResult struct {
	Index    int
	Metadata EventMetadata
}

type segmentProcessJob struct {
	index   int
	segment RawVideoSegment
}

func processRawSegmentsParallel(rawSegmentDir string, settings TrackingSettings, fallbackFPS float64, stopCh <-chan struct{}) (MergedSegmentTracking, error) {
	manifestPath := filepath.Join(rawSegmentDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return MergedSegmentTracking{}, fmt.Errorf("read raw segment manifest: %w", err)
	}

	var manifest RawSegmentManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return MergedSegmentTracking{}, fmt.Errorf("decode raw segment manifest: %w", err)
	}
	if len(manifest.Segments) == 0 {
		return MergedSegmentTracking{}, errors.New("raw segment manifest does not contain any segments")
	}

	processedDir := filepath.Join(rawSegmentDir, "processed")
	if err := os.MkdirAll(processedDir, 0755); err != nil {
		return MergedSegmentTracking{}, fmt.Errorf("create processed segment directory: %w", err)
	}

	workerCount := runtime.NumCPU()
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > len(manifest.Segments) {
		workerCount = len(manifest.Segments)
	}

	jobs := make(chan segmentProcessJob, len(manifest.Segments))
	results := make(chan segmentProcessResult, len(manifest.Segments))
	errCh := make(chan error, 1)

	var wg sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for current := range jobs {
				if stopped(stopCh) {
					select {
					case errCh <- ErrStopTracking:
					default:
					}
					return
				}

				segmentPath := filepath.Join(rawSegmentDir, current.segment.File)
				metadata, err := trackVideoMetadata(segmentPath, settings, fallbackFPS, stopCh)
				if err != nil {
					select {
					case errCh <- fmt.Errorf("process raw segment %s: %w", current.segment.File, err):
					default:
					}
					return
				}

				segmentDir := filepath.Join(processedDir, fmt.Sprintf("segment_%06d", current.segment.Index))
				if err := os.MkdirAll(segmentDir, 0755); err != nil {
					select {
					case errCh <- fmt.Errorf("create processed segment directory: %w", err):
					default:
					}
					return
				}
				if err := writeJSONFile(filepath.Join(segmentDir, "tracking.json"), metadata); err != nil {
					select {
					case errCh <- fmt.Errorf("write processed segment tracking: %w", err):
					default:
					}
					return
				}

				results <- segmentProcessResult{
					Index:    current.index,
					Metadata: metadata,
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for index, segment := range manifest.Segments {
			if stopped(stopCh) {
				return
			}
			jobs <- segmentProcessJob{index: index, segment: segment}
		}
	}()

	segments := make([]EventMetadata, len(manifest.Segments))
	for completed := 0; completed < len(manifest.Segments); completed++ {
		select {
		case result := <-results:
			segments[result.Index] = result.Metadata
		case err := <-errCh:
			wg.Wait()
			return MergedSegmentTracking{}, err
		}
	}

	wg.Wait()
	select {
	case err := <-errCh:
		return MergedSegmentTracking{}, err
	default:
	}

	merged, err := MergeSegmentTrackingResults(manifest, segments, settings)
	if err != nil {
		return MergedSegmentTracking{}, fmt.Errorf("merge processed segment tracking: %w", err)
	}

	if err := writeMergedSegmentOutputs(processedDir, merged, settings); err != nil {
		return MergedSegmentTracking{}, err
	}
	return merged, nil
}

func trackVideoMetadata(input string, settings TrackingSettings, fallbackFPS float64, stopCh <-chan struct{}) (EventMetadata, error) {
	settings = NormalizeTrackingSettings(settings)
	settings.RawSegmentDuration = 0
	settings.RawSegmentOverlap = 0

	var metadata EventMetadata
	frames := make([]FrameMetadata, 0, 1024)

	engine := TrackerEngine{
		Config: TrackerConfig{
			Input:        input,
			InputLabel:   "video file",
			ShowMask:     false,
			RecordEvents: false,
			FallbackFPS:  fallbackFPS,
			Settings:     settings,
		},
		Stop: stopCh,
		Hooks: TrackerHooks{
			OnReady: func(ready TrackerReady) error {
				metadata = EventMetadata{
					StartedAt: time.Time{},
					FPS:       ready.FPS,
					Width:     ready.Width,
					Height:    ready.Height,
				}
				return nil
			},
			OnFrame: func(update *FrameUpdate) error {
				frames = append(frames, update.Metadata)
				return nil
			},
		},
	}

	if err := engine.Run(); err != nil {
		return EventMetadata{}, err
	}
	metadata.Frames = frames
	return metadata, nil
}

func writeMergedSegmentOutputs(processedDir string, merged MergedSegmentTracking, settings TrackingSettings) error {
	if err := writeJSONFile(filepath.Join(processedDir, "merged_tracking.json"), merged.Metadata); err != nil {
		return fmt.Errorf("write merged tracking metadata: %w", err)
	}
	if err := writeJSONFile(filepath.Join(processedDir, "merged_assignments.json"), merged.Assignments); err != nil {
		return fmt.Errorf("write merged track assignments: %w", err)
	}
	if err := writeJSONFile(filepath.Join(processedDir, "merged_boundary_matches.json"), merged.BoundaryMatch); err != nil {
		return fmt.Errorf("write merged boundary matches: %w", err)
	}

	summary := EventSummary{
		EventID:           "parallel_segment_merge",
		StartedAt:         merged.Metadata.StartedAt,
		EndedAt:           mergedTrackingEndTime(merged.Metadata),
		DurationSeconds:   mergedTrackingDurationSeconds(merged.Metadata),
		Width:             merged.Metadata.Width,
		Height:            merged.Metadata.Height,
		FPS:               merged.Metadata.FPS,
		Frames:            len(merged.Metadata.Frames),
		UniqueObjects:     countMergedUniqueObjects(merged.Metadata),
		HighestSpeedPxSec: maxMergedTrackSpeed(merged.Metadata),
		TrackingMetadata:  "merged_tracking.json",
		TrackingSettings:  NormalizeTrackingSettings(settings),
	}
	if err := writeJSONFile(filepath.Join(processedDir, "merged_event.json"), summary); err != nil {
		return fmt.Errorf("write merged event summary: %w", err)
	}
	return nil
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func mergedTrackingEndTime(metadata EventMetadata) time.Time {
	if metadata.StartedAt.IsZero() || len(metadata.Frames) == 0 {
		return time.Time{}
	}
	last := metadata.Frames[len(metadata.Frames)-1]
	return metadata.StartedAt.Add(time.Duration(last.TimeMS) * time.Millisecond)
}

func mergedTrackingDurationSeconds(metadata EventMetadata) float64 {
	if len(metadata.Frames) == 0 {
		return 0
	}
	return float64(metadata.Frames[len(metadata.Frames)-1].TimeMS) / 1000.0
}

func countMergedUniqueObjects(metadata EventMetadata) int {
	ids := make(map[int]struct{})
	for _, frame := range metadata.Frames {
		for _, track := range frame.Tracks {
			ids[track.ID] = struct{}{}
		}
	}
	return len(ids)
}

func maxMergedTrackSpeed(metadata EventMetadata) float64 {
	maxSpeed := 0.0
	for _, frame := range metadata.Frames {
		for _, track := range frame.Tracks {
			if track.Speed > maxSpeed {
				maxSpeed = track.Speed
			}
		}
	}
	return maxSpeed
}

func stopped(stopCh <-chan struct{}) bool {
	if stopCh == nil {
		return false
	}
	select {
	case <-stopCh:
		return true
	default:
		return false
	}
}
