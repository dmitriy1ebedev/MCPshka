package llmgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func (g *Gateway) request(ctx context.Context, c Config, key, method, path string, body any) ([]byte, error) {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("LLM invalid request")
	}
	req.Header.Set("Content-Type", "application/json")
	// api.giga.chat refuses requests without a User-Agent.
	req.Header.Set("User-Agent", "mcpshka-llmgateway")
	if id := sessionOf(ctx); id != "" && len(id) <= 64 {
		req.Header.Set("X-Session-ID", id)
	}
	release, err := g.slot(ctx, c)
	if err != nil {
		return nil, err
	}
	defer release()
	bearer, err := g.bearer(ctx, c, key)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	// Never forward credentials or filtered data to redirect destinations.
	bounded, err := c.client(g.HTTP)
	if err != nil {
		return nil, err
	}
	resp, err := bounded.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("LLM network request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil {
		return nil, errors.New("LLM body read failed")
	}
	if resp.StatusCode != http.StatusOK {
		// Redact before truncating: truncation must not hide a secret's recognisable shape.
		safe := g.redact(string(data), key, bearer).Text
		safe = strings.Join(strings.Fields(safe), " ")
		return nil, fmt.Errorf("LLM status %d: %s", resp.StatusCode, safe[:min(len(safe), 512)])
	}
	if len(data) > maxReply {
		return nil, errors.New("LLM reply too large")
	}
	return data, nil
}

func (g *Gateway) complete(ctx context.Context, c Config, key, model, system, user string, probe bool) (Reply, error) {
	body := map[string]any{
		"model": model, "temperature": 0.2,
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
	}
	if probe {
		body["max_tokens"] = 8
	}
	data, err := g.request(ctx, c, key, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return Reply{}, err
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &out) != nil || len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return Reply{}, errors.New("LLM reply has no content")
	}
	if out.Model == "" {
		out.Model = model
	}
	if !validModel(out.Model) {
		return Reply{}, errors.New("LLM invalid response model")
	}
	return Reply{Text: out.Choices[0].Message.Content, Model: out.Model}, nil
}

func validModel(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsAny(s, " \r\n\t\"")
}
