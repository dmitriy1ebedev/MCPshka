package mcpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"mcpshka/internal/rootbroker"
)

func TestTaskContentFitsAuthenticatedBrokerEnvelope(t *testing.T) {
	content := bytes.Repeat([]byte("x"), maxRootTaskPayload)
	parts, err := taskContentChunks(content)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range parts {
		if len(part) > 2800 {
			t.Fatal("broker argument bound exceeded")
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.Join(parts, ""))
	if err != nil || !bytes.Equal(decoded, content) {
		t.Fatalf("content transport changed bytes: %v", err)
	}
	args := append([]string{"authorize-task", strings.Repeat("a", 32), strings.Repeat("o", 160), strings.Repeat("b", 32), strings.Repeat("c", 64), strings.Repeat("d", 64)}, parts...)
	raw, err := json.Marshal(rootbroker.Request{Family: "admin-session", Args: args})
	if err != nil || len(raw)+1 > rootbroker.MaxRequestBytes {
		t.Fatalf("maximum supported content cannot fit broker envelope: %d %v", len(raw), err)
	}
	for _, content := range [][]byte{nil, make([]byte, maxRootTaskPayload+1)} {
		if _, err := taskContentChunks(content); err == nil {
			t.Fatal("empty or oversized staging content accepted")
		}
	}
}
