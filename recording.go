package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"time"

	"gocv.io/x/gocv"
)

const (
	minTrackCropWidth  = 96
	minTrackCropHeight = 96
)

// trimBuffer keeps only the pre-event time window in RAM and releases the Mats
// for frames that have aged out.
func trimBuffer(buffer []BufferedFrame, cutoff time.Time) []BufferedFrame {
	firstKeep := 0
	for firstKeep < len(buffer) && buffer[firstKeep].Timestamp.Before(cutoff) {
		buffer[firstKeep].Image.Close()
		buffer[firstKeep].Mask.Close()
		firstKeep++
	}

	if firstKeep == 0 {
		return buffer
	}
	return buffer[firstKeep:]
}

// closeBuffer releases all Mats still owned by the rolling pre-event buffer.
func closeBuffer(buffer []BufferedFrame) {
	for i := range buffer {
		buffer[i].Image.Close()
		buffer[i].Mask.Close()
	}
}

// startEvent creates a new event directory, opens the two output video writers,
// initializes metadata, and flushes the pre-event buffer into the recording.
func startEvent(
	outputRoot string,
	fps float64,
	width, height int,
	buffer []BufferedFrame,
	settings TrackingSettings,
) (*EventRecorder, error) {
	if len(buffer) == 0 {
		return nil, errors.New("cannot start event with empty buffer")
	}

	startedAt := buffer[0].Timestamp
	eventID := startedAt.Format("2006-01-02_150405.000")
	dir := filepath.Join(outputRoot, eventID)

	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(outputRoot, fmt.Sprintf("%s_%02d", eventID, suffix))
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	rawPath := filepath.Join(dir, "original.avi")
	trackedPath := filepath.Join(dir, "tracked.avi")
	maskedPath := filepath.Join(dir, "masked.avi")

	rawWriter, err := gocv.VideoWriterFile(rawPath, "MJPG", fps, width, height, true)
	if err != nil {
		return nil, fmt.Errorf("create original video: %w", err)
	}
	if !rawWriter.IsOpened() {
		rawWriter.Close()
		return nil, errors.New("original video writer did not open")
	}

	trackedWriter, err := gocv.VideoWriterFile(trackedPath, "MJPG", fps, width, height, true)
	if err != nil {
		rawWriter.Close()
		return nil, fmt.Errorf("create tracked video: %w", err)
	}
	if !trackedWriter.IsOpened() {
		rawWriter.Close()
		trackedWriter.Close()
		return nil, errors.New("tracked video writer did not open")
	}

	maskedWriter, err := gocv.VideoWriterFile(maskedPath, "MJPG", fps, width, height, true)
	if err != nil {
		rawWriter.Close()
		trackedWriter.Close()
		return nil, fmt.Errorf("create masked video: %w", err)
	}
	if !maskedWriter.IsOpened() {
		rawWriter.Close()
		trackedWriter.Close()
		maskedWriter.Close()
		return nil, errors.New("masked video writer did not open")
	}

	recorder := &EventRecorder{
		RawWriter:     rawWriter,
		TrackedWriter: trackedWriter,
		MaskedWriter:  maskedWriter,
		Directory:     dir,
		StartedAt:     startedAt,
		FPS:           fps,
		Width:         width,
		Height:        height,
		SeenIDs:       make(map[int]struct{}),
		Settings:      NormalizeTrackingSettings(settings),
		Metadata: EventMetadata{
			EventID:   filepath.Base(dir),
			StartedAt: startedAt,
			FPS:       fps,
			Width:     width,
			Height:    height,
			Frames:    make([]FrameMetadata, 0, len(buffer)+128),
		},
	}

	for _, bf := range buffer {
		if err := recorder.RecordFrame(bf.Image, bf.Mask, bf.Metadata); err != nil {
			recorder.RawWriter.Close()
			recorder.TrackedWriter.Close()
			recorder.MaskedWriter.Close()
			return nil, err
		}
	}

	return recorder, nil
}

