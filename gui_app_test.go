package main

import (
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadObjectListPreviewOwnsPixelsAfterMatClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frame_000001_000000000ms.jpg")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create preview fixture: %v", err)
	}
	fixture := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			fixture.Set(x, y, color.RGBA{R: 220, G: 40, B: 20, A: 255})
		}
	}
	if err := jpeg.Encode(file, fixture, nil); err != nil {
		_ = file.Close()
		t.Fatalf("encode preview fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close preview fixture: %v", err)
	}

	preview := loadObjectListPreview(trackedObjectDetail{CropPaths: []string{path}})
	r, g, b, _ := preview.At(4, 4).RGBA()
	if r < 0x8000 || g > 0x6000 || b > 0x6000 {
		t.Fatalf("preview pixels were lost after closing source Mat: r=%d g=%d b=%d", r, g, b)
	}
}

func TestLoadEventHistoryPreservesOptionalVideoOutputs(t *testing.T) {
	root := t.TempDir()
	eventDir := filepath.Join(root, "2026-09-10_120000.000")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatalf("create event directory: %v", err)
	}
	summary := EventSummary{
		EventID:       filepath.Base(eventDir),
		TrackedVideo:  "tracked.avi",
		OriginalVideo: "",
		MaskedVideo:   "",
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal event summary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "event.json"), data, 0o644); err != nil {
		t.Fatalf("write event summary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "tracked.avi"), []byte("video"), 0o644); err != nil {
		t.Fatalf("write tracked video fixture: %v", err)
	}

	entries, err := loadEventHistory(root)
	if err != nil {
		t.Fatalf("load event history: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one event, got %d", len(entries))
	}
	got := entries[0].Summary
	if got.OriginalVideo != "" || got.MaskedVideo != "" || got.TrackedVideo != "tracked.avi" {
		t.Fatalf("optional outputs were invented: %+v", got)
	}
	if path := eventReplayBasePath(entries[0]); path != filepath.Join(eventDir, "tracked.avi") {
		t.Fatalf("tracked-only event has no replay fallback: %q", path)
	}
}

func TestEventFrameIndexUsesEventRelativeVideoPosition(t *testing.T) {
	frames := []FrameMetadata{
		{SourceFrame: 224},
		{SourceFrame: 225},
		{SourceFrame: 226},
		{SourceFrame: 227},
	}
	if got := eventFrameIndexForSourceFrame(frames, 226); got != 2 {
		t.Fatalf("source frame mapped to event index %d, want 2", got)
	}
}

func TestDwarfMediaMatchesRecordingByTimestamp(t *testing.T) {
	recordingStartedAt := time.Date(2026, time.August, 21, 14, 35, 0, 0, time.Local)
	file := DwarfMediaFile{
		Path: "/DWARF3_TELE_2026-08-21-14-35-04-321.mp4",
		Name: "DWARF3_TELE_2026-08-21-14-35-04-321.mp4",
	}

	if !dwarfMediaMatchesRecording(file, dwarfCameraTele, "", recordingStartedAt) {
		t.Fatal("expected recording to match by parsed timestamp near the recording start")
	}
}

func TestDwarfStillPictureNameIncludesDatetime(t *testing.T) {
	capturedAt := time.Date(2026, time.September, 9, 14, 35, 12, 0, time.Local)
	photo := DwarfPhotoFile{FileName: "DWARF3_WIDE.jpg"}

	got := dwarfStillPictureName(photo, capturedAt)
	want := "2026-09-09_143512_DWARF3_WIDE.jpg"
	if got != want {
		t.Fatalf("still picture name = %q, want %q", got, want)
	}
}

func TestDwarfStillPictureNameUsesDeviceModificationTime(t *testing.T) {
	deviceTime := time.Date(2026, time.September, 9, 14, 36, 20, 0, time.Local)
	photo := DwarfPhotoFile{FilePath: "/DWARF3/Photos/still.jpg", ModificationTime: deviceTime.Unix()}

	got := dwarfStillPictureName(photo, time.Date(2000, time.January, 1, 0, 0, 0, 0, time.Local))
	want := "2026-09-09_143620_still.jpg"
	if got != want {
		t.Fatalf("still picture name = %q, want %q", got, want)
	}
}

