package knowledge

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMetadataValidationDetails(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*Record)
	}{
		{"future", "metadata.timestamp: in the future by 3m (limit 1m); server time is ", func(r *Record) { r.Timestamp = time.Now().Add(3 * time.Minute) }},
		{"missing", "metadata.provenance: required", func(r *Record) { r.Provenance = "" }},
		{"control", "metadata.source: contains control characters", func(r *Record) { r.Source = "PRIVATE\nVALUE" }},
		{"length", "metadata.scope: exceeds 256 bytes", func(r *Record) { r.Scope = strings.Repeat("PRIVATE", 100) }},
		{"encoding", "metadata.kind: invalid UTF-8", func(r *Record) { r.Kind = "\xffPRIVATE" }},
		{"zero", "metadata.timestamp: required", func(r *Record) { r.Timestamp = time.Time{} }},
		{"fields", "metadata.fields: requires 1 to 128 entries", func(r *Record) { r.Fields = nil }},
		{"key", "metadata.fields.key: contains control characters", func(r *Record) { r.Fields = map[string]Field{"PRIVATE\n": {}} }},
		{"value", "metadata.fields.value: invalid UTF-8", func(r *Record) { r.Fields = map[string]Field{"PRIVATE": {Value: "\xffPRIVATE"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := normalize(record("valid"))
			tc.change(&r)
			err := valid(r)
			if !errors.Is(err, ErrInvalid) || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("got %v; want %s", err, tc.want)
			}
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("content leaked")
			}
		})
	}
	r := normalize(record("valid"))
	r.Timestamp = time.Now().Add(59 * time.Second)
	if err := valid(r); err != nil {
		t.Fatal(err)
	}
}
