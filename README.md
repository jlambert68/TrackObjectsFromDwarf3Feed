# DWARF 3 Fast-Object Event Tracker

Reads the DWARF 3 live RTSP stream, detects fast-moving blobs, tracks multiple objects,
and automatically records event folders containing:

- `original.avi` — untouched frames from the RTSP stream
- `tracked.avi` — the same frames with boxes, IDs, trails, velocity arrows and speed
- `masked.avi` — the cleaned binary motion mask used for detection
- `track_crops/` — per-object close-up snapshots grouped by tracked object ID
- `track_names.json` — user-editable names for tracked objects in that event
- `tracking.json` — per-frame coordinates, bounding boxes and velocity metadata
- `event.json` — summary for the event

The program does not continuously record video. It keeps about 5 seconds of clean
frames in RAM. When a valid fast track appears it writes that prebuffer, records
while objects are present, and continues for 5 seconds after the last tracked object.

## Run

Make sure OpenCV compatible with GoCV v0.43.0 is installed.

```bash
go mod tidy
go run .
```

Default DWARF 3 telephoto stream:

```text
rtsp://192.168.88.1/ch0/stream0
```

Custom URL/output directory:

```bash
go run . -url rtsp://192.168.88.1/ch0/stream0 -out events
```

Hide the debug motion-mask window:

```bash
go run . -mask=false
```

If the RTSP backend does not report FPS, the fallback is 30 FPS:

```bash
go run . -fps 30
```

## Output

```text
events/
  2026-08-11_151000.123/
    original.avi
    tracked.avi
    masked.avi
    track_crops/
      object_0001/
        frame_000123_000004321ms.jpg
    track_names.json
    tracking.json
    event.json
```

## Parameters worth tuning

They are constants near the top of `main.go`:

- `minArea`: lower it for very tiny targets; raise it to reject speckle/noise.
- `minSpeed`: minimum pixels/second before a track triggers recording.
- `maxMatchDistance`: raise it if very fast targets repeatedly receive new IDs.
- `preEventDuration`: amount retained before detection.
- `postEventDuration`: amount retained after the last valid track disappears.

## Important telescope limitation

This first version uses background subtraction. If the telescope/image itself moves,
many stars/background features can temporarily look like moving objects. The next
major improvement for sky use is global-motion compensation/stabilization before
motion detection.

## Source comments

`main.go` is extensively commented around the capture pipeline, detection thresholds, tracking association, RAM ownership, event triggering, dual-video recording, and JSON metadata.

## Input source

The program can process either the DWARF 3 live RTSP feed or a stored video file.

Live DWARF 3 feed:

```bash
go run . -source=live
```

Custom DWARF 3 RTSP URL:

```bash
go run . -source=live -url rtsp://192.168.88.1/ch0/stream0
```

Stored video file:

```bash
go run . -source=file -file /path/to/video.mp4
```

The same detection, tracking, prebuffer, dual-video recording, and JSON metadata
pipeline is used for both input types.
