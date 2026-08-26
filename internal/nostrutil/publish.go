package nostrutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

const (
	DefaultRelayURL = "ws://127.0.0.1:7447"
	DefaultTimeout  = 10 * time.Second
)

type PublishOptions struct {
	RelayURL         string
	SecretKey        string
	Timeout          time.Duration
	Content          string
	Tags             nostr.Tags
	BlossomServerURL string
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

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	publishCtx := ctx
	var cancel context.CancelFunc
	if publishCtx == nil {
		publishCtx = context.Background()
	}
	if _, hasDeadline := publishCtx.Deadline(); !hasDeadline {
		publishCtx, cancel = context.WithTimeout(publishCtx, timeout)
		defer cancel()
	}

	content, tags, err := prepareBlossomMedia(publishCtx, note, opts.Tags, opts.BlossomServerURL)
	if err != nil {
		return "", fmt.Errorf("prepare blossom media: %w", err)
	}

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
