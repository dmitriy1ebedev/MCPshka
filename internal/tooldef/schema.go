package tooldef

import (
	"encoding/json"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"reflect"
)

var RawJSONSchemas = map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}}

// Match the pinned SDK's generic schema inference without changing its nil-output handling.
func Schema[T any](provided any, input bool) any {
	if provided != nil {
		return provided
	}
	t := reflect.TypeFor[T]()
	if t == reflect.TypeFor[any]() {
		if input {
			return &jsonschema.Schema{Type: "object"}
		}
		return nil
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	s, err := jsonschema.ForType(t, &jsonschema.ForOptions{TypeSchemas: RawJSONSchemas})
	if err != nil {
		panic(fmt.Errorf("capability schema %v: %w", t, err))
	}
	return s
}
