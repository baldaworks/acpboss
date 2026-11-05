package api

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var requestSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
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
	return compiler.Compile(location + "#/components/schemas/CreateResponse")
})

// RequestSchema returns the compiled generated request contract, including
// JSON Schema 2020-12 const/null constraints and component references.
func RequestSchema() (*jsonschema.Schema, error) { return requestSchema() }
