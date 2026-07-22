package validator_test

import (
	"strings"
	"testing"

	"github.com/muradImre/database-design/schema/validator"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Helper function to create a schema for testing
func createTestSchema() *jsonschema.Schema {
	// Sample JSON schema that expects an object with properties 'name' and 'age'
	schemaStr := `
	{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"age": {"type": "integer"}
		},
		"required": ["name", "age"]
	}`

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("testSchema.json", strings.NewReader(schemaStr)); err != nil {
		panic(err)
	}

	schema, err := compiler.Compile("testSchema.json")
	if err != nil {
		panic(err)
	}

	return schema
}

func TestValidator_ValidateDoc_Success(t *testing.T) {
	// Arrange
	schema := createTestSchema()
	validator := validator.New(schema)

	validDoc := map[string]interface{}{
		"name": "John",
		"age":  30,
	}

	// Act
	err := validator.ValidateDoc(validDoc)

	// Assert
	if err != nil {
		t.Errorf("Expected document to pass validation, but got error: %v", err)
	}
}

func TestValidator_ValidateDoc_MissingField(t *testing.T) {
	// Arrange
	schema := createTestSchema()
	validator := validator.New(schema)

	invalidDoc := map[string]interface{}{
		"name": "John",
		// Missing "age" field
	}

	// Act
	err := validator.ValidateDoc(invalidDoc)

	// Assert
	if err == nil {
		t.Error("Expected validation to fail due to missing 'age' field, but it passed.")
	}
}

func TestValidator_ValidateDoc_InvalidFieldType(t *testing.T) {
	// Arrange
	schema := createTestSchema()
	validator := validator.New(schema)

	invalidDoc := map[string]interface{}{
		"name": "John",
		"age":  "thirty", // Invalid type, should be an integer
	}

	// Act
	err := validator.ValidateDoc(invalidDoc)

	// Assert
	if err == nil {
		t.Error("Expected validation to fail due to 'age' field being the wrong type, but it passed.")
	}
}

func TestValidator_ValidateDoc_EmptyDocument(t *testing.T) {
	// Arrange
	schema := createTestSchema()
	validator := validator.New(schema)

	emptyDoc := map[string]interface{}{}

	// Act
	err := validator.ValidateDoc(emptyDoc)

	// Assert
	if err == nil {
		t.Error("Expected validation to fail for empty document, but it passed.")
	}
}

func TestValidator_ValidateDoc_ExtraFields(t *testing.T) {
	// Arrange
	schema := createTestSchema()
	validator := validator.New(schema)

	docWithExtraFields := map[string]interface{}{
		"name":    "John",
		"age":     30,
		"address": "123 Main St", // This field is not defined in the schema but should not cause validation errors
	}

	// Act
	err := validator.ValidateDoc(docWithExtraFields)

	// Assert
	if err != nil {
		t.Errorf("Expected document with extra fields to pass validation, but got error: %v", err)
	}
}
