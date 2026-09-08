package main

import (
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"time"

	"gocv.io/x/gocv"
)

var ErrStopTracking = errors.New("stop tracking")

// TrackerConfig defines one tracker run independently of any specific UI.
type TrackerConfig struct {
	Input        string
	InputLabel   string
	OutputDir    string
	ShowMask     bool
	RecordEvents bool
	FallbackFPS  float64
	Settings     TrackingSettings
	Nostr        NostrSettings
	Capture      CaptureMetadata
}

type NostrSettings struct {
	Enabled          bool
	RelayURL         string
	SecretKey        string
	BlossomServerURL string
	BlossomNoteURL   string
	MinTrackDistance float64
	UseObjectGIF     bool
	Timeout          time.Duration
}

// TrackerReady reports source properties once the input stream has opened.
type TrackerReady struct {
	Input          string
	InputLabel     string
	FPS            float64
	Width          int
	Height         int
	TotalFrames    int
	Duration       time.Duration
	HasFixedLength bool
}

// FrameUpdate is one rendered tracker snapshot suitable for a CLI preview or a
// future GUI surface.
type FrameUpdate struct {
	SourceFrame        int
	Timestamp          time.Time
	Metadata           FrameMetadata
	DetectionCount     int
	TrackCount         int
	FastCount          int
	SlowCount          int
	Recording          bool
	LearningBackground bool
	StatusText         string
	ProgressText       string
	Elapsed            time.Duration
	TotalDuration      time.Duration
	Progress           float64
	HasFixedLength     bool
	Display            gocv.Mat
	Mask               gocv.Mat

	hasDisplay bool
	hasMask    bool
}

// Close releases any Mats owned by this update.
func (u *FrameUpdate) Close() {
	if u == nil {
		return
	}
	if u.hasDisplay {
		u.Display.Close()
		u.hasDisplay = false
	}
	if u.hasMask {
		u.Mask.Close()
		u.hasMask = false
	}
}

// TrackerHooks lets callers attach a presentation layer without changing the
// tracking pipeline itself.
type TrackerHooks struct {
	OnReady      func(TrackerReady) error
	OnFrame      func(*FrameUpdate) error
	OnEventStart func(string) error
	OnEventSaved func(string) error
}

// TrackerEngine owns one run of the capture, tracking, and event recording
// pipeline.
type TrackerEngine struct {
	Config TrackerConfig
	Hooks  TrackerHooks
	Stop   <-chan struct{}

	RawSegmentDir string
}

func (e *TrackerEngine) effectiveSettings() TrackingSettings {
	return NormalizeTrackingSettings(e.Config.Settings)
}

func (e *TrackerEngine) shouldRecordEvents() bool {
	return e.Config.RecordEvents
}

