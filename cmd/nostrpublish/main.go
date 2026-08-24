package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

const (
	defaultRelayURL = "ws://127.0.0.1:7447"
	defaultTimeout  = 10 * time.Second
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "nostrpublish failed:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("nostrpublish", flag.ContinueOnError)
	fs.SetOutput(stderr)

	relayURL := fs.String("relay", defaultRelayURL, "relay websocket URL")
	secret := fs.String("secret", "", "Nostr private key as nsec or 64-char hex (falls back to NOSTR_SECRET_KEY)")
	content := fs.String("content", "", "note content; if empty, read from remaining args or stdin")
	timeout := fs.Duration("timeout", defaultTimeout, "relay publish timeout")

	if err := fs.Parse(args); err != nil {
		return err
	}

	note := strings.TrimSpace(*content)
	if note == "" && fs.NArg() > 0 {
		note = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if note == "" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		note = strings.TrimSpace(string(data))
	}
	if note == "" {
		return errors.New("missing note content; pass -content, a trailing argument, or pipe stdin")
	}

	secretKey, err := resolveSecretKey(*secret)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, *relayURL)
	if err != nil {
		return fmt.Errorf("connect relay %s: %w", *relayURL, err)
	}
	defer relay.Close()

	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      nostr.KindTextNote,
		Tags:      nil,
		Content:   note,
	}
	if err := event.Sign(secretKey); err != nil {
		return fmt.Errorf("sign note: %w", err)
	}

	if err := relay.Publish(ctx, event); err != nil {
		return fmt.Errorf("publish note: %w", err)
	}

	fmt.Fprintf(stdout, "published event %s to %s\n", event.ID, *relayURL)
	return nil
}

func resolveSecretKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("NOSTR_SECRET_KEY"))
	}
	if value == "" {
		return "", errors.New("missing secret key; pass -secret or set NOSTR_SECRET_KEY")
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
