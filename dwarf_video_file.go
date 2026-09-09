package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dwarf3-event-tracker/internal/applog"
)

const (
	dwarfVideoDownloadAttempts   = 5
	dwarfVideoDownloadRetryDelay = 3 * time.Second
)

// validateDwarfVideoFile rejects incomplete ISO base media files before they
// reach OpenCV/FFmpeg. DWARF writes the moov box last, so its absence means the
// recording was downloaded before the device finished closing the MP4.
func validateDwarfVideoFile(localPath string) error {
	return validateDwarfVideoFileAs(localPath, filepath.Ext(localPath))
}

func validateDwarfVideoFileAs(localPath, extension string) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("inspect video file: %w", err)
	}
	if info.IsDir() {
		return errors.New("video path is a directory")
	}
	if info.Size() == 0 {
		return errors.New("video file is empty")
	}

	switch strings.ToLower(extension) {
	case ".mp4", ".mov":
		return validateISOBaseMediaFile(localPath, info.Size())
	default:
		return nil
	}
}

func validateISOBaseMediaFile(localPath string, fileSize int64) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open video for validation: %w", err)
	}
	defer file.Close()

	foundMediaData := false
	foundMovieMetadata := false
	for offset := int64(0); offset < fileSize; {
		remaining := fileSize - offset
		if remaining < 8 {
			return fmt.Errorf("incomplete MP4 box header at byte %d", offset)
		}

		var header [16]byte
		if _, err := file.ReadAt(header[:8], offset); err != nil {
			return fmt.Errorf("read MP4 box at byte %d: %w", offset, err)
		}
		boxSize := uint64(binary.BigEndian.Uint32(header[:4]))
		headerSize := uint64(8)
		if boxSize == 1 {
			if remaining < 16 {
				return fmt.Errorf("incomplete extended MP4 box header at byte %d", offset)
			}
			if _, err := file.ReadAt(header[8:16], offset+8); err != nil {
				return fmt.Errorf("read extended MP4 box at byte %d: %w", offset, err)
			}
			boxSize = binary.BigEndian.Uint64(header[8:16])
			headerSize = 16
		} else if boxSize == 0 {
			boxSize = uint64(remaining)
		}

		boxType := string(header[4:8])
		if boxSize < headerSize {
			return fmt.Errorf("invalid MP4 %q box size %d at byte %d", boxType, boxSize, offset)
		}
		if boxSize > uint64(remaining) {
			return fmt.Errorf("incomplete MP4 %q box at byte %d: declared %d bytes, only %d available", boxType, offset, boxSize, remaining)
		}

		switch boxType {
		case "mdat":
			foundMediaData = true
		case "moov":
			foundMovieMetadata = true
		}
		offset += int64(boxSize)
	}

	if !foundMediaData {
		return errors.New("MP4 is missing its mdat media box")
	}
	if !foundMovieMetadata {
		return errors.New("MP4 is missing its moov metadata box; the recording is incomplete")
	}
	return nil
}

func downloadValidatedDwarfVideo(controller DwarfController, remotePath, localPath string) error {
	return downloadValidatedDwarfVideoWithRetry(
		remotePath,
		localPath,
		dwarfVideoDownloadAttempts,
		dwarfVideoDownloadRetryDelay,
		controller.DownloadFile,
		time.Sleep,
	)
}

func downloadValidatedDwarfVideoWithRetry(
	remotePath string,
	localPath string,
	attempts int,
	retryDelay time.Duration,
	download func(remotePath, localPath string) error,
	wait func(time.Duration),
) error {
	if attempts < 1 {
		attempts = 1
	}
	if wait == nil {
		wait = time.Sleep
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("create DWARF download directory: %w", err)
	}

	partialPath := localPath + ".part"
	defer os.Remove(partialPath)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := os.Remove(partialPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale partial DWARF download: %w", err)
		}

		if err := download(remotePath, partialPath); err != nil {
			lastErr = fmt.Errorf("download file: %w", err)
		} else if err := validateDwarfVideoFileAs(partialPath, filepath.Ext(localPath)); err != nil {
			lastErr = err
		} else {
			if err := os.Rename(partialPath, localPath); err != nil {
				return fmt.Errorf("publish completed DWARF download: %w", err)
			}
			return nil
		}

		if attempt < attempts {
			applog.InfofID(
				"84513592-0a5d-4ea8-831e-7d3a0b8f1ad3",
				"DWARF video is not ready; retrying download %d/%d in %s: remote=%s reason=%v",
				attempt+1,
				attempts,
				retryDelay,
				remotePath,
				lastErr,
			)
			wait(retryDelay)
		}
	}

	return fmt.Errorf("download a complete DWARF video after %d attempts: %w", attempts, lastErr)
}
