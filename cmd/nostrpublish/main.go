package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"dwarf3-event-tracker/internal/nostrutil"
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

	relayURL := fs.String("relay", nostrutil.DefaultRelayURL, "relay websocket URL")
	secret := fs.String("secret", "", "Nostr private key as nsec or 64-char hex (falls back to NOSTR_SECRET_KEY)")
	content := fs.String("content", "", "note content; if empty, read from remaining args or stdin")
	blossomURL := fs.String("blossom", nostrutil.DefaultBlossomServerURL, "Blossom media server base URL used for local image references")
	blossomNoteURL := fs.String("blossom-note-base", "", "base URL written into note media references; defaults to -blossom")
	timeout := fs.Duration("timeout", nostrutil.DefaultTimeout, "relay publish timeout")
	blossomTimeout := fs.Duration("blossom-timeout", nostrutil.DefaultBlossomTimeout, "Blossom media upload timeout")

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

	eventID, err := nostrutil.PublishTextNote(context.Background(), nostrutil.PublishOptions{
		RelayURL:         *relayURL,
		SecretKey:        *secret,
		Timeout:          *timeout,
		BlossomTimeout:   *blossomTimeout,
		Content:          note,
		BlossomServerURL: *blossomURL,
		BlossomNoteURL:   *blossomNoteURL,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "published event %s to %s\n", eventID, *relayURL)
	return nil
}
