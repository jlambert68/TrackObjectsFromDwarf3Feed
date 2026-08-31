package nostrutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	wantHash := expectedImageHash([]byte("jpeg-bytes"))
	wantURL := "http://blossom.test/" + wantHash + ".jpg"

	var mediaCalls atomic.Int32
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		switch r.URL.Path {
		case "/media":
			mediaCalls.Add(1)
			assertBlossomAuthHeader(t, r.Header.Get("Authorization"), "media", wantHash)
			return jsonResponse(http.StatusNotFound, map[string]any{"error": "not found"}), nil
		case "/upload":
			if got := r.Header.Get("Content-Type"); got != "image/jpeg" {
				t.Fatalf("unexpected content type: %s", got)
			}
			assertBlossomAuthHeader(t, r.Header.Get("Authorization"), "upload", "")
			return jsonResponse(http.StatusOK, map[string]any{
				"sha256": wantHash,
				"url":    wantURL,
				"size":   len([]byte("jpeg-bytes")),
				"type":   "image/jpeg",
				"nip94": [][]string{
					{"url", wantURL},
					{"m", "image/jpeg"},
					{"x", wantHash},
					{"size", "10"},
					{"dim", "320x240"},
				},
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
		"",
		"d9beda766a3c0062e12e1c701e5c6e25c16aa3e6ad246f3c43b513949497ec2b",
	)
	if err != nil {
		t.Fatalf("prepare blossom media: %v", err)
	}

	if got := content; got != "object\n"+wantURL+"\n"+wantURL {
		t.Fatalf("unexpected content: %q", got)
	}
	if len(tags) != 1 {
		t.Fatalf("unexpected tags length: %#v", tags)
	}
	if got := tags[0]; len(got) < 5 || got[0] != "imeta" ||
		got[1] != "url "+wantURL ||
		got[2] != "m image/jpeg" ||
		got[3] != "x "+wantHash ||
		got[4] != "size 10" {
		t.Fatalf("unexpected tags: %#v", tags)
	}
	if got := mediaCalls.Load(); got != 1 {
		t.Fatalf("expected one /media call, got %d", got)
	}
}

func TestPrepareBlossomMediaSynthesizesDescriptorFieldsFromUploadFallback(t *testing.T) {
	tempDir := t.TempDir()
	imagePath := filepath.Join(tempDir, "frame.jpg")
	imageBytes := []byte("jpeg-bytes")
	if err := os.WriteFile(imagePath, imageBytes, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	wantHash := expectedImageHash(imageBytes)
	wantURL := "http://blossom.test/" + wantHash + ".jpg"

	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/media":
			return jsonResponse(http.StatusNotFound, map[string]any{"error": "not found"}), nil
		case "/upload":
			return jsonResponse(http.StatusOK, map[string]any{
				"sha256": wantHash,
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
		imagePath,
		nostr.Tags{{"x", imagePath}},
		"http://blossom.test",
		"",
		"d9beda766a3c0062e12e1c701e5c6e25c16aa3e6ad246f3c43b513949497ec2b",
	)
	if err != nil {
		t.Fatalf("prepare blossom media: %v", err)
	}
	if content != wantURL {
		t.Fatalf("unexpected content: %q", content)
	}
	if len(tags) != 1 {
		t.Fatalf("unexpected tags: %#v", tags)
	}
	if tags[0][0] != "imeta" || tags[0][1] != "url "+wantURL || tags[0][2] != "m image/jpeg" {
		t.Fatalf("unexpected imeta tag: %#v", tags[0])
	}
}

func TestPrepareBlossomMediaCanonicalizesLoopbackBindAddress(t *testing.T) {
	tempDir := t.TempDir()
	imagePath := filepath.Join(tempDir, "frame.jpg")
	imageBytes := []byte("jpeg-bytes")
	if err := os.WriteFile(imagePath, imageBytes, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	wantHash := expectedImageHash(imageBytes)
	wantURL := "http://localhost:3000/" + wantHash + ".jpg"

	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.URL.Scheme + "://" + r.URL.Host; got != "http://localhost:3000" {
			t.Fatalf("unexpected canonical request base: %s", got)
		}
		switch r.URL.Path {
		case "/media":
			assertBlossomAuthHeaderForServer(t, r.Header.Get("Authorization"), "media", "localhost:3000", wantHash)
			return jsonResponse(http.StatusNotFound, map[string]any{"error": "not found"}), nil
		case "/upload":
			assertBlossomAuthHeaderForServer(t, r.Header.Get("Authorization"), "upload", "localhost:3000", "")
			return jsonResponse(http.StatusOK, map[string]any{
				"sha256": wantHash,
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
		imagePath,
		nostr.Tags{{"x", imagePath}},
		"http://0.0.0.0:3000",
		"",
		"d9beda766a3c0062e12e1c701e5c6e25c16aa3e6ad246f3c43b513949497ec2b",
	)
	if err != nil {
		t.Fatalf("prepare blossom media: %v", err)
	}
	if content != wantURL {
		t.Fatalf("unexpected content: %q", content)
	}
	if len(tags) != 1 {
		t.Fatalf("unexpected tags: %#v", tags)
	}
	if tags[0][1] != "url "+wantURL {
		t.Fatalf("unexpected imeta tag: %#v", tags[0])
	}
}

func TestPrepareBlossomMediaUsesCustomNoteBaseURL(t *testing.T) {
	tempDir := t.TempDir()
	imagePath := filepath.Join(tempDir, "frame.jpg")
	imageBytes := []byte("jpeg-bytes")
	if err := os.WriteFile(imagePath, imageBytes, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	wantHash := expectedImageHash(imageBytes)
	wantURL := "https://notes.example/media/" + wantHash + ".jpg"

	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/media":
			return jsonResponse(http.StatusNotFound, map[string]any{"error": "not found"}), nil
		case "/upload":
			return jsonResponse(http.StatusOK, map[string]any{
				"sha256": wantHash,
				"url":    "http://127.0.0.1:3000/" + wantHash + ".jpg",
				"nip94": [][]string{
					{"thumb", "http://127.0.0.1:3000/thumbs/" + wantHash + ".webp"},
				},
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
		imagePath,
		nostr.Tags{{"x", imagePath}},
		"http://127.0.0.1:3000",
		"https://notes.example/media",
		"d9beda766a3c0062e12e1c701e5c6e25c16aa3e6ad246f3c43b513949497ec2b",
	)
	if err != nil {
		t.Fatalf("prepare blossom media: %v", err)
	}
	if content != wantURL {
		t.Fatalf("unexpected content: %q", content)
	}
	if len(tags) != 1 {
		t.Fatalf("unexpected tags: %#v", tags)
	}
	if tags[0][1] != "url "+wantURL {
		t.Fatalf("unexpected imeta tag: %#v", tags[0])
	}
	if len(tags[0]) > 5 && tags[0][5] != "thumb https://notes.example/media/thumbs/"+wantHash+".webp" {
		t.Fatalf("unexpected thumb tag: %#v", tags[0])
	}
}

func TestBlossomIMetaTagUsesThumbURLOnly(t *testing.T) {
	tag := blossomIMetaTag(blossomBlobDescriptor{
		URL:    "http://127.0.0.1:3000/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpeg",
		SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:   1556,
		Type:   "image/jpeg",
		NIP94: [][]string{
			{"thumb", "http://127.0.0.1:3000/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.webp", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	})

	if len(tag) != 6 {
		t.Fatalf("unexpected imeta tag: %#v", tag)
	}
	if tag[5] != "thumb http://127.0.0.1:3000/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.webp" {
		t.Fatalf("unexpected thumb entry: %#v", tag)
	}
}

func TestPrepareBlossomMediaLeavesNonImageReferencesAlone(t *testing.T) {
	content, tags, err := prepareBlossomMedia(
		context.Background(),
		"note\n/tmp/not-an-image.txt",
		nostr.Tags{{"x", "/tmp/not-an-image.txt"}},
		"http://127.0.0.1:1",
		"",
		"d9beda766a3c0062e12e1c701e5c6e25c16aa3e6ad246f3c43b513949497ec2b",
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

func TestPrepareBlossomMediaUploadsGIFDirectlyToUploadEndpoint(t *testing.T) {
	tempDir := t.TempDir()
	imagePath := filepath.Join(tempDir, "object.gif")
	imageBytes := []byte("GIF89a-test-payload")
	if err := os.WriteFile(imagePath, imageBytes, 0644); err != nil {
		t.Fatalf("write gif: %v", err)
	}
	wantHash := expectedImageHash(imageBytes)
	wantURL := "http://blossom.test/" + wantHash + ".gif"

	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/media":
			t.Fatalf("gif upload should not use /media")
			return nil, nil
		case "/upload":
			if got := r.Header.Get("Content-Type"); got != "image/gif" {
				t.Fatalf("unexpected content type: %s", got)
			}
			assertBlossomAuthHeader(t, r.Header.Get("Authorization"), "upload", "")
			return jsonResponse(http.StatusOK, map[string]any{
				"sha256": wantHash,
				"url":    wantURL,
				"size":   len(imageBytes),
				"type":   "image/gif",
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
		imagePath,
		nostr.Tags{{"x", imagePath}},
		"http://blossom.test",
		"",
		"d9beda766a3c0062e12e1c701e5c6e25c16aa3e6ad246f3c43b513949497ec2b",
	)
	if err != nil {
		t.Fatalf("prepare blossom media: %v", err)
	}
	if content != wantURL {
		t.Fatalf("unexpected content: %q", content)
	}
	if len(tags) != 1 || tags[0][0] != "imeta" || tags[0][1] != "url "+wantURL || tags[0][2] != "m image/gif" {
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

func assertBlossomAuthHeader(t *testing.T, header, wantVerb, wantHash string) {
	t.Helper()
	assertBlossomAuthHeaderForServer(t, header, wantVerb, "blossom.test", wantHash)
}

func assertBlossomAuthHeaderForServer(t *testing.T, header, wantVerb, wantServer, wantHash string) {
	t.Helper()
	const prefix = "Nostr "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		t.Fatalf("unexpected authorization header: %q", header)
	}
	data, err := base64.StdEncoding.DecodeString(header[len(prefix):])
	if err != nil {
		t.Fatalf("decode auth header: %v", err)
	}
	var event nostr.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("unmarshal auth event: %v", err)
	}
	if event.Kind != 24242 {
		t.Fatalf("unexpected auth event kind: %d", event.Kind)
	}
	if event.PubKey == "" || event.Sig == "" {
		t.Fatalf("missing signed auth event fields: %#v", event)
	}
	if got := event.Tags.GetFirst([]string{"t"}); got == nil || got.Value() != wantVerb {
		t.Fatalf("missing %s tag: %#v", wantVerb, event.Tags)
	}
	if got := event.Tags.GetFirst([]string{"server"}); got == nil || got.Value() != wantServer {
		t.Fatalf("missing server tag: %#v", event.Tags)
	}
	xTag := event.Tags.GetFirst([]string{"x"})
	if wantHash == "" {
		if xTag != nil {
			t.Fatalf("unexpected x tag: %#v", event.Tags)
		}
		return
	}
	if xTag == nil || xTag.Value() != wantHash {
		t.Fatalf("missing x tag %q: %#v", wantHash, event.Tags)
	}
}

func expectedImageHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
