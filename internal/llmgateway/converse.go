package llmgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Function is one callable the model may ask for (JSON Schema parameters).
type Function struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Message is one turn of a function-calling conversation. Role is system,
// user, assistant or function (a tool result, with Name and CallID).
type Message struct {
	Role    string        `json:"role"`
	Content string        `json:"content"`
	Name    string        `json:"name,omitempty"`
	CallID  string        `json:"call_id,omitempty"`
	Call    *FunctionCall `json:"call,omitempty"`
}

// FunctionCall is the model's request to run one function.
type FunctionCall struct {
	ID string `json:"id,omitempty"`
	// StateID is GigaChat's functions_state_id, echoed back with the call.
	StateID   string         `json:"state_id,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// Turn is one model step: either text or a function call.
type Turn struct {
	Text  string        `json:"text,omitempty"`
	Call  *FunctionCall `json:"call,omitempty"`
	Model string        `json:"model"`
}

// Converse sends one step of a function-calling dialogue to the first
// endpoint whose model answers. Every message is redacted like Ask's input;
// the dialect (OpenAI tools or GigaChat functions) comes from the endpoint.
type sessionKey struct{}

// WithSession tags a dialogue so a provider can cache its stable prefix
// (GigaChat X-Session-ID: cached prompt tokens are not billed).
func WithSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionKey{}, id)
}

func sessionOf(ctx context.Context) string {
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}

func (g *Gateway) Converse(ctx context.Context, purpose string, msgs []Message, funcs []Function, model string) (Turn, error) {
	c, err := g.config()
	if err != nil {
		return Turn{}, err
	}
	if model != "" && !validModel(model) {
		return Turn{}, errors.New("invalid model name")
	}
	size := 0
	for _, m := range msgs {
		size += len(m.Content)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.BudgetSeconds)*time.Second)
	defer cancel()
	var failures []string
	eps := brainFirst(c.endpoints())
	if model != "" {
		eps = g.serving(ctx, eps, model)
		if len(eps) == 0 {
			return Turn{}, fmt.Errorf("LLM no endpoint lists model %q", model)
		}
	}
	for _, ep := range eps {
		if size > ep.MaxInputBytes {
			failures = append(failures, ep.BaseURL+": input over limit")
			continue
		}
		key, err := ep.key()
		if err != nil {
			failures = append(failures, ep.BaseURL+": "+err.Error())
			continue
		}
		clean := make([]Message, len(msgs))
		for i, m := range msgs {
			m.Content = g.redact(m.Content, key).Text
			clean[i] = m
		}
		pick := ep.Model
		if ep.BrainModel != "" {
			pick = ep.BrainModel
		}
		if model != "" {
			pick = model
		}
		start := time.Now()
		turn, err := g.converse(ctx, ep, key, pick, clean, funcs)
		status := "OK"
		if err != nil {
			status = "FAILED"
		}
		g.journal(Send{At: start.UTC().Format(time.RFC3339), Host: hostOf(ep.BaseURL), Model: pick, Purpose: purpose + ":converse",
			Bytes: size, Allowed: true, Status: status, MS: time.Since(start).Milliseconds()})
		if err == nil {
			return turn, nil
		}
		failures = append(failures, pick+": "+err.Error())
		if ctx.Err() != nil {
			break
		}
	}
	return Turn{}, fmt.Errorf("LLM no endpoint answered: %s", strings.Join(failures, "; "))
}

func (g *Gateway) converse(ctx context.Context, c Config, key, model string, msgs []Message, funcs []Function) (Turn, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	giga := c.Dialect == "gigachat"
	body := map[string]any{"model": model, "temperature": 0.1}
	wire := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		w := map[string]any{"role": m.Role, "content": m.Content}
		switch {
		case m.Role == "function" && !giga:
			w = map[string]any{"role": "tool", "tool_call_id": m.CallID, "content": m.Content}
		case m.Role == "function":
			// GigaChat wants the result as a JSON document inside content.
			w["name"], w["content"] = m.Name, jsonContent(m.Content)
		case m.Call != nil && giga:
			w["function_call"] = map[string]any{"name": m.Call.Name, "arguments": m.Call.Arguments}
			if m.Call.StateID != "" {
				w["functions_state_id"] = m.Call.StateID
			}
		case m.Call != nil:
			args, _ := json.Marshal(m.Call.Arguments)
			w["tool_calls"] = []any{map[string]any{"id": m.Call.ID, "type": "function",
				"function": map[string]any{"name": m.Call.Name, "arguments": string(args)}}}
		}
		wire = append(wire, w)
	}
	body["messages"] = wire
	if len(funcs) > 0 && giga {
		body["functions"], body["function_call"] = funcs, "auto"
	} else if len(funcs) > 0 {
		tools := make([]any, len(funcs))
		for i, f := range funcs {
			tools[i] = map[string]any{"type": "function", "function": f}
		}
		body["tools"] = tools
	}
	data, err := g.request(ctx, c, key, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return Turn{}, err
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content      string `json:"content"`
				StateID      string `json:"functions_state_id"`
				FunctionCall *struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function_call"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &out) != nil || len(out.Choices) == 0 {
		return Turn{}, errors.New("LLM reply has no choices")
	}
	if out.Model == "" {
		out.Model = model
	}
	msg := out.Choices[0].Message
	turn := Turn{Text: msg.Content, Model: out.Model}
	switch {
	case msg.FunctionCall != nil:
		turn.Call = &FunctionCall{Name: msg.FunctionCall.Name, StateID: msg.StateID}
		turn.Call.Arguments, err = callArgs(msg.FunctionCall.Arguments)
	case len(msg.ToolCalls) > 0:
		tc := msg.ToolCalls[0]
		turn.Call = &FunctionCall{ID: tc.ID, Name: tc.Function.Name}
		turn.Call.Arguments, err = callArgs(tc.Function.Arguments)
	}
	if err != nil {
		return Turn{}, err
	}
	if turn.Call == nil && strings.TrimSpace(turn.Text) == "" {
		return Turn{}, errors.New("LLM reply has no content")
	}
	return turn, nil
}

// jsonContent keeps a JSON result as is and wraps plain text as {"result": …}.
func jsonContent(s string) string {
	if json.Valid([]byte(s)) && strings.HasPrefix(strings.TrimSpace(s), "{") {
		return s
	}
	b, _ := json.Marshal(map[string]string{"result": s})
	return string(b)
}

// callArgs accepts an object (GigaChat) or a JSON string holding one (OpenAI).
func callArgs(raw json.RawMessage) (map[string]any, error) {
	args := map[string]any{}
	if len(raw) == 0 || string(raw) == "null" {
		return args, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	if s == "" && string(raw) == "" {
		return args, nil
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, errors.New("LLM function arguments are not a JSON object")
	}
	return args, nil
}

func hostOf(base string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	return h
}

// brainFirst orders endpoints with a brain_model ahead of the rest.
func brainFirst(eps []Config) []Config {
	out := make([]Config, 0, len(eps))
	for _, e := range eps {
		if e.BrainModel != "" {
			out = append(out, e)
		}
	}
	for _, e := range eps {
		if e.BrainModel == "" {
			out = append(out, e)
		}
	}
	return out
}

// serving keeps the endpoints whose catalog lists model (any AI the owner
// configured: GigaChat, FreeLLM, an OpenAI-compatible Claude or GPT key).
func (g *Gateway) serving(ctx context.Context, eps []Config, model string) []Config {
	var out []Config
	for _, ep := range eps {
		// The model the owner configured for this endpoint is served by it
		// even when its catalog does not list it: FreeLLMAPI routes "auto"
		// but /v1/models names only the concrete models (0.19.0 acceptance).
		if model == ep.Model || model == ep.BrainModel {
			out = append(out, ep)
			continue
		}
		key, err := ep.key()
		if err != nil {
			continue
		}
		list, err := g.models(ctx, ep, key)
		if err != nil {
			continue
		}
		for _, m := range list {
			if m.ID == model {
				out = append(out, ep)
				break
			}
		}
	}
	return out
}