func TestSelectDwarfMediaFileSkipsDownloadedAndUsesCameraFallback(t *testing.T) {
	files := []DwarfMediaFile{
		{
			Path:    "/DWARF3_TELE_2026-08-21-14-35-04-321.mp4",
			Name:    "DWARF3_TELE_2026-08-21-14-35-04-321.mp4",
			ModTime: time.Date(2026, time.August, 21, 14, 35, 4, 0, time.Local),
		},
		{
			Path:    "/DWARF3_TELE_2026-08-21-14-34-00-000.mp4",
			Name:    "DWARF3_TELE_2026-08-21-14-34-00-000.mp4",
			ModTime: time.Date(2026, time.August, 21, 14, 34, 0, 0, time.Local),
		},
	}
	downloaded := map[string]DwarfQueuedRecording{
		files[0].Path: {RemotePath: files[0].Path},
	}

	selected := selectDwarfMediaFile(files, downloaded, dwarfCameraTele, "", time.Time{})
	if selected == nil {
		t.Fatal("expected a fallback selection")
	}
	if selected.Path != files[1].Path {
		t.Fatalf("unexpected selection: %s", selected.Path)
	}
}

func TestSelectDwarfMediaFileDoesNotFallbackForNamedRecording(t *testing.T) {
	startedAt := time.Date(2026, time.August, 21, 15, 0, 0, 0, time.Local)
	files := []DwarfMediaFile{
		{
			Path:    "/DWARF3_TELE_2026-08-21-14-34-00-000.mp4",
			Name:    "DWARF3_TELE_2026-08-21-14-34-00-000.mp4",
			ModTime: startedAt.Add(-26 * time.Minute),
		},
	}

	selected := selectDwarfMediaFile(files, nil, dwarfCameraTele, "DWARF_20260821150000", startedAt)
	if selected != nil {
		t.Fatalf("selected unrelated fallback while requested recording was still absent: %+v", selected)
	}
}

func TestSelectDwarfMediaFileFallbackUsesNewestUnseenCameraFile(t *testing.T) {
	files := []DwarfMediaFile{
		{Path: "/Videos/DWARF3_TELE_2026-09-09-19-49-13-121.mp4", Name: "DWARF3_TELE_2026-09-09-19-49-13-121.mp4"},
		{Path: "/Videos/DWARF3_WIDE_2026-09-09-19-49-13-121.mp4", Name: "DWARF3_WIDE_2026-09-09-19-49-13-121.mp4"},
	}

	selected := selectDwarfMediaFile(files, nil, dwarfCameraTele, "", time.Time{})
	if selected == nil || selected.Path != files[0].Path {
		t.Fatalf("camera fallback selected %+v", selected)
	}
}

func TestFormatDwarfRecordStartReportIncludesRawAndNewFiles(t *testing.T) {
	report := DwarfRecordStartReport{
		Host:           "192.168.88.1",
		Camera:         dwarfCameraTele,
		RecordingName:  "DWARF_TEST_20260821150000",
		StartAckOK:     true,
		StartAckDetail: "start recording acknowledged",
		StartAckRaw:    `{"code":0}`,
		StopAckOK:      true,
		StopAckDetail:  "stop recording acknowledged",
		StopAckRaw:     `{"code":0}`,
		BeforeCount:    10,
		AfterCount:     11,
		NewFiles:       []string{"/DWARF3_TELE_2026-08-21-15-00-00-000.mp4"},
		WaitDuration:   4 * time.Second,
	}

	text := formatDwarfRecordStartReport(report)
	for _, want := range []string{
		"Host: 192.168.88.1",
		"Start Ack Raw: {\"code\":0}",
		"Stop Ack Raw: {\"code\":0}",
		"New Files: /DWARF3_TELE_2026-08-21-15-00-00-000.mp4",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected report to contain %q, got:\n%s", want, text)
		}
	}
}

