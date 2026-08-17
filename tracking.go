package main

import (
	"image"
	"math"
	"time"

	"gocv.io/x/gocv"
)

const maxMissedFrames = 2

// findDetections converts the cleaned foreground mask into bounding boxes and
// centers that can be handed to the tracker.
func findDetections(mask gocv.Mat, settings TrackingSettings) []Detection {
	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	detections := make([]Detection, 0, contours.Size())

	for i := 0; i < contours.Size(); i++ {
		contour := contours.At(i)
		area := gocv.ContourArea(contour)

		if area < settings.MinArea || area > settings.MaxArea {
			continue
		}

		rect := gocv.BoundingRect(contour)
		if rect.Dx() < 2 || rect.Dy() < 2 {
			continue
		}

		detections = append(detections, Detection{
			Rect: rect,
			Center: image.Pt(
				(rect.Min.X+rect.Max.X)/2,
				(rect.Min.Y+rect.Max.Y)/2,
			),
			Area: area,
		})
	}

	return mergeNearbyDetections(detections, settings)
}

func mergeNearbyDetections(detections []Detection, settings TrackingSettings) []Detection {
	if len(detections) < 2 {
		return detections
	}

	merged := append([]Detection(nil), detections...)
	for {
		changed := false

		for i := 0; i < len(merged); i++ {
			for j := i + 1; j < len(merged); j++ {
				if !shouldMergeDetections(merged[i], merged[j], settings) {
					continue
				}

				merged[i] = mergeDetectionPair(merged[i], merged[j])
				merged = append(merged[:j], merged[j+1:]...)
				changed = true
				break
			}
			if changed {
				break
			}
		}

		if !changed {
			return merged
		}
	}
}

func shouldMergeDetections(a, b Detection, settings TrackingSettings) bool {
	aRect := a.Rect
	bRect := b.Rect
	if aRect.Empty() || bRect.Empty() {
		return false
	}

	hGap := rectAxisGap(aRect.Min.X, aRect.Max.X, bRect.Min.X, bRect.Max.X)
	vGap := rectAxisGap(aRect.Min.Y, aRect.Max.Y, bRect.Min.Y, bRect.Max.Y)

	maxWidth := maxInt(aRect.Dx(), bRect.Dx())
	maxHeight := maxInt(aRect.Dy(), bRect.Dy())

	// Nearby mask fragments from one aircraft should be fused into one
	// detection before track association, otherwise the recorder saves partial
	// crops for separate track IDs.
	maxHorizontalGap := maxInt(24, maxWidth/2)
	maxVerticalGap := maxInt(12, maxHeight)

	if hGap > maxHorizontalGap || vGap > maxVerticalGap {
		return false
	}

	union := aRect.Union(bRect)
	if float64(union.Dx()*union.Dy()) > settings.MaxArea*1.5 {
		return false
	}

	return true
}

func mergeDetectionPair(a, b Detection) Detection {
	rect := a.Rect.Union(b.Rect)
	return Detection{
		Rect: rect,
		Center: image.Pt(
			(rect.Min.X+rect.Max.X)/2,
			(rect.Min.Y+rect.Max.Y)/2,
		),
		Area: a.Area + b.Area,
	}
}