// Run executes the tracker until the input ends, the caller asks it to stop,
// or an error occurs.
func (e *TrackerEngine) Run() (runErr error) {
	if e.stopped() {
		return nil
	}

	capture, fps, err := e.openCapture()
	if err != nil {
		return err
	}
	defer capture.Close()

	if err := e.emitReady(capture, fps); err != nil {
		if errors.Is(err, ErrStopTracking) {
			return nil
		}
		return err
	}

	frame := gocv.NewMat()
	defer frame.Close()
	gray := gocv.NewMat()
	defer gray.Close()
	blurred := gocv.NewMat()
	defer blurred.Close()
	mask := gocv.NewMat()
	defer mask.Close()
	cleanMask := gocv.NewMat()
	defer cleanMask.Close()
	settings := e.effectiveSettings()

	// Disable MOG2 shadow labeling so moving objects darker than the background
	// are still emitted as full foreground instead of being downgraded to the
	// intermediate "shadow" class and discarded by the later binary threshold.
	background := gocv.NewBackgroundSubtractorMOG2WithParams(settings.MOG2History, settings.MOG2VarThreshold, false)
	defer background.Close()

	openKernel := gocv.GetStructuringElement(gocv.MorphEllipse, image.Pt(3, 3))
	defer openKernel.Close()
	dilateKernel := gocv.GetStructuringElement(gocv.MorphEllipse, image.Pt(5, 5))
	defer dilateKernel.Close()
	var tracks []*Track
	var buffer []BufferedFrame
	var recorder *EventRecorder
	var segmentRecorder *RawSegmentRecorder
	bufferDir, err := os.MkdirTemp("", "trackobject-buffer-*")
	if err != nil {
		return fmt.Errorf("create frame spool directory: %w", err)
	}
	defer os.RemoveAll(bufferDir)

	lastFrameTime := time.Time{}
	defer func() {
		runErr = errors.Join(runErr, finishTrackerRunResources(buffer, recorder, segmentRecorder, lastFrameTime))
	}()

	nextTrackID := 1
	sourceFrame := 0
	firstFrameTime := time.Time{}
	lastInteresting := time.Time{}

	for {
		if e.stopped() {
			break
		}

		if ok := capture.Read(&frame); !ok || frame.Empty() {
			if e.Config.InputLabel == "video file" {
				break
			}
			continue
		}

		observedAt := time.Now()
		if firstFrameTime.IsZero() {
			firstFrameTime = observedAt
		}

		sourceFrame++
		now := sourceFrameTimestamp(e.Config.InputLabel, firstFrameTime, observedAt, sourceFrame, fps)
		frameDT := sourceFrameDelta(lastFrameTime, now, fps)
		lastFrameTime = now

		if e.shouldRecordEvents() && segmentRecorder == nil && settings.RawSegmentDuration > 0 {
			segmentRecorder, err = startRawSegmentRecorder(e.Config.OutputDir, fps, frame.Cols(), frame.Rows(), settings, e.Config.Capture)
			if err != nil {
				return err
			}
			e.RawSegmentDir = segmentRecorder.Directory
		}
		if segmentRecorder != nil {
			timeOffset := time.Duration(float64(sourceFrame-1) * float64(time.Second) / fps)
			if err := segmentRecorder.RecordFrame(frame, sourceFrame, timeOffset); err != nil {
				return err
			}
		}

		if err := gocv.CvtColor(frame, &gray, gocv.ColorBGRToGray); err != nil {
			continue
		}
		if err := gocv.GaussianBlur(gray, &blurred, image.Pt(settings.BlurSize, settings.BlurSize), 0, 0, gocv.BorderDefault); err != nil {
			continue
		}
		if err := background.Apply(blurred, &mask); err != nil {
			continue
		}
		gocv.Threshold(mask, &cleanMask, float32(settings.ForegroundThreshold), 255, gocv.ThresholdBinary)
		if err := gocv.MorphologyEx(cleanMask, &cleanMask, gocv.MorphOpen, openKernel); err != nil {
			continue
		}
		if err := gocv.Dilate(cleanMask, &cleanMask, dilateKernel); err != nil {
			continue
		}

		trackingROI := trackingROIForSize(frame.Cols(), frame.Rows(), settings)
		applyTrackingROI(&cleanMask, trackingROI)

		detections := findDetections(cleanMask, settings)
		tracks, nextTrackID = updateTracks(tracks, detections, now, frameDT, nextTrackID, settings)
		tracks = filterTracksToROI(tracks, trackingROI)

		meta := makeFrameMetadata(sourceFrame, firstFrameTime, now, tracks, settings)
		mean := frame.Mean()
		meta.MeanLuma = 0.114*mean.Val1 + 0.587*mean.Val2 + 0.299*mean.Val3
		interesting := hasFreshInterestingTracks(tracks, settings)

		if interesting {
			lastInteresting = now
		}

		if !e.shouldRecordEvents() {
			// Full-segment processing mode still emits per-frame metadata through
			// OnFrame, but it does not open per-event video writers.
		} else if recorder == nil {
			bufferedFrame, err := spoolBufferedFrame(frame, cleanMask, bufferDir, meta, now)
			if err != nil {
				return err
			}
			buffer = append(buffer, bufferedFrame)
			buffer = trimBuffer(buffer, now.Add(-settings.PreEventDuration))

			if interesting {
				recorder, err = startEvent(e.Config.OutputDir, fps, frame.Cols(), frame.Rows(), buffer, settings, e.Config.Capture)
				if err != nil {
					return err
				}
				closeBuffer(buffer)
				buffer = nil

				if err := e.emitEventStart(recorder.Directory); err != nil {
					if errors.Is(err, ErrStopTracking) {
						break
					}
					return err
				}
			}
		} else {
			if err := recorder.RecordFrame(frame, cleanMask, tracks, meta); err != nil {
				return err
			}

			if !lastInteresting.IsZero() && now.Sub(lastInteresting) >= settings.PostEventDuration {
				dir := recorder.Directory
				finishedRecorder := recorder
				recorder = nil
				if err := finishedRecorder.Finish(now); err != nil {
					return err
				}
				lastInteresting = time.Time{}

				if err := e.emitEventSaved(dir); err != nil {
					if errors.Is(err, ErrStopTracking) {
						break
					}
					return err
				}
			}
		}

		totalFrames := 0
		if e.Config.InputLabel == "video file" {
			totalFrames = int(math.Round(capture.Get(gocv.VideoCaptureFrameCount)))
		}
		update := buildFrameUpdate(frame, cleanMask, tracks, meta, len(detections), recorder != nil, sourceFrame < int(fps*3), e.Config.ShowMask, settings, fps, totalFrames)
		err = e.emitFrame(update, now)
		update.Close()
		if err != nil {
			if errors.Is(err, ErrStopTracking) {
				break
			}
			return err
		}
	}

	if recorder != nil {
		dir := recorder.Directory
		finishedRecorder := recorder
		recorder = nil
		if err := finishedRecorder.Finish(lastFrameTime); err != nil {
			return err
		}
		if err := e.emitEventSaved(dir); err != nil && !errors.Is(err, ErrStopTracking) {
			return err
		}
	}

	return nil
}