func TestDwarfDownloadLocalPathUsesRecordingSubdirectory(t *testing.T) {
	got := dwarfDownloadLocalPath("dwarf_downloads", "DWARF_20260823160000", "/Videos/DWARF3_WIDE_2026-08-23-16-00-19-049.mp4")
	want := filepath.Join("dwarf_downloads", "DWARF_20260823160000", "DWARF3_WIDE_2026-08-23-16-00-19-049.mp4")
	if got != want {
		t.Fatalf("unexpected local path: got %s want %s", got, want)
	}
}

func TestDwarfDownloadLocalPathWithoutRecordingUsesBaseDirectory(t *testing.T) {
	got := dwarfDownloadLocalPath("dwarf_downloads", "", "/Videos/DWARF3_TELE_2026-08-23-16-00-19-049.mp4")
	want := filepath.Join("dwarf_downloads", "DWARF3_TELE_2026-08-23-16-00-19-049.mp4")
	if got != want {
		t.Fatalf("unexpected local path: got %s want %s", got, want)
	}
}

func TestDwarfDownloadLocalPathUsesQueueDirectoryDirectly(t *testing.T) {
	got := dwarfDownloadLocalPath(filepath.Join("dwarf_downloads", "2026-08-23_161622", dwarfQueueStageQueue), "DWARF_20260823161622", "/Videos/DWARF3_WIDE_2026-08-23-16-16-30-000.mp4")
	want := filepath.Join("dwarf_downloads", "2026-08-23_161622", dwarfQueueStageQueue, "DWARF3_WIDE_2026-08-23-16-16-30-000.mp4")
	if got != want {
		t.Fatalf("unexpected queue path: got %s want %s", got, want)
	}
}

func TestDwarfCaptureSessionDirUsesStartTime(t *testing.T) {
	startedAt := time.Date(2026, time.August, 23, 16, 16, 22, 0, time.Local)
	got := dwarfCaptureSessionDir("dwarf_downloads", startedAt)
	want := filepath.Join("dwarf_downloads", "2026-08-23_161622")
	if got != want {
		t.Fatalf("unexpected session dir: got %s want %s", got, want)
	}
}

func TestMoveDwarfQueuedRecordingToStage(t *testing.T) {
	root := t.TempDir()
	queueDir := filepath.Join(root, dwarfQueueStageQueue)
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatalf("create queue dir: %v", err)
	}
	initialPath := filepath.Join(queueDir, "clip.mp4")
	if err := os.WriteFile(initialPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	recording := DwarfQueuedRecording{
		Camera:     dwarfCameraWide,
		RemotePath: "/Videos/clip.mp4",
		LocalPath:  initialPath,
	}
	if err := writeDwarfRecordingMetadata(recording); err != nil {
		t.Fatalf("write recording metadata: %v", err)
	}

	moved, err := moveDwarfQueuedRecordingToStage(recording, dwarfQueueStageUnderProcessing)
	if err != nil {
		t.Fatalf("move to under processing: %v", err)
	}
	wantUnderProcessing := filepath.Join(root, dwarfQueueStageUnderProcessing, "clip.mp4")
	if moved.LocalPath != wantUnderProcessing {
		t.Fatalf("unexpected under processing path: got %s want %s", moved.LocalPath, wantUnderProcessing)
	}
	if _, err := os.Stat(wantUnderProcessing); err != nil {
		t.Fatalf("expected moved file to exist: %v", err)
	}
	if _, err := os.Stat(wantUnderProcessing + ".metadata.json"); err != nil {
		t.Fatalf("expected moved metadata to exist: %v", err)
	}

	moved, err = moveDwarfQueuedRecordingToStage(moved, dwarfQueueStageProcessed)
	if err != nil {
		t.Fatalf("move to processed: %v", err)
	}
	wantProcessed := filepath.Join(root, dwarfQueueStageProcessed, "clip.mp4")
	if moved.LocalPath != wantProcessed {
		t.Fatalf("unexpected processed path: got %s want %s", moved.LocalPath, wantProcessed)
	}
	if _, err := os.Stat(wantProcessed); err != nil {
		t.Fatalf("expected processed file to exist: %v", err)
	}
	if _, err := os.Stat(wantProcessed + ".metadata.json"); err != nil {
		t.Fatalf("expected processed metadata to exist: %v", err)
	}
}

