package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"gocv.io/x/gocv"
)

type trackerApp struct {
	window fyne.Window

	sourceRadio              *widget.RadioGroup
	urlEntry                 *widget.Entry
	fileEntry                *widget.Entry
	outputEntry              *widget.Entry
	fpsEntry                 *widget.Entry
	showMask                 *widget.Check
	dateFilter               *widget.Entry
	objectFilter             *widget.Entry
	speedFilter              *widget.Entry
	sortSelect               *widget.Select
	presetSelect             *widget.Select
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
	mog2HistoryEntry         *widget.Entry
	mog2VarThresholdEntry    *widget.Entry
	roiHeightEntry           *widget.Entry

	fileButton            *widget.Button
	outputButton          *widget.Button
	startButton           *widget.Button
	stopButton            *widget.Button
	resetTrackingButton   *widget.Button
	importTrackingButton  *widget.Button
	exportTrackingButton  *widget.Button
	savePresetButton      *widget.Button
	applyPresetButton     *widget.Button
	deletePresetButton    *widget.Button
	refreshButton         *widget.Button
	openTrackedButton     *widget.Button
	openOriginalButton    *widget.Button
	restoreSettingsButton *widget.Button

	videoImage *canvas.Image
	maskImage  *canvas.Image
	tabs       *container.AppTabs

	statusLabel   *widget.Label
	eventLabel    *widget.Label
	historyList   *widget.List
	historyInfo   *widget.Label
	historyDetail *widget.Entry

	allHistoryEntries []eventHistoryEntry
	historyEntries    []eventHistoryEntry
	selectedHistory   int
	presets           map[string]TrackingSettings

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
}

type eventHistoryEntry struct {
	Directory string
	Summary   EventSummary
}

type eventHistoryDetail struct {
	Summary          EventSummary
	Tracking         *EventMetadata
	TrackingPath     string
	HasTracking      bool
	FirstActiveMS    int64
	LastActiveMS     int64
	MaxTracks        int
	MaxFastTracks    int
	MaxSlowTracks    int
	FramesWithTracks int
}

type playbackOverlay struct {
	Tracking EventMetadata
	Settings TrackingSettings
}

const (
	sortNewestFirst      = "Newest First"
	sortHighestObjects   = "Highest Object Count"
	sortHighestPeakSpeed = "Highest Peak Speed"

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
	prefTrackingMOG2History = "tracking.mog2_history"
	prefTrackingMOG2Var     = "tracking.mog2_var_threshold"
	prefTrackingROIHeight   = "tracking.roi_height_fraction"
	prefTrackingPresets     = "tracking.presets"
	prefTrackingPresetName  = "tracking.preset_name"
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
	application := app.New()
	window := application.NewWindow("DWARF 3 Fast Object Tracker")
	window.Resize(fyne.NewSize(1360, 860))

	ui := newTrackerApp(window)
	window.SetContent(ui.buildUI())
	window.SetCloseIntercept(func() {
		ui.stopTracking()
		window.Close()
	})

	window.ShowAndRun()
}

