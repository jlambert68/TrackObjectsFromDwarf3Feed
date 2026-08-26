package nostrutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/nbd-wtf/go-nostr"
)

const DefaultBlossomServerURL = "http://127.0.0.1:3000"

type blossomBlobDescriptor struct {
	SHA256 string `json:"sha256"`
}

func prepareBlossomMedia(ctx context.Context, content string, tags nostr.Tags, serverURL string) (string, nostr.Tags, error) {
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return content, tags, nil
	}

	cache := make(map[string]string)
	resolveHash := func(ref string) (string, bool, error) {
		localPath, ok := localImagePathFromReference(ref)
		if !ok {
			return "", false, nil
		}
		if hash, ok := cache[localPath]; ok {
			return hash, true, nil
		}
		hash, err := uploadImageToBlossom(ctx, serverURL, localPath)
		if err != nil {
			return "", true, err
		}
		cache[localPath] = hash
		return hash, true, nil
	}

	if content != "" {
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			hash, ok, err := resolveHash(trimmed)
			if err != nil {
				return "", nil, err
			}
			if !ok {
				continue
			}
			lines[i] = strings.Replace(line, trimmed, hash, 1)
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
			hash, ok, err := resolveHash(tag[1])
			if err != nil {
				return "", nil, err
			}
			if !ok {
				rewritten = append(rewritten, tag)
				continue
			}
			cloned := append(nostr.Tag(nil), tag...)
			cloned[1] = hash
			rewritten = append(rewritten, cloned)
		}
		tags = rewritten
	}

	return content, tags, nil
}

func uploadImageToBlossom(ctx context.Context, serverURL, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read image %s: %w", path, err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("read image %s: empty file", path)
	}

	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}

	descriptor, err := putBlossomBlob(ctx, serverURL, "/media", data, contentType)
	if err != nil {
		var statusErr *blossomStatusError
		if !errors.As(err, &statusErr) || (statusErr.StatusCode != http.StatusNotFound && statusErr.StatusCode != http.StatusMethodNotAllowed) {
			return "", err
		}
		descriptor, err = putBlossomBlob(ctx, serverURL, "/upload", data, contentType)
		if err != nil {
			return "", err
		}
	}

	hash := strings.TrimSpace(descriptor.SHA256)
	if len(hash) != 64 {
		return "", fmt.Errorf("blossom upload %s returned invalid sha256 %q", path, hash)
	}
	return hash, nil
}

type blossomStatusError struct {
	StatusCode int
	Message    string
}

func (e *blossomStatusError) Error() string {
	return fmt.Sprintf("blossom server returned %d: %s", e.StatusCode, e.Message)
}

func putBlossomBlob(ctx context.Context, serverURL, endpoint string, data []byte, contentType string) (blossomBlobDescriptor, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+endpoint, bytes.NewReader(data))
	if err != nil {
		return blossomBlobDescriptor{}, fmt.Errorf("build blossom request: %w", err)
	}
	req.ContentLength = int64(len(data))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

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
