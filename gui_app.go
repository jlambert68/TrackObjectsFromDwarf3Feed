package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dwarf3-event-tracker/internal/nostrutil"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/nbd-wtf/go-nostr"
	"gocv.io/x/gocv"
)

type trackerApp struct {
	window fyne.Window

	sourceRadio              *widget.RadioGroup
	urlEntry                 *widget.Entry
	fileEntry                *widget.Entry
	outputEntry              *widget.Entry
	fpsEntry                 *widget.Entry
	dwarfHostEntry           *widget.Entry
	dwarfCameraSelect        *widget.Select
	dwarfSegmentEntry        *widget.Entry
	dwarfDownloadDirEntry    *widget.Entry
	dwarfDeleteCheck         *widget.Check
	dwarfDebugWSCheck        *widget.Check
	showMask                 *widget.Check
	dateFilter               *widget.Entry
	objectFilter             *widget.Entry
	speedFilter              *widget.Entry
	sortSelect               *widget.Select
	presetSelect             *widget.Select
	profileSelect            *widget.Select
	presetNameEntry          *widget.Entry
	minAreaEntry             *widget.Entry
	maxAreaEntry             *widget.Entry
	slowSpeedEntry           *widget.Entry
	minSpeedEntry            *widget.Entry
	matchDistanceEntry       *widget.Entry
	minHitsEntry             *widget.Entry
	blurSizeEntry            *widget.Entry
	foregroundThresholdEntry *widget.Entry
	preEventEntry            *widget.Entry
	postEventEntry           *widget.Entry
	rawSegmentEntry          *widget.Entry
	rawSegmentOverlapEntry   *widget.Entry
	mog2HistoryEntry         *widget.Entry
	mog2VarThresholdEntry    *widget.Entry
	roiHeightEntry           *widget.Entry
	nostrEnableCheck         *widget.Check
	nostrRelayEntry          *widget.Entry
	nostrSecretEntry         *widget.Entry
	nostrMinDistanceEntry    *widget.Entry
	nostrTestMessageEntry    *widget.Entry

	fileButton               *widget.Button
	outputButton             *widget.Button
	dwarfDownloadButton      *widget.Button
	startButton              *widget.Button
	stopButton               *widget.Button
	startDwarfButton         *widget.Button
	stopDwarfButton          *widget.Button
	fetchDwarfButton         *widget.Button
	testDwarfButton          *widget.Button
	testDwarfRecordButton    *widget.Button
	rawDwarfWSButton         *widget.Button
	sessionProbeButton       *widget.Button
	resetTrackingButton      *widget.Button
	importTrackingButton     *widget.Button
	exportTrackingButton     *widget.Button
	savePresetButton         *widget.Button
	applyPresetButton        *widget.Button
	deletePresetButton       *widget.Button
	refreshButton            *widget.Button
	openTrackedButton        *widget.Button
	openOriginalButton       *widget.Button
	restoreSettingsButton    *widget.Button
	watchObjectButton        *widget.Button
	showFinalPositionsButton *widget.Button
	saveObjectNameButton     *widget.Button
	playPauseButton          *widget.Button
	sendNostrTestButton      *widget.Button

	videoImage     *canvas.Image
	maskImage      *canvas.Image
	objectImage    *canvas.Image
	objectMapImage *canvas.Image
	tabs           *container.AppTabs
	playbackSlider *widget.Slider

	statusLabel                *widget.Label
	eventLabel                 *widget.Label
	mediaServerLabel           *widget.Label
	dwarfStatusLabel           *widget.Label
	dwarfQueueLabel            *widget.Label
	playbackLabel              *widget.Label
	historyList                *widget.List
	historyInfo                *widget.Label
	historyDetail              *widget.Entry
	objectList                 *widget.List
	objectSearchEntry          *widget.Entry
	objectName                 *widget.Entry
	objectDetail               *widget.Entry
	objectMapSkipEntry         *widget.Entry
	finalPositionDistanceEntry *widget.Entry
	finalPositionSlider        *widget.Slider

	dwarfRawWSPayload        string
	dwarfSessionProbePayload string

	allHistoryEntries []eventHistoryEntry
	historyEntries    []eventHistoryEntry
	filteredObjectIDs []int
	selectedHistory   int
	selectedObjectID  int
	currentDetail     *eventHistoryDetail
	presets           map[string]TrackingSettings

	mu                      sync.Mutex
	running                 bool
	stopCh                  chan struct{}
	dwarfCaptureRunning     bool
	dwarfCaptureStopCh      chan struct{}
	dwarfQueuedFiles        []DwarfQueuedRecording
	dwarfDownloadedFiles    map[string]DwarfQueuedRecording
	dwarfQueueProcessing    bool
	playbackActive          bool
	playbackPaused          bool
	playbackSeekFrame       int
	playbackCurrentFrame    int
	playbackTotalFrames     int
	playbackFPS             float64
	updatingPlaybackUI      bool
	updatingFinalPositionUI bool
	activePlaybackOverlay   *playbackOverlay
	pendingRunStatus        string
	pendingRunError         error
	projectRoot             string
	mediaHTTPServer         *http.Server
	mediaHTTPListener       net.Listener
	mediaHTTPSServer        *http.Server
	mediaHTTPSListener      net.Listener
	mediaHTTPBaseURL        string
	mediaServerBaseURL      string
	mediaTLSCertPath        string
	mediaTLSKeyPath         string
}

func (ui *trackerApp) showError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
	dialog.ShowError(err, ui.window)
}

func (ui *trackerApp) showInfo(title, message string) {
	fmt.Fprintf(os.Stdout, "INFO [%s]: %s\n", title, message)
	dialog.ShowInformation(title, message, ui.window)
}

func (ui *trackerApp) startMediaServer() error {
	if ui.mediaHTTPServer != nil || ui.mediaHTTPSServer != nil {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle("/files/", http.StripPrefix("/files/", http.HandlerFunc(ui.serveProjectFile)))

	var httpErr error
	for port := mediaServerPortStart; port <= mediaServerPortEnd; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", mediaServerHost, port))
		if err == nil {
			ui.mediaHTTPListener = listener
			ui.mediaHTTPBaseURL = fmt.Sprintf("http://%s:%d/files/", mediaServerPublicHost, port)
			ui.mediaHTTPServer = &http.Server{Handler: mux}
			break
		}
		httpErr = err
	}
	if ui.mediaHTTPListener == nil {
		return fmt.Errorf("listen on %s:%d-%d: %w", mediaServerHost, mediaServerPortStart, mediaServerPortEnd, httpErr)
	}

	go func() {
		if serveErr := ui.mediaHTTPServer.Serve(ui.mediaHTTPListener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "media http server failed: %v\n", serveErr)
			fyne.Do(func() {
				ui.mediaServerLabel.SetText("Media HTTP: failed")
			})
		}
	}()

	if certErr := ui.ensureMediaTLSCertificate(); certErr == nil {
		var httpsErr error
		for port := mediaServerTLSPortStart; port <= mediaServerTLSPortEnd; port++ {
			listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", mediaServerHost, port))
			if err == nil {
				cert, loadErr := tls.LoadX509KeyPair(ui.mediaTLSCertPath, ui.mediaTLSKeyPath)
				if loadErr != nil {
					_ = listener.Close()
					return fmt.Errorf("load media tls certificate: %w", loadErr)
				}
				tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
				ui.mediaHTTPSListener = tls.NewListener(listener, tlsConfig)
				ui.mediaHTTPSServer = &http.Server{Handler: mux}
				ui.mediaServerBaseURL = fmt.Sprintf("https://%s:%d/files/", mediaServerPublicHost, port)
				break
			}
			httpsErr = err
		}
		if ui.mediaHTTPSListener != nil {
			go func() {
				if serveErr := ui.mediaHTTPSServer.Serve(ui.mediaHTTPSListener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
					fmt.Fprintf(os.Stderr, "media https server failed: %v\n", serveErr)
					fyne.Do(func() {
						ui.mediaServerLabel.SetText(fmt.Sprintf("Media HTTP: %s | HTTPS: failed", ui.mediaHTTPBaseURL))
					})
				}
			}()
			ui.mediaServerLabel.SetText(fmt.Sprintf("Media HTTP: %s | HTTPS: %s", ui.mediaHTTPBaseURL, ui.mediaServerBaseURL))
		} else {
			ui.mediaServerBaseURL = ui.mediaHTTPBaseURL
			ui.mediaServerLabel.SetText(fmt.Sprintf("Media HTTP: %s | HTTPS unavailable: %v", ui.mediaHTTPBaseURL, httpsErr))
		}
	} else {
		ui.mediaServerBaseURL = ui.mediaHTTPBaseURL
		ui.mediaServerLabel.SetText(fmt.Sprintf("Media HTTP: %s | HTTPS unavailable: %v", ui.mediaHTTPBaseURL, certErr))
	}

	if ui.mediaServerBaseURL == "" {
		ui.mediaServerBaseURL = ui.mediaHTTPBaseURL
	}

	return nil
}

