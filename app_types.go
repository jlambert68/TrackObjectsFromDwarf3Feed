package main

import (
	"image"
	"image/color"
	"time"

	"gocv.io/x/gocv"
)

// Detection and tracking thresholds. These values define the blob size window,
// velocity bands, and the amount of track persistence bookkeeping kept after
// detections temporarily disappear.
const (
	minArea = 6.0
	maxArea = 15000.0

	slowMinSpeed = 10.0
	minSpeed     = 40.0

	maxMatchDistance = 100.0
	minHits          = 2

	maxMissedFrames     = 8
	blurSize            = 5
	foregroundThreshold = 200.0

	preEventDuration  = 5 * time.Second
	postEventDuration = 5 * time.Second

	mog2History      = 500
	mog2VarThreshold = 16.0

	trackBoxScale     = 4
	trackCircleRadius = 16
	trackCrosshairArm = 24

	trackingROIHeightFraction = 0.90
)

// Overlay colors for the live view and the tracked event exports.
var (
	fastTrackColor = color.RGBA{R: 255, G: 255}
	fastArrowColor = color.RGBA{R: 255, G: 128}
	slowTrackColor = color.RGBA{R: 255}
	slowArrowColor = color.RGBA{R: 255, A: 255}
	textColor      = color.RGBA{G: 255}
	warnColor      = color.RGBA{R: 255, G: 255}
	roiColor       = color.RGBA{G: 255, A: 255}
)

// Detection is one foreground blob extracted from the motion mask for a frame.
type Detection struct {
	Rect   image.Rectangle
	Center image.Point
	Area   float64
}

// Track is the in-memory state for one object candidate as it moves across
// successive frames.
type Track struct {
	ID int

	Rect         image.Rectangle
	Position     image.Point
	PrevPosition image.Point

	VX    float64
	VY    float64
	Speed float64

	LastUpdate time.Time

	Hits   int
	Missed int

	Trail []image.Point
}

// TrackMetadata is the persisted JSON representation of one accepted track for
// a frame.
type TrailPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type TrackMetadata struct {
	ID   int    `json:"id"`
	Type string `json:"type"`

	X int `json:"x"`
	Y int `json:"y"`

	BoxX      int `json:"box_x"`
	BoxY      int `json:"box_y"`
	BoxWidth  int `json:"box_width"`
	BoxHeight int `json:"box_height"`

	VX    float64      `json:"vx"`
	VY    float64      `json:"vy"`
	Speed float64      `json:"speed_px_s"`
	Trail []TrailPoint `json:"trail"`
}

const (
	trackTypeSlow = "slow"
	trackTypeFast = "fast"
)

// FrameMetadata stores the tracking result for one source frame.
type FrameMetadata struct {
	SourceFrame int             `json:"source_frame"`
	TimeUnixNS  int64           `json:"time_unix_ns"`
	TimeMS      int64           `json:"time_ms"`
	Tracks      []TrackMetadata `json:"tracks"`
}

// EventSummary is the short per-event manifest written beside the captured
// videos and full tracking JSON.
type EventSummary struct {
	EventID           string    `json:"event_id"`
	StartedAt         time.Time `json:"started_at"`
	EndedAt           time.Time `json:"ended_at"`
	DurationSeconds   float64   `json:"duration_seconds"`
	Width             int       `json:"width"`
	Height            int       `json:"height"`
	FPS               float64   `json:"fps"`
	Frames            int       `json:"frames"`
	UniqueObjects     int       `json:"unique_objects"`
	HighestSpeedPxSec float64   `json:"highest_speed_px_s"`
	OriginalVideo     string    `json:"original_video"`
	TrackedVideo      string    `json:"tracked_video"`
	TrackingMetadata  string    `json:"tracking_metadata"`
}

// EventMetadata contains the full timeline for a recorded event.
type EventMetadata struct {
	EventID   string          `json:"event_id"`
	StartedAt time.Time       `json:"started_at"`
	FPS       float64         `json:"fps"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Frames    []FrameMetadata `json:"frames"`
}

// BufferedFrame is one pre-event frame kept in RAM so recording can include
// context before the first fast object is detected.
type BufferedFrame struct {
	Image     gocv.Mat
	Timestamp time.Time
	Metadata  FrameMetadata
}

// EventRecorder owns the output files and the metadata accumulated while one
// event is being written to disk.
type EventRecorder struct {
	RawWriter     *gocv.VideoWriter
	TrackedWriter *gocv.VideoWriter

	Directory string
	StartedAt time.Time
	FPS       float64
	Width     int
	Height    int

	Metadata EventMetadata
	SeenIDs  map[int]struct{}

	HighestSpeed float64
}

// Source mode names used by flags and the optional startup prompt.
const (
	sourceDwarf = "dwarf"
	sourceVideo = "video"
)
