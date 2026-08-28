package nostrutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

const DefaultBlossomServerURL = "http://127.0.0.1:3000"

type blossomBlobDescriptor struct {
	URL    string     `json:"url"`
	SHA256 string     `json:"sha256"`
	Size   int64      `json:"size"`
	Type   string     `json:"type"`
	NIP94  [][]string `json:"nip94"`
}

type BlossomProbeOptions struct {
	ServerURL string
	Endpoint  string
	Path      string
	SecretKey string
}

type BlossomProbeResult struct {
	ServerURL       string
	Endpoint        string
	Path            string
	ContentType     string
	BlobSHA256      string
	AuthEventJSON   string
	Authorization   string
	StatusCode      int
	ResponseBody    string
	ResponseHeaders http.Header
}

func prepareBlossomMedia(ctx context.Context, content string, tags nostr.Tags, serverURL, secretKey string) (string, nostr.Tags, error) {
	serverURL = canonicalBlossomServerURL(serverURL)
	if serverURL == "" {
		return content, tags, nil
	}

	cache := make(map[string]blossomBlobDescriptor)
	resolveDescriptor := func(ref string) (blossomBlobDescriptor, bool, error) {
		localPath, ok := localImagePathFromReference(ref)
		if !ok {
			return blossomBlobDescriptor{}, false, nil
		}
		if descriptor, ok := cache[localPath]; ok {
			return descriptor, true, nil
		}
		descriptor, err := uploadImageToBlossom(ctx, serverURL, localPath, secretKey)
		if err != nil {
			return blossomBlobDescriptor{}, true, err
		}
		cache[localPath] = descriptor
		return descriptor, true, nil
	}

	if content != "" {
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			descriptor, ok, err := resolveDescriptor(trimmed)
			if err != nil {
				return "", nil, err
			}
			if !ok {
				continue
			}
			replacement := strings.TrimSpace(descriptor.URL)
			if replacement == "" {
				replacement = strings.TrimSpace(descriptor.SHA256)
			}
			lines[i] = strings.Replace(line, trimmed, replacement, 1)
		}
		content = strings.Join(lines, "\n")
	}

	if len(tags) > 0 {
		rewritten := make(nostr.Tags, 0, len(tags))
		for _, tag := range tags {
			if len(tag) < 2 {
				rewritten = append(rewritten, tag)
				continue
			}
			descriptor, ok, err := resolveDescriptor(tag[1])
			if err != nil {
				return "", nil, err
			}
			if !ok {
				rewritten = append(rewritten, tag)
				continue
			}
			if tag[0] == "x" {
				if imeta := blossomIMetaTag(descriptor); len(imeta) > 1 {
					rewritten = append(rewritten, imeta)
				}
				continue
			}
			cloned := append(nostr.Tag(nil), tag...)
			cloned[1] = descriptor.SHA256
			rewritten = append(rewritten, cloned)
		}
		tags = rewritten
	}

	return content, tags, nil
}

func uploadImageToBlossom(ctx context.Context, serverURL, path, secretKey string) (blossomBlobDescriptor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return blossomBlobDescriptor{}, fmt.Errorf("read image %s: %w", path, err)
	}
	if len(data) == 0 {
		return blossomBlobDescriptor{}, fmt.Errorf("read image %s: empty file", path)
	}

	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	expectedHash := sha256Hex(data)

	endpoint := preferredBlossomUploadEndpoint(strings.ToLower(filepath.Ext(path)), contentType)
	descriptor, err := putBlossomBlob(ctx, serverURL, endpoint, data, contentType, secretKey, expectedHash)
	if err != nil {
		if endpoint != "/media" {
			return blossomBlobDescriptor{}, err
		}
		var statusErr *blossomStatusError
		if !errors.As(err, &statusErr) || (statusErr.StatusCode != http.StatusNotFound && statusErr.StatusCode != http.StatusMethodNotAllowed) {
			return blossomBlobDescriptor{}, err
		}
		descriptor, err = putBlossomBlob(ctx, serverURL, "/upload", data, contentType, secretKey, expectedHash)
		if err != nil {
			return blossomBlobDescriptor{}, err
		}
	}
	descriptor = normalizeBlossomBlobDescriptor(strings.TrimRight(strings.TrimSpace(serverURL), "/"), descriptor, contentType, strings.ToLower(filepath.Ext(path)), int64(len(data)))

	hash := strings.TrimSpace(descriptor.SHA256)
	if len(hash) != 64 {
		return blossomBlobDescriptor{}, fmt.Errorf("blossom upload %s returned invalid sha256 %q", path, hash)
	}
	return descriptor, nil
}