func (ui *trackerApp) ensureMediaTLSCertificate() error {
	if ui.projectRoot == "" {
		return errors.New("missing project root")
	}
	certDir := filepath.Join(ui.projectRoot, ".media_tls")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return fmt.Errorf("create tls directory: %w", err)
	}
	ui.mediaTLSCertPath = filepath.Join(certDir, "cert.pem")
	ui.mediaTLSKeyPath = filepath.Join(certDir, "key.pem")
	if _, certErr := os.Stat(ui.mediaTLSCertPath); certErr == nil {
		if _, keyErr := os.Stat(ui.mediaTLSKeyPath); keyErr == nil {
			return nil
		}
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate private key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate certificate serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   mediaServerPublicHost,
			Organization: []string{"TrackObjectInDwarfLifvFeed_v1"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	if ip := net.ParseIP(mediaServerPublicHost); ip != nil {
		template.IPAddresses = append(template.IPAddresses, ip)
	}
	if localhostIP := net.ParseIP("127.0.0.1"); localhostIP != nil {
		template.IPAddresses = append(template.IPAddresses, localhostIP)
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("create self-signed certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	if err := os.WriteFile(ui.mediaTLSCertPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("write certificate: %w", err)
	}
	if err := os.WriteFile(ui.mediaTLSKeyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	return nil
}

func (ui *trackerApp) stopMediaServer() {
	if ui.mediaHTTPServer != nil {
		_ = ui.mediaHTTPServer.Close()
		ui.mediaHTTPServer = nil
		ui.mediaHTTPListener = nil
	}
	if ui.mediaHTTPSServer != nil {
		_ = ui.mediaHTTPSServer.Close()
		ui.mediaHTTPSServer = nil
		ui.mediaHTTPSListener = nil
	}
}

func (ui *trackerApp) serveProjectFile(w http.ResponseWriter, r *http.Request) {
	requestPath := strings.TrimPrefix(r.URL.Path, "/")
	cleanPath := filepath.Clean(requestPath)
	if cleanPath == "." {
		http.NotFound(w, r)
		return
	}

	fullPath := filepath.Join(ui.projectRoot, cleanPath)
	absRoot, err := filepath.Abs(ui.projectRoot)
	if err != nil {
		http.Error(w, "resolve project root", http.StatusInternalServerError)
		return
	}
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		http.Error(w, "resolve file path", http.StatusBadRequest)
		return
	}
	if absPath != absRoot && !strings.HasPrefix(absPath, absRoot+string(os.PathSeparator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "stat file", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.NotFound(w, r)
		return
	}

	http.ServeFile(w, r, absPath)
}

func (ui *trackerApp) projectFileURL(path string) string {
	if ui.mediaServerBaseURL == "" || path == "" {
		return ""
	}
	absRoot, err := filepath.Abs(ui.projectRoot)
	if err != nil {
		return ""
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	relPath, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return ""
	}
	relPath = filepath.ToSlash(relPath)
	if strings.HasPrefix(relPath, "../") || relPath == ".." {
		return ""
	}
	return ui.mediaServerBaseURL + relPath
}

type eventHistoryEntry struct {
	Directory string
	Summary   EventSummary
}

type eventHistoryDetail struct {
	Summary          EventSummary
	Tracking         *EventMetadata
	TrackingPath     string
	TrackNames       map[int]string
	Objects          []trackedObjectDetail
	HasTracking      bool
	FirstActiveMS    int64
	LastActiveMS     int64
	MaxTracks        int
	MaxFastTracks    int
	MaxSlowTracks    int
	FramesWithTracks int
}

type trackedObjectDetail struct {
	ID             int
	Name           string
	PrimaryType    string
	FramesSeen     int
	FirstSeenFrame int
	LastSeenFrame  int
	FirstSeenMS    int64
	LastSeenMS     int64
	TravelDistance float64
	Path           []image.Point
	MaxSpeed       float64
	AverageWidth   float64
	AverageHeight  float64
	CropCount      int
	CropPaths      []string
	FirstCropPath  string
	LastCropPath   string
	FirstPositionX int
	FirstPositionY int
	LastPositionX  int
	LastPositionY  int
}

type dwarfDownloadRequest struct {
	controller         DwarfController
	camera             string
	queueDir           string
	recordingName      string
	recordingStartedAt time.Time
}

type playbackOverlay struct {
	Tracking                 EventMetadata
	Settings                 TrackingSettings
	SelectedTrackID          int
	SelectedObject           *trackedObjectDetail
	StartFrame               int
	Label                    string
	CropBySourceFrame        map[int]string
	CropSourceFrames         []int
	ObjectMapSkipCount       int
	ShowFrameTracks          bool
	ShowFinalPositions       bool
	FinalPositionObjects     []trackedObjectDetail
	FinalPositionMinDistance float64
	mu                       sync.RWMutex
}

func (overlay *playbackOverlay) frameTracksEnabled() bool {
	if overlay == nil {
		return false
	}
	overlay.mu.RLock()
	defer overlay.mu.RUnlock()
	return overlay.ShowFrameTracks
}

func (overlay *playbackOverlay) finalPositionConfig() (bool, float64, []trackedObjectDetail) {
	if overlay == nil {
		return false, 0, nil
	}
	overlay.mu.RLock()
	defer overlay.mu.RUnlock()
	return overlay.ShowFinalPositions, overlay.FinalPositionMinDistance, overlay.FinalPositionObjects
}

func (overlay *playbackOverlay) setFinalPositionMinDistance(value float64) {
	if overlay == nil {
		return
	}
	overlay.mu.Lock()
	overlay.FinalPositionMinDistance = value
	overlay.mu.Unlock()
}

const (
	sortNewestFirst         = "Newest First"
	sortHighestObjects      = "Highest Object Count"
	sortHighestPeakSpeed    = "Highest Peak Speed"
	appID                   = "com.jlambert.dwarf3-event-tracker"
	mediaServerHost         = "0.0.0.0"
	mediaServerPublicHost   = "192.168.50.215"
	mediaServerPortStart    = 8088
	mediaServerPortEnd      = 8098
	mediaServerTLSPortStart = 8443
	mediaServerTLSPortEnd   = 8453
	defaultNostrTestMessage = "test note\nhttps://cdn.grube.de/2024/10/24/square_icon_ch_100.png"

	prefHistoryDateFilter   = "history.date_filter"
	prefHistoryObjectFilter = "history.object_filter"
	prefHistorySpeedFilter  = "history.speed_filter"
	prefHistorySortMode     = "history.sort_mode"
	prefTrackingMinArea     = "tracking.min_area"
	prefTrackingMaxArea     = "tracking.max_area"
	prefTrackingSlowSpeed   = "tracking.slow_min_speed"
	prefTrackingMinSpeed    = "tracking.min_speed"
	prefTrackingMatchDist   = "tracking.max_match_distance"
	prefTrackingMinHits     = "tracking.min_hits"
	prefTrackingBlurSize    = "tracking.blur_size"
	prefTrackingThreshold   = "tracking.foreground_threshold"
	prefTrackingPreEvent    = "tracking.pre_event_seconds"
	prefTrackingPostEvent   = "tracking.post_event_seconds"
	prefTrackingRawSegment  = "tracking.raw_segment_seconds"
	prefTrackingRawOverlap  = "tracking.raw_segment_overlap_seconds"
	prefTrackingMOG2History = "tracking.mog2_history"
	prefTrackingMOG2Var     = "tracking.mog2_var_threshold"
	prefTrackingROIHeight   = "tracking.roi_height_fraction"
	prefTrackingPresets     = "tracking.presets"
	prefTrackingPresetName  = "tracking.preset_name"
	prefNostrEnabled        = "nostr.enabled"
	prefNostrRelayURL       = "nostr.relay_url"
	prefNostrSecretKey      = "nostr.secret_key"
	prefNostrMinDistance    = "nostr.min_track_distance"
	prefNostrTestMessage    = "nostr.test_message"
)

const (
	dwarfQueueStageQueue           = "queue"
	dwarfQueueStageUnderProcessing = "UnderProcessing"
	dwarfQueueStageProcessed       = "Processed"
)

func (e eventHistoryEntry) title() string {
	if !e.Summary.StartedAt.IsZero() {
		return e.Summary.StartedAt.Local().Format("2006-01-02 15:04:05")
	}
	if e.Summary.EventID != "" {
		return e.Summary.EventID
	}
	return filepath.Base(e.Directory)
}

func (e eventHistoryEntry) subtitle() string {
	parts := []string{}
	if e.Summary.Frames > 0 {
		parts = append(parts, fmt.Sprintf("%d frames", e.Summary.Frames))
	}
	if e.Summary.UniqueObjects > 0 {
		parts = append(parts, fmt.Sprintf("%d objects", e.Summary.UniqueObjects))
	}
	if e.Summary.DurationSeconds > 0 {
		parts = append(parts, fmt.Sprintf("%.1fs", e.Summary.DurationSeconds))
	}
	if len(parts) == 0 {
		return filepath.Base(e.Directory)
	}
	return stringsJoin(parts, "   ")
}

func main() {
	application := app.NewWithID(appID)
	window := application.NewWindow("DWARF 3 Fast Object Tracker")
	window.Resize(fyne.NewSize(1360, 860))

	ui := newTrackerApp(window)
	if err := ui.startMediaServer(); err != nil {
		ui.showError(fmt.Errorf("start media server: %w", err))
	}
	window.SetContent(ui.buildUI())
	window.SetCloseIntercept(func() {
		ui.stopTracking()
		ui.stopDwarfCapture()
		ui.stopMediaServer()
		window.Close()
	})

	window.ShowAndRun()
}

func newTrackerApp(window fyne.Window) *trackerApp {
	projectRoot, err := os.Getwd()
	if err != nil {
		projectRoot = "."
	}
	sourceRadio := widget.NewRadioGroup([]string{"Live Stream", "Video File"}, nil)
	sourceRadio.Horizontal = true
	sourceRadio.SetSelected("Live Stream")

	urlEntry := widget.NewEntry()
	urlEntry.SetText("rtsp://192.168.88.1/ch0/stream0")

	fileEntry := widget.NewEntry()
	fileEntry.SetPlaceHolder("Choose a video file")

	outputEntry := widget.NewEntry()
	outputEntry.SetText("events")

	fpsEntry := widget.NewEntry()
	fpsEntry.SetText("30")

	dwarfHostEntry := widget.NewEntry()
	dwarfHostEntry.SetText(dwarfDefaultHost)

	dwarfCameraSelect := widget.NewSelect([]string{"Tele", "Wide"}, nil)
	dwarfCameraSelect.SetSelected("Tele")

	dwarfSegmentEntry := widget.NewEntry()
	dwarfSegmentEntry.SetText("60")

	dwarfDownloadDirEntry := widget.NewEntry()
	dwarfDownloadDirEntry.SetText("dwarf_downloads")

	dwarfDeleteCheck := widget.NewCheck("Delete remote after download", nil)
	dwarfDeleteCheck.SetChecked(false)
	dwarfDebugWSCheck := widget.NewCheck("Log DWARF websocket JSON", nil)
	dwarfDebugWSCheck.SetChecked(false)

	showMask := widget.NewCheck("Show mask tab", nil)
	showMask.SetChecked(true)

	dateFilter := widget.NewEntry()
	dateFilter.SetPlaceHolder("Date contains YYYY-MM-DD")

	objectFilter := widget.NewEntry()
	objectFilter.SetPlaceHolder("Min objects")

	speedFilter := widget.NewEntry()
	speedFilter.SetPlaceHolder("Min peak px/s")

	sortSelect := widget.NewSelect([]string{
		sortNewestFirst,
		sortHighestObjects,
		sortHighestPeakSpeed,
	}, nil)
	sortSelect.SetSelected(sortNewestFirst)

	presetSelect := widget.NewSelect(nil, nil)
	presetNameEntry := widget.NewEntry()
	presetNameEntry.SetPlaceHolder("Preset name")

	defaults := DefaultTrackingSettings()
	profileSelect := widget.NewSelect(trackingProfileOptions(), nil)
	profileSelect.SetSelected(trackingProfileLabel(defaults.Profile))

	videoImage := canvas.NewImageFromImage(newPlaceholderFrame())
	videoImage.FillMode = canvas.ImageFillContain
	videoImage.SetMinSize(fyne.NewSize(480, 270))

	maskImage := canvas.NewImageFromImage(newPlaceholderFrame())
	maskImage.FillMode = canvas.ImageFillContain
	maskImage.SetMinSize(fyne.NewSize(960, 540))

	objectImage := canvas.NewImageFromImage(newPlaceholderFrame())
	objectImage.FillMode = canvas.ImageFillContain
	objectImage.SetMinSize(fyne.NewSize(320, 240))

	objectMapImage := canvas.NewImageFromImage(newPlaceholderFrame())
	objectMapImage.FillMode = canvas.ImageFillContain
	objectMapImage.SetMinSize(fyne.NewSize(480, 270))

	playbackSlider := widget.NewSlider(0, 1)
	playbackSlider.Step = 1
	playbackSlider.Disable()

	historyInfo := widget.NewLabel("Select an event")
	historyInfo.Wrapping = fyne.TextWrapWord
	playbackLabel := widget.NewLabel("00:00 / 00:00")
	historyDetail := widget.NewMultiLineEntry()
	historyDetail.SetText("Select an event to inspect event.json and tracking.json.")
	historyDetail.Disable()
	objectList := widget.NewList(
		func() int { return 0 },
		func() fyne.CanvasObject {
			thumb := canvas.NewImageFromImage(newPlaceholderFrame())
			thumb.FillMode = canvas.ImageFillContain
			thumb.SetMinSize(fyne.NewSize(88, 66))
			title := widget.NewLabel("Object")
			title.TextStyle = fyne.TextStyle{Bold: true}
			subtitle := widget.NewLabel("Preview")
			subtitle.Wrapping = fyne.TextWrapWord
			return container.NewHBox(thumb, container.NewVBox(title, subtitle))
		},
		func(widget.ListItemID, fyne.CanvasObject) {},
	)
	objectSearchEntry := widget.NewEntry()
	objectSearchEntry.SetPlaceHolder("Filter by object ID")
	objectName := widget.NewEntry()
	objectName.SetPlaceHolder("Object name")
	objectDetail := widget.NewMultiLineEntry()
	objectDetail.SetText("Select an event, then select an object to inspect its metadata.")
	objectMapSkipEntry := widget.NewEntry()
	objectMapSkipEntry.SetText("0")
	finalPositionDistanceEntry := widget.NewEntry()
	finalPositionDistanceEntry.SetText("0")
	finalPositionSlider := widget.NewSlider(0, 1)
	finalPositionSlider.Step = 1
	finalPositionSlider.Disable()

	minAreaEntry := widget.NewEntry()
	minAreaEntry.SetText(formatFloat(defaults.MinArea))
	maxAreaEntry := widget.NewEntry()
	maxAreaEntry.SetText(formatFloat(defaults.MaxArea))
	slowSpeedEntry := widget.NewEntry()
	slowSpeedEntry.SetText(formatFloat(defaults.SlowMinSpeed))
	minSpeedEntry := widget.NewEntry()
	minSpeedEntry.SetText(formatFloat(defaults.MinSpeed))
	matchDistanceEntry := widget.NewEntry()
	matchDistanceEntry.SetText(formatFloat(defaults.MaxMatchDistance))
	minHitsEntry := widget.NewEntry()
	minHitsEntry.SetText(strconv.Itoa(defaults.MinHits))
	blurSizeEntry := widget.NewEntry()
	blurSizeEntry.SetText(strconv.Itoa(defaults.BlurSize))
	foregroundThresholdEntry := widget.NewEntry()
	foregroundThresholdEntry.SetText(formatFloat(defaults.ForegroundThreshold))
	preEventEntry := widget.NewEntry()
	preEventEntry.SetText(formatFloat(defaults.PreEventDuration.Seconds()))
	postEventEntry := widget.NewEntry()
	postEventEntry.SetText(formatFloat(defaults.PostEventDuration.Seconds()))
	rawSegmentEntry := widget.NewEntry()
	rawSegmentEntry.SetText(formatFloat(defaults.RawSegmentDuration.Seconds()))
	rawSegmentOverlapEntry := widget.NewEntry()
	rawSegmentOverlapEntry.SetText(formatFloat(defaults.RawSegmentOverlap.Seconds()))
	mog2HistoryEntry := widget.NewEntry()
	mog2HistoryEntry.SetText(strconv.Itoa(defaults.MOG2History))
	mog2VarThresholdEntry := widget.NewEntry()
	mog2VarThresholdEntry.SetText(formatFloat(defaults.MOG2VarThreshold))
	roiHeightEntry := widget.NewEntry()
	roiHeightEntry.SetText(formatFloat(defaults.TrackingROIHeightFrac))
	nostrEnableCheck := widget.NewCheck("Publish Nostr note after video analysis", nil)
	nostrRelayEntry := widget.NewEntry()
	nostrRelayEntry.SetPlaceHolder("ws://127.0.0.1:7447")
	nostrSecretEntry := widget.NewPasswordEntry()
	nostrSecretEntry.SetPlaceHolder("nsec... or 64-char hex secret")
	nostrMinDistanceEntry := widget.NewEntry()
	nostrMinDistanceEntry.SetText("800")
	nostrTestMessageEntry := widget.NewMultiLineEntry()
	nostrTestMessageEntry.SetPlaceHolder("Write a Nostr test note")
	nostrTestMessageEntry.Wrapping = fyne.TextWrapWord

	ui := &trackerApp{
		window:                     window,
		sourceRadio:                sourceRadio,
		urlEntry:                   urlEntry,
		fileEntry:                  fileEntry,
		outputEntry:                outputEntry,
		fpsEntry:                   fpsEntry,
		dwarfHostEntry:             dwarfHostEntry,
		dwarfCameraSelect:          dwarfCameraSelect,
		dwarfSegmentEntry:          dwarfSegmentEntry,
		dwarfDownloadDirEntry:      dwarfDownloadDirEntry,
		dwarfDeleteCheck:           dwarfDeleteCheck,
		dwarfDebugWSCheck:          dwarfDebugWSCheck,
		showMask:                   showMask,
		dateFilter:                 dateFilter,
		objectFilter:               objectFilter,
		speedFilter:                speedFilter,
		sortSelect:                 sortSelect,
		presetSelect:               presetSelect,
		profileSelect:              profileSelect,
		presetNameEntry:            presetNameEntry,
		minAreaEntry:               minAreaEntry,
		maxAreaEntry:               maxAreaEntry,
		slowSpeedEntry:             slowSpeedEntry,
		minSpeedEntry:              minSpeedEntry,
		matchDistanceEntry:         matchDistanceEntry,
		minHitsEntry:               minHitsEntry,
		blurSizeEntry:              blurSizeEntry,
		foregroundThresholdEntry:   foregroundThresholdEntry,
		preEventEntry:              preEventEntry,
		postEventEntry:             postEventEntry,
		rawSegmentEntry:            rawSegmentEntry,
		rawSegmentOverlapEntry:     rawSegmentOverlapEntry,
		mog2HistoryEntry:           mog2HistoryEntry,
		mog2VarThresholdEntry:      mog2VarThresholdEntry,
		roiHeightEntry:             roiHeightEntry,
		nostrEnableCheck:           nostrEnableCheck,
		nostrRelayEntry:            nostrRelayEntry,
		nostrSecretEntry:           nostrSecretEntry,
		nostrMinDistanceEntry:      nostrMinDistanceEntry,
		nostrTestMessageEntry:      nostrTestMessageEntry,
		videoImage:                 videoImage,
		maskImage:                  maskImage,
		objectImage:                objectImage,
		objectMapImage:             objectMapImage,
		playbackSlider:             playbackSlider,
		statusLabel:                widget.NewLabel("Idle"),
		eventLabel:                 widget.NewLabel("No event yet"),
		dwarfStatusLabel:           widget.NewLabel("DWARF idle"),
		dwarfQueueLabel:            widget.NewLabel("DWARF queue: 0"),
		mediaServerLabel:           widget.NewLabel("Media HTTP: starting..."),
		playbackLabel:              playbackLabel,
		historyInfo:                historyInfo,
		historyDetail:              historyDetail,
		objectList:                 objectList,
		objectSearchEntry:          objectSearchEntry,
		objectName:                 objectName,
		objectDetail:               objectDetail,
		objectMapSkipEntry:         objectMapSkipEntry,
		finalPositionDistanceEntry: finalPositionDistanceEntry,
		finalPositionSlider:        finalPositionSlider,
		selectedHistory:            -1,
		presets:                    make(map[string]TrackingSettings),
		projectRoot:                projectRoot,
		dwarfDownloadedFiles:       make(map[string]DwarfQueuedRecording),
		playbackSeekFrame:          -1,
		dwarfRawWSPayload:          "{\n  \"interface\": 10007,\n  \"camId\": 0,\n  \"name\": \"DWARF_TEST_MANUAL\"\n}",
		dwarfSessionProbePayload:   "{\n  \"clientId\": \"DAF3\",\n  \"type\": \"ping\"\n}",
	}

	ui.fileButton = widget.NewButtonWithIcon("", theme.FolderOpenIcon(), ui.pickVideoFile)
	ui.outputButton = widget.NewButtonWithIcon("", theme.FolderOpenIcon(), ui.pickOutputFolder)
	ui.dwarfDownloadButton = widget.NewButtonWithIcon("", theme.FolderOpenIcon(), ui.pickDwarfDownloadFolder)
	ui.startButton = widget.NewButtonWithIcon("Start", theme.MediaPlayIcon(), ui.startTracking)
	ui.stopButton = widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), ui.stopTracking)
	ui.startDwarfButton = widget.NewButtonWithIcon("Start Dwarf", theme.MediaRecordIcon(), ui.startDwarfCapture)
	ui.stopDwarfButton = widget.NewButtonWithIcon("Stop Dwarf", theme.MediaStopIcon(), ui.stopDwarfCapture)
	ui.fetchDwarfButton = widget.NewButtonWithIcon("Fetch Latest", theme.DownloadIcon(), ui.fetchLatestDwarfVideo)
	ui.testDwarfButton = widget.NewButtonWithIcon("Test Connection", theme.ViewRefreshIcon(), ui.testDwarfConnection)
	ui.testDwarfRecordButton = widget.NewButtonWithIcon("Test Record Start", theme.MediaRecordIcon(), ui.testDwarfRecordStart)
	ui.rawDwarfWSButton = widget.NewButtonWithIcon("Raw WS Command", theme.ComputerIcon(), ui.openRawDwarfWSDialog)
	ui.sessionProbeButton = widget.NewButtonWithIcon("Session Probe", theme.InfoIcon(), ui.openSessionProbeDialog)
	ui.resetTrackingButton = widget.NewButtonWithIcon("Reset Tracking Settings", theme.ViewRefreshIcon(), ui.resetTrackingSettings)
	ui.importTrackingButton = widget.NewButtonWithIcon("Import Settings", theme.FolderOpenIcon(), ui.importTrackingSettings)
	ui.exportTrackingButton = widget.NewButtonWithIcon("Export Settings", theme.DocumentSaveIcon(), ui.exportTrackingSettings)
	ui.savePresetButton = widget.NewButtonWithIcon("Save Preset", theme.DocumentCreateIcon(), ui.saveTrackingPreset)
	ui.applyPresetButton = widget.NewButtonWithIcon("Apply Preset", theme.ConfirmIcon(), ui.applySelectedPreset)
	ui.deletePresetButton = widget.NewButtonWithIcon("Delete Preset", theme.DeleteIcon(), ui.deleteSelectedPreset)
	ui.refreshButton = widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), ui.refreshEventHistory)
	ui.openTrackedButton = widget.NewButtonWithIcon("Open Tracked", theme.MediaPlayIcon(), func() {
		ui.openSelectedEventVideo(true)
	})
	ui.openOriginalButton = widget.NewButtonWithIcon("Open Original", theme.MediaPlayIcon(), func() {
		ui.openSelectedEventVideo(false)
	})
	ui.restoreSettingsButton = widget.NewButtonWithIcon("Restore Settings", theme.ViewRefreshIcon(), ui.restoreSettingsFromSelectedEvent)
	ui.watchObjectButton = widget.NewButtonWithIcon("Watch Object", theme.MediaPlayIcon(), ui.watchSelectedObject)
	ui.showFinalPositionsButton = widget.NewButtonWithIcon("Show Final Positions", theme.VisibilityIcon(), ui.watchFinalPositions)
	ui.saveObjectNameButton = widget.NewButtonWithIcon("Save Name", theme.DocumentSaveIcon(), ui.saveSelectedObjectName)
	ui.playPauseButton = widget.NewButtonWithIcon("Pause", theme.MediaPauseIcon(), ui.togglePlaybackPause)
	ui.sendNostrTestButton = widget.NewButtonWithIcon("Send Test Note", theme.MailSendIcon(), ui.sendNostrTestNote)
	ui.stopButton.Disable()
	ui.stopDwarfButton.Disable()
	ui.openTrackedButton.Disable()
	ui.openOriginalButton.Disable()
	ui.restoreSettingsButton.Disable()
	ui.watchObjectButton.Disable()
	ui.showFinalPositionsButton.Disable()
	ui.saveObjectNameButton.Disable()
	ui.applyPresetButton.Disable()
	ui.deletePresetButton.Disable()
	ui.playPauseButton.Disable()
	ui.finalPositionSlider.Disable()
	ui.finalPositionDistanceEntry.Disable()

	ui.sourceRadio.OnChanged = func(string) {
		ui.refreshSourceControls()
	}
	ui.refreshSourceControls()
	ui.dateFilter.OnChanged = func(string) {
		ui.saveHistoryPreferences()
		ui.applyHistoryFilters()
	}
	ui.objectFilter.OnChanged = func(string) {
		ui.saveHistoryPreferences()
		ui.applyHistoryFilters()
	}
	ui.speedFilter.OnChanged = func(string) {
		ui.saveHistoryPreferences()
		ui.applyHistoryFilters()
	}
	ui.sortSelect.OnChanged = func(string) {
		ui.saveHistoryPreferences()
		ui.applyHistoryFilters()
	}
	ui.presetSelect.OnChanged = func(selected string) {
		ui.presetNameEntry.SetText(selected)
		ui.updatePresetButtons()
	}
	ui.profileSelect.OnChanged = func(selected string) {
		settings := DefaultTrackingSettingsForProfile(trackingProfileFromLabel(selected))
		ui.applyTrackingSettingsToForm(settings)
	}
	ui.historyList = widget.NewList(
		func() int {
			return len(ui.historyEntries)
		},
		func() fyne.CanvasObject {
			title := widget.NewLabel("Event")
			title.TextStyle = fyne.TextStyle{Bold: true}
			subtitle := widget.NewLabel("Details")
			subtitle.Wrapping = fyne.TextWrapWord
			return container.NewVBox(title, subtitle)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			entry := ui.historyEntries[id]
			box := obj.(*fyne.Container)
			box.Objects[0].(*widget.Label).SetText(entry.title())
			box.Objects[1].(*widget.Label).SetText(entry.subtitle())
		},
	)
	ui.historyList.OnSelected = func(id widget.ListItemID) {
		ui.selectedHistory = id
		ui.updateHistorySelection()
	}
	ui.objectList.Length = func() int {
		return len(ui.filteredObjectIDs)
	}
	ui.objectList.UpdateItem = func(id widget.ListItemID, obj fyne.CanvasObject) {
		object, ok := ui.filteredObjectAt(id)
		if !ok {
			return
		}
		row := obj.(*fyne.Container)
		thumb := row.Objects[0].(*canvas.Image)
		info := row.Objects[1].(*fyne.Container)
		title := info.Objects[0].(*widget.Label)
		subtitle := info.Objects[1].(*widget.Label)
		thumb.Image = loadObjectListPreview(object)
		thumb.Refresh()
		title.SetText(formatObjectOption(object))
		subtitle.SetText(fmt.Sprintf("%.0f px/s   %.0f px travel   %d frames", object.MaxSpeed, object.TravelDistance, object.FramesSeen))
	}
	ui.objectList.OnSelected = func(id widget.ListItemID) {
		object, ok := ui.filteredObjectAt(id)
		if !ok {
			return
		}
		ui.selectedObjectID = object.ID
		ui.updateSelectedObject()
	}
	ui.playbackSlider.OnChanged = func(value float64) {
		ui.handlePlaybackSliderChanged(value)
	}
	ui.playbackSlider.OnChangeEnded = func(value float64) {
		ui.handlePlaybackSliderChanged(value)
	}
	ui.finalPositionSlider.OnChanged = func(value float64) {
		ui.handleFinalPositionSliderChanged(value)
	}
	ui.finalPositionDistanceEntry.OnChanged = func(value string) {
		ui.handleFinalPositionDistanceEntryChanged(value)
	}
	ui.objectSearchEntry.OnChanged = func(string) {
		ui.applyObjectListFilter()
	}

	ui.loadTrackingPreferences()
	ui.loadTrackingPresets()
	ui.loadHistoryPreferences()
	ui.refreshEventHistory()
	ui.resetPlaybackControls()

	return ui
}