func TestDwarfQueuedRecordingSessionDirHandlesUnstagedDownload(t *testing.T) {
	path := filepath.Join("dwarf_downloads", "clip.mp4")
	if got, want := dwarfQueuedRecordingSessionDir(path), "dwarf_downloads"; got != want {
		t.Fatalf("unexpected session directory: got %s want %s", got, want)
	}
}

func TestRecoverDwarfQueuedRecordingsReturnsUnderProcessingFileToQueue(t *testing.T) {
	downloadDir := t.TempDir()
	sessionDir := filepath.Join(downloadDir, "2026-09-08_120000")
	underProcessingDir := filepath.Join(sessionDir, dwarfQueueStageUnderProcessing)
	if err := os.MkdirAll(underProcessingDir, 0o755); err != nil {
		t.Fatalf("create under-processing directory: %v", err)
	}
	videoPath := filepath.Join(underProcessingDir, "DWARF3_WIDE_2026-09-08-12-00-00-000.mp4")
	writeTestMP4(t, videoPath, "ftyp", "mdat", "moov")
	latitude := 59.3293
	recording := DwarfQueuedRecording{
		Camera:       dwarfCameraWide,
		RemotePath:   "/Videos/" + filepath.Base(videoPath),
		RemoteName:   filepath.Base(videoPath),
		LocalPath:    videoPath,
		DownloadedAt: time.Date(2026, time.September, 8, 12, 1, 0, 0, time.UTC),
		Capture:      CaptureMetadata{Source: "DWARF 3", Camera: dwarfCameraWide, Latitude: &latitude},
	}
	if err := writeDwarfRecordingMetadata(recording); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	recovered, err := recoverDwarfQueuedRecordings(downloadDir)
	if err != nil {
		t.Fatalf("recover queue: %v", err)
	}
	if len(recovered) != 1 {
		t.Fatalf("expected one recovered recording, got %d: %+v", len(recovered), recovered)
	}
	wantPath := filepath.Join(sessionDir, dwarfQueueStageQueue, filepath.Base(videoPath))
	if recovered[0].LocalPath != wantPath {
		t.Fatalf("unexpected recovered path: got %s want %s", recovered[0].LocalPath, wantPath)
	}
	if recovered[0].Capture.Latitude == nil || *recovered[0].Capture.Latitude != latitude {
		t.Fatalf("capture metadata was not recovered: %+v", recovered[0].Capture)
	}
	stored, err := readDwarfRecordingMetadata(wantPath)
	if err != nil {
		t.Fatalf("read recovered metadata: %v", err)
	}
	if stored.LocalPath != wantPath {
		t.Fatalf("metadata retained stale path: got %s want %s", stored.LocalPath, wantPath)
	}
	if _, err := os.Stat(videoPath); !os.IsNotExist(err) {
		t.Fatalf("under-processing video still exists, stat error=%v", err)
	}
}

func TestRecoverDwarfQueuedRecordingsQuarantinesDamagedMP4(t *testing.T) {
	downloadDir := t.TempDir()
	sessionDir := filepath.Join(downloadDir, "2026-09-09_180000")
	queueDir := filepath.Join(sessionDir, dwarfQueueStageQueue)
	damagedPath := filepath.Join(queueDir, "damaged.mp4")
	completePath := filepath.Join(queueDir, "complete.mp4")
	writeTestMP4(t, damagedPath, "ftyp", "mdat")
	writeTestMP4(t, completePath, "ftyp", "mdat", "moov")
	for _, recording := range []DwarfQueuedRecording{
		{LocalPath: damagedPath, RemotePath: "/Videos/damaged.mp4", DownloadedAt: time.Now().Add(-time.Minute)},
		{LocalPath: completePath, RemotePath: "/Videos/complete.mp4", DownloadedAt: time.Now()},
	} {
		if err := writeDwarfRecordingMetadata(recording); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
	}

	recovered, err := recoverDwarfQueuedRecordings(downloadDir)
	if err != nil {
		t.Fatalf("recover queue: %v", err)
	}
	if len(recovered) != 1 || recovered[0].LocalPath != completePath {
		t.Fatalf("unexpected recovered recordings: %+v", recovered)
	}
	failedPath := filepath.Join(sessionDir, dwarfQueueStageFailed, filepath.Base(damagedPath))
	if _, err := os.Stat(failedPath); err != nil {
		t.Fatalf("damaged recording was not quarantined: %v", err)
	}
	failed, err := readDwarfRecordingMetadata(failedPath)
	if err != nil {
		t.Fatalf("read failed metadata: %v", err)
	}
	if !strings.Contains(failed.FailureReason, "moov") {
		t.Fatalf("unexpected failure reason: %q", failed.FailureReason)
	}
}