func preferredBlossomUploadEndpoint(ext, contentType string) string {
	if ext == ".gif" || strings.EqualFold(strings.TrimSpace(contentType), "image/gif") {
		return "/upload"
	}
	return "/media"
}

func ProbeBlossomUpload(ctx context.Context, opts BlossomProbeOptions) (BlossomProbeResult, error) {
	serverURL := canonicalBlossomServerURL(opts.ServerURL)
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		endpoint = "/media"
	}
	if endpoint != "/media" && endpoint != "/upload" {
		return BlossomProbeResult{}, fmt.Errorf("unsupported blossom endpoint %q; use /media or /upload", endpoint)
	}
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		return BlossomProbeResult{}, errors.New("missing image path")
	}

	secretKey, err := ResolveSecretKey(opts.SecretKey)
	if err != nil {
		return BlossomProbeResult{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return BlossomProbeResult{}, fmt.Errorf("read image %s: %w", path, err)
	}
	if len(data) == 0 {
		return BlossomProbeResult{}, fmt.Errorf("read image %s: empty file", path)
	}

	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	blobHash := sha256Hex(data)
	authEvent, err := blossomAuthorizationEvent(serverURL, endpoint, secretKey, blobHash)
	if err != nil {
		return BlossomProbeResult{}, err
	}
	authEventJSON, err := json.Marshal(authEvent)
	if err != nil {
		return BlossomProbeResult{}, fmt.Errorf("marshal blossom authorization: %w", err)
	}
	authHeader := "Nostr " + base64.StdEncoding.EncodeToString(authEventJSON)

	baseURL := strings.TrimRight(serverURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+endpoint, bytes.NewReader(data))
	if err != nil {
		return BlossomProbeResult{}, fmt.Errorf("build blossom probe request: %w", err)
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Authorization", authHeader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return BlossomProbeResult{}, fmt.Errorf("send blossom probe request to %s%s: %w", baseURL, endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return BlossomProbeResult{}, fmt.Errorf("read blossom probe response: %w", err)
	}

	return BlossomProbeResult{
		ServerURL:       baseURL,
		Endpoint:        endpoint,
		Path:            path,
		ContentType:     contentType,
		BlobSHA256:      blobHash,
		AuthEventJSON:   string(authEventJSON),
		Authorization:   authHeader,
		StatusCode:      resp.StatusCode,
		ResponseBody:    strings.TrimSpace(string(body)),
		ResponseHeaders: resp.Header.Clone(),
	}, nil
}

type blossomStatusError struct {
	StatusCode int
	Message    string
}

func (e *blossomStatusError) Error() string {
	return fmt.Sprintf("blossom server returned %d: %s", e.StatusCode, e.Message)
}

func putBlossomBlob(ctx context.Context, serverURL, endpoint string, data []byte, contentType, secretKey, blobHash string) (blossomBlobDescriptor, error) {
	baseURL := strings.TrimRight(canonicalBlossomServerURL(serverURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+endpoint, bytes.NewReader(data))
	if err != nil {
		return blossomBlobDescriptor{}, fmt.Errorf("build blossom request: %w", err)
	}
	req.ContentLength = int64(len(data))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	authHeader, err := blossomAuthorizationHeader(baseURL, endpoint, secretKey, blobHash)
	if err != nil {
		return blossomBlobDescriptor{}, err
	}
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return blossomBlobDescriptor{}, fmt.Errorf("upload to blossom %s%s: %w", baseURL, endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return blossomBlobDescriptor{}, fmt.Errorf("read blossom response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return blossomBlobDescriptor{}, &blossomStatusError{
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(body)),
		}
	}

	var descriptor blossomBlobDescriptor
	if err := json.Unmarshal(body, &descriptor); err != nil {
		return blossomBlobDescriptor{}, fmt.Errorf("decode blossom response: %w", err)
	}
	return descriptor, nil
}

func canonicalBlossomServerURL(serverURL string) string {
	trimmed := strings.TrimSpace(serverURL)
	if trimmed == "" {
		return ""
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}

	hostname := strings.TrimSpace(parsed.Hostname())
	if hostname != "0.0.0.0" && hostname != "::" && hostname != "[::]" {
		return strings.TrimRight(parsed.String(), "/")
	}

	port := parsed.Port()
	if port != "" {
		parsed.Host = "localhost:" + port
	} else {
		parsed.Host = "localhost"
	}
	return strings.TrimRight(parsed.String(), "/")
}

func blossomAuthorizationHeader(serverURL, endpoint, secretKey, blobHash string) (string, error) {
	event, err := blossomAuthorizationEvent(serverURL, endpoint, secretKey, blobHash)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal blossom authorization: %w", err)
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(data), nil
}

