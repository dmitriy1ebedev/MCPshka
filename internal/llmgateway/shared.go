package llmgateway

import (
	"os"
	"path/filepath"
)

// Shared is the server's gateway: config /etc/mcpshka/llm.json, journal in
// the state dir. Without the config every ask is LLM_UNAVAILABLE.
var Shared = &Gateway{JournalPath: filepath.Join(stateDir(), "llm", "sends.jsonl"), HealthPath: filepath.Join(stateDir(), "llm", "health.json")}

// DigestSystem steers the outside model to compress tool data for another AI.
const DigestSystem = "Ты помощник, который сжимает данные инструментов для другого ИИ. Отвечай коротко, по-русски, только фактами из данных; чего нет в данных — пиши «нет данных»."

func stateDir() string {
	if d := os.Getenv("MCPSHKA_STATE_DIR"); d != "" {
		return d
	}
	return "/var/lib/mcpshka/state"
}
