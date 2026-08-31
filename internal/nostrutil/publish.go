package nostrutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"dwarf3-event-tracker/internal/applog"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

const (
	DefaultRelayURL       = "ws://127.0.0.1:7447"
	DefaultTimeout        = 10 * time.Second
	DefaultBlossomTimeout = 45 * time.Second
)

type PublishOptions struct {
	RelayURL         string
	SecretKey        string
	Timeout          time.Duration
	BlossomTimeout   time.Duration
	Content          string
	Tags             nostr.Tags
	BlossomServerURL string
	BlossomNoteURL   string
}

func PublishTextNote(ctx context.Context, opts PublishOptions) (string, error) {
	note := strings.TrimSpace(opts.Content)
	if note == "" {
		return "", errors.New("missing note content")
	}

	relayURL := strings.TrimSpace(opts.RelayURL)
	if relayURL == "" {
		relayURL = DefaultRelayURL
	}

	secretKey, err := ResolveSecretKey(opts.SecretKey)
	if err != nil {
		return "", err
	}

	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	blossomTimeout := opts.BlossomTimeout
	if blossomTimeout <= 0 {
		blossomTimeout = DefaultBlossomTimeout
	}
	blossomCtx, cancelBlossom := context.WithTimeout(baseCtx, blossomTimeout)
	defer cancelBlossom()

	applog.InfofID(
		"32d1bc68-b2da-4df4-b004-72c412b3fd07",
		"nostr blossom config: upload=%q note=%q",
		strings.TrimSpace(opts.BlossomServerURL),
		strings.TrimSpace(opts.BlossomNoteURL),
	)
	content, tags, err := prepareBlossomMedia(blossomCtx, note, opts.Tags, opts.BlossomServerURL, opts.BlossomNoteURL, secretKey)
	if err != nil {
		if fallbackContent, fallbackTags, ok := blossomTextFallback(note, opts.Tags, err); ok {
			applog.ErrorfID("513c7d8d-df5a-47c4-bd77-5d4eb8e965ea", "nostr blossom upload failed, continuing with text-only note: %v", err)
			content = fallbackContent
			tags = fallbackTags
		} else if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("prepare blossom media via %s exceeded timeout after %s: %w", strings.TrimSpace(opts.BlossomServerURL), blossomTimeout, err)
		} else {
			return "", fmt.Errorf("prepare blossom media: %w", err)
		}
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	publishCtx, cancelPublish := context.WithTimeout(baseCtx, timeout)
	defer cancelPublish()

	relay, err := nostr.RelayConnect(publishCtx, relayURL)
	if err != nil {
		return "", fmt.Errorf("connect relay %s: %w", relayURL, err)
	}
	defer relay.Close()

	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      nostr.KindTextNote,
		Content:   content,
		Tags:      tags,
	}
	if err := event.Sign(secretKey); err != nil {
		return "", fmt.Errorf("sign note: %w", err)
	}
	if err := validateEventAgainstSchema(event); err != nil {
		return "", err
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal signed note: %w", err)
	}
	applog.InfofID("85536d4d-6e75-4828-b4e5-2b4804a064e1", "nostr event json: %s", eventJSON)

	if err := relay.Publish(publishCtx, event); err != nil {
		return "", fmt.Errorf("publish note: %w", err)
	}

	return event.ID, nil
}

func ResolveSecretKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("NOSTR_SECRET_KEY"))
	}
	if value == "" {
		return "", errors.New("missing secret key; pass one explicitly or set NOSTR_SECRET_KEY")
	}

	if strings.HasPrefix(value, "nsec1") {
		prefix, decoded, err := nip19.Decode(value)
		if err != nil {
			return "", fmt.Errorf("decode nsec: %w", err)
		}
		if prefix != "nsec" {
			return "", fmt.Errorf("expected nsec key, got %s", prefix)
		}
		secretKey, ok := decoded.(string)
		if !ok || secretKey == "" {
			return "", errors.New("decoded nsec did not produce a secret key")
		}
		return secretKey, nil
	}

	if len(value) != 64 {
		return "", errors.New("hex secret key must be 64 characters")
	}
	return value, nil
}

func blossomTextFallback(content string, tags nostr.Tags, err error) (string, nostr.Tags, bool) {
	if !shouldFallbackWithoutBlossom(err) {
		return "", nil, false
	}

	sanitizedContent := stripLocalImageLines(content)
	if strings.TrimSpace(sanitizedContent) == "" {
		return "", nil, false
	}
	if sanitizedContent == content && len(tags) == 0 {
		return "", nil, false
	}
	return sanitizedContent, nil, true
}

func shouldFallbackWithoutBlossom(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}

	var statusErr *blossomStatusError
	if errors.As(err, &statusErr) && statusErr.StatusCode >= 500 {
		return true
	}

	return false
}

func stripLocalImageLines(content string) string {
	if strings.TrimSpace(content) == "" {
		return content
	}

	lines := strings.Split(content, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if _, ok := localImagePathFromReference(strings.TrimSpace(line)); ok {
			continue
		}
		filtered = append(filtered, line)
	}

	return strings.TrimSpace(strings.Join(filtered, "\n"))
}
