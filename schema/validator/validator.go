package validator

import (
	"log/slog"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

type Validator struct {
	schema *jsonschema.Schema
}

func (v *Validator) ValidateDoc(document any) error {
	// func (s *Schema) validate(scope []schemaRef, vscope int, spath string, v interface{}, vloc string) (result validationResult, err error) {
	slog.Info("Starting document validation", "schema", v.schema, "document", document)

	if err := v.schema.Validate(document); err != nil {
		slog.Error("Document validation failed", "error", err)
		return err
	}
	slog.Info("Document validation succeeded")
	return nil
}

// New creates a new validator with the given schema *jsonschema.Schema
func New(s *jsonschema.Schema) *Validator {
	return &Validator{schema: s}
}
