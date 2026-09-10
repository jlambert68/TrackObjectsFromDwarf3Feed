package applog

import (
	"strings"
	"testing"
)

func TestOutputRejectsInvalidUUID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for invalid UUID")
		}
	}()
	output(infoLogger, "INFO", "not-a-uuid", "logger smoke test")
}

func TestOutputAcceptsHardcodedUUID(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("did not expect panic, got %v", recovered)
		}
	}()
	output(infoLogger, "INFO", "6e2f4c52-71e9-4d1a-8f91-41f8f9f71fb2", "logger smoke test")
}

func TestSubscriberReceivesOneLineLogEntry(t *testing.T) {
	entries := make(chan string, 1)
	unsubscribe := Subscribe(func(entry string) {
		entries <- entry
	})
	defer unsubscribe()

	InfofID("6e2f4c52-71e9-4d1a-8f91-41f8f9f71fb2", "first line\nsecond line\r\nthird line")
	entry := <-entries
	if strings.ContainsAny(entry, "\r\n") {
		t.Fatalf("subscriber received a multiline entry: %q", entry)
	}
	if !strings.Contains(entry, "first line second line third line") {
		t.Fatalf("subscriber received unexpected entry: %q", entry)
	}

	unsubscribe()
	InfofID("6e2f4c52-71e9-4d1a-8f91-41f8f9f71fb2", "after unsubscribe")
	select {
	case unexpected := <-entries:
		t.Fatalf("subscriber was called after unsubscribe: %q", unexpected)
	default:
	}
}
