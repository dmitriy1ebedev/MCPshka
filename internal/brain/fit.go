package brain

import "mcpshka/internal/llmgateway"

// fit shortens the oldest tool results until the conversation's content is
// within budget; the task, the latest result and the model's own turns stay
// whole. A shortened result keeps its head and says so.
func fit(msgs []llmgateway.Message, budget int) []llmgateway.Message {
	size := 0
	for _, m := range msgs {
		size += len(m.Content)
	}
	last := -1
	for i, m := range msgs {
		if m.Role == "function" {
			last = i
		}
	}
	for i := range msgs {
		if size <= budget {
			break
		}
		if msgs[i].Role != "function" || i == last || len(msgs[i].Content) <= 600 {
			continue
		}
		short := clip(msgs[i].Content, 400) + " …[старый результат сокращён; повтори вызов, если он нужен целиком]"
		size -= len(msgs[i].Content) - len(short)
		msgs[i].Content = short
	}
	return msgs
}
