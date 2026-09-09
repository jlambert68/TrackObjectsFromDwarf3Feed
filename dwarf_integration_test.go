package main

import (
	"bytes"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const dwarfIntegrationEnv = "RUN_DWARF_INTEGRATION"

func requireDwarfIntegrationController(t *testing.T) DwarfController {
	t.Helper()

	if os.Getenv(dwarfIntegrationEnv) != "1" {
		t.Skip("set RUN_DWARF_INTEGRATION=1 to run live DWARF integration tests")
	}

	host := strings.TrimSpace(os.Getenv("DWARF_HOST"))
	if host == "" {
		t.Skip("set DWARF_HOST to the live DWARF IP to run integration tests")
	}

	controller := DefaultDwarfController()
	controller.Host = host

	if portText := strings.TrimSpace(os.Getenv("DWARF_WS_PORT")); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil {
			t.Fatalf("invalid DWARF_WS_PORT %q: %v", portText, err)
		}
		controller.WSPort = port
	}
	if portText := strings.TrimSpace(os.Getenv("DWARF_FTP_PORT")); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil {
			t.Fatalf("invalid DWARF_FTP_PORT %q: %v", portText, err)
		}
		controller.FTPPort = port
	}
	if timeoutText := strings.TrimSpace(os.Getenv("DWARF_TIMEOUT_SECONDS")); timeoutText != "" {
		seconds, err := strconv.Atoi(timeoutText)
		if err != nil {
			t.Fatalf("invalid DWARF_TIMEOUT_SECONDS %q: %v", timeoutText, err)
		}
		controller.Timeout = time.Duration(seconds) * time.Second
	}
	if os.Getenv("DWARF_DEBUG_WS") == "1" {
		controller.DebugWS = true
	}

	return controller
}

func dwarfIntegrationCamera() string {
	camera := strings.ToLower(strings.TrimSpace(os.Getenv("DWARF_CAMERA")))
	switch camera {
	case dwarfCameraWide:
		return dwarfCameraWide
	default:
		return dwarfCameraTele
	}
}

func TestDwarfIntegrationConnections(t *testing.T) {
	controller := requireDwarfIntegrationController(t)

	report := controller.TestConnections()
	if !report.WebSocketOK {
		t.Fatalf("websocket connection failed: %s", report.WebSocketDetail)
	}
	if !report.FTPOK {
		t.Fatalf("ftp connection failed: %s", report.FTPDetail)
	}
}

func TestDwarfIntegrationPhotoCaptureAlbumAndDownload(t *testing.T) {
	controller := requireDwarfIntegrationController(t)
	camera := dwarfIntegrationCamera()

	before, err := controller.ListPhotoFiles()
	if err != nil {
		t.Fatalf("list photos before capture: %v", err)
	}

	photo, err := controller.TakePhoto(camera)
	if err != nil {
		t.Fatalf("take %s photo: %v", camera, err)
	}
	if photo.FilePath == "" || photo.FileName == "" {
		t.Fatalf("captured photo has incomplete album metadata: %+v", photo)
	}
	if photo.CameraID != dwarfCameraID(camera) {
		t.Logf("firmware reported camId=%d for requested %s camera", photo.CameraID, camera)
	}
	if containsDwarfPhotoPath(before, photo.FilePath) {
		t.Fatalf("TakePhoto returned an album item that existed before capture: %s", photo.FilePath)
	}

	after, err := controller.ListPhotoFiles()
	if err != nil {
		t.Fatalf("list photos after capture: %v", err)
	}
	if !containsDwarfPhotoPath(after, photo.FilePath) {
		t.Fatalf("captured photo is absent from album listing: %s", photo.FilePath)
	}

	localPath := filepath.Join(t.TempDir(), dwarfStillPictureName(photo, time.Now()))
	if err := controller.DownloadPhoto(photo, localPath); err != nil {
		t.Fatalf("download photo %s: %v", photo.FilePath, err)
	}
	assertDownloadedFile(t, localPath, photo.FileSize)
	file, err := os.Open(localPath)
	if err != nil {
		t.Fatalf("open downloaded photo: %v", err)
	}
	defer file.Close()
	config, format, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("decode downloaded photo: %v", err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		t.Fatalf("downloaded photo has invalid dimensions %dx%d", config.Width, config.Height)
	}
	t.Logf("captured and downloaded %s photo: %s (%s, %dx%d)", camera, localPath, format, config.Width, config.Height)
}

