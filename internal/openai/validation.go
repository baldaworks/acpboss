package openai

import (
	"errors"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// schemaParam reports a location, never a schema error containing user data.
// For union failures it identifies the containing content part rather than a
// possibly unrelated field in one of the rejected alternative schemas.
func schemaParam(err error) string {
	var validation *jsonschema.ValidationError
	if !errors.As(err, &validation) {
		return ""
	}
	var location []string
	var visit func(*jsonschema.ValidationError)
	visit = func(e *jsonschema.ValidationError) {
		here := e.InstanceLocation
		if len(here) == 0 {
			switch k := e.ErrorKind.(type) {
			case *kind.Required:
				if len(k.Missing) > 0 {
					here = []string{k.Missing[0]}
				}
			case *kind.AdditionalProperties:
				if len(k.Properties) > 0 {
					here = []string{k.Properties[0]}
				}
			}
		}
		if len(here) > len(location) {
			location = here
		}
		for _, cause := range e.Causes {
			visit(cause)
		}
	}
	visit(validation)
	if len(location) > 4 && location[0] == "input" && location[2] == "content" {
		location = location[:4]
	}
	var out strings.Builder
	for i, token := range location {
		if _, err := strconv.Atoi(token); err == nil {
			out.WriteString("[" + token + "]")
		} else {
			if i > 0 {
				out.WriteByte('.')
			}
			out.WriteString(token)
		}
	}
	return out.String()
}