func (ui *trackerApp) buildUI() fyne.CanvasObject {
	sourceRow := container.NewVBox(
		widget.NewLabelWithStyle("Source", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		ui.sourceRadio,
	)

	urlRow := container.NewBorder(nil, nil, widget.NewLabel("RTSP URL"), nil, ui.urlEntry)
	fileRow := container.NewBorder(nil, nil, widget.NewLabel("Video File"), ui.fileButton, ui.fileEntry)
	outputRow := container.NewBorder(nil, nil, widget.NewLabel("Output Dir"), ui.outputButton, ui.outputEntry)
	dwarfHostRow := container.NewBorder(nil, nil, widget.NewLabel("Dwarf Host"), nil, ui.dwarfHostEntry)
	dwarfCameraRow := container.NewBorder(nil, nil, widget.NewLabel("Dwarf Camera"), nil, ui.dwarfCameraSelect)
	dwarfSegmentRow := container.NewBorder(nil, nil, widget.NewLabel("Dwarf Segment Seconds"), nil, ui.dwarfSegmentEntry)
	dwarfDownloadRow := container.NewBorder(nil, nil, widget.NewLabel("Dwarf Download Dir"), ui.dwarfDownloadButton, ui.dwarfDownloadDirEntry)

	options := container.NewHBox(
		widget.NewLabel("Fallback FPS"),
		ui.fpsEntry,
		ui.showMask,
	)

	trackingSettings := widget.NewAccordion(
		widget.NewAccordionItem("Tracking Parameters", container.NewVBox(
			container.NewHBox(ui.resetTrackingButton, ui.importTrackingButton, ui.exportTrackingButton),
			container.NewBorder(nil, nil, widget.NewLabel("Preset"), nil, ui.presetSelect),
			container.NewBorder(nil, nil, widget.NewLabel("Preset Name"), nil, ui.presetNameEntry),
			container.NewHBox(ui.savePresetButton, ui.applyPresetButton, ui.deletePresetButton),
			container.NewGridWithColumns(2,
				container.NewBorder(nil, nil, widget.NewLabel("Tracking Profile"), nil, ui.profileSelect),
				widget.NewLabel(""),
				container.NewBorder(nil, nil, widget.NewLabel("Min Area"), nil, ui.minAreaEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Max Area"), nil, ui.maxAreaEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Slow Min Speed"), nil, ui.slowSpeedEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Fast Min Speed"), nil, ui.minSpeedEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Match Distance"), nil, ui.matchDistanceEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Min Hits"), nil, ui.minHitsEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Blur Size"), nil, ui.blurSizeEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Foreground Threshold"), nil, ui.foregroundThresholdEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Pre Event Seconds"), nil, ui.preEventEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Post Event Seconds"), nil, ui.postEventEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Raw Segment Seconds"), nil, ui.rawSegmentEntry),
				container.NewBorder(nil, nil, widget.NewLabel("Raw Segment Overlap Seconds"), nil, ui.rawSegmentOverlapEntry),
				container.NewBorder(nil, nil, widget.NewLabel("MOG2 History"), nil, ui.mog2HistoryEntry),
				container.NewBorder(nil, nil, widget.NewLabel("MOG2 Var Threshold"), nil, ui.mog2VarThresholdEntry),
				container.NewBorder(nil, nil, widget.NewLabel("ROI Height Fraction"), nil, ui.roiHeightEntry),
			),
		)),
	)

	nostrSettings := widget.NewAccordion(
		widget.NewAccordionItem("Nostr Settings", container.NewVBox(
			ui.nostrEnableCheck,
			container.NewBorder(nil, nil, widget.NewLabel("Relay URL"), nil, ui.nostrRelayEntry),
			container.NewBorder(nil, nil, widget.NewLabel("Secret Key"), nil, ui.nostrSecretEntry),
			container.NewBorder(nil, nil, widget.NewLabel("Min Straight-Line Track Px"), nil, ui.nostrMinDistanceEntry),
			widget.NewLabel("Test Note"),
			ui.nostrTestMessageEntry,
			container.NewHBox(ui.sendNostrTestButton),
		)),
	)

	dwarfCapture := widget.NewAccordion(
		widget.NewAccordionItem("Dwarf Capture", container.NewVBox(
			dwarfHostRow,
			dwarfCameraRow,
			dwarfSegmentRow,
			dwarfDownloadRow,
			ui.dwarfDeleteCheck,
			ui.dwarfDebugWSCheck,
			container.NewHBox(ui.startDwarfButton, ui.stopDwarfButton, ui.fetchDwarfButton, ui.testDwarfButton, ui.testDwarfRecordButton),
			container.NewHBox(ui.rawDwarfWSButton, ui.sessionProbeButton),
			ui.dwarfStatusLabel,
			ui.dwarfQueueLabel,
		)),
	)

	actions := container.NewHBox(ui.startButton, ui.stopButton)

	controls := container.NewVBox(
		sourceRow,
		urlRow,
		fileRow,
		outputRow,
		options,
		dwarfCapture,
		trackingSettings,
		nostrSettings,
		actions,
	)

	statusBar := container.NewVBox(
		widget.NewSeparator(),
		ui.mediaServerLabel,
		ui.statusLabel,
		ui.eventLabel,
	)

	playbackControls := container.NewBorder(
		nil,
		nil,
		ui.playPauseButton,
		ui.playbackLabel,
		ui.playbackSlider,
	)

	trackedPanel := container.NewHSplit(
		container.NewBorder(
			widget.NewLabelWithStyle("Tracked Replay", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			playbackControls,
			nil,
			nil,
			container.NewPadded(ui.videoImage),
		),
		container.NewBorder(
			widget.NewLabelWithStyle("Object Map", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			nil,
			nil,
			nil,
			container.NewPadded(ui.objectMapImage),
		),
	)
	trackedPanel.Offset = 0.5
	trackedTab := container.NewTabItem("Tracked", trackedPanel)
	maskTab := container.NewTabItem("Mask", container.NewPadded(ui.maskImage))
	ui.tabs = container.NewAppTabs(trackedTab, maskTab)

	historyHeader := container.NewBorder(nil, nil, widget.NewLabelWithStyle("Event History", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), ui.refreshButton)
	historyActions := container.NewHBox(ui.openTrackedButton, ui.openOriginalButton, ui.restoreSettingsButton)
	objectActions := container.NewHBox(
		ui.watchObjectButton,
		ui.showFinalPositionsButton,
		widget.NewLabel("Skip"),
		ui.objectMapSkipEntry,
		ui.saveObjectNameButton,
	)
	finalPositionControls := container.NewVBox(
		widget.NewLabelWithStyle("Final Position Filter", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewBorder(nil, nil, widget.NewLabel("Min Travel"), nil, ui.finalPositionSlider),
		container.NewBorder(nil, nil, widget.NewLabel("Min Travel Px"), nil, ui.finalPositionDistanceEntry),
	)
	filterRow := container.NewGridWithColumns(1,
		container.NewBorder(nil, nil, widget.NewLabel("Date"), nil, ui.dateFilter),
		container.NewBorder(nil, nil, widget.NewLabel("Min Objects"), nil, ui.objectFilter),
		container.NewBorder(nil, nil, widget.NewLabel("Min Peak Speed"), nil, ui.speedFilter),
		container.NewBorder(nil, nil, widget.NewLabel("Sort"), nil, ui.sortSelect),
	)
	ui.historyDetail.SetMinRowsVisible(14)
	ui.objectDetail.SetMinRowsVisible(10)
	objectPreviewPanel := container.NewBorder(
		widget.NewLabelWithStyle("Object View", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil,
		nil,
		nil,
		ui.objectImage,
	)
	objectInfoPanel := container.NewVBox(
		widget.NewLabelWithStyle("Tracked Object", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewBorder(nil, nil, widget.NewLabel("Find Object ID"), nil, ui.objectSearchEntry),
		ui.objectList,
		container.NewBorder(nil, nil, widget.NewLabel("Name"), nil, ui.objectName),
		objectActions,
		finalPositionControls,
		ui.objectDetail,
	)
	objectPanel := container.NewHSplit(objectInfoPanel, container.NewPadded(objectPreviewPanel))
	objectPanel.Offset = 0.62
	historyTop := container.NewVBox(historyHeader, filterRow, historyActions, ui.historyInfo, ui.historyDetail, objectPanel)
	historyPanel := container.NewBorder(historyTop, nil, nil, nil, ui.historyList)
	mainPanel := container.NewBorder(controls, statusBar, nil, nil, ui.tabs)
	content := container.NewHSplit(mainPanel, container.NewPadded(historyPanel))
	content.Offset = 0.76

	return content
}

func (ui *trackerApp) refreshSourceControls() {
	fileMode := ui.sourceRadio.Selected == "Video File"
	if fileMode {
		ui.fileEntry.Enable()
		ui.fileButton.Enable()
		ui.urlEntry.Disable()
		return
	}

	ui.fileEntry.Disable()
	ui.fileButton.Disable()
	ui.urlEntry.Enable()
}

func (ui *trackerApp) pickVideoFile() {
	picker := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			ui.showError(err)
			return
		}
		if reader == nil {
			return
		}
		path := reader.URI().Path()
		reader.Close()
		ui.fileEntry.SetText(path)
	}, ui.window)
	picker.SetFilter(storage.NewExtensionFileFilter([]string{".mp4", ".avi", ".mov", ".mkv", ".m4v"}))
	picker.Show()
}

func (ui *trackerApp) pickOutputFolder() {
	picker := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			ui.showError(err)
			return
		}
		if uri == nil {
			return
		}
		ui.outputEntry.SetText(uri.Path())
		ui.refreshEventHistory()
	}, ui.window)
	picker.Show()
}

func (ui *trackerApp) pickDwarfDownloadFolder() {
	picker := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			ui.showError(err)
			return
		}
		if uri == nil {
			return
		}
		ui.dwarfDownloadDirEntry.SetText(uri.Path())
	}, ui.window)
	picker.Show()
}

func (ui *trackerApp) buildDwarfController() (DwarfController, string, time.Duration, string, error) {
	segmentSeconds, err := strconv.ParseFloat(ui.dwarfSegmentEntry.Text, 64)
	if err != nil || segmentSeconds <= 0 {
		return DwarfController{}, "", 0, "", errors.New("DWARF segment seconds must be a positive number")
	}

	downloadDir := ui.dwarfDownloadDirEntry.Text
	if downloadDir == "" {
		downloadDir = "dwarf_downloads"
	}

	controller := DefaultDwarfController()
	if strings.TrimSpace(ui.dwarfHostEntry.Text) != "" {
		controller.Host = strings.TrimSpace(ui.dwarfHostEntry.Text)
	}
	controller.DebugWS = ui.dwarfDebugWSCheck.Checked

	return controller, dwarfCameraFromLabel(ui.dwarfCameraSelect.Selected), time.Duration(segmentSeconds * float64(time.Second)), downloadDir, nil
}

func (ui *trackerApp) startDwarfCapture() {
	ui.mu.Lock()
	if ui.dwarfCaptureRunning {
		ui.mu.Unlock()
		return
	}
	ui.mu.Unlock()

	controller, camera, segmentDuration, downloadDir, err := ui.buildDwarfController()
	if err != nil {
		ui.showError(err)
		return
	}
	sessionDir := dwarfCaptureSessionDir(downloadDir, time.Now())

	stopCh := make(chan struct{})

	ui.mu.Lock()
	ui.dwarfCaptureRunning = true
	ui.dwarfCaptureStopCh = stopCh
	ui.mu.Unlock()

	ui.startDwarfButton.Disable()
	ui.stopDwarfButton.Enable()
	ui.fetchDwarfButton.Disable()
	ui.dwarfStatusLabel.SetText("DWARF capture starting...")

	go ui.runDwarfCapture(controller, camera, segmentDuration, sessionDir, stopCh)
}

func (ui *trackerApp) stopDwarfCapture() {
	ui.mu.Lock()
	defer ui.mu.Unlock()

	if !ui.dwarfCaptureRunning || ui.dwarfCaptureStopCh == nil {
		return
	}

	close(ui.dwarfCaptureStopCh)
	ui.dwarfCaptureStopCh = nil
	ui.dwarfStatusLabel.SetText("DWARF capture stopping...")
}

func (ui *trackerApp) fetchLatestDwarfVideo() {
	controller, camera, _, downloadDir, err := ui.buildDwarfController()
	if err != nil {
		ui.showError(err)
		return
	}

	ui.fetchDwarfButton.Disable()
	ui.dwarfStatusLabel.SetText("Fetching latest DWARF video...")

	go func() {
		recording, warningText, fetchErr := ui.downloadLatestDwarfVideo(controller, camera, downloadDir, "", time.Time{})
		fyne.Do(func() {
			ui.fetchDwarfButton.Enable()
			if fetchErr != nil {
				ui.dwarfStatusLabel.SetText("DWARF fetch failed")
				ui.showError(fetchErr)
				return
			}
			ui.enqueueDwarfFile(recording)
			if warningText != "" {
				ui.dwarfStatusLabel.SetText(warningText)
				return
			}
			ui.dwarfStatusLabel.SetText("Fetched latest DWARF video")
		})
	}()
}

func (ui *trackerApp) testDwarfConnection() {
	controller, _, _, _, err := ui.buildDwarfController()
	if err != nil {
		ui.showError(err)
		return
	}

	ui.testDwarfButton.Disable()
	ui.dwarfStatusLabel.SetText(fmt.Sprintf("Testing DWARF connection to %s...", controller.Host))

	go func() {
		report := controller.TestConnections()
		summary := formatDwarfConnectionReport(report)

		fyne.Do(func() {
			ui.testDwarfButton.Enable()
			if report.WebSocketOK && report.FTPOK {
				ui.dwarfStatusLabel.SetText(fmt.Sprintf("DWARF connection OK: %s", report.Host))
				ui.showInfo("DWARF Connection Test", summary)
				return
			}

			ui.dwarfStatusLabel.SetText(fmt.Sprintf("DWARF connection issue: %s", report.Host))
			ui.showError(errors.New(summary))
		})
	}()
}

func (ui *trackerApp) testDwarfRecordStart() {
	controller, camera, _, _, err := ui.buildDwarfController()
	if err != nil {
		ui.showError(err)
		return
	}

	ui.testDwarfRecordButton.Disable()
	ui.dwarfStatusLabel.SetText(fmt.Sprintf("Testing DWARF record start on %s...", controller.Host))

	go func() {
		report := controller.TestRecordStart(camera, 4*time.Second)
		summary := formatDwarfRecordStartReport(report)

		fyne.Do(func() {
			ui.testDwarfRecordButton.Enable()
			if report.StartAckOK && report.StopAckOK && len(report.NewFiles) > 0 && report.ListAfterErr == "" {
				ui.dwarfStatusLabel.SetText(fmt.Sprintf("DWARF record test OK: %s", report.Host))
				ui.showInfo("DWARF Record Start Test", summary)
				return
			}

			ui.dwarfStatusLabel.SetText(fmt.Sprintf("DWARF record test issue: %s", report.Host))
			ui.showError(errors.New(summary))
		})
	}()
}

func (ui *trackerApp) openRawDwarfWSDialog() {
	ui.openDwarfWSDialog(
		"Raw DWARF WS Command",
		"Send arbitrary JSON to the DWARF websocket endpoint.",
		ui.dwarfRawWSPayload,
		true,
		func(payload string, allowTimeoutSuccess bool) {
			ui.dwarfRawWSPayload = payload
			ui.runDwarfRawWSCommand("Raw DWARF WS Command", payload, allowTimeoutSuccess)
		},
	)
}

func (ui *trackerApp) openSessionProbeDialog() {
	ui.openDwarfWSDialog(
		"DWARF Session Probe",
		"Reusable probe payload for keepalive/session-init tests.",
		ui.dwarfSessionProbePayload,
		false,
		func(payload string, allowTimeoutSuccess bool) {
			ui.dwarfSessionProbePayload = payload
			ui.runDwarfRawWSCommand("DWARF Session Probe", payload, allowTimeoutSuccess)
		},
	)
}

func (ui *trackerApp) openDwarfWSDialog(title string, helpText string, initialPayload string, allowTimeoutDefault bool, onSubmit func(payload string, allowTimeoutSuccess bool)) {
	payloadEntry := widget.NewMultiLineEntry()
	payloadEntry.SetText(initialPayload)
	payloadEntry.Wrapping = fyne.TextWrapWord
	payloadEntry.SetMinRowsVisible(10)

	timeoutCheck := widget.NewCheck("Treat timeout as success", nil)
	timeoutCheck.SetChecked(allowTimeoutDefault)

	content := container.NewVBox(
		widget.NewLabel(helpText),
		payloadEntry,
		timeoutCheck,
	)

	dialog.NewCustomConfirm(title, "Send", "Cancel", content, func(confirmed bool) {
		if !confirmed {
			return
		}
		onSubmit(payloadEntry.Text, timeoutCheck.Checked)
	}, ui.window).Show()
}

func (ui *trackerApp) runDwarfRawWSCommand(title string, payload string, allowTimeoutSuccess bool) {
	controller, _, _, _, err := ui.buildDwarfController()
	if err != nil {
		ui.showError(err)
		return
	}

	ui.rawDwarfWSButton.Disable()
	ui.sessionProbeButton.Disable()
	ui.dwarfStatusLabel.SetText(fmt.Sprintf("Sending DWARF websocket command to %s...", controller.Host))

	go func() {
		report := controller.SendRawWSCommand(payload, allowTimeoutSuccess)
		summary := formatDwarfRawWSReport(report)

		fyne.Do(func() {
			ui.rawDwarfWSButton.Enable()
			ui.sessionProbeButton.Enable()
			if report.Err == "" {
				ui.dwarfStatusLabel.SetText(fmt.Sprintf("DWARF websocket response received from %s", report.Host))
				ui.showInfo(title, summary)
				return
			}
			ui.dwarfStatusLabel.SetText(fmt.Sprintf("DWARF websocket command failed: %s", report.Host))
			ui.showError(errors.New(summary))
		})
	}()
}

func (ui *trackerApp) runDwarfCapture(controller DwarfController, camera string, segmentDuration time.Duration, sessionDir string, stopCh <-chan struct{}) {
	var runErr error
	queueDir := dwarfQueueStageDir(sessionDir, dwarfQueueStageQueue)
	downloadRequests := make(chan dwarfDownloadRequest, 8)
	downloadErrCh := make(chan error, 1)
	downloadDoneCh := make(chan struct{})

	go ui.runDwarfDownloadWorker(downloadRequests, downloadErrCh, downloadDoneCh)
	defer func() {
		close(downloadRequests)
		<-downloadDoneCh
		select {
		case err := <-downloadErrCh:
			if err != nil && runErr == nil {
				runErr = err
			}
		default:
		}
	}()

	for {
		select {
		case err := <-downloadErrCh:
			if err != nil {
				runErr = err
				goto finish
			}
		default:
		}

		recordingName := fmt.Sprintf("DWARF_%s", time.Now().Format("20060102150405"))
		recordingStartedAt := time.Now()
		if _, err := controller.StartVideoRecording(camera, recordingName); err != nil {
			runErr = err
			break
		}

		fyne.Do(func() {
			ui.dwarfStatusLabel.SetText(fmt.Sprintf("DWARF %s recording: %s", strings.ToUpper(camera), recordingName))
		})

		timer := time.NewTimer(segmentDuration)
		stopRequested := false
		abortAfterStop := false
		select {
		case <-stopCh:
			stopRequested = true
		case err := <-downloadErrCh:
			if err != nil {
				runErr = err
				stopRequested = true
				abortAfterStop = true
			}
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}

		if _, err := controller.StopVideoRecording(camera); err != nil {
			runErr = err
			break
		}

		request := dwarfDownloadRequest{
			controller:         controller,
			camera:             camera,
			queueDir:           queueDir,
			recordingName:      recordingName,
			recordingStartedAt: recordingStartedAt,
		}
		select {
		case downloadRequests <- request:
			fyne.Do(func() {
				ui.dwarfStatusLabel.SetText(fmt.Sprintf("DWARF %s stopped: %s   next segment can start while download runs", strings.ToUpper(camera), recordingName))
			})
		case err := <-downloadErrCh:
			if err != nil {
				fyne.Do(func() {
					ui.dwarfStatusLabel.SetText("DWARF download failed")
				})
				runErr = err
			}
		}
		if runErr != nil {
			break
		}

		if stopRequested || abortAfterStop {
			break
		}
	}

finish:
	fyne.Do(func() {
		ui.finishDwarfCapture(runErr)
	})
}

func (ui *trackerApp) runDwarfDownloadWorker(requests <-chan dwarfDownloadRequest, errCh chan<- error, done chan<- struct{}) {
	defer close(done)

	for request := range requests {
		time.Sleep(3 * time.Second)

		recording, warningText, err := ui.downloadLatestDwarfVideo(
			request.controller,
			request.camera,
			request.queueDir,
			request.recordingName,
			request.recordingStartedAt,
		)
		if err != nil {
			fyne.Do(func() {
				ui.dwarfStatusLabel.SetText("DWARF download failed")
			})
			select {
			case errCh <- err:
			default:
			}
			return
		}

		fyne.Do(func() {
			ui.enqueueDwarfFile(recording)
			if warningText != "" {
				ui.dwarfStatusLabel.SetText(warningText)
				return
			}
			ui.dwarfStatusLabel.SetText(fmt.Sprintf("DWARF queued: %s (%s)", filepath.Base(recording.LocalPath), strings.ToUpper(recording.Camera)))
		})
	}
}

func (ui *trackerApp) finishDwarfCapture(err error) {
	ui.mu.Lock()
	ui.dwarfCaptureRunning = false
	ui.dwarfCaptureStopCh = nil
	ui.mu.Unlock()

	ui.startDwarfButton.Enable()
	ui.stopDwarfButton.Disable()
	ui.fetchDwarfButton.Enable()

	switch {
	case err == nil || errors.Is(err, ErrStopTracking):
		if len(ui.dwarfQueuedFiles) > 0 {
			ui.dwarfStatusLabel.SetText("DWARF capture idle")
		} else {
			ui.dwarfStatusLabel.SetText("DWARF idle")
		}
	default:
		ui.dwarfStatusLabel.SetText("DWARF capture error")
		ui.showError(err)
	}
}

func (ui *trackerApp) downloadLatestDwarfVideo(controller DwarfController, camera string, downloadDir string, recordingName string, recordingStartedAt time.Time) (DwarfQueuedRecording, string, error) {
	ui.mu.Lock()
	downloaded := make(map[string]DwarfQueuedRecording, len(ui.dwarfDownloadedFiles))
	for key, value := range ui.dwarfDownloadedFiles {
		downloaded[key] = value
	}
	ui.mu.Unlock()

	selected, err := findDwarfMediaFileForDownload(controller, downloaded, camera, recordingName, recordingStartedAt)
	if err != nil {
		return DwarfQueuedRecording{}, "", err
	}
	if selected == nil {
		return DwarfQueuedRecording{}, "", errors.New("no new DWARF video file available for download")
	}

	localPath := dwarfDownloadLocalPath(downloadDir, recordingName, selected.Path)
	if err := controller.DownloadFile(selected.Path, localPath); err != nil {
		return DwarfQueuedRecording{}, "", err
	}

	recording := DwarfQueuedRecording{
		Camera:          normalizeDwarfCamera(camera),
		RemotePath:      selected.Path,
		RemoteName:      selected.Name,
		LocalPath:       localPath,
		RecordingName:   recordingName,
		RecordingStart:  recordingStartedAt,
		DownloadedAt:    time.Now(),
		DeleteRequested: false,
	}

	ui.mu.Lock()
	deleteRemote := ui.dwarfDeleteCheck.Checked
	recording.DeleteRequested = deleteRemote
	ui.dwarfDownloadedFiles[selected.Path] = recording
	ui.mu.Unlock()

	if !deleteRemote {
		return recording, "", nil
	}

	if err := controller.DeleteFile(selected.Path); err != nil {
		return recording, fmt.Sprintf("Downloaded %s (%s) but remote delete failed: %v", filepath.Base(localPath), strings.ToUpper(recording.Camera), err), nil
	}

	return recording, "", nil
}

func dwarfDownloadLocalPath(downloadDir string, recordingName string, remotePath string) string {
	if filepath.Base(filepath.Clean(downloadDir)) == dwarfQueueStageQueue {
		return filepath.Join(downloadDir, filepath.Base(remotePath))
	}
	if strings.TrimSpace(recordingName) == "" {
		return filepath.Join(downloadDir, filepath.Base(remotePath))
	}
	return filepath.Join(downloadDir, recordingName, filepath.Base(remotePath))
}

func dwarfCaptureSessionDir(downloadDir string, startedAt time.Time) string {
	return filepath.Join(downloadDir, startedAt.Format("2006-01-02_150405"))
}

func dwarfQueueStageDir(sessionDir string, stage string) string {
	return filepath.Join(sessionDir, stage)
}

func findDwarfMediaFileForDownload(controller DwarfController, downloaded map[string]DwarfQueuedRecording, camera string, recordingName string, recordingStartedAt time.Time) (*DwarfMediaFile, error) {
	waitUntil := time.Now()
	if recordingName != "" {
		waitUntil = waitUntil.Add(90 * time.Second)
	}

	for {
		files, err := controller.ListVideoFiles()
		if err != nil {
			return nil, err
		}

		selected := selectDwarfMediaFile(files, downloaded, camera, recordingName, recordingStartedAt)
		if selected != nil {
			return selected, nil
		}

		if recordingName == "" || time.Now().After(waitUntil) {
			return nil, nil
		}

		time.Sleep(3 * time.Second)
	}
}

