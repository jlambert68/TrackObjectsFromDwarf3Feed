package main

import (
	"os"
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
}