func TestDwarfIntegrationRecordStart(t *testing.T) {
	controller := requireDwarfIntegrationController(t)
	camera := dwarfIntegrationCamera()

	wait := 4 * time.Second
	if waitText := strings.TrimSpace(os.Getenv("DWARF_RECORD_WAIT_SECONDS")); waitText != "" {
		seconds, err := strconv.Atoi(waitText)
		if err != nil {
			t.Fatalf("invalid DWARF_RECORD_WAIT_SECONDS %q: %v", waitText, err)
		}
		wait = time.Duration(seconds) * time.Second
	}

	report := controller.TestRecordStart(camera, wait)
	if !report.StartAckOK {
		t.Fatalf("start recording failed: %s\nreport:\n%s", report.StartAckDetail, formatDwarfRecordStartReport(report))
	}
	if !report.StopAckOK {
		t.Fatalf("stop recording failed: %s\nreport:\n%s", report.StopAckDetail, formatDwarfRecordStartReport(report))
	}
	if report.ListAfterErr != "" {
		t.Fatalf("post-record ftp listing failed: %s\nreport:\n%s", report.ListAfterErr, formatDwarfRecordStartReport(report))
	}
	if len(report.NewFiles) == 0 {
		t.Fatalf("record start produced no new files; report:\n%s", formatDwarfRecordStartReport(report))
	}

	files, err := controller.ListVideoFiles()
	if err != nil {
		t.Fatalf("list video files after recording: %v", err)
	}
	byPath := make(map[string]DwarfMediaFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	for _, remotePath := range report.NewFiles {
		media, ok := byPath[remotePath]
		if !ok {
			t.Fatalf("new recording is absent from FTP listing: %s", remotePath)
		}
		localPath := filepath.Join(t.TempDir(), filepath.Base(media.Name))
		if err := downloadValidatedDwarfVideo(controller, remotePath, localPath); err != nil {
			t.Fatalf("download recorded video %s: %v", remotePath, err)
		}
		if err := validateDwarfVideoFile(localPath); err != nil {
			t.Fatalf("downloaded recording is incomplete: %v", err)
		}
		refreshedFiles, err := controller.ListVideoFiles()
		if err != nil {
			t.Fatalf("refresh video size after download: %v", err)
		}
		refreshedByPath := make(map[string]DwarfMediaFile, len(refreshedFiles))
		for _, refreshed := range refreshedFiles {
			refreshedByPath[refreshed.Path] = refreshed
		}
		refreshed, ok := refreshedByPath[remotePath]
		if !ok {
			t.Fatalf("downloaded recording disappeared from FTP listing: %s", remotePath)
		}
		assertDownloadedFile(t, localPath, refreshed.Size)

		if os.Getenv("DWARF_DELETE_TEST_MEDIA") == "1" {
			if err := controller.DeleteFile(remotePath); err != nil {
				t.Fatalf("delete test recording %s: %v", remotePath, err)
			}
			remaining, err := controller.ListVideoFiles()
			if err != nil {
				t.Fatalf("list videos after deleting test recording: %v", err)
			}
			if containsDwarfMediaPath(remaining, remotePath) {
				t.Fatalf("deleted test recording remains in FTP listing: %s", remotePath)
			}
		}
	}
}

func TestDwarfIntegrationRawWebSocketCommand(t *testing.T) {
	controller := requireDwarfIntegrationController(t)
	payload := strings.TrimSpace(os.Getenv("DWARF_RAW_WS_PAYLOAD"))
	if payload == "" {
		t.Skip("set DWARF_RAW_WS_PAYLOAD to exercise the raw WebSocket command interface")
	}
	report := controller.SendRawWSCommand(payload, os.Getenv("DWARF_RAW_WS_ALLOW_TIMEOUT") == "1")
	if report.Err != "" {
		t.Fatalf("raw WebSocket command failed: %s; response=%s", report.Err, report.ResponseRaw)
	}
	if strings.TrimSpace(report.ResponseRaw) == "" {
		t.Fatal("raw WebSocket command returned an empty response")
	}
}

func containsDwarfPhotoPath(files []DwarfPhotoFile, path string) bool {
	for _, file := range files {
		if file.FilePath == path {
			return true
		}
	}
	return false
}

func containsDwarfMediaPath(files []DwarfMediaFile, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}

func assertDownloadedFile(t *testing.T, path string, deviceSize int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		t.Fatalf("downloaded file is empty: %s", path)
	}
	if deviceSize > 0 && int64(len(data)) != deviceSize {
		t.Fatalf("downloaded size = %d, device reports %d for %s", len(data), deviceSize, path)
	}
}
