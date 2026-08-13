package main

import (
	"errors"
	"fmt"
	"image"
	"math"
	"time"

	"gocv.io/x/gocv"
)

var ErrStopTracking = errors.New("stop tracking")

// TrackerConfig defines one tracker run independently of any specific UI.
type TrackerConfig struct {
	Input       string
	InputLabel  string
	OutputDir   string
	ShowMask    bool
	FallbackFPS float64
}

// TrackerReady reports source properties once the input stream has opened.
type TrackerReady struct {
	Input      string
	InputLabel string
	FPS        float64
	Width      int
	Height     int
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
}

// Run executes the tracker until the input ends, the caller asks it to stop,
// or an error occurs.
func (e *TrackerEngine) Run() error {
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

	// Disable MOG2 shadow labeling so moving objects darker than the background
	// are still emitted as full foreground instead of being downgraded to the
	// intermediate "shadow" class and discarded by the later binary threshold.
	background := gocv.NewBackgroundSubtractorMOG2WithParams(mog2History, mog2VarThreshold, false)
	defer background.Close()

	openKernel := gocv.GetStructuringElement(gocv.MorphEllipse, image.Pt(3, 3))
	defer openKernel.Close()
	dilateKernel := gocv.GetStructuringElement(gocv.MorphEllipse, image.Pt(5, 5))
	defer dilateKernel.Close()

	var tracks []*Track
	var buffer []BufferedFrame
	var recorder *EventRecorder

	nextTrackID := 1
	sourceFrame := 0
	firstFrameTime := time.Time{}
	lastFrameTime := time.Now()
	lastInteresting := time.Time{}

	for {
		if ok := capture.Read(&frame); !ok || frame.Empty() {
			if e.Config.InputLabel == "video file" {
				break
			}
			continue
		}

		now := time.Now()
		if firstFrameTime.IsZero() {
			firstFrameTime = now
		}

		frameDT := now.Sub(lastFrameTime).Seconds()
		lastFrameTime = now
		if frameDT <= 0 || frameDT > 1 {
			frameDT = 1.0 / fps
		}

		sourceFrame++

		if err := gocv.CvtColor(frame, &gray, gocv.ColorBGRToGray); err != nil {
			continue
		}
		if err := gocv.GaussianBlur(gray, &blurred, image.Pt(blurSize, blurSize), 0, 0, gocv.BorderDefault); err != nil {
			continue
		}
		if err := background.Apply(blurred, &mask); err != nil {
			continue
		}
		gocv.Threshold(mask, &cleanMask, foregroundThreshold, 255, gocv.ThresholdBinary)
		if err := gocv.MorphologyEx(cleanMask, &cleanMask, gocv.MorphOpen, openKernel); err != nil {
			continue
		}
		if err := gocv.Dilate(cleanMask, &cleanMask, dilateKernel); err != nil {
			continue
		}

		trackingROI := trackingROIForSize(frame.Cols(), frame.Rows())
		applyTrackingROI(&cleanMask, trackingROI)

		detections := findDetections(cleanMask)
		tracks, nextTrackID = updateTracks(tracks, detections, now, frameDT, nextTrackID)
		tracks = filterTracksToROI(tracks, trackingROI)

		meta := makeFrameMetadata(sourceFrame, firstFrameTime, now, tracks)
		interesting := hasFreshInterestingTracks(tracks)

		if interesting {
			lastInteresting = now
		}

		if recorder == nil {
			buffer = append(buffer, BufferedFrame{
				Image:     frame.Clone(),
				Timestamp: now,
				Metadata:  meta,
			})
			buffer = trimBuffer(buffer, now.Add(-preEventDuration))

			if interesting {
				recorder, err = startEvent(e.Config.OutputDir, fps, frame.Cols(), frame.Rows(), buffer)
				if err != nil {
					closeBuffer(buffer)
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
			if err := recorder.RecordFrame(frame, meta); err != nil {
				return err
			}

			if !lastInteresting.IsZero() && now.Sub(lastInteresting) >= postEventDuration {
				dir := recorder.Directory
				if err := recorder.Finish(now); err != nil {
					return err
				}
				recorder = nil
				lastInteresting = time.Time{}

				if err := e.emitEventSaved(dir); err != nil {
					if errors.Is(err, ErrStopTracking) {
						break
					}
					return err
				}
			}
		}

		update := buildFrameUpdate(frame, cleanMask, tracks, meta, len(detections), recorder != nil, sourceFrame < int(fps*3), e.Config.ShowMask)
		err = e.emitFrame(update, now)
		update.Close()
		if err != nil {
			if errors.Is(err, ErrStopTracking) {
				break
			}
			return err
		}
	}

	closeBuffer(buffer)

	if recorder != nil {
		dir := recorder.Directory
		if err := recorder.Finish(time.Now()); err != nil {
			return err
		}
		if err := e.emitEventSaved(dir); err != nil && !errors.Is(err, ErrStopTracking) {
			return err
		}
	}

	return nil
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
	if fps <= 1 || math.IsNaN(fps) || math.IsInf(fps, 0) {
		fps = e.Config.FallbackFPS
	}

	return capture, fps, nil
}

func (e *TrackerEngine) emitReady(capture *gocv.VideoCapture, fps float64) error {
	if e.Hooks.OnReady == nil {
		return nil
	}
	return e.Hooks.OnReady(TrackerReady{
		Input:      e.Config.Input,
		InputLabel: e.Config.InputLabel,
		FPS:        fps,
		Width:      int(capture.Get(gocv.VideoCaptureFrameWidth)),
		Height:     int(capture.Get(gocv.VideoCaptureFrameHeight)),
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
) FrameUpdate {
	display := frame.Clone()
	drawLiveOverlay(&display, tracks)

	fastCount, slowCount := countTrackTypes(meta.Tracks)
	status := fmt.Sprintf("FAST: %d   SLOW: %d   detections: %d   tracks: %d",
		fastCount, slowCount, detectionCount, len(tracks))
	gocv.PutText(&display, status, image.Pt(20, 30),
		gocv.FontHersheySimplex, 0.65, textColor, 2)

	if recording {
		gocv.PutText(&display, "RECORDING EVENT", image.Pt(20, 60),
			gocv.FontHersheySimplex, 0.65, warnColor, 2)
	} else {
		gocv.PutText(&display, fmt.Sprintf("RAM PREBUFFER: %.0fs", preEventDuration.Seconds()),
			image.Pt(20, 60), gocv.FontHersheySimplex, 0.5, textColor, 1)
	}

	if learningBackground {
		gocv.PutText(&display, "LEARNING BACKGROUND...", image.Pt(20, 90),
			gocv.FontHersheySimplex, 0.65, warnColor, 2)
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
		Display:            display,
		hasDisplay:         true,
	}

	if includeMask {
		update.Mask = cleanMask.Clone()
		update.hasMask = true
	}

	return update
}