func blossomAuthorizationEvent(serverURL, endpoint, secretKey, blobHash string) (nostr.Event, error) {
	parsed, err := url.Parse(canonicalBlossomServerURL(serverURL))
	if err != nil {
		return nostr.Event{}, fmt.Errorf("parse blossom server url %q: %w", serverURL, err)
	}
	serverTag := strings.TrimSpace(parsed.Host)
	if serverTag == "" {
		serverTag = strings.TrimSpace(parsed.Hostname())
	}
	if serverTag == "" {
		return nostr.Event{}, fmt.Errorf("missing blossom server hostname in %q", serverURL)
	}
	verb, err := blossomAuthVerbForEndpoint(endpoint)
	if err != nil {
		return nostr.Event{}, err
	}

	now := time.Now().Unix()
	tags := nostr.Tags{
		{"t", verb},
		{"expiration", fmt.Sprintf("%d", now+60)},
		{"server", serverTag},
	}
	if verb == "media" {
		if strings.TrimSpace(blobHash) == "" {
			return nostr.Event{}, errors.New("missing blob hash for blossom media authorization")
		}
		tags = append(tags, nostr.Tag{"x", blobHash})
	}

	event := nostr.Event{
		CreatedAt: nostr.Timestamp(now),
		Kind:      24242,
		Content:   fmt.Sprintf("Authorize %s", verb),
		Tags:      tags,
	}
	if err := event.Sign(secretKey); err != nil {
		return nostr.Event{}, fmt.Errorf("sign blossom authorization: %w", err)
	}
	return event, nil
}

func blossomAuthVerbForEndpoint(endpoint string) (string, error) {
	switch endpoint {
	case "/media":
		return "media", nil
	case "/upload":
		return "upload", nil
	default:
		return "", fmt.Errorf("unsupported blossom endpoint for auth verb: %s", endpoint)
	}
}

func normalizeBlossomBlobDescriptor(serverURL string, descriptor blossomBlobDescriptor, contentType, originalExt string, size int64) blossomBlobDescriptor {
	descriptor.SHA256 = strings.TrimSpace(descriptor.SHA256)
	descriptor.URL = strings.TrimSpace(descriptor.URL)
	descriptor.Type = strings.TrimSpace(descriptor.Type)
	if descriptor.Type == "" {
		descriptor.Type = contentType
	}
	if descriptor.Size <= 0 {
		descriptor.Size = size
	}
	if descriptor.URL == "" && descriptor.SHA256 != "" {
		blobURL := strings.TrimRight(serverURL, "/") + "/" + descriptor.SHA256
		if ext := preferredBlossomFileExtension(descriptor.Type, originalExt); ext != "" {
			blobURL += ext
		}
		descriptor.URL = blobURL
	}
	return descriptor
}

func preferredBlossomFileExtension(mimeType, originalExt string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/avif":
		return ".avif"
	}
	if originalExt != "" {
		return originalExt
	}
	exts, err := mime.ExtensionsByType(mimeType)
	if err != nil || len(exts) == 0 {
		return ""
	}
	return exts[0]
}

func blossomIMetaTag(descriptor blossomBlobDescriptor) nostr.Tag {
	tag := nostr.Tag{"imeta"}
	if url := strings.TrimSpace(descriptor.URL); url != "" {
		tag = append(tag, "url "+url)
	}
	if mimeType := strings.TrimSpace(descriptor.Type); mimeType != "" {
		tag = append(tag, "m "+strings.ToLower(mimeType))
	}
	if hash := strings.TrimSpace(descriptor.SHA256); hash != "" {
		tag = append(tag, "x "+hash)
	}
	if descriptor.Size > 0 {
		tag = append(tag, "size "+strconv.FormatInt(descriptor.Size, 10))
	}
	for _, entry := range descriptor.NIP94 {
		if len(entry) < 2 {
			continue
		}
		name := strings.TrimSpace(entry[0])
		if name == "" || name == "url" || name == "m" || name == "x" || name == "size" {
			continue
		}
		values := make([]string, 0, len(entry)-1)
		for i, value := range entry[1:] {
			if name == "thumb" && i > 0 {
				break
			}
			value = strings.TrimSpace(value)
			if value != "" {
				values = append(values, value)
			}
		}
		if len(values) == 0 {
			continue
		}
		tag = append(tag, name+" "+strings.Join(values, " "))
	}
	return tag
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func localImagePathFromReference(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}

	if strings.HasPrefix(ref, "file://") {
		parsed, err := url.Parse(ref)
		if err != nil {
			return "", false
		}
		ref = parsed.Path
	}

	info, err := os.Stat(ref)
	if err != nil || info.IsDir() {
		return "", false
	}
	switch strings.ToLower(filepath.Ext(ref)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".avif":
		return ref, true
	default:
		return "", false
	}
}