func formatDwarfConnectionReport(report DwarfConnectionReport) string {
	return fmt.Sprintf(
		"Host: %s\nWebSocket %s:%d: %s\nFTP %s:%d: %s",
		report.Host,
		report.Host,
		report.WSPort,
		connectionStatusText(report.WebSocketOK, report.WebSocketDetail),
		report.Host,
		report.FTPPort,
		connectionStatusText(report.FTPOK, report.FTPDetail),
	)
}

func connectionStatusText(ok bool, detail string) string {
	status := "FAILED"
	if ok {
		status = "OK"
	}
	if strings.TrimSpace(detail) == "" {
		return status
	}
	return fmt.Sprintf("%s (%s)", status, detail)
}

func formatDwarfRecordStartReport(report DwarfRecordStartReport) string {
	lines := []string{
		fmt.Sprintf("Host: %s", report.Host),
		fmt.Sprintf("Camera: %s", strings.ToUpper(report.Camera)),
		fmt.Sprintf("Recording Name: %s", report.RecordingName),
		fmt.Sprintf("Wait: %s", report.WaitDuration),
		fmt.Sprintf("Start Ack: %s", connectionStatusText(report.StartAckOK, report.StartAckDetail)),
		fmt.Sprintf("Stop Ack: %s", connectionStatusText(report.StopAckOK, report.StopAckDetail)),
	}

	if strings.TrimSpace(report.StartAckRaw) != "" {
		lines = append(lines, fmt.Sprintf("Start Ack Raw: %s", report.StartAckRaw))
	}
	if strings.TrimSpace(report.StopAckRaw) != "" {
		lines = append(lines, fmt.Sprintf("Stop Ack Raw: %s", report.StopAckRaw))
	}

	if strings.TrimSpace(report.ListBeforeErr) != "" {
		lines = append(lines, fmt.Sprintf("FTP Before List: FAILED (%s)", report.ListBeforeErr))
	} else {
		lines = append(lines, fmt.Sprintf("FTP Before List Count: %d", report.BeforeCount))
	}

	if strings.TrimSpace(report.ListAfterErr) != "" {
		lines = append(lines, fmt.Sprintf("FTP After List: FAILED (%s)", report.ListAfterErr))
	} else {
		lines = append(lines, fmt.Sprintf("FTP After List Count: %d", report.AfterCount))
	}

	if len(report.NewFiles) == 0 {
		lines = append(lines, "New Files: none")
	} else {
		lines = append(lines, fmt.Sprintf("New Files: %s", strings.Join(report.NewFiles, ", ")))
	}

	return strings.Join(lines, "\n")
}

func formatDwarfRawWSReport(report DwarfRawWSReport) string {
	lines := []string{
		fmt.Sprintf("Host: %s", report.Host),
		fmt.Sprintf("WebSocket Port: %d", report.WSPort),
		fmt.Sprintf("Treat Timeout As Success: %t", report.AllowTimeoutSuccess),
		fmt.Sprintf("Payload: %s", report.Payload),
	}

	if strings.TrimSpace(report.ResponseRaw) != "" {
		lines = append(lines, fmt.Sprintf("Response Raw: %s", report.ResponseRaw))
	} else {
		lines = append(lines, "Response Raw: <empty>")
	}

	if strings.TrimSpace(report.Err) != "" {
		lines = append(lines, fmt.Sprintf("Error: %s", report.Err))
	} else {
		lines = append(lines, "Error: <none>")
	}

	return strings.Join(lines, "\n")
}

func selectDwarfMediaFile(files []DwarfMediaFile, downloaded map[string]DwarfQueuedRecording, camera string, recordingName string, recordingStartedAt time.Time) *DwarfMediaFile {
	if recordingName != "" {
		for i := range files {
			if _, seen := downloaded[files[i].Path]; seen {
				continue
			}
			if dwarfMediaMatchesRecording(files[i], camera, recordingName, recordingStartedAt) {
				return &files[i]
			}
		}
	}

	for i := range files {
		if _, seen := downloaded[files[i].Path]; seen {
			continue
		}
		if !dwarfMediaMatchesCamera(files[i], camera) {
			continue
		}
		return &files[i]
	}
	return nil
}

func dwarfMediaMatchesRecording(file DwarfMediaFile, camera string, recordingName string, recordingStartedAt time.Time) bool {
	if !dwarfMediaMatchesCamera(file, camera) {
		return false
	}

	baseName := strings.TrimSuffix(file.Name, filepath.Ext(file.Name))
	if baseName == recordingName || strings.HasPrefix(baseName, recordingName) || strings.Contains(baseName, recordingName) {
		return true
	}

	if !recordingStartedAt.IsZero() {
		if recordedAt, ok := parseDwarfVideoTimestamp(file.Name); ok {
			delta := recordedAt.Sub(recordingStartedAt)
			if delta < 0 {
				delta = -delta
			}
			if delta <= 15*time.Second {
				return true
			}
		}
	}

	if !recordingStartedAt.IsZero() && !file.ModTime.IsZero() {
		// Fall back to FTP modtime if the filename could not be parsed.
		if !file.ModTime.Before(recordingStartedAt.Add(-10 * time.Second)) {
			return true
		}
	}

	return false
}