func finishTrackerRunResources(buffer []BufferedFrame, recorder *EventRecorder, segmentRecorder *RawSegmentRecorder, endedAt time.Time) error {
	closeBuffer(buffer)
	var errs []error
	if recorder != nil {
		if endedAt.IsZero() {
			endedAt = recorder.StartedAt
		}
		if err := recorder.Finish(endedAt); err != nil {
			errs = append(errs, fmt.Errorf("finalize open event recorder: %w", err))
		}
	}
	if segmentRecorder != nil {
		if err := segmentRecorder.Close(); err != nil {
			errs = append(errs, fmt.Errorf("finalize raw segment recorder: %w", err))
		}
	}
	return errors.Join(errs...)
}

func sourceFrameTimestamp(inputLabel string, anchor, observedAt time.Time, sourceFrame int, fps float64) time.Time {
	if inputLabel != "video file" || sourceFrame <= 1 || !validFPS(fps) {
		return observedAt
	}
	offset := time.Duration(float64(sourceFrame-1) * float64(time.Second) / fps)
	return anchor.Add(offset)
}

func sourceFrameDelta(previous, current time.Time, fps float64) float64 {
	if !previous.IsZero() {
		delta := current.Sub(previous).Seconds()
		if delta > 0 && delta <= 1 {
			return delta
		}
	}
	if validFPS(fps) {
		return 1.0 / fps
	}
	return 1.0 / 30.0
}

func validFPS(fps float64) bool {
	return fps > 0 && !math.IsNaN(fps) && !math.IsInf(fps, 0)
}

func (e *TrackerEngine) stopped() bool {
	if e.Stop == nil {
		return false
	}

	select {
	case <-e.Stop:
		return true
	default:
		return false
	}
}

func (e *TrackerEngine) openCapture() (*gocv.VideoCapture, float64, error) {
	capture, err := gocv.VideoCaptureFile(e.Config.Input)
	if err != nil {
		return nil, 0, fmt.Errorf("open input source: %w", err)
	}
	if !capture.IsOpened() {
		capture.Close()
		return nil, 0, errors.New("input source did not open")
	}

	fps := capture.Get(gocv.VideoCaptureFPS)
	if fps <= 1 || !validFPS(fps) {
		fps = e.Config.FallbackFPS
	}
	if !validFPS(fps) {
		fps = 30
	}

	return capture, fps, nil
}