func newTrackerApp(window fyne.Window) *trackerApp {
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

	videoImage := canvas.NewImageFromImage(newPlaceholderFrame())
	videoImage.FillMode = canvas.ImageFillContain
	videoImage.SetMinSize(fyne.NewSize(960, 540))

	maskImage := canvas.NewImageFromImage(newPlaceholderFrame())
	maskImage.FillMode = canvas.ImageFillContain
	maskImage.SetMinSize(fyne.NewSize(960, 540))

	historyInfo := widget.NewLabel("Select an event")
	historyInfo.Wrapping = fyne.TextWrapWord
	historyDetail := widget.NewMultiLineEntry()
	historyDetail.SetText("Select an event to inspect event.json and tracking.json.")
	historyDetail.Disable()

	defaults := DefaultTrackingSettings()
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
	mog2HistoryEntry := widget.NewEntry()
	mog2HistoryEntry.SetText(strconv.Itoa(defaults.MOG2History))
	mog2VarThresholdEntry := widget.NewEntry()
	mog2VarThresholdEntry.SetText(formatFloat(defaults.MOG2VarThreshold))
	roiHeightEntry := widget.NewEntry()
	roiHeightEntry.SetText(formatFloat(defaults.TrackingROIHeightFrac))

	ui := &trackerApp{
		window:                   window,
		sourceRadio:              sourceRadio,
		urlEntry:                 urlEntry,
		fileEntry:                fileEntry,
		outputEntry:              outputEntry,
		fpsEntry:                 fpsEntry,
		showMask:                 showMask,
		dateFilter:               dateFilter,
		objectFilter:             objectFilter,
		speedFilter:              speedFilter,
		sortSelect:               sortSelect,
		presetSelect:             presetSelect,
		presetNameEntry:          presetNameEntry,
		minAreaEntry:             minAreaEntry,
		maxAreaEntry:             maxAreaEntry,
		slowSpeedEntry:           slowSpeedEntry,
		minSpeedEntry:            minSpeedEntry,
		matchDistanceEntry:       matchDistanceEntry,
		minHitsEntry:             minHitsEntry,
		blurSizeEntry:            blurSizeEntry,
		foregroundThresholdEntry: foregroundThresholdEntry,
		preEventEntry:            preEventEntry,
		postEventEntry:           postEventEntry,
		mog2HistoryEntry:         mog2HistoryEntry,
		mog2VarThresholdEntry:    mog2VarThresholdEntry,
		roiHeightEntry:           roiHeightEntry,
		videoImage:               videoImage,
		maskImage:                maskImage,
		statusLabel:              widget.NewLabel("Idle"),
		eventLabel:               widget.NewLabel("No event yet"),
		historyInfo:              historyInfo,
		historyDetail:            historyDetail,
		selectedHistory:          -1,
		presets:                  make(map[string]TrackingSettings),
	}

	ui.fileButton = widget.NewButtonWithIcon("", theme.FolderOpenIcon(), ui.pickVideoFile)
	ui.outputButton = widget.NewButtonWithIcon("", theme.FolderOpenIcon(), ui.pickOutputFolder)
	ui.startButton = widget.NewButtonWithIcon("Start", theme.MediaPlayIcon(), ui.startTracking)
	ui.stopButton = widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), ui.stopTracking)
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
	ui.stopButton.Disable()
	ui.openTrackedButton.Disable()
	ui.openOriginalButton.Disable()
	ui.restoreSettingsButton.Disable()
	ui.applyPresetButton.Disable()
	ui.deletePresetButton.Disable()

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

	ui.loadTrackingPreferences()
	ui.loadTrackingPresets()
	ui.loadHistoryPreferences()
	ui.refreshEventHistory()

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
				container.NewBorder(nil, nil, widget.NewLabel("MOG2 History"), nil, ui.mog2HistoryEntry),
				container.NewBorder(nil, nil, widget.NewLabel("MOG2 Var Threshold"), nil, ui.mog2VarThresholdEntry),
				container.NewBorder(nil, nil, widget.NewLabel("ROI Height Fraction"), nil, ui.roiHeightEntry),
			),
		)),
	)

	actions := container.NewHBox(ui.startButton, ui.stopButton)

	controls := container.NewVBox(
		sourceRow,
		urlRow,
		fileRow,
		outputRow,
		options,
		trackingSettings,
		actions,
	)

	statusBar := container.NewVBox(
		widget.NewSeparator(),
		ui.statusLabel,
		ui.eventLabel,
	)

	trackedTab := container.NewTabItem("Tracked", container.NewPadded(ui.videoImage))
	maskTab := container.NewTabItem("Mask", container.NewPadded(ui.maskImage))
	ui.tabs = container.NewAppTabs(trackedTab, maskTab)

	historyHeader := container.NewBorder(nil, nil, widget.NewLabelWithStyle("Event History", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), ui.refreshButton)
	historyActions := container.NewHBox(ui.openTrackedButton, ui.openOriginalButton, ui.restoreSettingsButton)
	filterRow := container.NewGridWithColumns(1,
		container.NewBorder(nil, nil, widget.NewLabel("Date"), nil, ui.dateFilter),
		container.NewBorder(nil, nil, widget.NewLabel("Min Objects"), nil, ui.objectFilter),
		container.NewBorder(nil, nil, widget.NewLabel("Min Peak Speed"), nil, ui.speedFilter),
		container.NewBorder(nil, nil, widget.NewLabel("Sort"), nil, ui.sortSelect),
	)
	ui.historyDetail.SetMinRowsVisible(14)
	historyTop := container.NewVBox(historyHeader, filterRow, historyActions, ui.historyInfo, ui.historyDetail)
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
			dialog.ShowError(err, ui.window)
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
			dialog.ShowError(err, ui.window)
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

