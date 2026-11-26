package mcpserver

import (
	"encoding/json"
	"reflect"
	"testing"
)

func FuzzMCPJSONObject(f *testing.F) {
	for _, seed := range []string{`{"method":"tools/call","params":{"name":"state_status"}}`, `null`, `[]`, `{"n":9007199254740993}`, `{"nested":{"a":[true,null,"µ"]}}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 32768 {
			return
		}
		var input any
		if json.Unmarshal([]byte(raw), &input) != nil {
			return
		}
		out, err := object(input)
		if input != nil {
			if _, ok := input.(map[string]any); !ok && err == nil {
				t.Fatal("non-object accepted")
			}
		}
		if err != nil {
			return
		}
		next, err := object(out)
		if err != nil || !reflect.DeepEqual(out, next) {
			t.Fatal("JSON object normalization unstable")
		}
	})
}
