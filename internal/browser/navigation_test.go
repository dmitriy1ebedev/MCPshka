package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
)

// Speak the actual NUL-framed CDP protocol over pipes; no Chromium or network.
func TestNavigationFailureStages(t *testing.T) {
	for _, tc := range []struct {
		name, detail, url, stage string
		status                   int
	}{
		{name: "TLS error document", detail: "net::ERR_CERT_DATE_INVALID", url: "chrome-error://chromewebdata/", stage: "tls"},
		{name: "TLS", detail: "net::ERR_CERT_AUTHORITY_INVALID", url: "https://gitverse.ru/", stage: "tls"},
		{name: "page load timeout", url: "https://gitverse.ru/", stage: "navigation"},
		{name: "DNS", detail: "net::ERR_NAME_NOT_RESOLVED", url: "https://gitverse.ru/", stage: "navigation"},
		{name: "HA login", url: "https://gitverse.ru/auth/authorize", stage: "auth"},
		{name: "HTTP unauthorized", url: "https://gitverse.ru/", status: 401, stage: "auth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			replies, server, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer requests.Close()
			defer w.Close()
			defer replies.Close()
			defer server.Close()
			b := &cdp{w: w, r: bufio.NewReader(replies), site: "gitverse.ru", pending: map[int]chan reply{}, waiters: map[string][]chan struct{}{}, done: make(chan struct{}), stderr: &tailBuffer{limit: 1024}}
			go b.read()
			done := make(chan struct{})
			go func() {
				defer close(done)
				rd := bufio.NewReader(requests)
				send := func(v any) { raw, _ := json.Marshal(v); server.Write(append(raw, 0)) }
				for {
					raw, err := rd.ReadBytes(0)
					if err != nil {
						return
					}
					var m message
					if json.Unmarshal(raw[:len(raw)-1], &m) != nil {
						return
					}
					result := any(map[string]any{})
					switch m.Method {
					case "Page.navigate":
						result = map[string]any{"errorText": tc.detail}
						if tc.status != 0 {
							send(map[string]any{"method": "Network.responseReceived", "params": map[string]any{"type": "Document", "response": map[string]any{"status": tc.status}}})
						}
					case "Runtime.evaluate":
						result = map[string]any{"result": map[string]any{"value": tc.url}}
					}
					send(map[string]any{"id": m.ID, "result": result})
					if m.Method == "Page.navigate" && tc.detail == "" && tc.name != "page load timeout" {
						send(map[string]any{"method": "Page.loadEventFired"})
					}
				}
			}()
			spec := Spec{Site: "gitverse.ru"}
			res := Result{}
			got := b.step(context.Background(), spec, Step{Action: "goto", URL: "https://gitverse.ru/", TimeoutMS: 500}, &res)
			if got.OK || got.FailureStage != tc.stage {
				t.Errorf("%+v", got)
			}
			w.Close()
			<-done
			server.Close()
			<-b.done
		})
	}
}
