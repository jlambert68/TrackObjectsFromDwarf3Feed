package main

import (
	"image"
	"math"
	"testing"
	"time"
)

func TestDefaultGeneralTrackingSettingsAreHardened(t *testing.T) {
	settings := DefaultTrackingSettings()
	if settings.MinArea != 10.0 {
		t.Fatalf("unexpected MinArea: %v", settings.MinArea)
	}
	if settings.SlowMinSpeed != 14.0 {
		t.Fatalf("unexpected SlowMinSpeed: %v", settings.SlowMinSpeed)
	}
	if settings.MinSpeed != 55.0 {
		t.Fatalf("unexpected MinSpeed: %v", settings.MinSpeed)
	}
	if settings.MinHits != 3 {
		t.Fatalf("unexpected MinHits: %d", settings.MinHits)
	}
	if settings.ForegroundThreshold != 220.0 {
		t.Fatalf("unexpected ForegroundThreshold: %v", settings.ForegroundThreshold)
	}
	if settings.MOG2VarThreshold != 24.0 {
		t.Fatalf("unexpected MOG2VarThreshold: %v", settings.MOG2VarThreshold)
	}
}

func TestClassifyTrackRejectsJitterWithoutMeaningfulTravel(t *testing.T) {
	settings := DefaultTrackingSettings()
	track := &Track{
		Hits:  5,
		Speed: 70.0,
		Trail: []image.Point{
			{X: 100, Y: 100},
			{X: 103, Y: 101},
			{X: 101, Y: 99},
			{X: 104, Y: 100},
			{X: 102, Y: 101},
		},
	}

	if trackType, ok := classifyTrack(track, settings); ok {
		t.Fatalf("expected jitter track to be rejected, got type=%s", trackType)
	}
}

func TestClassifyTrackAcceptsRealMotion(t *testing.T) {
	settings := DefaultTrackingSettings()
	track := &Track{
		Hits:  5,
		Speed: 70.0,
		Trail: []image.Point{
			{X: 100, Y: 100},
			{X: 108, Y: 101},
			{X: 116, Y: 103},
			{X: 125, Y: 104},
			{X: 134, Y: 106},
		},
	}

	trackType, ok := classifyTrack(track, settings)
	if !ok {
		t.Fatal("expected real motion track to be accepted")
	}
	if trackType != trackTypeFast {
		t.Fatalf("expected fast track, got %s", trackType)
	}
}

func TestNormalizeTrackingSettingsPreservesAcceptedZeroValues(t *testing.T) {
	settings := DefaultTrackingSettings()
	settings.MinArea = 0
	settings.SlowMinSpeed = 0
	settings.MinSpeed = 0
	settings.ForegroundThreshold = 0
	settings.PreEventDuration = 0
	settings.PostEventDuration = 0

	got := NormalizeTrackingSettings(settings)
	if got.MinArea != 0 || got.SlowMinSpeed != 0 || got.MinSpeed != 0 || got.ForegroundThreshold != 0 {
		t.Fatalf("numeric zero values were replaced: %+v", got)
	}
	if got.PreEventDuration != 0 || got.PostEventDuration != 0 {
		t.Fatalf("zero event buffers were replaced: pre=%s post=%s", got.PreEventDuration, got.PostEventDuration)
	}
}

func TestNormalizeTrackingSettingsReplacesNonFiniteValues(t *testing.T) {
	settings := DefaultTrackingSettings()
	settings.MinArea = math.NaN()
	settings.MinSpeed = math.Inf(1)
	settings.TrackingROIHeightFrac = math.NaN()
	settings.PreEventDuration = -time.Second

	got := NormalizeTrackingSettings(settings)
	defaults := DefaultTrackingSettings()
	if got.MinArea != defaults.MinArea || got.MinSpeed != defaults.MinSpeed || got.TrackingROIHeightFrac != defaults.TrackingROIHeightFrac {
		t.Fatalf("non-finite settings were not normalized: %+v", got)
	}
	if got.PreEventDuration != defaults.PreEventDuration {
		t.Fatalf("negative pre-event duration was not normalized: %s", got.PreEventDuration)
	}
}