func TestFailedDwarfRecordingIsRequeued(t *testing.T) {
	root := t.TempDir()
	underProcessingDir := filepath.Join(root, dwarfQueueStageUnderProcessing)
	if err := os.MkdirAll(underProcessingDir, 0o755); err != nil {
		t.Fatalf("create under-processing directory: %v", err)
	}
	videoPath := filepath.Join(underProcessingDir, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	recording := DwarfQueuedRecording{
		RemotePath: "/Videos/clip.mp4",
		RemoteName: "clip.mp4",
		LocalPath:  videoPath,
	}
	if err := writeDwarfRecordingMetadata(recording); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	ui := &trackerApp{dwarfDownloadedFiles: make(map[string]DwarfQueuedRecording)}
	processErr := errors.New("processing failed")
	if err := ui.requeueFailedDwarfRecording(recording, processErr); !errors.Is(err, processErr) {
		t.Fatalf("processing error was not preserved: %v", err)
	}
	if len(ui.dwarfQueuedFiles) != 1 {
		t.Fatalf("expected failed recording back in queue, got %+v", ui.dwarfQueuedFiles)
	}
	wantPath := filepath.Join(root, dwarfQueueStageQueue, "clip.mp4")
	if ui.dwarfQueuedFiles[0].LocalPath != wantPath {
		t.Fatalf("unexpected requeued path: got %s want %s", ui.dwarfQueuedFiles[0].LocalPath, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("requeued video is missing: %v", err)
	}
}

func TestMediaHandlerServesOnlyExplicitlyRegisteredFiles(t *testing.T) {
	root := t.TempDir()
	secretPath := filepath.Join(root, "secret.txt")
	mediaPath := filepath.Join(root, "preview.jpg")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.WriteFile(mediaPath, []byte("media"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}

	ui := &trackerApp{
		projectRoot:        root,
		mediaServerBaseURL: "http://media.test/files/",
		mediaFiles:         make(map[string]string),
	}
	mux := http.NewServeMux()
	mux.Handle("/files/", http.StripPrefix("/files/", http.HandlerFunc(ui.serveProjectFile)))

	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "http://media.test/files/secret.txt", nil))
	if unauthorized.Code != http.StatusNotFound {
		t.Fatalf("unregistered project file returned status %d", unauthorized.Code)
	}

	mediaURL := ui.projectFileURL(mediaPath)
	if mediaURL == "" {
		t.Fatal("expected registered media URL")
	}
	if strings.Contains(mediaURL, filepath.Base(mediaPath)) {
		t.Fatalf("media URL disclosed the project path: %s", mediaURL)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, mediaURL, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("registered media returned status %d", response.Code)
	}
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatalf("read media response: %v", err)
	}
	if string(body) != "media" {
		t.Fatalf("unexpected media response: %q", body)
	}
}

func TestMediaServerBindsOnlyToLoopback(t *testing.T) {
	ip := net.ParseIP(mediaServerHost)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("media server bind address is not loopback-only: %q", mediaServerHost)
	}
}

func TestFloatParsersRejectNonFiniteValues(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		if _, err := parseRequiredFloat(value, "Value"); err == nil {
			t.Errorf("parseRequiredFloat accepted %q", value)
		}
		if _, ok := parseOptionalFloat(value); ok {
			t.Errorf("parseOptionalFloat accepted %q", value)
		}
	}
}