func (e *TrackerEngine) emitReady(capture *gocv.VideoCapture, fps float64) error {
	if e.Hooks.OnReady == nil {
		return nil
	}
	totalFrames := int(math.Round(capture.Get(gocv.VideoCaptureFrameCount)))
	hasFixedLength := e.Config.InputLabel == "video file" && totalFrames > 0
	duration := time.Duration(0)
	if hasFixedLength && fps > 0 {
		duration = time.Duration(float64(time.Second) * (float64(totalFrames) / fps))
	}
	return e.Hooks.OnReady(TrackerReady{
		Input:          e.Config.Input,
		InputLabel:     e.Config.InputLabel,
		FPS:            fps,
		Width:          int(capture.Get(gocv.VideoCaptureFrameWidth)),
		Height:         int(capture.Get(gocv.VideoCaptureFrameHeight)),
		TotalFrames:    totalFrames,
		Duration:       duration,
		HasFixedLength: hasFixedLength,
	})
}

func (e *TrackerEngine) emitEventStart(dir string) error {
	if e.Hooks.OnEventStart == nil {
		return nil
	}
	return e.Hooks.OnEventStart(dir)
}

func (e *TrackerEngine) emitEventSaved(dir string) error {
	if e.Hooks.OnEventSaved == nil {
		return nil
	}
	return e.Hooks.OnEventSaved(dir)
}

func (e *TrackerEngine) emitFrame(update FrameUpdate, now time.Time) error {
	if e.Hooks.OnFrame == nil {
		return nil
	}
	update.Timestamp = now
	return e.Hooks.OnFrame(&update)
}

func buildFrameUpdate(
	frame gocv.Mat,
	cleanMask gocv.Mat,
	tracks []*Track,
	meta FrameMetadata,
	detectionCount int,
	recording bool,
	learningBackground bool,
	includeMask bool,
	settings TrackingSettings,
	fps float64,
	totalFrames int,
) FrameUpdate {
	display := frame.Clone()
	drawLiveOverlay(&display, tracks, settings)

	fastCount, slowCount := countTrackTypes(meta.Tracks)
	status := fmt.Sprintf("FAST: %d   SLOW: %d   detections: %d   tracks: %d",
		fastCount, slowCount, detectionCount, len(tracks))
	gocv.PutText(&display, status, image.Pt(20, 30),
		gocv.FontHersheySimplex, 0.9, textColor, 2)

	if recording {
		gocv.PutText(&display, "RECORDING EVENT", image.Pt(20, 60),
			gocv.FontHersheySimplex, 0.9, warnColor, 2)
	} else {
		gocv.PutText(&display, fmt.Sprintf("RAM PREBUFFER: %.0fs", settings.PreEventDuration.Seconds()),
			image.Pt(20, 60), gocv.FontHersheySimplex, 0.75, textColor, 2)
	}

	if learningBackground {
		gocv.PutText(&display, "LEARNING BACKGROUND...", image.Pt(20, 90),
			gocv.FontHersheySimplex, 0.9, warnColor, 2)
	}

	elapsed := time.Duration(0)
	if fps > 0 && meta.SourceFrame > 0 {
		elapsed = time.Duration(float64(time.Second) * (float64(meta.SourceFrame) / fps))
	}

	totalDuration := time.Duration(0)
	hasFixedLength := fps > 0 && totalFrames > 0
	progress := 0.0
	progressText := ""
	if hasFixedLength {
		totalDuration = time.Duration(float64(time.Second) * (float64(totalFrames) / fps))
		progress = math.Min(1, float64(meta.SourceFrame)/float64(totalFrames))
		progressText = fmt.Sprintf("Video: %s / %s (%.0f%%)",
			formatVideoProgressDuration(elapsed),
			formatVideoProgressDuration(totalDuration),
			progress*100)
		gocv.PutText(&display, progressText, image.Pt(20, 120),
			gocv.FontHersheySimplex, 0.85, textColor, 2)
	}

	update := FrameUpdate{
		SourceFrame:        meta.SourceFrame,
		Metadata:           meta,
		DetectionCount:     detectionCount,
		TrackCount:         len(tracks),
		FastCount:          fastCount,
		SlowCount:          slowCount,
		Recording:          recording,
		LearningBackground: learningBackground,
		StatusText:         status,
		ProgressText:       progressText,
		Elapsed:            elapsed,
		TotalDuration:      totalDuration,
		Progress:           progress,
		HasFixedLength:     hasFixedLength,
		Display:            display,
		hasDisplay:         true,
	}

	if includeMask {
		update.Mask = cleanMask.Clone()
		update.hasMask = true
	}

	return update
}

func formatVideoProgressDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalSeconds := int(math.Round(d.Seconds()))
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}
