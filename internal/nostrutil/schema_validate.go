package nostrutil

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/nbd-wtf/go-nostr"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

const nostrNoteSchemaRelativePath = "json-schema/nostr-json-schema.json"

var (
	nostrNoteSchemaOnce sync.Once
	nostrNoteSchema     *jsonschema.Schema
	nostrNoteSchemaErr  error
)

type SchemaValidationError struct {
	SchemaPath    string
	SchemaJSON    string
	InstanceJSON  string
	ValidationErr error
}

func (e *SchemaValidationError) Error() string {
	return fmt.Sprintf(
		"validate note against schema %s: %v\nnote json:\n%s\njson schema:\n%s",
		e.SchemaPath,
		e.ValidationErr,
		e.InstanceJSON,
		e.SchemaJSON,
	)
}

func (e *SchemaValidationError) Unwrap() error {
	return e.ValidationErr
}

func validateEventAgainstSchema(event nostr.Event) error {
	instanceBytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal note for schema validation: %w", err)
	}
	return validateEventJSONBytes(instanceBytes, "decode marshaled note for schema validation")
}

func ValidateEventJSON(instanceBytes []byte) error {
	return validateEventJSONBytes(instanceBytes, "decode note json")
}

func validateEventJSONBytes(instanceBytes []byte, decodeContext string) error {
	var instance any
	if err := json.Unmarshal(instanceBytes, &instance); err != nil {
		return fmt.Errorf("%s: %w", decodeContext, err)
	}

	schema, schemaPath, err := loadNostrNoteSchema()
	if err != nil {
		return err
	}
	if err := schema.Validate(instance); err != nil {
		schemaJSON, readErr := os.ReadFile(schemaPath)
		if readErr != nil {
			return fmt.Errorf("validate note against schema %s: %w (also failed to read schema: %v)", schemaPath, err, readErr)
		}
		return &SchemaValidationError{
			SchemaPath:    schemaPath,
			SchemaJSON:    string(schemaJSON),
			InstanceJSON:  string(instanceBytes),
			ValidationErr: err,
		}
	}
	return nil
}

func ValidateEventJSONReader(r io.Reader) error {
	instanceBytes, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read note json: %w", err)
	}
	return ValidateEventJSON(instanceBytes)
}

func loadNostrNoteSchema() (*jsonschema.Schema, string, error) {
	schemaPath, err := resolveNostrNoteSchemaPath()
	if err != nil {
		return nil, "", err
	}

	nostrNoteSchemaOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		nostrNoteSchema, nostrNoteSchemaErr = compiler.Compile(schemaPath)
		if nostrNoteSchemaErr != nil {
			nostrNoteSchemaErr = fmt.Errorf("compile nostr note schema %s: %w", schemaPath, nostrNoteSchemaErr)
		}
	})

	if nostrNoteSchemaErr != nil {
		return nil, "", nostrNoteSchemaErr
	}
	return nostrNoteSchema, schemaPath, nil
}

func resolveNostrNoteSchemaPath() (string, error) {
	candidates := []string{nostrNoteSchemaRelativePath}

	if _, sourceFile, _, ok := runtime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
		candidates = append(candidates, filepath.Join(repoRoot, nostrNoteSchemaRelativePath))
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			absolutePath, err := filepath.Abs(candidate)
			if err != nil {
				return "", fmt.Errorf("resolve absolute schema path %s: %w", candidate, err)
			}
			return absolutePath, nil
		}
	}

	return "", fmt.Errorf("locate nostr note schema %s", nostrNoteSchemaRelativePath)
}
