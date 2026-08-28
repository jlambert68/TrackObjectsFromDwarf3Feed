package nostrutil

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestValidateEventAgainstSchemaAcceptsValidTextNote(t *testing.T) {
	imageHash := strings.Repeat("a", 64)
	imageURL := "http://example.test/" + imageHash + ".jpeg"

	event := nostr.Event{
		ID:        strings.Repeat("1", 64),
		PubKey:    strings.Repeat("2", 64),
		CreatedAt: 1,
		Kind:      nostr.KindTextNote,
		Tags: nostr.Tags{
			{
				"imeta",
				"url " + imageURL,
				"m image/jpeg",
				"x " + imageHash,
				"size 1",
				"dim 1x1",
				"alt tracked object",
			},
		},
		Content: imageURL,
		Sig:     strings.Repeat("3", 128),
	}

	if err := validateEventAgainstSchema(event); err != nil {
		t.Fatalf("validate valid event: %v", err)
	}
}

func TestValidateEventAgainstSchemaAcceptsValidGIFNote(t *testing.T) {
	imageHash := strings.Repeat("b", 64)
	imageURL := "http://example.test/" + imageHash + ".gif"

	event := nostr.Event{
		ID:        strings.Repeat("1", 64),
		PubKey:    strings.Repeat("2", 64),
		CreatedAt: 1,
		Kind:      nostr.KindTextNote,
		Tags: nostr.Tags{
			{
				"imeta",
				"url " + imageURL,
				"m image/gif",
				"x " + imageHash,
				"size 1",
				"dim 1x1",
			},
		},
		Content: imageURL,
		Sig:     strings.Repeat("3", 128),
	}

	if err := validateEventAgainstSchema(event); err != nil {
		t.Fatalf("validate valid gif event: %v", err)
	}
}

func TestValidateEventAgainstSchemaRejectsInvalidTags(t *testing.T) {
	event := nostr.Event{
		ID:        strings.Repeat("1", 64),
		PubKey:    strings.Repeat("2", 64),
		CreatedAt: 1,
		Kind:      nostr.KindTextNote,
		Tags:      nostr.Tags{{"x", "not-an-imeta-tag"}},
		Content:   "tracking note",
		Sig:       strings.Repeat("3", 128),
	}

	err := validateEventAgainstSchema(event)
	if err == nil {
		t.Fatal("expected schema validation to fail")
	}
	if !strings.Contains(err.Error(), "validate note against schema") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), `"tags":[["x","not-an-imeta-tag"]]`) {
		t.Fatalf("expected note json in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), `"type": "object"`) {
		t.Fatalf("expected schema json in error, got: %v", err)
	}
}