func parseDwarfVideoTimestamp(fileName string) (time.Time, bool) {
	baseName := strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	for _, prefix := range []string{"DWARF3_TELE_", "DWARF3_WIDE_", "DWARF_TELE_", "DWARF_WIDE_"} {
		if !strings.HasPrefix(baseName, prefix) {
			continue
		}

		stamp := strings.TrimPrefix(baseName, prefix)
		parsed, err := time.ParseInLocation("2006-01-02-15-04-05-000", stamp, time.Local)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func dwarfMediaMatchesCamera(file DwarfMediaFile, camera string) bool {
	name := strings.TrimSuffix(filepath.Base(file.Name), filepath.Ext(file.Name))
	for _, prefix := range dwarfCameraFilePrefixes(camera) {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func dwarfCameraFromLabel(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "wide":
		return dwarfCameraWide
	default:
		return dwarfCameraTele
	}
}

func (ui *trackerApp) enqueueDwarfFile(recording DwarfQueuedRecording) {
	ui.mu.Lock()
	ui.dwarfQueuedFiles = append(ui.dwarfQueuedFiles, recording)
	queueCount := len(ui.dwarfQueuedFiles)
	ui.mu.Unlock()

	ui.dwarfQueueLabel.SetText(fmt.Sprintf("DWARF queue: %d   latest: %s (%s)", queueCount, filepath.Base(recording.LocalPath), strings.ToUpper(recording.Camera)))
	ui.maybeStartDwarfQueueProcessor()
}

func (ui *trackerApp) maybeStartDwarfQueueProcessor() {
	ui.mu.Lock()
	if ui.dwarfQueueProcessing || ui.running || len(ui.dwarfQueuedFiles) == 0 {
		ui.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	ui.dwarfQueueProcessing = true
	ui.running = true
	ui.stopCh = stopCh
	ui.mu.Unlock()

	ui.startButton.Disable()
	ui.stopButton.Enable()
	ui.statusLabel.SetText("Processing DWARF queue...")
	go ui.runDwarfQueueProcessor(stopCh)
}

func (ui *trackerApp) runDwarfQueueProcessor(stopCh chan struct{}) {
	err := ui.processDwarfQueue(stopCh)
	fyne.Do(func() {
		ui.finishDwarfQueueProcessing(err)
	})
}

func (ui *trackerApp) processDwarfQueue(stopCh <-chan struct{}) error {
	for {
		select {
		case <-stopCh:
			return ErrStopTracking
		default:
		}

		ui.mu.Lock()
		if len(ui.dwarfQueuedFiles) == 0 {
			ui.mu.Unlock()
			return nil
		}
		recording := ui.dwarfQueuedFiles[0]
		ui.dwarfQueuedFiles = ui.dwarfQueuedFiles[1:]
		remaining := len(ui.dwarfQueuedFiles)
		ui.mu.Unlock()

		fyne.Do(func() {
			ui.dwarfQueueLabel.SetText(fmt.Sprintf("DWARF queue: %d   processing: %s (%s)", remaining, filepath.Base(recording.LocalPath), strings.ToUpper(recording.Camera)))
			ui.statusLabel.SetText("Processing queued DWARF video...")
		})

		processingRecording, err := moveDwarfQueuedRecordingToStage(recording, dwarfQueueStageUnderProcessing)
		if err != nil {
			return err
		}
		ui.updateDownloadedDwarfRecording(processingRecording)

		config, err := ui.buildQueuedDwarfTrackerConfig(processingRecording.LocalPath)
		if err != nil {
			return err
		}
		if err := ui.executeTracker(config, stopCh); err != nil {
			return err
		}

		processedRecording, err := moveDwarfQueuedRecordingToStage(processingRecording, dwarfQueueStageProcessed)
		if err != nil {
			return err
		}
		ui.updateDownloadedDwarfRecording(processedRecording)

		fyne.Do(func() {
			ui.dwarfStatusLabel.SetText(fmt.Sprintf("DWARF processed: %s (%s)", filepath.Base(processedRecording.LocalPath), strings.ToUpper(processedRecording.Camera)))
			ui.refreshEventHistory()
		})
	}
}

func (ui *trackerApp) finishDwarfQueueProcessing(err error) {
	ui.mu.Lock()
	ui.dwarfQueueProcessing = false
	ui.running = false
	ui.stopCh = nil
	pending := len(ui.dwarfQueuedFiles)
	ui.mu.Unlock()

	ui.startButton.Enable()
	ui.stopButton.Disable()
	if pending > 0 {
		ui.dwarfQueueLabel.SetText(fmt.Sprintf("DWARF queue: %d", pending))
	} else {
		ui.dwarfQueueLabel.SetText("DWARF queue: 0")
	}

	switch {
	case err == nil || errors.Is(err, ErrStopTracking):
		ui.statusLabel.SetText("Idle")
	default:
		ui.statusLabel.SetText("Stopped with error")
		ui.showError(err)
	}
}

func (ui *trackerApp) updateDownloadedDwarfRecording(recording DwarfQueuedRecording) {
	ui.mu.Lock()
	ui.dwarfDownloadedFiles[recording.RemotePath] = recording
	ui.mu.Unlock()
}

func moveDwarfQueuedRecordingToStage(recording DwarfQueuedRecording, stage string) (DwarfQueuedRecording, error) {
	stageDir := dwarfQueueStageDir(dwarfQueuedRecordingSessionDir(recording.LocalPath), stage)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return DwarfQueuedRecording{}, fmt.Errorf("create dwarf %s directory: %w", stage, err)
	}

	nextPath := filepath.Join(stageDir, filepath.Base(recording.LocalPath))
	if err := os.Rename(recording.LocalPath, nextPath); err != nil {
		return DwarfQueuedRecording{}, fmt.Errorf("move dwarf recording to %s: %w", stage, err)
	}
	recording.LocalPath = nextPath
	return recording, nil
}

func dwarfQueuedRecordingSessionDir(localPath string) string {
	return filepath.Dir(filepath.Dir(localPath))
}

func (ui *trackerApp) startTracking() {
	ui.mu.Lock()
	if ui.running {
		ui.mu.Unlock()
		return
	}
	ui.mu.Unlock()

	config, err := ui.buildConfig()
	if err != nil {
		ui.showError(err)
		return
	}

	stopCh := make(chan struct{})

	ui.mu.Lock()
	ui.running = true
	ui.stopCh = stopCh
	ui.pendingRunStatus = ""
	ui.pendingRunError = nil
	ui.mu.Unlock()

	ui.startButton.Disable()
	ui.stopButton.Enable()
	ui.statusLabel.SetText("Starting tracker...")
	ui.eventLabel.SetText("No event yet")
	ui.resetImages()

	go ui.runTracker(config, stopCh)
}

func (ui *trackerApp) stopTracking() {
	ui.mu.Lock()
	defer ui.mu.Unlock()

	if !ui.running || ui.stopCh == nil {
		return
	}

	close(ui.stopCh)
	ui.stopCh = nil
	ui.statusLabel.SetText("Stopping tracker...")
}

func (ui *trackerApp) togglePlaybackPause() {
	ui.mu.Lock()
	defer ui.mu.Unlock()

	if !ui.playbackActive {
		return
	}

	ui.playbackPaused = !ui.playbackPaused
	paused := ui.playbackPaused
	label := "Pause"
	icon := theme.MediaPauseIcon()
	status := "Playing"
	if paused {
		label = "Resume"
		icon = theme.MediaPlayIcon()
		status = "Paused"
	}
	ui.playPauseButton.SetText(label)
	ui.playPauseButton.SetIcon(icon)
	ui.statusLabel.SetText(fmt.Sprintf("%s %s", status, ui.currentPlaybackLabelLocked()))
}

func (ui *trackerApp) handlePlaybackSliderChanged(value float64) {
	ui.mu.Lock()
	defer ui.mu.Unlock()

	if ui.updatingPlaybackUI {
		return
	}

	frame := int(math.Round(value))
	if frame < 0 {
		frame = 0
	}
	if ui.playbackTotalFrames > 0 && frame >= ui.playbackTotalFrames {
		frame = ui.playbackTotalFrames - 1
	}

	ui.playbackCurrentFrame = frame
	ui.playbackLabel.SetText(ui.formatPlaybackLabelLocked())

	if !ui.playbackActive {
		return
	}

	ui.playbackPaused = true
	ui.playbackSeekFrame = frame
	ui.playPauseButton.SetText("Resume")
	ui.playPauseButton.SetIcon(theme.MediaPlayIcon())
	ui.statusLabel.SetText(fmt.Sprintf("Paused %s", ui.currentPlaybackLabelLocked()))
}

func (ui *trackerApp) resetPlaybackControls() {
	ui.mu.Lock()
	ui.playbackActive = false
	ui.playbackPaused = false
	ui.playbackSeekFrame = -1
	ui.playbackCurrentFrame = 0
	ui.playbackTotalFrames = 0
	ui.playbackFPS = 0
	ui.mu.Unlock()

	ui.updatingPlaybackUI = true
	ui.playbackSlider.Min = 0
	ui.playbackSlider.Max = 1
	ui.playbackSlider.Step = 1
	ui.playbackSlider.SetValue(0)
	ui.updatingPlaybackUI = false
	ui.playbackSlider.Disable()
	ui.playPauseButton.SetText("Pause")
	ui.playPauseButton.SetIcon(theme.MediaPauseIcon())
	ui.playPauseButton.Disable()
	ui.playbackLabel.SetText("00:00 / 00:00")
}

func (ui *trackerApp) startPlaybackControls(totalFrames int, fps float64) {
	if totalFrames < 1 {
		totalFrames = 1
	}
	if fps <= 0 {
		fps = 30
	}

	ui.mu.Lock()
	ui.playbackActive = true
	ui.playbackPaused = false
	ui.playbackSeekFrame = -1
	ui.playbackCurrentFrame = 0
	ui.playbackTotalFrames = totalFrames
	ui.playbackFPS = fps
	label := ui.formatPlaybackLabelLocked()
	ui.mu.Unlock()

	ui.updatingPlaybackUI = true
	ui.playbackSlider.Min = 0
	ui.playbackSlider.Max = float64(totalFrames - 1)
	ui.playbackSlider.Step = 1
	ui.playbackSlider.SetValue(0)
	ui.updatingPlaybackUI = false
	ui.playbackSlider.Enable()
	ui.playPauseButton.SetText("Pause")
	ui.playPauseButton.SetIcon(theme.MediaPauseIcon())
	ui.playPauseButton.Enable()
	ui.playbackLabel.SetText(label)
}

func (ui *trackerApp) updatePlaybackFrame(frameIndex int, fps float64) {
	if frameIndex < 0 {
		frameIndex = 0
	}
	ui.mu.Lock()
	if ui.playbackTotalFrames > 0 && frameIndex >= ui.playbackTotalFrames {
		frameIndex = ui.playbackTotalFrames - 1
	}
	ui.playbackCurrentFrame = frameIndex
	label := ui.formatPlaybackLabelLocked()
	status := "Playing"
	if ui.playbackPaused {
		status = "Paused"
	}
	playbackStatus := ui.currentPlaybackLabelLocked()
	ui.mu.Unlock()

	ui.updatingPlaybackUI = true
	ui.playbackSlider.SetValue(float64(frameIndex))
	ui.updatingPlaybackUI = false
	ui.playbackLabel.SetText(label)
	ui.statusLabel.SetText(fmt.Sprintf("%s %s at %.3f FPS", status, playbackStatus, fps))
}

func (ui *trackerApp) currentPlaybackState() (paused bool, seekFrame int) {
	ui.mu.Lock()
	defer ui.mu.Unlock()

	paused = ui.playbackPaused
	seekFrame = ui.playbackSeekFrame
	ui.playbackSeekFrame = -1
	return paused, seekFrame
}

func (ui *trackerApp) currentPlaybackLabelLocked() string {
	totalFrames := ui.playbackTotalFrames
	if totalFrames < 1 {
		totalFrames = 1
	}
	return fmt.Sprintf("frame %d/%d   %s", ui.playbackCurrentFrame+1, totalFrames, ui.formatPlaybackLabelLocked())
}

func (ui *trackerApp) formatPlaybackLabelLocked() string {
	if ui.playbackTotalFrames <= 0 {
		return "00:00 / 00:00"
	}
	fps := ui.playbackFPS
	if fps <= 0 {
		fps = 30
	}
	current := time.Duration(float64(ui.playbackCurrentFrame) * float64(time.Second) / fps)
	totalFrames := ui.playbackTotalFrames - 1
	if totalFrames < 0 {
		totalFrames = 0
	}
	total := time.Duration(float64(totalFrames) * float64(time.Second) / fps)
	return fmt.Sprintf("%s / %s", formatVideoProgressDuration(current), formatVideoProgressDuration(total))
}

func (ui *trackerApp) buildConfig() (TrackerConfig, error) {
	fallbackFPS, err := strconv.ParseFloat(ui.fpsEntry.Text, 64)
	if err != nil || fallbackFPS <= 0 {
		return TrackerConfig{}, errors.New("fallback FPS must be a positive number")
	}

	settings, err := ui.buildTrackingSettings()
	if err != nil {
		return TrackerConfig{}, err
	}
	nostrSettings, err := ui.buildNostrSettings()
	if err != nil {
		return TrackerConfig{}, err
	}

	outputDir := ui.outputEntry.Text
	if outputDir == "" {
		outputDir = "events"
	}

	if ui.sourceRadio.Selected == "Video File" {
		if ui.fileEntry.Text == "" {
			return TrackerConfig{}, errors.New("video file path is required")
		}
		return TrackerConfig{
			Input:        ui.fileEntry.Text,
			InputLabel:   "video file",
			OutputDir:    outputDir,
			ShowMask:     ui.showMask.Checked,
			RecordEvents: true,
			FallbackFPS:  fallbackFPS,
			Settings:     settings,
			Nostr:        nostrSettings,
		}, nil
	}

	if ui.urlEntry.Text == "" {
		return TrackerConfig{}, errors.New("RTSP URL is required")
	}

	return TrackerConfig{
		Input:        ui.urlEntry.Text,
		InputLabel:   "DWARF 3 live stream",
		OutputDir:    outputDir,
		ShowMask:     ui.showMask.Checked,
		RecordEvents: true,
		FallbackFPS:  fallbackFPS,
		Settings:     settings,
		Nostr:        nostrSettings,
	}, nil
}

func (ui *trackerApp) buildQueuedDwarfTrackerConfig(videoPath string) (TrackerConfig, error) {
	settings, err := ui.buildTrackingSettings()
	if err != nil {
		return TrackerConfig{}, err
	}
	nostrSettings, err := ui.buildNostrSettings()
	if err != nil {
		return TrackerConfig{}, err
	}

	outputDir := ui.outputEntry.Text
	if outputDir == "" {
		outputDir = "events"
	}

	return TrackerConfig{
		Input:        videoPath,
		InputLabel:   "video file",
		OutputDir:    outputDir,
		ShowMask:     ui.showMask.Checked,
		RecordEvents: true,
		FallbackFPS:  ui.parseFallbackFPS(),
		Settings:     settings,
		Nostr:        nostrSettings,
	}, nil
}

func (ui *trackerApp) parseFallbackFPS() float64 {
	fallbackFPS, err := strconv.ParseFloat(ui.fpsEntry.Text, 64)
	if err != nil || fallbackFPS <= 0 {
		return 30
	}
	return fallbackFPS
}

func (ui *trackerApp) runTracker(config TrackerConfig, stopCh chan struct{}) {
	err := ui.executeTracker(config, stopCh)
	fyne.Do(func() {
		ui.finishRun(err, "Idle")
	})
}

func (ui *trackerApp) executeTracker(config TrackerConfig, stopCh <-chan struct{}) error {
	savedEventDirs := make([]string, 0, 4)
	engine := TrackerEngine{
		Config: config,
		Stop:   stopCh,
		Hooks: TrackerHooks{
			OnReady: func(ready TrackerReady) error {
				fyne.Do(func() {
					statusText := fmt.Sprintf("Running %s at %.3f FPS (%dx%d)", ready.InputLabel, ready.FPS, ready.Width, ready.Height)
					if ready.HasFixedLength {
						statusText = fmt.Sprintf("%s   video length %s   %d frames",
							statusText,
							formatVideoProgressDuration(ready.Duration),
							ready.TotalFrames)
					}
					ui.statusLabel.SetText(statusText)
				})
				return nil
			},
			OnEventStart: func(dir string) error {
				fyne.Do(func() {
					ui.eventLabel.SetText("Recording: " + dir)
				})
				return nil
			},
			OnEventSaved: func(dir string) error {
				savedEventDirs = append(savedEventDirs, dir)
				fyne.Do(func() {
					ui.eventLabel.SetText("Saved: " + dir)
					ui.refreshEventHistory()
				})
				return nil
			},
			OnFrame: func(update *FrameUpdate) error {
				displayImage, err := update.Display.ToImage()
				if err != nil {
					return err
				}

				var maskImage image.Image
				if update.hasMask {
					maskImage, err = update.Mask.ToImage()
					if err != nil {
						return err
					}
				}

				statusText := update.StatusText
				if update.ProgressText != "" {
					statusText += "   " + update.ProgressText
				}
				if update.Recording {
					statusText += "   recording"
				}
				if update.LearningBackground {
					statusText += "   learning background"
				}

				fyne.Do(func() {
					ui.videoImage.Image = displayImage
					ui.videoImage.Refresh()
					if maskImage != nil {
						ui.maskImage.Image = maskImage
						ui.maskImage.Refresh()
					}
					ui.statusLabel.SetText(statusText)
				})
				return nil
			},
		},
	}

	err := engine.Run()
	if err == nil && config.InputLabel == "video file" && config.Nostr.Enabled {
		if publishErr := ui.publishCompletedVideoAnalysisNostrNote(savedEventDirs, config); publishErr != nil {
			fmt.Fprintf(os.Stderr, "nostr publish failed for %s: %v\n", config.Input, publishErr)
			ui.mu.Lock()
			ui.pendingRunStatus = "Video analysis finished, but Nostr publish failed"
			ui.pendingRunError = publishErr
			ui.mu.Unlock()
		} else {
			ui.mu.Lock()
			ui.pendingRunStatus = "Video analysis finished and published Nostr note"
			ui.pendingRunError = nil
			ui.mu.Unlock()
		}
	}
	if err == nil && config.InputLabel == "video file" && NormalizeTrackingSettings(config.Settings).RawSegmentDuration > 0 && engine.RawSegmentDir != "" {
		fyne.Do(func() {
			ui.statusLabel.SetText("Processing raw segments in parallel...")
		})
		_, processErr := processRawSegmentsParallel(engine.RawSegmentDir, config.Settings, config.FallbackFPS, stopCh)
		if processErr != nil {
			err = processErr
		} else {
			fyne.Do(func() {
				ui.eventLabel.SetText("Merged segment tracking: " + filepath.Join(engine.RawSegmentDir, "processed", "merged_tracking.json"))
			})
		}
	}
	return err
}

func (ui *trackerApp) runPlayback(path, label, maskPath string, stopCh chan struct{}, overlay *playbackOverlay) {
	capture, err := gocv.VideoCaptureFile(path)
	if err != nil {
		fyne.Do(func() {
			ui.finishRun(fmt.Errorf("open playback: %w", err), "Idle")
		})
		return
	}
	defer capture.Close()

	var maskCapture *gocv.VideoCapture
	if maskPath != "" {
		maskCapture, err = gocv.VideoCaptureFile(maskPath)
		if err == nil && maskCapture.IsOpened() {
			defer maskCapture.Close()
		} else {
			if maskCapture != nil {
				maskCapture.Close()
			}
			maskCapture = nil
		}
	}

	if !capture.IsOpened() {
		fyne.Do(func() {
			ui.finishRun(errors.New("playback source did not open"), "Idle")
		})
		return
	}

	fps := capture.Get(gocv.VideoCaptureFPS)
	if fps <= 1 {
		fps = ui.parseFallbackFPS()
	}
	frameDelay := time.Duration(float64(time.Second) / fps)
	if frameDelay <= 0 {
		frameDelay = time.Second / 30
	}
	totalFrames := int(math.Round(capture.Get(gocv.VideoCaptureFrameCount)))
	if totalFrames <= 0 && overlay != nil && len(overlay.Tracking.Frames) > 0 {
		totalFrames = len(overlay.Tracking.Frames)
	}
	if totalFrames <= 0 {
		totalFrames = 1
	}

	frame := gocv.NewMat()
	defer frame.Close()
	maskFrame := gocv.NewMat()
	defer maskFrame.Close()
	cropFrame := gocv.NewMat()
	defer cropFrame.Close()
	var overlayFrame gocv.Mat
	var objectMapCanvas *image.RGBA
	var lastObjectImage image.Image
	objectMapSeen := 0
	frameIndex := 0
	if overlay != nil && overlay.StartFrame > 0 {
		frameIndex = overlay.StartFrame
	}
	placeholder := newPlaceholderFrame()
	if overlay != nil && overlay.SelectedObject != nil {
		lastObjectImage = loadObjectListPreview(*overlay.SelectedObject)
	}
	if frameIndex > 0 {
		capture.Set(gocv.VideoCapturePosFrames, float64(frameIndex))
		if maskCapture != nil {
			maskCapture.Set(gocv.VideoCapturePosFrames, float64(frameIndex))
		}
	}

	fyne.Do(func() {
		ui.startPlaybackControls(totalFrames, fps)
		if frameIndex > 0 {
			ui.updatePlaybackFrame(frameIndex, fps)
		}
		ui.statusLabel.SetText(fmt.Sprintf("Playing %s at %.3f FPS", label, fps))
		ui.eventLabel.SetText(path)
		ui.tabs.SelectIndex(0)
	})

	for {
		select {
		case <-stopCh:
			fyne.Do(func() {
				ui.finishRun(ErrStopTracking, "Idle")
			})
			return
		default:
		}

		paused, seekFrame := ui.currentPlaybackState()
		if seekFrame >= 0 {
			if seekFrame >= totalFrames {
				seekFrame = totalFrames - 1
			}
			if seekFrame < 0 {
				seekFrame = 0
			}
			frameIndex = seekFrame
			objectMapCanvas = nil
			objectMapSeen = 0
			lastObjectImage = nil
			capture.Set(gocv.VideoCapturePosFrames, float64(frameIndex))
			if maskCapture != nil {
				maskCapture.Set(gocv.VideoCapturePosFrames, float64(frameIndex))
			}
		} else if paused {
			time.Sleep(30 * time.Millisecond)
			continue
		}

		if ok := capture.Read(&frame); !ok || frame.Empty() {
			break
		}

		if objectMapCanvas == nil && overlay != nil && overlay.SelectedTrackID > 0 {
			var err error
			objectMapCanvas, err = newObjectMapCanvas(frame)
			if err != nil {
				fyne.Do(func() {
					ui.finishRun(err, "Idle")
				})
				return
			}
		}

		displayMat := frame
		hasOverlayFrame := false
		if overlay != nil && frameIndex < len(overlay.Tracking.Frames) {
			overlayFrame = frame.Clone()
			hasOverlayFrame = true
			frameMeta := overlay.Tracking.Frames[frameIndex]
			frameTracks := frameMeta.Tracks
			if overlay.frameTracksEnabled() {
				if overlay.SelectedTrackID > 0 {
					if selectedTrack, ok := selectedTrackInFrame(frameMeta, overlay.SelectedTrackID); ok {
						drawSelectedReplayOverlay(&overlayFrame, selectedTrack, overlay.SelectedObject, overlay.Settings)
					}
				} else {
					drawMetadataOverlay(&overlayFrame, frameTracks, overlay.Settings)
				}
			}
			showFinalPositions, minDistance, objects := overlay.finalPositionConfig()
			if showFinalPositions {
				drawFinalPositionOverlay(&overlayFrame, objects, minDistance)
			}
			displayMat = overlayFrame
		}

		displayImage, err := displayMat.ToImage()
		if hasOverlayFrame {
			overlayFrame.Close()
		}
		if err != nil {
			fyne.Do(func() {
				ui.finishRun(err, "Idle")
			})
			return
		}
		displayImage = cloneImage(displayImage)

		var maskImage image.Image
		if maskCapture != nil {
			if ok := maskCapture.Read(&maskFrame); ok && !maskFrame.Empty() {
				maskImage, err = maskFrame.ToImage()
				if err != nil {
					fyne.Do(func() {
						ui.finishRun(err, "Idle")
					})
					return
				}
				maskImage = cloneImage(maskImage)
			}
		}

		var objectImage image.Image
		if overlay != nil && overlay.SelectedTrackID > 0 && frameIndex < len(overlay.Tracking.Frames) && overlay.CropBySourceFrame != nil {
			frameMeta := overlay.Tracking.Frames[frameIndex]
			sourceFrame := frameMeta.SourceFrame
			selectedTrack, hasSelectedTrack := selectedTrackInFrame(frameMeta, overlay.SelectedTrackID)
			if cropPath := lookupObjectCropPath(overlay, sourceFrame); cropPath != "" {
				cropFrame = gocv.IMRead(cropPath, gocv.IMReadColor)
				if !cropFrame.Empty() {
					objectImage, err = cropFrame.ToImage()
					if err == nil {
						objectImage = cloneImage(objectImage)
					}
					if err == nil && objectMapCanvas != nil && hasSelectedTrack {
						if objectMapSeen%(overlay.ObjectMapSkipCount+1) == 0 {
							maskCropImage, maskErr := extractObjectMaskImage(maskFrame, selectedTrack)
							if maskErr == nil {
								addObjectCropToMap(objectMapCanvas, objectImage, maskCropImage, selectedTrack)
							}
						}
						objectMapSeen++
					}
					cropFrame.Close()
					if err != nil {
						fyne.Do(func() {
							ui.finishRun(err, "Idle")
						})
						return
					}
				} else {
					cropFrame.Close()
				}
			}
		}
		if objectImage != nil {
			lastObjectImage = objectImage
		}

		done := make(chan struct{})
		fyne.Do(func() {
			ui.videoImage.Image = displayImage
			ui.videoImage.Refresh()
			if maskImage != nil {
				ui.maskImage.Image = maskImage
			} else {
				ui.maskImage.Image = placeholder
			}
			ui.maskImage.Refresh()
			if objectImage != nil {
				ui.objectImage.Image = objectImage
			} else if lastObjectImage != nil {
				ui.objectImage.Image = lastObjectImage
			} else {
				ui.objectImage.Image = placeholder
			}
			ui.objectImage.Refresh()
			if objectMapCanvas != nil {
				if overlay != nil && overlay.SelectedObject != nil {
					if rendered := renderObjectMapImage(objectMapCanvas, *overlay.SelectedObject); rendered != nil {
						ui.objectMapImage.Image = rendered
					} else {
						ui.objectMapImage.Image = objectMapCanvas
					}
				} else {
					ui.objectMapImage.Image = objectMapCanvas
				}
			} else {
				ui.objectMapImage.Image = placeholder
			}
			ui.objectMapImage.Refresh()
			ui.updatePlaybackFrame(frameIndex, fps)
			close(done)
		})
		<-done

		frameIndex++
		if paused || seekFrame >= 0 {
			continue
		}
		time.Sleep(frameDelay)
	}

	fyne.Do(func() {
		ui.finishRun(nil, "Playback finished")
	})
}

func (ui *trackerApp) finishRun(err error, idleText string) {
	ui.mu.Lock()
	ui.running = false
	ui.stopCh = nil
	ui.activePlaybackOverlay = nil
	pendingRunStatus := ui.pendingRunStatus
	pendingRunError := ui.pendingRunError
	ui.pendingRunStatus = ""
	ui.pendingRunError = nil
	ui.mu.Unlock()

	ui.startButton.Enable()
	ui.stopButton.Disable()
	ui.resetPlaybackControls()

	switch {
	case err == nil || errors.Is(err, ErrStopTracking):
		if pendingRunStatus != "" {
			ui.statusLabel.SetText(pendingRunStatus)
			if pendingRunError != nil {
				ui.showError(pendingRunError)
			}
		} else {
			ui.statusLabel.SetText(idleText)
		}
	default:
		ui.statusLabel.SetText("Stopped with error")
		ui.showError(err)
	}

	ui.maybeStartDwarfQueueProcessor()
}

func (ui *trackerApp) refreshEventHistory() {
	entries, err := loadEventHistory(ui.outputEntry.Text)
	if err != nil {
		ui.allHistoryEntries = nil
		ui.historyEntries = nil
		ui.selectedHistory = -1
		ui.currentDetail = nil
		ui.resetObjectSelection("Could not load event objects.")
		ui.historyList.Refresh()
		ui.historyInfo.SetText("Could not load events")
		ui.openTrackedButton.Disable()
		ui.openOriginalButton.Disable()
		ui.restoreSettingsButton.Disable()
		ui.showError(err)
		return
	}

	ui.allHistoryEntries = entries
	ui.selectedHistory = -1
	ui.currentDetail = nil
	ui.applyHistoryFilters()

	if len(entries) == 0 {
		ui.historyInfo.SetText("No saved events found")
		ui.historyDetail.SetText("No saved events found in the selected output directory.")
		ui.resetObjectSelection("No tracked objects available.")
		ui.openTrackedButton.Disable()
		ui.openOriginalButton.Disable()
		ui.restoreSettingsButton.Disable()
		return
	}
}

func (ui *trackerApp) updateHistorySelection() {
	if ui.selectedHistory < 0 || ui.selectedHistory >= len(ui.historyEntries) {
		ui.currentDetail = nil
		ui.historyInfo.SetText("Select an event")
		ui.historyDetail.SetText("Select an event to inspect event.json and tracking.json.")
		ui.resetObjectSelection("Select an event, then select an object to inspect its metadata.")
		ui.openTrackedButton.Disable()
		ui.openOriginalButton.Disable()
		ui.restoreSettingsButton.Disable()
		return
	}

	entry := ui.historyEntries[ui.selectedHistory]
	detail, err := loadEventDetail(entry)
	if err != nil {
		ui.currentDetail = nil
		ui.historyInfo.SetText(entry.title())
		ui.historyDetail.SetText(fmt.Sprintf("Could not load detail preview:\n%v", err))
		ui.resetObjectSelection("Could not load tracked objects for this event.")
		ui.openTrackedButton.Enable()
		ui.openOriginalButton.Enable()
		ui.restoreSettingsButton.Enable()
		return
	}

	ui.currentDetail = &detail
	ui.historyInfo.SetText(fmt.Sprintf("%s   %.1fs   %d objects", entry.title(), entry.Summary.DurationSeconds, entry.Summary.UniqueObjects))
	ui.historyDetail.SetText(formatEventDetail(detail, entry.Directory))
	ui.populateObjectSelection(detail)
	ui.openTrackedButton.Enable()
	ui.openOriginalButton.Enable()
	ui.restoreSettingsButton.Enable()
}

func (ui *trackerApp) openSelectedEventVideo(tracked bool) {
	ui.mu.Lock()
	running := ui.running
	ui.mu.Unlock()

	if running {
		ui.showInfo("Busy", "Stop the current tracker or playback first.")
		return
	}

	if ui.selectedHistory < 0 || ui.selectedHistory >= len(ui.historyEntries) {
		ui.showInfo("No Event Selected", "Select an event from the history list first.")
		return
	}

	entry := ui.historyEntries[ui.selectedHistory]
	detail, err := loadEventDetail(entry)
	if err != nil {
		ui.showError(err)
		return
	}
	filename := "original.avi"
	label := "original"
	var overlay *playbackOverlay
	if tracked {
		filename = "tracked.avi"
		label = "tracked"
		if _, err := os.Stat(filepath.Join(entry.Directory, filename)); err != nil && detail.HasTracking && detail.Tracking != nil {
			overlay = &playbackOverlay{
				Tracking:        *detail.Tracking,
				Settings:        NormalizeTrackingSettings(detail.Summary.TrackingSettings),
				Label:           label,
				ShowFrameTracks: true,
			}
			filename = "original.avi"
			label = "tracked (reconstructed)"
		}
	}

	path := filepath.Join(entry.Directory, filename)
	if _, err := os.Stat(path); err != nil {
		ui.showError(fmt.Errorf("open %s: %w", filename, err))
		return
	}

	maskPath := filepath.Join(entry.Directory, entry.Summary.MaskedVideo)
	if entry.Summary.MaskedVideo == "" {
		maskPath = filepath.Join(entry.Directory, "masked.avi")
	}

	ui.sourceRadio.SetSelected("Video File")
	ui.fileEntry.SetText(path)
	ui.refreshSourceControls()

	stopCh := make(chan struct{})
	ui.mu.Lock()
	ui.running = true
	ui.stopCh = stopCh
	ui.activePlaybackOverlay = overlay
	ui.mu.Unlock()

	ui.startButton.Disable()
	ui.stopButton.Enable()
	ui.statusLabel.SetText("Starting playback...")
	ui.eventLabel.SetText(path)
	ui.resetImages()

	go ui.runPlayback(path, label, maskPath, stopCh, overlay)
}

func (ui *trackerApp) applyHistoryFilters() {
	dateContains := ui.dateFilter.Text
	minObjects, objectFilterValid := parseOptionalInt(ui.objectFilter.Text)
	minSpeed, speedFilterValid := parseOptionalFloat(ui.speedFilter.Text)

	filtered := make([]eventHistoryEntry, 0, len(ui.allHistoryEntries))
	for _, entry := range ui.allHistoryEntries {
		if dateContains != "" && !matchesDateFilter(entry, dateContains) {
			continue
		}
		if objectFilterValid && entry.Summary.UniqueObjects < minObjects {
			continue
		}
		if speedFilterValid && entry.Summary.HighestSpeedPxSec < minSpeed {
			continue
		}
		filtered = append(filtered, entry)
	}

	sortHistoryEntries(filtered, ui.sortSelect.Selected)

	ui.historyEntries = filtered
	ui.selectedHistory = -1
	ui.currentDetail = nil
	ui.historyList.UnselectAll()
	ui.historyList.Refresh()
	ui.resetObjectSelection("Select an event, then select an object to inspect its metadata.")
	ui.openTrackedButton.Disable()
	ui.openOriginalButton.Disable()
	ui.restoreSettingsButton.Disable()

	switch {
	case !objectFilterValid:
		ui.historyInfo.SetText("Min objects filter must be an integer")
		ui.historyDetail.SetText("Fix the Min Objects filter to apply it.")
	case !speedFilterValid:
		ui.historyInfo.SetText("Min peak speed filter must be a number")
		ui.historyDetail.SetText("Fix the Min Peak Speed filter to apply it.")
	case len(ui.allHistoryEntries) == 0:
		ui.historyInfo.SetText("No saved events found")
		ui.historyDetail.SetText("No saved events found in the selected output directory.")
	case len(filtered) == 0:
		ui.historyInfo.SetText("No events match the current filters")
		ui.historyDetail.SetText("Adjust the date, object-count, or peak-speed filters.")
	default:
		ui.historyInfo.SetText(fmt.Sprintf("%d of %d events match filters", len(filtered), len(ui.allHistoryEntries)))
		ui.historyDetail.SetText("Select an event to inspect event.json and tracking.json.")
	}
}

func (ui *trackerApp) restoreSettingsFromSelectedEvent() {
	if ui.selectedHistory < 0 || ui.selectedHistory >= len(ui.historyEntries) {
		ui.showInfo("No Event Selected", "Select an event from the history list first.")
		return
	}

	entry := ui.historyEntries[ui.selectedHistory]
	detail, err := loadEventDetail(entry)
	if err != nil {
		ui.showError(err)
		return
	}

	settings := NormalizeTrackingSettings(detail.Summary.TrackingSettings)
	ui.applyTrackingSettingsToForm(settings)
	ui.saveTrackingPreferences()
	ui.statusLabel.SetText("Restored tracking settings from selected event")
}

func (ui *trackerApp) resetTrackingSettings() {
	ui.applyTrackingSettingsToForm(DefaultTrackingSettings())
	ui.saveTrackingPreferences()
	ui.statusLabel.SetText("Reset tracking settings to defaults")
}

func (ui *trackerApp) importTrackingSettings() {
	picker := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			ui.showError(err)
			return
		}
		if reader == nil {
			return
		}
		defer reader.Close()

		var settings TrackingSettings
		if err := json.NewDecoder(reader).Decode(&settings); err != nil {
			ui.showError(fmt.Errorf("decode tracking settings: %w", err))
			return
		}

		settings = NormalizeTrackingSettings(settings)
		ui.applyTrackingSettingsToForm(settings)
		ui.saveTrackingPreferences()
		ui.statusLabel.SetText("Imported tracking settings from JSON")
	}, ui.window)
	picker.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
	picker.Show()
}

func (ui *trackerApp) exportTrackingSettings() {
	settings, err := ui.buildTrackingSettings()
	if err != nil {
		ui.showError(err)
		return
	}

	saver := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			ui.showError(err)
			return
		}
		if writer == nil {
			return
		}
		defer writer.Close()

		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(settings); err != nil {
			ui.showError(fmt.Errorf("write tracking settings: %w", err))
			return
		}

		ui.statusLabel.SetText("Exported tracking settings to JSON")
	}, ui.window)
	saver.SetFileName("tracking_settings.json")
	saver.Show()
}

