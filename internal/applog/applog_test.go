package applog

import (
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
