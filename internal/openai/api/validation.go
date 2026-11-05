package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var requestSchemas = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	spec, err := GetSwagger()
	if err != nil {
		return nil, err
	}
	if err := spec.Validate(context.Background()); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	const location = "https://acpboss.invalid/openapi.json"
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	result := map[string]*jsonschema.Schema{}
	for _, name := range []string{"CreateResponse", "CreateChatCompletion", "CreateConversation", "UpdateConversation"} {
		schema, err := compiler.Compile(location + "#/components/schemas/" + name)
		if err != nil {
			return nil, err
		}
		result[name] = schema
	}
	return result, nil
})

// Schema returns an operation's compiled generated request contract.
func Schema(name string) (*jsonschema.Schema, error) {
	schemas, err := requestSchemas()
	if err != nil {
		return nil, err
	}
	schema, ok := schemas[name]
	if !ok {
		return nil, fmt.Errorf("unknown request schema")
	}
	return schema, nil
}

// RequestSchema returns the Responses request contract for existing consumers.
func RequestSchema() (*jsonschema.Schema, error) { return Schema("CreateResponse") }
