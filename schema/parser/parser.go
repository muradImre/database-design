// Package parser compiles a JSON Schema file into a validator that the server
// uses to enforce document shape.
package parser

import (
	"fmt"
	"log/slog"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// SchemaParser compiles the JSON Schema at the given path.
func SchemaParser(schema string) (*jsonschema.Schema, error) {

	compiler := jsonschema.NewCompiler()

	sch, err := compiler.Compile(schema)
	if err != nil {
		slog.Error("Error parsing arguments", slog.String("schema", schema), slog.Any("error", err))
		return nil, fmt.Errorf("error: %v", err)
	}

	return sch, err
}
