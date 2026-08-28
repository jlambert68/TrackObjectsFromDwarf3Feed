package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"dwarf3-event-tracker/internal/nostrutil"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "nostrvalidatejson failed:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("nostrvalidatejson", flag.ContinueOnError)
	fs.SetOutput(stderr)

	jsonPath := fs.String("file", "", "Path to a Nostr event JSON file; omit to read stdin")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if path := strings.TrimSpace(*jsonPath); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if err := nostrutil.ValidateEventJSON(data); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "valid: %s\n", path)
		return nil
	}

	if err := nostrutil.ValidateEventJSONReader(os.Stdin); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "valid: stdin")
	return nil
}
