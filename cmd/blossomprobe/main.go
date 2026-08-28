package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"dwarf3-event-tracker/internal/nostrutil"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "blossomprobe failed:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("blossomprobe", flag.ContinueOnError)
	fs.SetOutput(stderr)

	serverURL := fs.String("server", nostrutil.DefaultBlossomServerURL, "Blossom server base URL")
	endpoint := fs.String("endpoint", "/media", "Blossom endpoint to probe: /media or /upload")
	imagePath := fs.String("file", "", "Local image path to upload")
	secret := fs.String("secret", "", "Nostr private key as nsec or 64-char hex (falls back to NOSTR_SECRET_KEY)")
	timeout := fs.Duration("timeout", 45*time.Second, "HTTP request timeout")
	showAuthHeader := fs.Bool("print-auth-header", false, "Print the full Authorization header")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*imagePath) == "" {
		return errors.New("missing -file")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := nostrutil.ProbeBlossomUpload(ctx, nostrutil.BlossomProbeOptions{
		ServerURL: *serverURL,
		Endpoint:  *endpoint,
		Path:      *imagePath,
		SecretKey: *secret,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "server_url: %s\n", result.ServerURL)
	fmt.Fprintf(stdout, "endpoint: %s\n", result.Endpoint)
	fmt.Fprintf(stdout, "file: %s\n", result.Path)
	fmt.Fprintf(stdout, "content_type: %s\n", result.ContentType)
	fmt.Fprintf(stdout, "sha256: %s\n", result.BlobSHA256)
	fmt.Fprintf(stdout, "auth_event_json: %s\n", result.AuthEventJSON)
	if *showAuthHeader {
		fmt.Fprintf(stdout, "authorization: %s\n", result.Authorization)
	}
	fmt.Fprintf(stdout, "status_code: %d\n", result.StatusCode)
	if len(result.ResponseHeaders) > 0 {
		keys := make([]string, 0, len(result.ResponseHeaders))
		for key := range result.ResponseHeaders {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Fprintln(stdout, "response_headers:")
		for _, key := range keys {
			fmt.Fprintf(stdout, "  %s: %s\n", key, strings.Join(result.ResponseHeaders.Values(key), ", "))
		}
	}
	if result.ResponseBody != "" {
		fmt.Fprintf(stdout, "response_body: %s\n", result.ResponseBody)
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return fmt.Errorf("blossom probe returned HTTP %d", result.StatusCode)
	}
	return nil
}