func (ui *trackerApp) loadHistoryPreferences() {
	prefs := fyne.CurrentApp().Preferences()

	ui.dateFilter.SetText(prefs.StringWithFallback(prefHistoryDateFilter, ""))
	ui.objectFilter.SetText(prefs.StringWithFallback(prefHistoryObjectFilter, ""))
	ui.speedFilter.SetText(prefs.StringWithFallback(prefHistorySpeedFilter, ""))

	sortMode := prefs.StringWithFallback(prefHistorySortMode, sortNewestFirst)
	if sortMode != sortNewestFirst && sortMode != sortHighestObjects && sortMode != sortHighestPeakSpeed {
		sortMode = sortNewestFirst
	}
	ui.sortSelect.SetSelected(sortMode)
}

func (ui *trackerApp) saveHistoryPreferences() {
	prefs := fyne.CurrentApp().Preferences()
	prefs.SetString(prefHistoryDateFilter, ui.dateFilter.Text)
	prefs.SetString(prefHistoryObjectFilter, ui.objectFilter.Text)
	prefs.SetString(prefHistorySpeedFilter, ui.speedFilter.Text)
	prefs.SetString(prefHistorySortMode, ui.sortSelect.Selected)
}

func (ui *trackerApp) loadTrackingPreferences() {
	prefs := fyne.CurrentApp().Preferences()
	defaults := DefaultTrackingSettings()

	ui.minAreaEntry.SetText(prefs.StringWithFallback(prefTrackingMinArea, formatFloat(defaults.MinArea)))
	ui.maxAreaEntry.SetText(prefs.StringWithFallback(prefTrackingMaxArea, formatFloat(defaults.MaxArea)))
	ui.slowSpeedEntry.SetText(prefs.StringWithFallback(prefTrackingSlowSpeed, formatFloat(defaults.SlowMinSpeed)))
	ui.minSpeedEntry.SetText(prefs.StringWithFallback(prefTrackingMinSpeed, formatFloat(defaults.MinSpeed)))
	ui.matchDistanceEntry.SetText(prefs.StringWithFallback(prefTrackingMatchDist, formatFloat(defaults.MaxMatchDistance)))
	ui.minHitsEntry.SetText(prefs.StringWithFallback(prefTrackingMinHits, strconv.Itoa(defaults.MinHits)))
	ui.blurSizeEntry.SetText(prefs.StringWithFallback(prefTrackingBlurSize, strconv.Itoa(defaults.BlurSize)))
	ui.foregroundThresholdEntry.SetText(prefs.StringWithFallback(prefTrackingThreshold, formatFloat(defaults.ForegroundThreshold)))
	ui.preEventEntry.SetText(prefs.StringWithFallback(prefTrackingPreEvent, formatFloat(defaults.PreEventDuration.Seconds())))
	ui.postEventEntry.SetText(prefs.StringWithFallback(prefTrackingPostEvent, formatFloat(defaults.PostEventDuration.Seconds())))
	ui.rawSegmentEntry.SetText(prefs.StringWithFallback(prefTrackingRawSegment, formatFloat(defaults.RawSegmentDuration.Seconds())))
	ui.rawSegmentOverlapEntry.SetText(prefs.StringWithFallback(prefTrackingRawOverlap, formatFloat(defaults.RawSegmentOverlap.Seconds())))
	ui.mog2HistoryEntry.SetText(prefs.StringWithFallback(prefTrackingMOG2History, strconv.Itoa(defaults.MOG2History)))
	ui.mog2VarThresholdEntry.SetText(prefs.StringWithFallback(prefTrackingMOG2Var, formatFloat(defaults.MOG2VarThreshold)))
	ui.roiHeightEntry.SetText(prefs.StringWithFallback(prefTrackingROIHeight, formatFloat(defaults.TrackingROIHeightFrac)))
	ui.nostrEnableCheck.SetChecked(prefs.BoolWithFallback(prefNostrEnabled, false))
	ui.nostrRelayEntry.SetText(prefs.StringWithFallback(prefNostrRelayURL, nostrutil.DefaultRelayURL))
	ui.nostrSecretEntry.SetText(prefs.StringWithFallback(prefNostrSecretKey, ""))
	ui.nostrMinDistanceEntry.SetText(prefs.StringWithFallback(prefNostrMinDistance, "800"))
	nostrTestMessage := prefs.StringWithFallback(prefNostrTestMessage, "")
	if nostrTestMessage == "" || nostrTestMessage == "Nostr integration test" {
		nostrTestMessage = defaultNostrTestMessage
	}
	ui.nostrTestMessageEntry.SetText(nostrTestMessage)
}

func (ui *trackerApp) saveTrackingPreferences() {
	prefs := fyne.CurrentApp().Preferences()
	prefs.SetString(prefTrackingMinArea, ui.minAreaEntry.Text)
	prefs.SetString(prefTrackingMaxArea, ui.maxAreaEntry.Text)
	prefs.SetString(prefTrackingSlowSpeed, ui.slowSpeedEntry.Text)
	prefs.SetString(prefTrackingMinSpeed, ui.minSpeedEntry.Text)
	prefs.SetString(prefTrackingMatchDist, ui.matchDistanceEntry.Text)
	prefs.SetString(prefTrackingMinHits, ui.minHitsEntry.Text)
	prefs.SetString(prefTrackingBlurSize, ui.blurSizeEntry.Text)
	prefs.SetString(prefTrackingThreshold, ui.foregroundThresholdEntry.Text)
	prefs.SetString(prefTrackingPreEvent, ui.preEventEntry.Text)
	prefs.SetString(prefTrackingPostEvent, ui.postEventEntry.Text)
	prefs.SetString(prefTrackingRawSegment, ui.rawSegmentEntry.Text)
	prefs.SetString(prefTrackingRawOverlap, ui.rawSegmentOverlapEntry.Text)
	prefs.SetString(prefTrackingMOG2History, ui.mog2HistoryEntry.Text)
	prefs.SetString(prefTrackingMOG2Var, ui.mog2VarThresholdEntry.Text)
	prefs.SetString(prefTrackingROIHeight, ui.roiHeightEntry.Text)
	prefs.SetBool(prefNostrEnabled, ui.nostrEnableCheck.Checked)
	prefs.SetString(prefNostrRelayURL, ui.nostrRelayEntry.Text)
	prefs.SetString(prefNostrSecretKey, ui.nostrSecretEntry.Text)
	prefs.SetString(prefNostrMinDistance, ui.nostrMinDistanceEntry.Text)
	prefs.SetString(prefNostrTestMessage, ui.nostrTestMessageEntry.Text)
}

func (ui *trackerApp) loadTrackingPresets() {
	prefs := fyne.CurrentApp().Preferences()
	encoded := prefs.StringWithFallback(prefTrackingPresets, "")
	ui.presets = make(map[string]TrackingSettings)
	if encoded != "" {
		_ = json.Unmarshal([]byte(encoded), &ui.presets)
	}
	for name, settings := range ui.presets {
		ui.presets[name] = NormalizeTrackingSettings(settings)
	}
	ui.refreshPresetOptions()
	selected := prefs.StringWithFallback(prefTrackingPresetName, "")
	if _, ok := ui.presets[selected]; ok {
		ui.presetSelect.SetSelected(selected)
	} else {
		ui.presetSelect.ClearSelected()
	}
	ui.updatePresetButtons()
}

func (ui *trackerApp) saveTrackingPresets() {
	prefs := fyne.CurrentApp().Preferences()
	data, err := json.Marshal(ui.presets)
	if err == nil {
		prefs.SetString(prefTrackingPresets, string(data))
	}
	prefs.SetString(prefTrackingPresetName, ui.presetSelect.Selected)
}

func (ui *trackerApp) refreshPresetOptions() {
	names := make([]string, 0, len(ui.presets))
	for name := range ui.presets {
		names = append(names, name)
	}
	sort.Strings(names)
	ui.presetSelect.SetOptions(names)
}

func (ui *trackerApp) updatePresetButtons() {
	hasSelection := ui.presetSelect.Selected != ""
	if hasSelection {
		ui.applyPresetButton.Enable()
		ui.deletePresetButton.Enable()
	} else {
		ui.applyPresetButton.Disable()
		ui.deletePresetButton.Disable()
	}
}

func (ui *trackerApp) applyTrackingSettingsToForm(settings TrackingSettings) {
	settings = NormalizeTrackingSettings(settings)
	ui.profileSelect.SetSelected(trackingProfileLabel(settings.Profile))
	ui.minAreaEntry.SetText(formatFloat(settings.MinArea))
	ui.maxAreaEntry.SetText(formatFloat(settings.MaxArea))
	ui.slowSpeedEntry.SetText(formatFloat(settings.SlowMinSpeed))
	ui.minSpeedEntry.SetText(formatFloat(settings.MinSpeed))
	ui.matchDistanceEntry.SetText(formatFloat(settings.MaxMatchDistance))
	ui.minHitsEntry.SetText(strconv.Itoa(settings.MinHits))
	ui.blurSizeEntry.SetText(strconv.Itoa(settings.BlurSize))
	ui.foregroundThresholdEntry.SetText(formatFloat(settings.ForegroundThreshold))
	ui.preEventEntry.SetText(formatFloat(settings.PreEventDuration.Seconds()))
	ui.postEventEntry.SetText(formatFloat(settings.PostEventDuration.Seconds()))
	ui.rawSegmentEntry.SetText(formatFloat(settings.RawSegmentDuration.Seconds()))
	ui.rawSegmentOverlapEntry.SetText(formatFloat(settings.RawSegmentOverlap.Seconds()))
	ui.mog2HistoryEntry.SetText(strconv.Itoa(settings.MOG2History))
	ui.mog2VarThresholdEntry.SetText(formatFloat(settings.MOG2VarThreshold))
	ui.roiHeightEntry.SetText(formatFloat(settings.TrackingROIHeightFrac))
}

func (ui *trackerApp) buildTrackingSettings() (TrackingSettings, error) {
	settings := DefaultTrackingSettingsForProfile(trackingProfileFromLabel(ui.profileSelect.Selected))

	var err error
	if settings.MinArea, err = parseRequiredFloat(ui.minAreaEntry.Text, "Min Area"); err != nil {
		return TrackingSettings{}, err
	}
	if settings.MaxArea, err = parseRequiredFloat(ui.maxAreaEntry.Text, "Max Area"); err != nil {
		return TrackingSettings{}, err
	}
	if settings.SlowMinSpeed, err = parseRequiredFloat(ui.slowSpeedEntry.Text, "Slow Min Speed"); err != nil {
		return TrackingSettings{}, err
	}
	if settings.MinSpeed, err = parseRequiredFloat(ui.minSpeedEntry.Text, "Fast Min Speed"); err != nil {
		return TrackingSettings{}, err
	}
	if settings.MaxMatchDistance, err = parseRequiredFloat(ui.matchDistanceEntry.Text, "Match Distance"); err != nil {
		return TrackingSettings{}, err
	}
	if settings.MinHits, err = parseRequiredInt(ui.minHitsEntry.Text, "Min Hits"); err != nil {
		return TrackingSettings{}, err
	}
	if settings.BlurSize, err = parseRequiredInt(ui.blurSizeEntry.Text, "Blur Size"); err != nil {
		return TrackingSettings{}, err
	}
	if settings.ForegroundThreshold, err = parseRequiredFloat(ui.foregroundThresholdEntry.Text, "Foreground Threshold"); err != nil {
		return TrackingSettings{}, err
	}
	preEventSeconds, err := parseRequiredFloat(ui.preEventEntry.Text, "Pre Event Seconds")
	if err != nil {
		return TrackingSettings{}, err
	}
	postEventSeconds, err := parseRequiredFloat(ui.postEventEntry.Text, "Post Event Seconds")
	if err != nil {
		return TrackingSettings{}, err
	}
	rawSegmentSeconds, err := parseRequiredFloat(ui.rawSegmentEntry.Text, "Raw Segment Seconds")
	if err != nil {
		return TrackingSettings{}, err
	}
	rawSegmentOverlapSeconds, err := parseRequiredFloat(ui.rawSegmentOverlapEntry.Text, "Raw Segment Overlap Seconds")
	if err != nil {
		return TrackingSettings{}, err
	}
	if settings.MOG2History, err = parseRequiredInt(ui.mog2HistoryEntry.Text, "MOG2 History"); err != nil {
		return TrackingSettings{}, err
	}
	if settings.MOG2VarThreshold, err = parseRequiredFloat(ui.mog2VarThresholdEntry.Text, "MOG2 Var Threshold"); err != nil {
		return TrackingSettings{}, err
	}
	if settings.TrackingROIHeightFrac, err = parseRequiredFloat(ui.roiHeightEntry.Text, "ROI Height Fraction"); err != nil {
		return TrackingSettings{}, err
	}

	settings.PreEventDuration = time.Duration(preEventSeconds * float64(time.Second))
	settings.PostEventDuration = time.Duration(postEventSeconds * float64(time.Second))
	settings.RawSegmentDuration = time.Duration(rawSegmentSeconds * float64(time.Second))
	settings.RawSegmentOverlap = time.Duration(rawSegmentOverlapSeconds * float64(time.Second))

	if settings.MinArea < 0 || settings.MaxArea <= settings.MinArea {
		return TrackingSettings{}, errors.New("Max Area must be greater than Min Area")
	}
	if settings.MinSpeed < settings.SlowMinSpeed {
		return TrackingSettings{}, errors.New("Fast Min Speed must be greater than or equal to Slow Min Speed")
	}
	if settings.MinHits < 1 {
		return TrackingSettings{}, errors.New("Min Hits must be at least 1")
	}
	if settings.BlurSize < 1 || settings.BlurSize%2 == 0 {
		return TrackingSettings{}, errors.New("Blur Size must be a positive odd integer")
	}
	if settings.PreEventDuration < 0 || settings.PostEventDuration < 0 || settings.RawSegmentDuration < 0 || settings.RawSegmentOverlap < 0 {
		return TrackingSettings{}, errors.New("Pre/Post Event and Raw Segment Seconds must be non-negative")
	}
	if settings.RawSegmentDuration > 0 && settings.RawSegmentOverlap >= settings.RawSegmentDuration {
		return TrackingSettings{}, errors.New("Raw Segment Overlap Seconds must be smaller than Raw Segment Seconds")
	}
	if settings.MOG2History < 1 {
		return TrackingSettings{}, errors.New("MOG2 History must be at least 1")
	}
	if settings.TrackingROIHeightFrac <= 0 || settings.TrackingROIHeightFrac > 1 {
		return TrackingSettings{}, errors.New("ROI Height Fraction must be in the range (0, 1]")
	}

	ui.saveTrackingPreferences()
	return settings, nil
}

func (ui *trackerApp) buildNostrSettings() (NostrSettings, error) {
	settings := NostrSettings{
		Enabled:   ui.nostrEnableCheck.Checked,
		RelayURL:  strings.TrimSpace(ui.nostrRelayEntry.Text),
		SecretKey: strings.TrimSpace(ui.nostrSecretEntry.Text),
		Timeout:   nostrutil.DefaultTimeout,
	}

	minDistanceText := strings.TrimSpace(ui.nostrMinDistanceEntry.Text)
	if minDistanceText == "" {
		minDistanceText = "0"
	}
	minDistance, err := strconv.ParseFloat(minDistanceText, 64)
	if err != nil {
		return NostrSettings{}, errors.New("Nostr min straight-line track distance must be a number")
	}
	if minDistance < 0 {
		return NostrSettings{}, errors.New("Nostr min straight-line track distance must be 0 or greater")
	}
	settings.MinTrackDistance = minDistance

	if err := validateNostrSettings(settings, settings.Enabled); err != nil {
		return NostrSettings{}, err
	}

	return settings, nil
}

func validateNostrSettings(settings NostrSettings, requirePublishConfig bool) error {
	if !requirePublishConfig {
		return nil
	}
	if settings.RelayURL == "" {
		return errors.New("Nostr relay URL is required")
	}
	if settings.SecretKey == "" {
		return errors.New("Nostr secret key is required")
	}
	if _, err := nostrutil.ResolveSecretKey(settings.SecretKey); err != nil {
		return fmt.Errorf("invalid Nostr secret key: %w", err)
	}
	return nil
}

func (ui *trackerApp) sendNostrTestNote() {
	settings, err := ui.buildNostrSettings()
	if err != nil {
		ui.showError(err)
		return
	}
	if err := validateNostrSettings(settings, true); err != nil {
		ui.showError(err)
		return
	}

	content := strings.TrimSpace(ui.nostrTestMessageEntry.Text)
	if content == "" {
		ui.showError(errors.New("Nostr test note content is required"))
		return
	}

	ui.saveTrackingPreferences()
	ui.sendNostrTestButton.Disable()
	ui.statusLabel.SetText("Sending Nostr test note...")

	go func() {
		eventID, publishErr := nostrutil.PublishTextNote(context.Background(), nostrutil.PublishOptions{
			RelayURL:  settings.RelayURL,
			SecretKey: settings.SecretKey,
			Timeout:   settings.Timeout,
			Content:   content,
		})

		fyne.Do(func() {
			ui.sendNostrTestButton.Enable()
			if publishErr != nil {
				ui.statusLabel.SetText("Nostr test note failed")
				ui.showError(publishErr)
				return
			}
			ui.statusLabel.SetText("Nostr test note sent")
			ui.showInfo("Nostr Test Note Sent", fmt.Sprintf("Published event ID:\n%s", eventID))
		})
	}()
}

func (ui *trackerApp) saveTrackingPreset() {
	name := ui.presetNameEntry.Text
	if name == "" {
		ui.showInfo("Preset Name Required", "Enter a preset name first.")
		return
	}
	settings, err := ui.buildTrackingSettings()
	if err != nil {
		ui.showError(err)
		return
	}

	savePreset := func() {
		ui.presets[name] = settings
		ui.refreshPresetOptions()
		ui.presetSelect.SetSelected(name)
		ui.saveTrackingPresets()
		ui.updatePresetButtons()
		ui.statusLabel.SetText("Saved tracking preset: " + name)
	}

	if _, exists := ui.presets[name]; exists {
		dialog.ShowConfirm(
			"Overwrite Preset?",
			fmt.Sprintf("Replace the existing preset %q with the current tracking settings?", name),
			func(confirm bool) {
				if confirm {
					savePreset()
				}
			},
			ui.window,
		)
		return
	}

	savePreset()
}

func (ui *trackerApp) applySelectedPreset() {
	name := ui.presetSelect.Selected
	settings, ok := ui.presets[name]
	if !ok || name == "" {
		ui.showInfo("No Preset Selected", "Select a preset first.")
		return
	}
	ui.applyTrackingSettingsToForm(settings)
	ui.saveTrackingPreferences()
	ui.saveTrackingPresets()
	ui.statusLabel.SetText("Applied tracking preset: " + name)
}

func (ui *trackerApp) deleteSelectedPreset() {
	name := ui.presetSelect.Selected
	if name == "" {
		ui.showInfo("No Preset Selected", "Select a preset first.")
		return
	}
	delete(ui.presets, name)
	ui.refreshPresetOptions()
	ui.presetSelect.ClearSelected()
	ui.presetNameEntry.SetText("")
	ui.saveTrackingPresets()
	ui.updatePresetButtons()
	ui.statusLabel.SetText("Deleted tracking preset: " + name)
}

func sortHistoryEntries(entries []eventHistoryEntry, sortMode string) {
	switch sortMode {
	case sortHighestObjects:
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Summary.UniqueObjects == entries[j].Summary.UniqueObjects {
				return compareHistoryEntriesNewest(entries[i], entries[j])
			}
			return entries[i].Summary.UniqueObjects > entries[j].Summary.UniqueObjects
		})
	case sortHighestPeakSpeed:
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Summary.HighestSpeedPxSec == entries[j].Summary.HighestSpeedPxSec {
				return compareHistoryEntriesNewest(entries[i], entries[j])
			}
			return entries[i].Summary.HighestSpeedPxSec > entries[j].Summary.HighestSpeedPxSec
		})
	default:
		sort.Slice(entries, func(i, j int) bool {
			return compareHistoryEntriesNewest(entries[i], entries[j])
		})
	}
}

func compareHistoryEntriesNewest(left, right eventHistoryEntry) bool {
	if !left.Summary.StartedAt.IsZero() && !right.Summary.StartedAt.IsZero() {
		return left.Summary.StartedAt.After(right.Summary.StartedAt)
	}
	return left.Directory > right.Directory
}

func (ui *trackerApp) resetImages() {
	placeholder := newPlaceholderFrame()
	ui.videoImage.Image = placeholder
	ui.videoImage.Refresh()
	ui.maskImage.Image = placeholder
	ui.maskImage.Refresh()
	ui.objectImage.Image = placeholder
	ui.objectImage.Refresh()
	ui.objectMapImage.Image = placeholder
	ui.objectMapImage.Refresh()
}

func (ui *trackerApp) resetObjectSelection(message string) {
	ui.selectedObjectID = 0
	ui.filteredObjectIDs = nil
	ui.objectList.UnselectAll()
	ui.objectList.Refresh()
	ui.objectName.SetText("")
	ui.objectName.Disable()
	ui.objectDetail.SetText(message)
	ui.objectImage.Image = newPlaceholderFrame()
	ui.objectImage.Refresh()
	ui.watchObjectButton.Disable()
	ui.showFinalPositionsButton.Disable()
	ui.saveObjectNameButton.Disable()
	ui.configureFinalPositionControls(nil)
}

