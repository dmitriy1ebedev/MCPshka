package llmgateway

import "testing"

func TestExplicitModelGoesToItsEndpointFirst(t *testing.T) {
	free := Config{BaseURL: "http://127.0.0.1:3001/v1", Model: "auto"}
	giga := Config{BaseURL: "https://gigachat.example/api/v1", Model: "GigaChat-2", Dialect: "gigachat"}
	other := Config{BaseURL: "https://other.example/v1", Model: "gpt-x"}
	eps := []Config{free, giga, other}
	if got := preferEndpoint(eps, "GigaChat-2-Max"); got[0].BaseURL != giga.BaseURL || got[1].BaseURL != free.BaseURL || got[2].BaseURL != other.BaseURL {
		t.Fatal("GigaChat model must start at the GigaChat endpoint, order otherwise kept:", got)
	}
	if got := preferEndpoint(eps, "gpt-x"); got[0].BaseURL != other.BaseURL {
		t.Fatal("configured model owner first:", got)
	}
	if got := preferEndpoint(eps, ""); got[0].BaseURL != free.BaseURL {
		t.Fatal("no model keeps the configured order")
	}
	if got := preferEndpoint(eps, "gemini-flash"); got[0].BaseURL != free.BaseURL || len(got) != 3 {
		t.Fatal("unknown owner keeps the order:", got)
	}
}
