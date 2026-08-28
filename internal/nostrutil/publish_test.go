package nostrutil

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

func TestPublishTextNoteReportsBlossomTimeoutSeparately(t *testing.T) {
	tempDir := t.TempDir()
	imagePath := tempDir + "/frame.jpg"
	if err := os.WriteFile(imagePath, []byte("jpeg-bytes"), 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	t.Cleanup(func() {
		http.DefaultClient = originalClient
	})

	_, err := PublishTextNote(context.Background(), PublishOptions{
		RelayURL:         "ws://127.0.0.1:1",
		SecretKey:        strings.Repeat("1", 64),
		Content:          imagePath,
		Tags:             nil,
		BlossomServerURL: "http://blossom.test",
		BlossomTimeout:   5 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected blossom timeout error")
	}
	if !strings.Contains(err.Error(), "prepare blossom media via http://blossom.test exceeded timeout") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got: %v", err)
	}
}

func TestPublishTextNoteFallsBackToTextOnlyWhenBlossomTimesOut(t *testing.T) {
	tempDir := t.TempDir()
	imagePath := tempDir + "/frame.jpg"
	if err := os.WriteFile(imagePath, []byte("jpeg-bytes"), 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	t.Cleanup(func() {
		http.DefaultClient = originalClient
	})

	content, tags, ok := blossomTextFallback("File: sample\nObjects:\n#0001\n"+imagePath, nostr.Tags{{"x", imagePath}}, context.DeadlineExceeded)
	if !ok {
		t.Fatal("expected fallback")
	}
	if content != "File: sample\nObjects:\n#0001" {
		t.Fatalf("unexpected fallback content: %q", content)
	}
	if tags != nil {
		t.Fatalf("expected no fallback tags, got %#v", tags)
	}
}
