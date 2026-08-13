package main

import (
	"fmt"
	"image"
	"image/color"

	"gocv.io/x/gocv"
)

// drawLiveOverlay renders the current accepted tracks onto the interactive live
// preview window.
func drawLiveOverlay(frame *gocv.Mat, tracks []*Track) {
	drawTrackingROI(frame)

	for _, track := range tracks {
		trackType, ok := classifyTrack(track)
		if track.Hits < minHits || !ok {
			continue
		}

		trackColor, arrowColor := overlayColors(trackType)
		gocv.Rectangle(frame, scaleRectAroundCenter(track.Rect, trackBoxScale), trackColor, 1)

		if len(track.Trail) >= 2 {
			for i := 1; i < len(track.Trail); i++ {
				gocv.Line(frame, track.Trail[i-1], track.Trail[i], trackColor, 1)
			}
		}

		drawTargetAndLabel(frame, track.ID, track.Position, track.VX, track.VY, track.Speed, trackColor, arrowColor)
	}
}

// drawMetadataOverlay reconstructs track trails from saved metadata so the
// tracked export video shows the same context as the live preview.
func drawMetadataOverlay(frame *gocv.Mat, tracks []TrackMetadata) {
	drawTrackingROI(frame)

	for _, t := range tracks {
		trackColor, arrowColor := overlayColors(t.Type)
		center := image.Pt(t.X, t.Y)

		rect := scaleRectAroundCenter(
			image.Rect(t.BoxX, t.BoxY, t.BoxX+t.BoxWidth, t.BoxY+t.BoxHeight),
			trackBoxScale,
		)
		gocv.Rectangle(frame, rect, trackColor, 1)

		trail := make([]image.Point, 0, len(t.Trail))
		for _, point := range t.Trail {
			trail = append(trail, image.Pt(point.X, point.Y))
		}

		for i := 1; i < len(trail); i++ {
			gocv.Line(frame, trail[i-1], trail[i], trackColor, 1)
		}

		drawTargetAndLabel(frame, t.ID, center, t.VX, t.VY, t.Speed, trackColor, arrowColor)
	}
}

func overlayColors(trackType string) (trackColor, arrowColor color.RGBA) {
	if trackType == trackTypeSlow {
		return slowTrackColor, slowArrowColor
	}
	return fastTrackColor, fastArrowColor
}

// scaleRectAroundCenter expands a rectangle around its center so the displayed
// tracking box is easier to see without changing the underlying track state.
func scaleRectAroundCenter(rect image.Rectangle, scale int) image.Rectangle {
	if scale <= 1 {
		return rect
	}

	width := rect.Dx()
	height := rect.Dy()
	if width <= 0 || height <= 0 {
		return rect
	}

	centerX := (rect.Min.X + rect.Max.X) / 2
	centerY := (rect.Min.Y + rect.Max.Y) / 2
	halfWidth := width * scale / 2
	halfHeight := height * scale / 2

	return image.Rect(
		centerX-halfWidth,
		centerY-halfHeight,
		centerX+halfWidth,
		centerY+halfHeight,
	)
}

// drawTargetAndLabel draws the crosshair, velocity arrow, and speed label for
// one target.
func drawTargetAndLabel(
	frame *gocv.Mat,
	id int,
	center image.Point,
	vx, vy, speed float64,
	trackColor, arrowColor color.RGBA,
) {
	gocv.Circle(frame, center, trackCircleRadius, trackColor, 2)
	gocv.Line(frame, image.Pt(center.X-trackCrosshairArm, center.Y), image.Pt(center.X+trackCrosshairArm, center.Y), trackColor, 1)
	gocv.Line(frame, image.Pt(center.X, center.Y-trackCrosshairArm), image.Pt(center.X, center.Y+trackCrosshairArm), trackColor, 1)

	const arrowSeconds = 0.20
	end := image.Pt(
		center.X+int(vx*arrowSeconds),
		center.Y+int(vy*arrowSeconds),
	)
	gocv.Line(frame, center, end, arrowColor, 2)

	label := fmt.Sprintf("#%d  %.0f px/s", id, speed)
	gocv.PutText(frame, label, image.Pt(center.X+12, center.Y-12),
		gocv.FontHersheySimplex, 0.45, trackColor, 1)
}