func rectAxisGap(aMin, aMax, bMin, bMax int) int {
	if aMax < bMin {
		return bMin - aMax
	}
	if bMax < aMin {
		return aMin - bMax
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// updateTracks matches detections to existing tracks by nearest predicted
// position, updates velocity estimates, creates new tracks, and keeps stale
// tracks available for display after detections disappear.
func updateTracks(
	tracks []*Track,
	detections []Detection,
	now time.Time,
	frameDT float64,
	nextTrackID int,
	settings TrackingSettings,
) ([]*Track, int) {
	detectionUsed := make([]bool, len(detections))
	trackMatched := make([]bool, len(tracks))

	for {
		bestTrack := -1
		bestDetection := -1
		bestDistance := math.MaxFloat64

		// Greedy nearest-neighbor association is enough here because the scenes
		// are sparse and the targets are expected to be small and fast.
		for ti, track := range tracks {
			if trackMatched[ti] {
				continue
			}

			dt := now.Sub(track.LastUpdate).Seconds()
			if dt <= 0 || dt > 1 {
				dt = frameDT
			}

			predictedX := float64(track.Position.X) + track.VX*dt
			predictedY := float64(track.Position.Y) + track.VY*dt

			for di, detection := range detections {
				if detectionUsed[di] {
					continue
				}

				dx := float64(detection.Center.X) - predictedX
				dy := float64(detection.Center.Y) - predictedY
				distance := math.Hypot(dx, dy)

				if distance < bestDistance && distance <= settings.MaxMatchDistance {
					bestDistance = distance
					bestTrack = ti
					bestDetection = di
				}
			}
		}

		if bestTrack == -1 {
			break
		}

		track := tracks[bestTrack]
		detection := detections[bestDetection]

		dt := now.Sub(track.LastUpdate).Seconds()
		if dt <= 0 || dt > 1 {
			dt = frameDT
		}

		oldPosition := track.Position
		dx := float64(detection.Center.X - oldPosition.X)
		dy := float64(detection.Center.Y - oldPosition.Y)

		measuredVX := dx / dt
		measuredVY := dy / dt

		const velocityAlpha = 0.45
		if track.Hits <= 1 {
			track.VX = measuredVX
			track.VY = measuredVY
		} else {
			track.VX = track.VX*(1-velocityAlpha) + measuredVX*velocityAlpha
			track.VY = track.VY*(1-velocityAlpha) + measuredVY*velocityAlpha
		}

		track.Speed = math.Hypot(track.VX, track.VY)
		track.PrevPosition = oldPosition
		track.Position = detection.Center
		track.Rect = detection.Rect
		track.LastUpdate = now
		track.Hits++
		track.Missed = 0

		track.Trail = append(track.Trail, detection.Center)

		trackMatched[bestTrack] = true
		detectionUsed[bestDetection] = true
	}

	for i, track := range tracks {
		if !trackMatched[i] {
			track.Missed++
		}
	}

	activeTracks := tracks[:0]
	for _, track := range tracks {
		if track.Missed > maxMissedFrames {
			continue
		}
		activeTracks = append(activeTracks, track)
	}
	tracks = activeTracks

	for i, detection := range detections {
		if detectionUsed[i] {
			continue
		}

		tracks = append(tracks, &Track{
			ID:           nextTrackID,
			Rect:         detection.Rect,
			Position:     detection.Center,
			PrevPosition: detection.Center,
			LastUpdate:   now,
			Hits:         1,
			Trail:        []image.Point{detection.Center},
		})
		nextTrackID++
	}

	return tracks, nextTrackID
}

// makeFrameMetadata filters the active tracks down to the ones considered
// stable and interesting enough to persist.
func makeFrameMetadata(sourceFrame int, start, now time.Time, tracks []*Track, settings TrackingSettings) FrameMetadata {
	meta := FrameMetadata{
		SourceFrame: sourceFrame,
		TimeUnixNS:  now.UnixNano(),
		TimeMS:      now.Sub(start).Milliseconds(),
		Tracks:      make([]TrackMetadata, 0),
	}

	for _, t := range tracks {
		if t.Missed > 0 {
			continue
		}

		trackType, ok := classifyTrack(t, settings)
		if t.Hits < settings.MinHits || !ok {
			continue
		}

		meta.Tracks = append(meta.Tracks, TrackMetadata{
			ID:        t.ID,
			Type:      trackType,
			X:         t.Position.X,
			Y:         t.Position.Y,
			BoxX:      t.Rect.Min.X,
			BoxY:      t.Rect.Min.Y,
			BoxWidth:  t.Rect.Dx(),
			BoxHeight: t.Rect.Dy(),
			VX:        t.VX,
			VY:        t.VY,
			Speed:     t.Speed,
			Trail:     makeTrailMetadata(t.Trail),
		})
	}

	return meta
}

func makeTrailMetadata(points []image.Point) []TrailPoint {
	if len(points) == 0 {
		return nil
	}

	trail := make([]TrailPoint, len(points))
	for i, point := range points {
		trail[i] = TrailPoint{X: point.X, Y: point.Y}
	}
	return trail
}

func classifyTrack(t *Track, settings TrackingSettings) (string, bool) {
	if t.Speed >= settings.MinSpeed {
		return trackTypeFast, true
	}
	if t.Speed >= settings.SlowMinSpeed {
		return trackTypeSlow, true
	}
	return "", false
}

func hasFreshInterestingTracks(tracks []*Track, settings TrackingSettings) bool {
	for _, track := range tracks {
		trackType, ok := classifyTrack(track, settings)
		if ok && trackType == trackTypeFast && track.Missed == 0 {
			return true
		}
	}
	return false
}

func countTrackTypes(tracks []TrackMetadata) (fastCount, slowCount int) {
	for _, track := range tracks {
		switch track.Type {
		case trackTypeFast:
			fastCount++
		case trackTypeSlow:
			slowCount++
		}
	}
	return fastCount, slowCount
}
