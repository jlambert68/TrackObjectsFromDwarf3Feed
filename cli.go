package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"gocv.io/x/gocv"
)

// main parses startup options, optionally prompts the user for an input source,
// validates the chosen source, and hands control to the processing loop.
func main() {
	defaultURL := "rtsp://192.168.88.1/ch0/stream0"

	sourceMode := flag.String("source", sourceDwarf, "input source: dwarf or video")
	rtspURL := flag.String("url", defaultURL, "DWARF 3 RTSP URL")
	videoPath := flag.String("video", "", "path to an existing video file to analyze")
	legacyVideoFile := flag.String("file", "", "legacy alias for -video")
	outputDir := flag.String("out", "events", "directory for saved events")
	showMask := flag.Bool("mask", true, "show motion-mask window")
	fallbackFPS := flag.Float64("fps", 30.0, "fallback recording FPS if source does not report FPS")
	flag.Parse()
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -source=dwarf [-url rtsp://...]\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -source=video -video /path/to/video.mp4\n\n", os.Args[0])
		flag.PrintDefaults()
	}

	if *videoPath == "" {
		*videoPath = *legacyVideoFile
	}

	if shouldPromptForSourceChoice() {
		chosenSource, chosenVideoPath, err := promptForSourceChoice(*rtspURL)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		*sourceMode = chosenSource
		if chosenVideoPath != "" {
			*videoPath = chosenVideoPath
		}
	}

	var input string
	var inputLabel string

	switch normalizeSourceMode(*sourceMode) {
	case sourceDwarf:
		input = *rtspURL
		inputLabel = "DWARF 3 live stream"
	case sourceVideo:
		if *videoPath == "" {
			fmt.Fprintln(os.Stderr, "error: -video is required when -source=video")
			os.Exit(1)
		}
		if _, err := os.Stat(*videoPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: video file %q: %v\n", *videoPath, err)
			os.Exit(1)
		}
		input = *videoPath
		inputLabel = "video file"
	default:
		fmt.Fprintln(os.Stderr, "error: -source must be dwarf or video")
		os.Exit(1)
	}

	if err := runCLI(TrackerConfig{
		Input:       input,
		InputLabel:  inputLabel,
		OutputDir:   *outputDir,
		ShowMask:    *showMask,
		FallbackFPS: *fallbackFPS,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// shouldPromptForSourceChoice decides whether startup should fall back to the
// interactive menu instead of relying purely on flags.
func shouldPromptForSourceChoice() bool {
	if len(os.Args) == 1 {
		return true
	}

	promptFlags := map[string]struct{}{
		"source": {},
		"url":    {},
		"video":  {},
		"file":   {},
	}

	explicitSourceSelection := false
	flag.Visit(func(f *flag.Flag) {
		if _, ok := promptFlags[f.Name]; ok {
			explicitSourceSelection = true
		}
	})

	return !explicitSourceSelection
}

// promptForSourceChoice presents a minimal text menu for interactive runs.
func promptForSourceChoice(defaultURL string) (string, string, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Select input source:")
	fmt.Println("  1) DWARF live feed")
	fmt.Println("  2) Existing video file")
	fmt.Print("Choice [1/2]: ")

	choice, err := reader.ReadString('\n')
	if err != nil {
		return "", "", fmt.Errorf("read menu choice: %w", err)
	}

	switch strings.TrimSpace(choice) {
	case "", "1":
		fmt.Printf("Using DWARF live feed: %s\n", defaultURL)
		return sourceDwarf, "", nil
	case "2":
		fmt.Print("Video path: ")
		videoPath, err := reader.ReadString('\n')
		if err != nil {
			return "", "", fmt.Errorf("read video path: %w", err)
		}
		videoPath = strings.TrimSpace(videoPath)
		if videoPath == "" {
			return "", "", errors.New("video path cannot be empty")
		}
		return sourceVideo, videoPath, nil
	default:
		return "", "", fmt.Errorf("invalid menu choice %q", strings.TrimSpace(choice))
	}
}

// normalizeSourceMode keeps backward compatibility with the earlier live/file
// flag values while exposing the clearer dwarf/video names.
func normalizeSourceMode(mode string) string {
	switch mode {
	case sourceDwarf, "live":
		return sourceDwarf
	case sourceVideo, "file":
		return sourceVideo
	default:
		return mode
	}
}

// runCLI is the current desktop-preview shell around the reusable tracker
// engine. A future Fyne UI can replace this function without changing the
// capture and tracking pipeline.
func runCLI(config TrackerConfig) error {
	window := gocv.NewWindow("DWARF 3 Fast Object Tracker")
	defer window.Close()

	var maskWindow *gocv.Window
	if config.ShowMask {
		maskWindow = gocv.NewWindow("Motion Mask")
		defer maskWindow.Close()
	}

	engine := TrackerEngine{
		Config: config,
		Hooks: TrackerHooks{
			OnReady: func(ready TrackerReady) error {
				fmt.Printf("Opening %s: %s\n", ready.InputLabel, ready.Input)
				fmt.Printf("Recording FPS: %.3f\n", ready.FPS)
				fmt.Println("Tracking started. ESC = quit")
				return nil
			},
			OnEventStart: func(dir string) error {
				fmt.Println("EVENT START:", dir)
				return nil
			},
			OnEventSaved: func(dir string) error {
				fmt.Println("EVENT SAVED:", dir)
				return nil
			},
			OnFrame: func(update *FrameUpdate) error {
				window.IMShow(update.Display)
				if maskWindow != nil && update.hasMask {
					maskWindow.IMShow(update.Mask)
				}
				if window.WaitKey(1) == 27 {
					return ErrStopTracking
				}
				return nil
			},
		},
	}

	return engine.Run()
}