func (ui *trackerApp) populateObjectSelection(detail eventHistoryDetail) {
	ui.configureFinalPositionControls(&detail)
	if len(detail.Objects) == 0 {
		ui.resetObjectSelection("No persisted tracked objects were found for this event.")
		return
	}
	ui.applyObjectListFilter()
}

func (ui *trackerApp) updateSelectedObject() {
	object, ok := ui.selectedObject()
	if !ok {
		ui.objectName.SetText("")
		ui.objectName.Disable()
		ui.objectDetail.SetText("Select an event, then select an object to inspect its metadata.")
		ui.watchObjectButton.Disable()
		ui.saveObjectNameButton.Disable()
		return
	}

	ui.objectName.Enable()
	ui.objectName.SetText(object.Name)
	ui.objectDetail.SetText(formatObjectDetail(object))
	previewPath := representativeObjectCropPath(object)
	if previewPath != "" {
		crop := gocv.IMRead(previewPath, gocv.IMReadColor)
		if !crop.Empty() {
			if img, err := crop.ToImage(); err == nil {
				ui.objectImage.Image = img
				ui.objectImage.Refresh()
			}
		}
		crop.Close()
	} else {
		ui.objectImage.Image = newPlaceholderFrame()
		ui.objectImage.Refresh()
	}
	ui.watchObjectButton.Enable()
	if ui.currentDetail != nil && ui.currentDetail.HasTracking && len(ui.currentDetail.Objects) > 0 {
		ui.showFinalPositionsButton.Enable()
	}
	ui.saveObjectNameButton.Enable()
}

func (ui *trackerApp) configureFinalPositionControls(detail *eventHistoryDetail) {
	maxDistance := 1.0
	enabled := false
	if detail != nil && detail.HasTracking && len(detail.Objects) > 0 {
		maxDistance = math.Max(1, math.Ceil(maxTravelDistance(detail.Objects)))
		enabled = true
	}

	currentThreshold := 0.0
	if ui.finalPositionDistanceEntry != nil && ui.finalPositionDistanceEntry.Text != "" {
		if parsed, err := strconv.ParseFloat(ui.finalPositionDistanceEntry.Text, 64); err == nil {
			currentThreshold = parsed
		}
	}
	currentThreshold = clampFloat(currentThreshold, 0, maxDistance)

	ui.updatingFinalPositionUI = true
	ui.finalPositionSlider.Min = 0
	ui.finalPositionSlider.Max = maxDistance
	ui.finalPositionSlider.Step = 1
	ui.finalPositionSlider.SetValue(currentThreshold)
	ui.finalPositionDistanceEntry.SetText(formatFloat(currentThreshold))
	ui.updatingFinalPositionUI = false

	if enabled {
		ui.finalPositionSlider.Enable()
		ui.finalPositionDistanceEntry.Enable()
	} else {
		ui.finalPositionSlider.Disable()
		ui.finalPositionDistanceEntry.Disable()
	}
}

func (ui *trackerApp) handleFinalPositionSliderChanged(value float64) {
	if ui.updatingFinalPositionUI {
		return
	}
	ui.applyFinalPositionThreshold(value, true)
}

func (ui *trackerApp) handleFinalPositionDistanceEntryChanged(value string) {
	if ui.updatingFinalPositionUI || value == "" {
		return
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return
	}
	ui.applyFinalPositionThreshold(parsed, false)
}

func (ui *trackerApp) applyFinalPositionThreshold(value float64, updateEntry bool) {
	maxDistance := math.Max(1, ui.finalPositionSlider.Max)
	value = clampFloat(value, 0, maxDistance)

	ui.updatingFinalPositionUI = true
	if math.Abs(ui.finalPositionSlider.Value-value) > 0.0001 {
		ui.finalPositionSlider.SetValue(value)
	}
	if updateEntry || ui.finalPositionDistanceEntry.Text == "" {
		ui.finalPositionDistanceEntry.SetText(formatFloat(value))
	} else if parsed, err := strconv.ParseFloat(ui.finalPositionDistanceEntry.Text, 64); err == nil && math.Abs(parsed-value) > 0.0001 {
		ui.finalPositionDistanceEntry.SetText(formatFloat(value))
	}
	ui.updatingFinalPositionUI = false

	ui.mu.Lock()
	activeOverlay := ui.activePlaybackOverlay
	ui.mu.Unlock()
	if activeOverlay != nil {
		showFinalPositions, _, _ := activeOverlay.finalPositionConfig()
		if showFinalPositions {
			activeOverlay.setFinalPositionMinDistance(value)
			ui.refreshActivePlaybackFrame()
		}
	}
}

func (ui *trackerApp) refreshActivePlaybackFrame() {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if !ui.playbackActive || ui.activePlaybackOverlay == nil {
		return
	}
	ui.playbackSeekFrame = ui.playbackCurrentFrame
}

func (ui *trackerApp) selectedObject() (trackedObjectDetail, bool) {
	if ui.currentDetail == nil || ui.selectedObjectID == 0 {
		return trackedObjectDetail{}, false
	}
	for _, object := range ui.currentDetail.Objects {
		if object.ID == ui.selectedObjectID {
			return object, true
		}
	}
	return trackedObjectDetail{}, false
}

func (ui *trackerApp) filteredObjectAt(id widget.ListItemID) (trackedObjectDetail, bool) {
	if ui.currentDetail == nil || id < 0 || id >= len(ui.filteredObjectIDs) {
		return trackedObjectDetail{}, false
	}
	objectIndex := ui.filteredObjectIDs[id]
	if objectIndex < 0 || objectIndex >= len(ui.currentDetail.Objects) {
		return trackedObjectDetail{}, false
	}
	return ui.currentDetail.Objects[objectIndex], true
}

func (ui *trackerApp) applyObjectListFilter() {
	if ui.currentDetail == nil {
		ui.resetObjectSelection("Select an event, then select an object to inspect its metadata.")
		return
	}

	filter := strings.TrimSpace(ui.objectSearchEntry.Text)
	ui.filteredObjectIDs = ui.filteredObjectIDs[:0]
	for i, object := range ui.currentDetail.Objects {
		if filter == "" || strings.Contains(strconv.Itoa(object.ID), filter) {
			ui.filteredObjectIDs = append(ui.filteredObjectIDs, i)
		}
	}

	ui.objectList.UnselectAll()
	ui.objectList.Refresh()
	ui.showFinalPositionsButton.Disable()
	if ui.currentDetail.HasTracking && len(ui.currentDetail.Objects) > 0 {
		ui.showFinalPositionsButton.Enable()
	}

	if len(ui.filteredObjectIDs) == 0 {
		ui.selectedObjectID = 0
		ui.objectName.SetText("")
		ui.objectName.Disable()
		ui.objectDetail.SetText("No tracked objects match the object ID filter.")
		ui.objectImage.Image = newPlaceholderFrame()
		ui.objectImage.Refresh()
		ui.watchObjectButton.Disable()
		ui.saveObjectNameButton.Disable()
		return
	}

	selectedVisible := false
	for _, objectIndex := range ui.filteredObjectIDs {
		if ui.currentDetail.Objects[objectIndex].ID == ui.selectedObjectID {
			selectedVisible = true
			break
		}
	}
	if !selectedVisible {
		ui.selectedObjectID = ui.currentDetail.Objects[ui.filteredObjectIDs[0]].ID
	}

	for visibleIndex, objectIndex := range ui.filteredObjectIDs {
		if ui.currentDetail.Objects[objectIndex].ID == ui.selectedObjectID {
			ui.objectList.Select(visibleIndex)
			return
		}
	}
}

func (ui *trackerApp) watchSelectedObject() {
	ui.mu.Lock()
	running := ui.running
	ui.mu.Unlock()
	if running {
		ui.showInfo("Busy", "Stop the current tracker or playback first.")
		return
	}

	object, ok := ui.selectedObject()
	if !ok {
		ui.showInfo("No Object Selected", "Select an object first.")
		return
	}
	if ui.currentDetail == nil || ui.currentDetail.Tracking == nil || !ui.currentDetail.HasTracking {
		ui.showInfo("No Tracking Metadata", "No saved tracking metadata was found for the selected object.")
		return
	}
	objectMapSkipCount, err := parseRequiredInt(ui.objectMapSkipEntry.Text, "Object Map Skip")
	if err != nil {
		ui.showError(err)
		return
	}
	if objectMapSkipCount < 0 {
		ui.showError(errors.New("Object Map Skip must be 0 or greater"))
		return
	}

	entry := ui.historyEntries[ui.selectedHistory]
	videoPath := filepath.Join(entry.Directory, ui.currentDetail.Summary.OriginalVideo)
	if ui.currentDetail.Summary.OriginalVideo == "" {
		videoPath = filepath.Join(entry.Directory, "original.avi")
	}
	if _, err := os.Stat(videoPath); err != nil {
		ui.showError(fmt.Errorf("open original video: %w", err))
		return
	}
	maskPath := filepath.Join(entry.Directory, ui.currentDetail.Summary.MaskedVideo)
	if ui.currentDetail.Summary.MaskedVideo == "" {
		maskPath = filepath.Join(entry.Directory, "masked.avi")
	}

	cropBySourceFrame, cropSourceFrames := makeCropBySourceFrame(object.CropPaths)
	selectedObject := object
	overlay := &playbackOverlay{
		Tracking:           *ui.currentDetail.Tracking,
		Settings:           NormalizeTrackingSettings(ui.currentDetail.Summary.TrackingSettings),
		SelectedTrackID:    object.ID,
		SelectedObject:     &selectedObject,
		StartFrame:         max(0, object.FirstSeenFrame-1),
		Label:              formatObjectOption(object),
		CropBySourceFrame:  cropBySourceFrame,
		CropSourceFrames:   cropSourceFrames,
		ObjectMapSkipCount: objectMapSkipCount,
		ShowFrameTracks:    true,
	}

	stopCh := make(chan struct{})
	ui.mu.Lock()
	ui.running = true
	ui.stopCh = stopCh
	ui.activePlaybackOverlay = overlay
	ui.mu.Unlock()

	ui.startButton.Disable()
	ui.stopButton.Enable()
	ui.statusLabel.SetText("Starting object playback...")
	ui.eventLabel.SetText(videoPath)
	ui.resetImages()

	go ui.runPlayback(videoPath, formatObjectOption(object), maskPath, stopCh, overlay)
}

func (ui *trackerApp) watchFinalPositions() {
	ui.mu.Lock()
	running := ui.running
	ui.mu.Unlock()
	if running {
		ui.showInfo("Busy", "Stop the current tracker or playback first.")
		return
	}

	if ui.currentDetail == nil || ui.currentDetail.Tracking == nil || !ui.currentDetail.HasTracking {
		ui.showInfo("No Tracking Metadata", "No saved tracking metadata was found for the selected event.")
		return
	}
	if len(ui.currentDetail.Objects) == 0 {
		ui.showInfo("No Objects", "No tracked objects were found for the selected event.")
		return
	}

	minDistance, err := parseRequiredFloat(ui.finalPositionDistanceEntry.Text, "Min Travel Px")
	if err != nil {
		ui.showError(err)
		return
	}
	minDistance = clampFloat(minDistance, 0, math.Max(1, ui.finalPositionSlider.Max))
	ui.applyFinalPositionThreshold(minDistance, true)

	entry := ui.historyEntries[ui.selectedHistory]
	videoPath := filepath.Join(entry.Directory, ui.currentDetail.Summary.OriginalVideo)
	if ui.currentDetail.Summary.OriginalVideo == "" {
		videoPath = filepath.Join(entry.Directory, "original.avi")
	}
	if _, err := os.Stat(videoPath); err != nil {
		ui.showError(fmt.Errorf("open original video: %w", err))
		return
	}

	maskPath := filepath.Join(entry.Directory, ui.currentDetail.Summary.MaskedVideo)
	if ui.currentDetail.Summary.MaskedVideo == "" {
		maskPath = filepath.Join(entry.Directory, "masked.avi")
	}

	overlay := &playbackOverlay{
		Tracking:                 *ui.currentDetail.Tracking,
		Settings:                 NormalizeTrackingSettings(ui.currentDetail.Summary.TrackingSettings),
		Label:                    "Final positions",
		ShowFrameTracks:          false,
		ShowFinalPositions:       true,
		FinalPositionObjects:     append([]trackedObjectDetail(nil), ui.currentDetail.Objects...),
		FinalPositionMinDistance: minDistance,
	}

	stopCh := make(chan struct{})
	ui.mu.Lock()
	ui.running = true
	ui.stopCh = stopCh
	ui.activePlaybackOverlay = overlay
	ui.mu.Unlock()

	ui.startButton.Disable()
	ui.stopButton.Enable()
	ui.statusLabel.SetText("Starting final-position replay...")
	ui.eventLabel.SetText(videoPath)
	ui.resetImages()

	go ui.runPlayback(videoPath, "Final positions", maskPath, stopCh, overlay)
}

func (ui *trackerApp) saveSelectedObjectName() {
	if ui.currentDetail == nil || ui.selectedHistory < 0 || ui.selectedHistory >= len(ui.historyEntries) {
		ui.showInfo("No Event Selected", "Select an event first.")
		return
	}
	object, ok := ui.selectedObject()
	if !ok {
		ui.showInfo("No Object Selected", "Select an object first.")
		return
	}

	name := ui.objectName.Text
	entry := ui.historyEntries[ui.selectedHistory]
	trackNamesPath := filepath.Join(entry.Directory, ui.currentDetail.Summary.TrackNamesFile)
	if ui.currentDetail.Summary.TrackNamesFile == "" {
		trackNamesPath = filepath.Join(entry.Directory, "track_names.json")
	}

	if err := saveTrackNames(trackNamesPath, ui.currentDetail.TrackNames, object.ID, name); err != nil {
		ui.showError(err)
		return
	}

	ui.currentDetail.TrackNames[object.ID] = name
	for i := range ui.currentDetail.Objects {
		if ui.currentDetail.Objects[i].ID == object.ID {
			ui.currentDetail.Objects[i].Name = name
			break
		}
	}
	ui.populateObjectSelection(*ui.currentDetail)
	for i := range ui.currentDetail.Objects {
		if ui.currentDetail.Objects[i].ID == object.ID {
			ui.selectedObjectID = object.ID
			ui.objectList.Select(i)
			break
		}
	}
	ui.statusLabel.SetText(fmt.Sprintf("Saved object name for track #%d", object.ID))
}

func loadEventHistory(root string) ([]eventHistoryEntry, error) {
	if root == "" {
		root = "events"
	}

	items, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	entries := make([]eventHistoryEntry, 0, len(items))
	for _, item := range items {
		if !item.IsDir() {
			continue
		}

		dir := filepath.Join(root, item.Name())
		summary := EventSummary{
			EventID: item.Name(),
		}

		eventJSON := filepath.Join(dir, "event.json")
		if data, err := os.ReadFile(eventJSON); err == nil {
			_ = json.Unmarshal(data, &summary)
		}
		summary.TrackingSettings = NormalizeTrackingSettings(summary.TrackingSettings)

		if summary.OriginalVideo == "" {
			summary.OriginalVideo = "original.avi"
		}
		if summary.TrackedVideo == "" {
			summary.TrackedVideo = "tracked.avi"
		}
		if summary.MaskedVideo == "" {
			summary.MaskedVideo = "masked.avi"
		}
		if summary.TrackCropsDir == "" {
			summary.TrackCropsDir = "track_crops"
		}
		if summary.TrackNamesFile == "" {
			summary.TrackNamesFile = "track_names.json"
		}

		if _, err := os.Stat(filepath.Join(dir, summary.OriginalVideo)); err != nil {
			if _, trackedErr := os.Stat(filepath.Join(dir, summary.TrackedVideo)); trackedErr != nil {
				continue
			}
		}

		entries = append(entries, eventHistoryEntry{
			Directory: dir,
			Summary:   summary,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Directory > entries[j].Directory
	})

	return entries, nil
}

func loadEventSummaryFromDir(dir string) EventSummary {
	summary := EventSummary{
		EventID: dir,
	}

	eventJSON := filepath.Join(dir, "event.json")
	if data, err := os.ReadFile(eventJSON); err == nil {
		_ = json.Unmarshal(data, &summary)
	}
	summary.TrackingSettings = NormalizeTrackingSettings(summary.TrackingSettings)
	if summary.OriginalVideo == "" {
		summary.OriginalVideo = "original.avi"
	}
	if summary.TrackedVideo == "" {
		summary.TrackedVideo = "tracked.avi"
	}
	if summary.MaskedVideo == "" {
		summary.MaskedVideo = "masked.avi"
	}
	if summary.TrackCropsDir == "" {
		summary.TrackCropsDir = "track_crops"
	}
	if summary.TrackNamesFile == "" {
		summary.TrackNamesFile = "track_names.json"
	}
	if summary.TrackingMetadata == "" {
		summary.TrackingMetadata = "tracking.json"
	}
	if summary.EventID == "" {
		summary.EventID = filepath.Base(dir)
	}
	return summary
}

func (ui *trackerApp) publishVideoAnalysisNostrNote(dir string, config TrackerConfig) error {
	detail, err := loadEventDetail(eventHistoryEntry{
		Directory: dir,
		Summary:   loadEventSummaryFromDir(dir),
	})
	if err != nil {
		return fmt.Errorf("load event detail: %w", err)
	}

	content := ui.formatNostrEventSummary(detail, config)
	_, err = nostrutil.PublishTextNote(context.Background(), nostrutil.PublishOptions{
		RelayURL:  config.Nostr.RelayURL,
		SecretKey: config.Nostr.SecretKey,
		Timeout:   config.Nostr.Timeout,
		Content:   content,
		Tags:      ui.nostrImageTags(detail, config),
	})
	if err != nil {
		return err
	}
	return nil
}

func (ui *trackerApp) publishCompletedVideoAnalysisNostrNote(eventDirs []string, config TrackerConfig) error {
	completionContent := fmt.Sprintf("Finished processing video file: %s", filepath.Base(config.Input))
	if _, err := nostrutil.PublishTextNote(context.Background(), nostrutil.PublishOptions{
		RelayURL:  config.Nostr.RelayURL,
		SecretKey: config.Nostr.SecretKey,
		Timeout:   config.Nostr.Timeout,
		Content:   completionContent,
	}); err != nil {
		return err
	}

	if len(eventDirs) == 0 {
		content := fmt.Sprintf(
			"Video analysis complete: %s | no recorded events found | 0 objects >= %.0f px straight-line travel",
			filepath.Base(config.Input),
			config.Nostr.MinTrackDistance,
		)
		_, err := nostrutil.PublishTextNote(context.Background(), nostrutil.PublishOptions{
			RelayURL:  config.Nostr.RelayURL,
			SecretKey: config.Nostr.SecretKey,
			Timeout:   config.Nostr.Timeout,
			Content:   content,
		})
		return err
	}

	if len(eventDirs) == 1 {
		return ui.publishVideoAnalysisNostrNote(eventDirs[0], config)
	}

	lines := []string{
		fmt.Sprintf(
			"Video analysis complete: %s | %d recorded events | objects listed when >= %.0f px straight-line travel",
			filepath.Base(config.Input),
			len(eventDirs),
			config.Nostr.MinTrackDistance,
		),
	}
	tags := make(nostr.Tags, 0)

	for i, dir := range eventDirs {
		detail, err := loadEventDetail(eventHistoryEntry{
			Directory: dir,
			Summary:   loadEventSummaryFromDir(dir),
		})
		if err != nil {
			return fmt.Errorf("load event detail %s: %w", dir, err)
		}

		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("Event %d", i+1))
		lines = append(lines, ui.formatNostrEventSummary(detail, config))
		tags = append(tags, ui.nostrImageTags(detail, config)...)
	}

	_, err := nostrutil.PublishTextNote(context.Background(), nostrutil.PublishOptions{
		RelayURL:  config.Nostr.RelayURL,
		SecretKey: config.Nostr.SecretKey,
		Timeout:   config.Nostr.Timeout,
		Content:   strings.Join(lines, "\n"),
		Tags:      dedupeNostrTags(tags),
	})
	return err
}

func (ui *trackerApp) formatNostrEventSummary(detail eventHistoryDetail, config TrackerConfig) string {
	qualified := make([]trackedObjectDetail, 0, len(detail.Objects))
	for _, object := range detail.Objects {
		if object.TravelDistance >= config.Nostr.MinTrackDistance {
			qualified = append(qualified, object)
		}
	}
	sort.Slice(qualified, func(i, j int) bool {
		if qualified[i].TravelDistance == qualified[j].TravelDistance {
			return qualified[i].ID < qualified[j].ID
		}
		return qualified[i].TravelDistance > qualified[j].TravelDistance
	})

	eventTime := detail.Summary.StartedAt
	if eventTime.IsZero() {
		eventTime = time.Now()
	}

	objectLines := make([]string, 0, len(qualified))
	for _, object := range qualified {
		lineParts := []string{fmt.Sprintf("#%04d", object.ID)}
		if cropURL := ui.projectFileURL(representativeObjectCropPath(object)); cropURL != "" {
			lineParts = append(lineParts, cropURL)
		}
		objectLines = append(objectLines, strings.Join(lineParts, "\n"))
	}
	if len(objectLines) == 0 {
		objectLines = append(objectLines, "none")
	}

	return strings.Join([]string{
		fmt.Sprintf("File: %s", filepath.Base(config.Input)),
		fmt.Sprintf("Datetime: %s", eventTime.Local().Format("2006-01-02 15:04:05")),
		fmt.Sprintf("Objects:\n%s", strings.Join(objectLines, "\n")),
	}, "\n")
}

