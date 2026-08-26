package nostrutil

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestPrepareBlossomMediaReplacesLocalImageReferences(t *testing.T) {
	tempDir := t.TempDir()
	imagePath := filepath.Join(tempDir, "frame.jpg")
	if err := os.WriteFile(imagePath, []byte("jpeg-bytes"), 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	var mediaCalls atomic.Int32
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		switch r.URL.Path {
		case "/media":
			mediaCalls.Add(1)
			return jsonResponse(http.StatusNotFound, map[string]any{"error": "not found"}), nil
		case "/upload":
			if got := r.Header.Get("Content-Type"); got != "image/jpeg" {
				t.Fatalf("unexpected content type: %s", got)
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}), nil
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
			return nil, nil
		}
	})}
	t.Cleanup(func() {
		http.DefaultClient = originalClient
	})

	content, tags, err := prepareBlossomMedia(
		context.Background(),
		"object\n"+imagePath+"\n"+imagePath,
		nostr.Tags{{"x", imagePath}},
		"http://blossom.test",
	)
	if err != nil {
		t.Fatalf("prepare blossom media: %v", err)
	}

	const wantHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := content; got != "object\n"+wantHash+"\n"+wantHash {
		t.Fatalf("unexpected content: %q", got)
	}
	if len(tags) != 1 || len(tags[0]) < 2 || tags[0][1] != wantHash {
		t.Fatalf("unexpected tags: %#v", tags)
	}
	if got := mediaCalls.Load(); got != 1 {
		t.Fatalf("expected one /media call, got %d", got)
	}
}

func TestPrepareBlossomMediaLeavesNonImageReferencesAlone(t *testing.T) {
	content, tags, err := prepareBlossomMedia(
		context.Background(),
		"note\n/tmp/not-an-image.txt",
		nostr.Tags{{"x", "/tmp/not-an-image.txt"}},
		"http://127.0.0.1:1",
	)
	if err != nil {
		t.Fatalf("prepare blossom media: %v", err)
	}
	if got := content; got != "note\n/tmp/not-an-image.txt" {
		t.Fatalf("unexpected content: %q", got)
	}
	if len(tags) != 1 || tags[0][1] != "/tmp/not-an-image.txt" {
		t.Fatalf("unexpected tags: %#v", tags)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func jsonResponse(statusCode int, body map[string]any) *http.Response {
	data, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}