func (ui *trackerApp) startTracking() {
	ui.mu.Lock()
	if ui.running {
		ui.mu.Unlock()
		return
	}
	ui.mu.Unlock()

	config, err := ui.buildConfig()
	if err != nil {
		dialog.ShowError(err, ui.window)
		return
	}

	stopCh := make(chan struct{})

	ui.mu.Lock()
	ui.running = true
	ui.stopCh = stopCh
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

func (ui *trackerApp) buildConfig() (TrackerConfig, error) {
	fallbackFPS, err := strconv.ParseFloat(ui.fpsEntry.Text, 64)
	if err != nil || fallbackFPS <= 0 {
		return TrackerConfig{}, errors.New("fallback FPS must be a positive number")
	}

	settings, err := ui.buildTrackingSettings()
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
			Input:       ui.fileEntry.Text,
			InputLabel:  "video file",
			OutputDir:   outputDir,
			ShowMask:    ui.showMask.Checked,
			FallbackFPS: fallbackFPS,
			Settings:    settings,
		}, nil
	}

	if ui.urlEntry.Text == "" {
		return TrackerConfig{}, errors.New("RTSP URL is required")
	}

	return TrackerConfig{
		Input:       ui.urlEntry.Text,
		InputLabel:  "DWARF 3 live stream",
		OutputDir:   outputDir,
		ShowMask:    ui.showMask.Checked,
		FallbackFPS: fallbackFPS,
		Settings:    settings,
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
	engine := TrackerEngine{
		Config: config,
		Stop:   stopCh,
		Hooks: TrackerHooks{
			OnReady: func(ready TrackerReady) error {
				fyne.Do(func() {
					ui.statusLabel.SetText(fmt.Sprintf("Running %s at %.3f FPS (%dx%d)", ready.InputLabel, ready.FPS, ready.Width, ready.Height))
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

	fyne.Do(func() {
		ui.finishRun(err, "Idle")
	})
}

func (ui *trackerApp) runPlayback(path, label string, stopCh chan struct{}, overlay *playbackOverlay) {
	capture, err := gocv.VideoCaptureFile(path)
	if err != nil {
		fyne.Do(func() {
			ui.finishRun(fmt.Errorf("open playback: %w", err), "Idle")
		})
		return
	}
	defer capture.Close()

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

	frame := gocv.NewMat()
	defer frame.Close()
	var overlayFrame gocv.Mat
	frameIndex := 0

	fyne.Do(func() {
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

		if ok := capture.Read(&frame); !ok || frame.Empty() {
			break
		}

		displayMat := frame
		if overlay != nil && frameIndex < len(overlay.Tracking.Frames) {
			overlayFrame = frame.Clone()
			drawMetadataOverlay(&overlayFrame, overlay.Tracking.Frames[frameIndex].Tracks, overlay.Settings)
			displayMat = overlayFrame
		}

		displayImage, err := displayMat.ToImage()
		if &displayMat == &overlayFrame {
			overlayFrame.Close()
		}
		if err != nil {
			fyne.Do(func() {
				ui.finishRun(err, "Idle")
			})
			return
		}

		fyne.Do(func() {
			ui.videoImage.Image = displayImage
			ui.videoImage.Refresh()
			ui.maskImage.Image = newPlaceholderFrame()
			ui.maskImage.Refresh()
			ui.statusLabel.SetText(fmt.Sprintf("Playing %s", label))
		})

		frameIndex++
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
	ui.mu.Unlock()

	ui.startButton.Enable()
	ui.stopButton.Disable()

	switch {
	case err == nil || errors.Is(err, ErrStopTracking):
		ui.statusLabel.SetText(idleText)
	default:
		ui.statusLabel.SetText("Stopped with error")
		dialog.ShowError(err, ui.window)
	}
}

func (ui *trackerApp) refreshEventHistory() {
	entries, err := loadEventHistory(ui.outputEntry.Text)
	if err != nil {
		ui.allHistoryEntries = nil
		ui.historyEntries = nil
		ui.selectedHistory = -1
		ui.historyList.Refresh()
		ui.historyInfo.SetText("Could not load events")
		ui.openTrackedButton.Disable()
		ui.openOriginalButton.Disable()
		ui.restoreSettingsButton.Disable()
		dialog.ShowError(err, ui.window)
		return
	}

	ui.allHistoryEntries = entries
	ui.selectedHistory = -1
	ui.applyHistoryFilters()

	if len(entries) == 0 {
		ui.historyInfo.SetText("No saved events found")
		ui.historyDetail.SetText("No saved events found in the selected output directory.")
		ui.openTrackedButton.Disable()
		ui.openOriginalButton.Disable()
		ui.restoreSettingsButton.Disable()
		return
	}
}

func (ui *trackerApp) updateHistorySelection() {
	if ui.selectedHistory < 0 || ui.selectedHistory >= len(ui.historyEntries) {
		ui.historyInfo.SetText("Select an event")
		ui.historyDetail.SetText("Select an event to inspect event.json and tracking.json.")
		ui.openTrackedButton.Disable()
		ui.openOriginalButton.Disable()
		ui.restoreSettingsButton.Disable()
		return
	}

	entry := ui.historyEntries[ui.selectedHistory]
	detail, err := loadEventDetail(entry)
	if err != nil {
		ui.historyInfo.SetText(entry.title())
		ui.historyDetail.SetText(fmt.Sprintf("Could not load detail preview:\n%v", err))
		ui.openTrackedButton.Enable()
		ui.openOriginalButton.Enable()
		ui.restoreSettingsButton.Enable()
		return
	}

	ui.historyInfo.SetText(fmt.Sprintf("%s   %.1fs   %d objects", entry.title(), entry.Summary.DurationSeconds, entry.Summary.UniqueObjects))
	ui.historyDetail.SetText(formatEventDetail(detail, entry.Directory))
	ui.openTrackedButton.Enable()
	ui.openOriginalButton.Enable()
	ui.restoreSettingsButton.Enable()
}

func (ui *trackerApp) openSelectedEventVideo(tracked bool) {
	ui.mu.Lock()
	running := ui.running
	ui.mu.Unlock()

	if running {
		dialog.ShowInformation("Busy", "Stop the current tracker or playback first.", ui.window)
		return
	}

	if ui.selectedHistory < 0 || ui.selectedHistory >= len(ui.historyEntries) {
		dialog.ShowInformation("No Event Selected", "Select an event from the history list first.", ui.window)
		return
	}

	entry := ui.historyEntries[ui.selectedHistory]
	detail, err := loadEventDetail(entry)
	if err != nil {
		dialog.ShowError(err, ui.window)
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
				Tracking: *detail.Tracking,
				Settings: NormalizeTrackingSettings(detail.Summary.TrackingSettings),
			}
			filename = "original.avi"
			label = "tracked (reconstructed)"
		}
	}

	path := filepath.Join(entry.Directory, filename)
	if _, err := os.Stat(path); err != nil {
		dialog.ShowError(fmt.Errorf("open %s: %w", filename, err), ui.window)
		return
	}

	ui.sourceRadio.SetSelected("Video File")
	ui.fileEntry.SetText(path)
	ui.refreshSourceControls()

	stopCh := make(chan struct{})
	ui.mu.Lock()
	ui.running = true
	ui.stopCh = stopCh
	ui.mu.Unlock()

	ui.startButton.Disable()
	ui.stopButton.Enable()
	ui.statusLabel.SetText("Starting playback...")
	ui.eventLabel.SetText(path)
	ui.resetImages()

	go ui.runPlayback(path, label, stopCh, overlay)
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
	ui.historyList.UnselectAll()
	ui.historyList.Refresh()
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
		dialog.ShowInformation("No Event Selected", "Select an event from the history list first.", ui.window)
		return
	}

	entry := ui.historyEntries[ui.selectedHistory]
	detail, err := loadEventDetail(entry)
	if err != nil {
		dialog.ShowError(err, ui.window)
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
			dialog.ShowError(err, ui.window)
			return
		}
		if reader == nil {
			return
		}
		defer reader.Close()

		var settings TrackingSettings
		if err := json.NewDecoder(reader).Decode(&settings); err != nil {
			dialog.ShowError(fmt.Errorf("decode tracking settings: %w", err), ui.window)
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
		dialog.ShowError(err, ui.window)
		return
	}

	saver := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, ui.window)
			return
		}
		if writer == nil {
			return
		}
		defer writer.Close()

		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(settings); err != nil {
			dialog.ShowError(fmt.Errorf("write tracking settings: %w", err), ui.window)
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
	ui.mog2HistoryEntry.SetText(prefs.StringWithFallback(prefTrackingMOG2History, strconv.Itoa(defaults.MOG2History)))
	ui.mog2VarThresholdEntry.SetText(prefs.StringWithFallback(prefTrackingMOG2Var, formatFloat(defaults.MOG2VarThreshold)))
	ui.roiHeightEntry.SetText(prefs.StringWithFallback(prefTrackingROIHeight, formatFloat(defaults.TrackingROIHeightFrac)))
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
	prefs.SetString(prefTrackingMOG2History, ui.mog2HistoryEntry.Text)
	prefs.SetString(prefTrackingMOG2Var, ui.mog2VarThresholdEntry.Text)
	prefs.SetString(prefTrackingROIHeight, ui.roiHeightEntry.Text)
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
	ui.mog2HistoryEntry.SetText(strconv.Itoa(settings.MOG2History))
	ui.mog2VarThresholdEntry.SetText(formatFloat(settings.MOG2VarThreshold))
	ui.roiHeightEntry.SetText(formatFloat(settings.TrackingROIHeightFrac))
}

func (ui *trackerApp) buildTrackingSettings() (TrackingSettings, error) {
	settings := DefaultTrackingSettings()

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
	if settings.PreEventDuration < 0 || settings.PostEventDuration < 0 {
		return TrackingSettings{}, errors.New("Pre/Post Event Seconds must be non-negative")
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

func (ui *trackerApp) saveTrackingPreset() {
	name := ui.presetNameEntry.Text
	if name == "" {
		dialog.ShowInformation("Preset Name Required", "Enter a preset name first.", ui.window)
		return
	}
	settings, err := ui.buildTrackingSettings()
	if err != nil {
		dialog.ShowError(err, ui.window)
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
		dialog.ShowInformation("No Preset Selected", "Select a preset first.", ui.window)
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
		dialog.ShowInformation("No Preset Selected", "Select a preset first.", ui.window)
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

func loadEventDetail(entry eventHistoryEntry) (eventHistoryDetail, error) {
	detail := eventHistoryDetail{
		Summary: entry.Summary,
	}
	detail.Summary.TrackingSettings = NormalizeTrackingSettings(detail.Summary.TrackingSettings)

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
	}

	return detail, nil
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