// RecordFrame writes one raw frame, creates the annotated tracked frame, and
// appends the JSON metadata for that frame.
func (r *EventRecorder) RecordFrame(clean gocv.Mat, mask gocv.Mat, meta FrameMetadata) error {
	meta.TimeMS = time.Unix(0, meta.TimeUnixNS).Sub(r.StartedAt).Milliseconds()

	if err := r.RawWriter.Write(clean); err != nil {
		return fmt.Errorf("write original video: %w", err)
	}

	overlay := clean.Clone()
	drawMetadataOverlay(&overlay, meta.Tracks, r.Settings)
	if err := r.TrackedWriter.Write(overlay); err != nil {
		overlay.Close()
		return fmt.Errorf("write tracked video: %w", err)
	}
	overlay.Close()

	maskBGR := gocv.NewMat()
	defer maskBGR.Close()
	if err := gocv.CvtColor(mask, &maskBGR, gocv.ColorGrayToBGR); err != nil {
		return fmt.Errorf("convert mask video frame: %w", err)
	}
	if err := r.MaskedWriter.Write(maskBGR); err != nil {
		return fmt.Errorf("write masked video: %w", err)
	}

	if err := r.saveTrackCrops(clean, meta); err != nil {
		return err
	}

	for _, t := range meta.Tracks {
		r.SeenIDs[t.ID] = struct{}{}
		if t.Speed > r.HighestSpeed {
			r.HighestSpeed = t.Speed
		}
	}

	r.Metadata.Frames = append(r.Metadata.Frames, meta)
	return nil
}

// Finish closes the writers and emits the final JSON files for the event.
func (r *EventRecorder) Finish(endedAt time.Time) error {
	var errs []error

	if r.RawWriter != nil {
		if err := r.RawWriter.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.TrackedWriter != nil {
		if err := r.TrackedWriter.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.MaskedWriter != nil {
		if err := r.MaskedWriter.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	trackingPath := filepath.Join(r.Directory, "tracking.json")
	data, err := json.MarshalIndent(r.Metadata, "", "  ")
	if err != nil {
		errs = append(errs, err)
	} else if err := os.WriteFile(trackingPath, data, 0644); err != nil {
		errs = append(errs, err)
	}

	summary := EventSummary{
		EventID:           filepath.Base(r.Directory),
		StartedAt:         r.StartedAt,
		EndedAt:           endedAt,
		DurationSeconds:   endedAt.Sub(r.StartedAt).Seconds(),
		Width:             r.Width,
		Height:            r.Height,
		FPS:               r.FPS,
		Frames:            len(r.Metadata.Frames),
		UniqueObjects:     len(r.SeenIDs),
		HighestSpeedPxSec: r.HighestSpeed,
		OriginalVideo:     "original.avi",
		TrackedVideo:      "tracked.avi",
		MaskedVideo:       "masked.avi",
		TrackCropsDir:     "track_crops",
		TrackNamesFile:    "track_names.json",
		TrackingMetadata:  "tracking.json",
		TrackingSettings:  r.Settings,
	}

	summaryData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		errs = append(errs, err)
	} else if err := os.WriteFile(filepath.Join(r.Directory, "event.json"), summaryData, 0644); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func (r *EventRecorder) saveTrackCrops(frame gocv.Mat, meta FrameMetadata) error {
	if frame.Empty() || len(meta.Tracks) == 0 {
		return nil
	}

	root := filepath.Join(r.Directory, "track_crops")
	frameBounds := image.Rect(0, 0, frame.Cols(), frame.Rows())

	for _, track := range meta.Tracks {
		rect := image.Rect(
			track.BoxX,
			track.BoxY,
			track.BoxX+track.BoxWidth,
			track.BoxY+track.BoxHeight,
		)
		rect = expandedTrackCropRect(rect).Intersect(frameBounds)
		if rect.Empty() {
			continue
		}

		objectDir := filepath.Join(root, fmt.Sprintf("object_%04d", track.ID))
		if err := os.MkdirAll(objectDir, 0755); err != nil {
			return fmt.Errorf("create track crop directory: %w", err)
		}

		crop := frame.Region(rect)
		filename := filepath.Join(objectDir,
			fmt.Sprintf("frame_%06d_%09dms.jpg", meta.SourceFrame, meta.TimeMS))
		if ok := gocv.IMWrite(filename, crop); !ok {
			crop.Close()
			return fmt.Errorf("save track crop %s", filename)
		}
		crop.Close()
	}

	return nil
}

func expandedTrackCropRect(rect image.Rectangle) image.Rectangle {
	rect = scaleRectAroundCenter(rect, trackBoxScale)
	if rect.Empty() {
		return rect
	}

	centerX := (rect.Min.X + rect.Max.X) / 2
	centerY := (rect.Min.Y + rect.Max.Y) / 2
	width := rect.Dx()
	height := rect.Dy()
	if width < minTrackCropWidth {
		width = minTrackCropWidth
	}
	if height < minTrackCropHeight {
		height = minTrackCropHeight
	}

	halfWidth := width / 2
	halfHeight := height / 2
	return image.Rect(
		centerX-halfWidth,
		centerY-halfHeight,
		centerX-halfWidth+width,
		centerY-halfHeight+height,
	)
}