func (ui *trackerApp) nostrImageTags(detail eventHistoryDetail, config TrackerConfig) nostr.Tags {
	qualified := make([]trackedObjectDetail, 0, len(detail.Objects))
	for _, object := range detail.Objects {
		if object.TravelDistance >= config.Nostr.MinTrackDistance {
			qualified = append(qualified, object)
		}
	}
	sort.Slice(qualified, func(i, j int) bool {
		if qualified[i].TravelDistance == qualified[j].TravelDistance {
			return qualified[i].ID < qualified[j].ID
		}
		return qualified[i].TravelDistance > qualified[j].TravelDistance
	})

	tags := make(nostr.Tags, 0, len(qualified))
	for _, object := range qualified {
		if cropURL := ui.projectFileURL(representativeObjectCropPath(object)); cropURL != "" {
			tags = append(tags, nostr.Tag{"r", cropURL})
		}
	}
	return tags
}

func dedupeNostrTags(tags nostr.Tags) nostr.Tags {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	result := make(nostr.Tags, 0, len(tags))
	for _, tag := range tags {
		key := strings.Join(tag, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, tag)
	}
	return result
}

func loadEventDetail(entry eventHistoryEntry) (eventHistoryDetail, error) {
	detail := eventHistoryDetail{
		Summary: entry.Summary,
	}
	detail.Summary.TrackingSettings = NormalizeTrackingSettings(detail.Summary.TrackingSettings)
	if detail.Summary.TrackNamesFile == "" {
		detail.Summary.TrackNamesFile = "track_names.json"
	}
	trackNamesPath := filepath.Join(entry.Directory, detail.Summary.TrackNamesFile)
	trackNames, err := loadTrackNames(trackNamesPath)
	if err != nil {
		return detail, err
	}
	detail.TrackNames = trackNames

	trackingPath := filepath.Join(entry.Directory, entry.Summary.TrackingMetadata)
	if entry.Summary.TrackingMetadata == "" {
		trackingPath = filepath.Join(entry.Directory, "tracking.json")
	}
	detail.TrackingPath = trackingPath

	data, err := os.ReadFile(trackingPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return detail, nil
		}
		return detail, err
	}

	var tracking EventMetadata
	if err := json.Unmarshal(data, &tracking); err != nil {
		return detail, err
	}

	detail.Tracking = &tracking
	detail.HasTracking = true
	detail.FirstActiveMS = -1
	detail.LastActiveMS = -1
	objectMap := make(map[int]*trackedObjectDetail)

	for _, frame := range tracking.Frames {
		trackCount := len(frame.Tracks)
		if trackCount > detail.MaxTracks {
			detail.MaxTracks = trackCount
		}

		fastCount, slowCount := countTrackTypes(frame.Tracks)
		if fastCount > detail.MaxFastTracks {
			detail.MaxFastTracks = fastCount
		}
		if slowCount > detail.MaxSlowTracks {
			detail.MaxSlowTracks = slowCount
		}

		if trackCount == 0 {
			continue
		}

		detail.FramesWithTracks++
		if detail.FirstActiveMS < 0 {
			detail.FirstActiveMS = frame.TimeMS
		}
		detail.LastActiveMS = frame.TimeMS

		for _, track := range frame.Tracks {
			object := objectMap[track.ID]
			if object == nil {
				object = &trackedObjectDetail{
					ID:             track.ID,
					Name:           trackNames[track.ID],
					PrimaryType:    track.Type,
					FirstSeenFrame: frame.SourceFrame,
					LastSeenFrame:  frame.SourceFrame,
					FirstSeenMS:    frame.TimeMS,
					LastSeenMS:     frame.TimeMS,
					MaxSpeed:       track.Speed,
					Path:           make([]image.Point, 0, 32),
					FirstPositionX: track.X,
					FirstPositionY: track.Y,
					LastPositionX:  track.X,
					LastPositionY:  track.Y,
				}
				objectMap[track.ID] = object
			}

			object.FramesSeen++
			object.LastSeenFrame = frame.SourceFrame
			object.LastSeenMS = frame.TimeMS
			object.LastPositionX = track.X
			object.LastPositionY = track.Y
			object.Path = append(object.Path, image.Pt(track.X, track.Y))
			object.AverageWidth += float64(track.BoxWidth)
			object.AverageHeight += float64(track.BoxHeight)
			if track.Speed > object.MaxSpeed {
				object.MaxSpeed = track.Speed
			}
			if object.PrimaryType != trackTypeFast && track.Type == trackTypeFast {
				object.PrimaryType = trackTypeFast
			}
		}
	}

	cropsRoot := filepath.Join(entry.Directory, detail.Summary.TrackCropsDir)
	for id, object := range objectMap {
		crops, err := listObjectCropPaths(filepath.Join(cropsRoot, fmt.Sprintf("object_%04d", id)))
		if err != nil {
			return detail, err
		}
		object.CropPaths = crops
		object.CropCount = len(crops)
		if len(crops) > 0 {
			object.FirstCropPath = crops[0]
			object.LastCropPath = crops[len(crops)-1]
		}
		object.TravelDistance = math.Hypot(
			float64(object.LastPositionX-object.FirstPositionX),
			float64(object.LastPositionY-object.FirstPositionY),
		)
		if object.FramesSeen > 0 {
			object.AverageWidth /= float64(object.FramesSeen)
			object.AverageHeight /= float64(object.FramesSeen)
		}
		detail.Objects = append(detail.Objects, *object)
	}
	sort.Slice(detail.Objects, func(i, j int) bool {
		return detail.Objects[i].ID < detail.Objects[j].ID
	})

	return detail, nil
}

func loadTrackNames(path string) (map[int]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[int]string), nil
		}
		return nil, err
	}

	raw := make(map[string]string)
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	names := make(map[int]string, len(raw))
	for key, value := range raw {
		id, err := strconv.Atoi(key)
		if err != nil {
			continue
		}
		names[id] = value
	}
	return names, nil
}

func saveTrackNames(path string, existing map[int]string, objectID int, name string) error {
	if existing == nil {
		existing = make(map[int]string)
	}
	if name == "" {
		delete(existing, objectID)
	} else {
		existing[objectID] = name
	}

	raw := make(map[string]string, len(existing))
	for id, value := range existing {
		if value == "" {
			continue
		}
		raw[strconv.Itoa(id)] = value
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func listObjectCropPaths(dir string) ([]string, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	paths := make([]string, 0, len(items))
	for _, item := range items {
		if item.IsDir() {
			continue
		}
		paths = append(paths, filepath.Join(dir, item.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

func makeCropBySourceFrame(paths []string) (map[int]string, []int) {
	if len(paths) == 0 {
		return nil, nil
	}

	crops := make(map[int]string, len(paths))
	frames := make([]int, 0, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		var sourceFrame int
		if _, err := fmt.Sscanf(name, "frame_%d_", &sourceFrame); err == nil && sourceFrame > 0 {
			crops[sourceFrame] = path
			frames = append(frames, sourceFrame)
		}
	}
	sort.Ints(frames)
	return crops, frames
}

func lookupObjectCropPath(overlay *playbackOverlay, sourceFrame int) string {
	if overlay == nil || overlay.CropBySourceFrame == nil || sourceFrame <= 0 {
		return ""
	}
	if path := overlay.CropBySourceFrame[sourceFrame]; path != "" {
		return path
	}
	for i := len(overlay.CropSourceFrames) - 1; i >= 0; i-- {
		frame := overlay.CropSourceFrames[i]
		if frame > sourceFrame {
			continue
		}
		return overlay.CropBySourceFrame[frame]
	}
	for i := 0; i < len(overlay.CropSourceFrames); i++ {
		frame := overlay.CropSourceFrames[i]
		if frame < sourceFrame {
			continue
		}
		return overlay.CropBySourceFrame[frame]
	}
	return ""
}

func newObjectMapCanvas(frame gocv.Mat) (*image.RGBA, error) {
	img, err := frame.ToImage()
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	canvas := image.NewRGBA(bounds)
	draw.Draw(canvas, bounds, img, bounds.Min, draw.Src)
	return canvas, nil
}

func selectedTrackInFrame(frame FrameMetadata, selectedTrackID int) (TrackMetadata, bool) {
	for _, track := range frame.Tracks {
		if track.ID == selectedTrackID {
			return track, true
		}
	}
	return TrackMetadata{}, false
}

func addObjectCropToMap(canvas *image.RGBA, crop image.Image, mask image.Image, track TrackMetadata) {
	if canvas == nil || crop == nil || mask == nil {
		return
	}

	fullRect := image.Rect(
		track.BoxX,
		track.BoxY,
		track.BoxX+track.BoxWidth,
		track.BoxY+track.BoxHeight,
	)
	fullRect = expandedTrackCropRect(fullRect)
	rect := fullRect.Intersect(canvas.Bounds())
	if rect.Empty() {
		return
	}

	sourcePoint := crop.Bounds().Min.Add(image.Pt(rect.Min.X-fullRect.Min.X, rect.Min.Y-fullRect.Min.Y))
	maskPoint := mask.Bounds().Min.Add(image.Pt(rect.Min.X-fullRect.Min.X, rect.Min.Y-fullRect.Min.Y))
	alphaMask := buildAlphaMask(mask)
	draw.DrawMask(canvas, rect, crop, sourcePoint, alphaMask, maskPoint, draw.Over)
}

func extractObjectMaskImage(maskFrame gocv.Mat, track TrackMetadata) (image.Image, error) {
	if maskFrame.Empty() {
		return nil, errors.New("empty mask frame")
	}

	fullRect := image.Rect(
		track.BoxX,
		track.BoxY,
		track.BoxX+track.BoxWidth,
		track.BoxY+track.BoxHeight,
	)
	fullRect = expandedTrackCropRect(fullRect).Intersect(image.Rect(0, 0, maskFrame.Cols(), maskFrame.Rows()))
	if fullRect.Empty() {
		return nil, errors.New("empty mask crop rect")
	}

	maskCrop := maskFrame.Region(fullRect)
	defer maskCrop.Close()
	return maskCrop.ToImage()
}

func buildAlphaMask(mask image.Image) *image.Alpha {
	bounds := mask.Bounds()
	alpha := image.NewAlpha(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := mask.At(x, y).RGBA()
			if r != 0 || g != 0 || b != 0 {
				alpha.SetAlpha(x, y, color.Alpha{A: 255})
			}
		}
	}
	return alpha
}

func representativeObjectCropPath(object trackedObjectDetail) string {
	if len(object.CropPaths) >= 10 {
		return object.CropPaths[9]
	}
	if len(object.CropPaths) >= 2 {
		return object.CropPaths[len(object.CropPaths)-2]
	}
	if len(object.CropPaths) == 1 {
		return object.CropPaths[0]
	}
	return ""
}

func loadObjectListPreview(object trackedObjectDetail) image.Image {
	previewPath := representativeObjectCropPath(object)
	if previewPath == "" {
		return newPlaceholderFrame()
	}

	crop := gocv.IMRead(previewPath, gocv.IMReadColor)
	if crop.Empty() {
		crop.Close()
		return newPlaceholderFrame()
	}
	img, err := crop.ToImage()
	crop.Close()
	if err != nil {
		return newPlaceholderFrame()
	}
	return img
}

func formatEventDetail(detail eventHistoryDetail, dir string) string {
	summary := detail.Summary
	settings := NormalizeTrackingSettings(summary.TrackingSettings)
	lines := []string{
		fmt.Sprintf("Event ID: %s", summary.EventID),
		fmt.Sprintf("Started: %s", formatTimeValue(summary.StartedAt)),
		fmt.Sprintf("Ended: %s", formatTimeValue(summary.EndedAt)),
		fmt.Sprintf("Duration: %.2fs", summary.DurationSeconds),
		fmt.Sprintf("Resolution: %dx%d", summary.Width, summary.Height),
		fmt.Sprintf("FPS: %.3f", summary.FPS),
		fmt.Sprintf("Frames: %d", summary.Frames),
		fmt.Sprintf("Unique objects: %d", summary.UniqueObjects),
		fmt.Sprintf("Peak speed: %.2f px/s", summary.HighestSpeedPxSec),
		fmt.Sprintf("Original video: %s", filepath.Join(dir, summary.OriginalVideo)),
		fmt.Sprintf("Tracked video: %s", filepath.Join(dir, summary.TrackedVideo)),
		fmt.Sprintf("Masked video: %s", filepath.Join(dir, summary.MaskedVideo)),
		fmt.Sprintf("Track crops: %s", filepath.Join(dir, summary.TrackCropsDir)),
		fmt.Sprintf("Track names: %s", filepath.Join(dir, summary.TrackNamesFile)),
	}

	trackingPath := detail.TrackingPath
	if trackingPath == "" {
		trackingPath = filepath.Join(dir, "tracking.json")
	}
	lines = append(lines, fmt.Sprintf("Tracking metadata: %s", trackingPath))

	lines = append(lines, "")
	lines = append(lines, "saved tracking settings:")
	lines = append(lines, fmt.Sprintf("  Min Area: %s", formatFloat(settings.MinArea)))
	lines = append(lines, fmt.Sprintf("  Max Area: %s", formatFloat(settings.MaxArea)))
	lines = append(lines, fmt.Sprintf("  Slow Min Speed: %s", formatFloat(settings.SlowMinSpeed)))
	lines = append(lines, fmt.Sprintf("  Fast Min Speed: %s", formatFloat(settings.MinSpeed)))
	lines = append(lines, fmt.Sprintf("  Match Distance: %s", formatFloat(settings.MaxMatchDistance)))
	lines = append(lines, fmt.Sprintf("  Min Hits: %d", settings.MinHits))
	lines = append(lines, fmt.Sprintf("  Blur Size: %d", settings.BlurSize))
	lines = append(lines, fmt.Sprintf("  Foreground Threshold: %s", formatFloat(settings.ForegroundThreshold)))
	lines = append(lines, fmt.Sprintf("  Pre Event Seconds: %s", formatFloat(settings.PreEventDuration.Seconds())))
	lines = append(lines, fmt.Sprintf("  Post Event Seconds: %s", formatFloat(settings.PostEventDuration.Seconds())))
	lines = append(lines, fmt.Sprintf("  Raw Segment Seconds: %s", formatFloat(settings.RawSegmentDuration.Seconds())))
	lines = append(lines, fmt.Sprintf("  Raw Segment Overlap Seconds: %s", formatFloat(settings.RawSegmentOverlap.Seconds())))
	lines = append(lines, fmt.Sprintf("  MOG2 History: %d", settings.MOG2History))
	lines = append(lines, fmt.Sprintf("  MOG2 Var Threshold: %s", formatFloat(settings.MOG2VarThreshold)))
	lines = append(lines, fmt.Sprintf("  ROI Height Fraction: %s", formatFloat(settings.TrackingROIHeightFrac)))

	if !detail.HasTracking || detail.Tracking == nil {
		lines = append(lines, "", "tracking.json preview: not available")
		return stringsJoin(lines, "\n")
	}

	lines = append(lines, "")
	lines = append(lines, "tracking.json preview:")
	lines = append(lines, fmt.Sprintf("  Frames in metadata: %d", len(detail.Tracking.Frames)))
	lines = append(lines, fmt.Sprintf("  Frames with tracks: %d", detail.FramesWithTracks))
	lines = append(lines, fmt.Sprintf("  First tracked activity: %s", formatDurationMS(detail.FirstActiveMS)))
	lines = append(lines, fmt.Sprintf("  Last tracked activity: %s", formatDurationMS(detail.LastActiveMS)))
	lines = append(lines, fmt.Sprintf("  Max simultaneous tracks: %d", detail.MaxTracks))
	lines = append(lines, fmt.Sprintf("  Max simultaneous fast tracks: %d", detail.MaxFastTracks))
	lines = append(lines, fmt.Sprintf("  Max simultaneous slow tracks: %d", detail.MaxSlowTracks))

	return stringsJoin(lines, "\n")
}

func formatObjectOption(object trackedObjectDetail) string {
	if object.Name != "" {
		return fmt.Sprintf("#%04d  %s  (last frame %d)", object.ID, object.Name, object.LastSeenFrame)
	}
	return fmt.Sprintf("#%04d  (last frame %d)", object.ID, object.LastSeenFrame)
}

func formatObjectDetail(object trackedObjectDetail) string {
	lines := []string{
		fmt.Sprintf("Object ID: %d", object.ID),
		fmt.Sprintf("Name: %s", fallbackString(object.Name, "n/a")),
		fmt.Sprintf("Type: %s", fallbackString(object.PrimaryType, "n/a")),
		fmt.Sprintf("Frames seen: %d", object.FramesSeen),
		fmt.Sprintf("First seen frame: %d", object.FirstSeenFrame),
		fmt.Sprintf("Last seen frame: %d", object.LastSeenFrame),
		fmt.Sprintf("First seen: %s", formatDurationMS(object.FirstSeenMS)),
		fmt.Sprintf("Last seen: %s", formatDurationMS(object.LastSeenMS)),
		fmt.Sprintf("Travel distance: %.2f px", object.TravelDistance),
		fmt.Sprintf("Peak speed: %.2f px/s", object.MaxSpeed),
		fmt.Sprintf("Average box: %.1fx%.1f px", object.AverageWidth, object.AverageHeight),
		fmt.Sprintf("First position: (%d, %d)", object.FirstPositionX, object.FirstPositionY),
		fmt.Sprintf("Last position: (%d, %d)", object.LastPositionX, object.LastPositionY),
		fmt.Sprintf("Saved crops: %d", object.CropCount),
		fmt.Sprintf("First crop: %s", fallbackString(object.FirstCropPath, "n/a")),
		fmt.Sprintf("Last crop: %s", fallbackString(object.LastCropPath, "n/a")),
	}
	return stringsJoin(lines, "\n")
}

func fallbackString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func formatTimeValue(value time.Time) string {
	if value.IsZero() {
		return "n/a"
	}
	return value.Local().Format(time.RFC3339)
}

func formatDurationMS(ms int64) string {
	if ms < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.3fs", float64(ms)/1000.0)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func clampFloat(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func maxTravelDistance(objects []trackedObjectDetail) float64 {
	maxDistance := 0.0
	for _, object := range objects {
		if object.TravelDistance > maxDistance {
			maxDistance = object.TravelDistance
		}
	}
	return maxDistance
}

func parseRequiredFloat(value, label string) (float64, error) {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", label)
	}
	return parsed, nil
}

func parseRequiredInt(value, label string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", label)
	}
	return parsed, nil
}

func parseOptionalInt(value string) (int, bool) {
	if value == "" {
		return 0, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func parseOptionalFloat(value string) (float64, bool) {
	if value == "" {
		return 0, true
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func matchesDateFilter(entry eventHistoryEntry, filter string) bool {
	if entry.Summary.EventID != "" && containsFold(entry.Summary.EventID, filter) {
		return true
	}
	if !entry.Summary.StartedAt.IsZero() && containsFold(entry.Summary.StartedAt.Local().Format("2006-01-02 15:04:05"), filter) {
		return true
	}
	return containsFold(filepath.Base(entry.Directory), filter)
}

func containsFold(text, needle string) bool {
	return len(needle) == 0 || (len(text) >= 0 && stringsContainsFold(text, needle))
}

func stringsContainsFold(text, needle string) bool {
	textRunes := []rune(text)
	needleRunes := []rune(needle)
	if len(needleRunes) == 0 {
		return true
	}
	for i := 0; i+len(needleRunes) <= len(textRunes); i++ {
		match := true
		for j := range needleRunes {
			tr := textRunes[i+j]
			nr := needleRunes[j]
			if tr >= 'A' && tr <= 'Z' {
				tr += 'a' - 'A'
			}
			if nr >= 'A' && nr <= 'Z' {
				nr += 'a' - 'A'
			}
			if tr != nr {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func stringsJoin(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	joined := parts[0]
	for i := 1; i < len(parts); i++ {
		joined += sep + parts[i]
	}
	return joined
}

func newPlaceholderFrame() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	fill := color.RGBA{R: 12, G: 12, B: 12, A: 255}
	for y := 0; y < 720; y++ {
		for x := 0; x < 1280; x++ {
			img.Set(x, y, fill)
		}
	}
	return img
}

func cloneImage(src image.Image) image.Image {
	if src == nil {
		return nil
	}

	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, src, bounds.Min, draw.Src)
	return dst
}

func renderObjectMapImage(canvas *image.RGBA, object trackedObjectDetail) image.Image {
	if canvas == nil {
		return nil
	}

	mat, err := gocv.ImageToMatRGBA(cloneImage(canvas))
	if err != nil {
		return nil
	}
	defer mat.Close()

	drawSelectedObjectPathOverlay(&mat, object)
	rendered, err := mat.ToImage()
	if err != nil {
		return nil
	}
	return cloneImage(rendered)
}
