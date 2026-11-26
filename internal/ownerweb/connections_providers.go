package ownerweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/haprovider"
)

func connHeaders(req *http.Request, h map[string]string) {
	req.Header.Set("User-Agent", "mcpshka-connections")
	for k, v := range h {
		req.Header.Set(k, v)
	}
}

// connGet calls a provider API with a bounded body and decodes JSON.
func connGet(ctx context.Context, c *http.Client, url string, h map[string]string) (int, map[string]any, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	connHeaders(req, h)
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, nil, errors.New("provider unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body, resp.Header, nil
}

func connStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// connSimple builds a check that passes on HTTP 200 and names the account.
func connSimple(url func(token string) string, headers func(token string) map[string]string, account string) func(context.Context, *http.Client, string) ConnCheck {
	return func(ctx context.Context, c *http.Client, token string) ConnCheck {
		code, body, _, err := connGet(ctx, c, url(token), headers(token))
		switch {
		case err != nil:
			return ConnCheck{Detail: err.Error()}
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return ConnCheck{Detail: fmt.Sprintf("провайдер отклонил токен (HTTP %d)", code)}
		case code != http.StatusOK:
			return ConnCheck{Detail: fmt.Sprintf("HTTP %d", code)}
		}
		out := ConnCheck{OK: true, Account: connStr(body, account)}
		if r, ok := body["result"].(map[string]any); ok && out.Account == "" {
			out.Account = connStr(r, account)
		}
		return out
	}
}

func bearer(t string) map[string]string { return map[string]string{"Authorization": "Bearer " + t} }

func checkGitHub(ctx context.Context, c *http.Client, token string) ConnCheck {
	code, body, h, err := connGet(ctx, c, "https://api.github.com/user", map[string]string{"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"})
	if err != nil {
		return ConnCheck{Detail: err.Error()}
	}
	if code != http.StatusOK {
		return ConnCheck{Detail: fmt.Sprintf("GitHub отклонил токен (HTTP %d)", code)}
	}
	out := ConnCheck{OK: true, Account: connStr(body, "login"), Expires: h.Get("GitHub-Authentication-Token-Expiration")}
	if s := h.Get("X-OAuth-Scopes"); s != "" {
		out.Scopes = s
	} else {
		out.Scopes = "fine-grained (права заданы в настройках токена)"
	}
	return out
}

func checkMosHub(ctx context.Context, c *http.Client, token string) ConnCheck {
	h := map[string]string{"PRIVATE-TOKEN": token}
	code, body, _, err := connGet(ctx, c, "https://hub.mos.ru/api/v4/user", h)
	if err != nil {
		return ConnCheck{Detail: err.Error()}
	}
	if code != http.StatusOK {
		return ConnCheck{Detail: fmt.Sprintf("MosHub отклонил токен (HTTP %d)", code)}
	}
	out := ConnCheck{OK: true, Account: connStr(body, "username")}
	if code, self, _, err := connGet(ctx, c, "https://hub.mos.ru/api/v4/personal_access_tokens/self", h); err == nil && code == http.StatusOK {
		if list, ok := self["scopes"].([]any); ok {
			var s []string
			for _, v := range list {
				if t, ok := v.(string); ok {
					s = append(s, t)
				}
			}
			out.Scopes = strings.Join(s, ", ")
			if !strings.Contains(out.Scopes, "api") {
				out.Detail = "нет права api: forge_ci moshub не сможет публиковать"
			}
		}
		out.Expires = connStr(self, "expires_at")
	}
	return out
}

func haBase() string {
	if u := strings.TrimRight(os.Getenv("MCPSHKA_HA_URL"), "/"); u != "" {
		return u
	}
	return haprovider.DefaultURL
}

// connCatalog is every connection the page offers, in display order. Kind
// "planned" rows only explain what is coming; they accept nothing.
var connCatalog = []connProvider{
	{ID: "github", Group: "Код и CI", Title: "GitHub", Kind: "token", Path: "github/api_token",
		Why:     "зеркало main, race-доля и fuzz в Actions, PR (forge_ci provider=github)",
		Hint:    "Fine-grained token на репозиторий mcpshka: Contents, Actions, Workflows, Pull requests — Read and write; срок до года.",
		HintURL: "https://github.com/settings/personal-access-tokens/new", check: checkGitHub},
	{ID: "gitverse", Group: "Код и CI", Title: "GitVerse", Kind: "token", Path: "gitverse/api_token",
		Why:     "полный CI-шлюз snapshot-contracts, зеркало main, сборка MosHub-образа",
		Hint:    "Профиль → Настройки → Токены доступа: репозитории и Actions (чтение и запись).",
		HintURL: "https://gitverse.ru/settings",
		check: connSimple(func(string) string { return "https://api.gitverse.ru/user" }, func(t string) map[string]string {
			return map[string]string{"Authorization": "Bearer " + t, "Accept": "application/vnd.gitverse.object+json;version=1"}
		}, "login")},
	{ID: "moshub", Group: "Код и CI", Title: "MosHub", Kind: "token", Path: "moshub/api_token",
		Why:     "race-доля CI на раннерах MosHub, реестр offline-образа",
		Hint:    "Personal access token с правами api, read_repository, write_repository, read_registry, write_registry.",
		HintURL: "https://hub.mos.ru/-/user_settings/personal_access_tokens", check: checkMosHub},
	{ID: "sourcecraft", Group: "Код и CI", Title: "SourceCraft", Kind: "token", Path: "sourcecraft/token",
		Why:     "запасная CI-площадка и зеркало main",
		Hint:    "Персональный токен с доступом к репозиторию lda6994/mcpshka-ci и CI.",
		HintURL: "https://sourcecraft.dev",
		check:   connSimple(func(string) string { return "https://api.sourcecraft.tech/repos/lda6994/mcpshka-ci" }, bearer, "name")},
	{ID: "gitflic", Group: "Код и CI", Title: "GitFlic", Kind: "token", Path: "gitflic/token",
		Why:     "запасная CI-площадка (contracts, fuzz, vet)",
		Hint:    "Настройки → API-токены: доступ к проекту и CI.",
		HintURL: "https://gitflic.ru/settings/oauth/token",
		check: connSimple(func(string) string { return "https://api.gitflic.ru/user/me" }, func(t string) map[string]string {
			return map[string]string{"Authorization": "token " + t}
		}, "username")},
	{ID: "drive", Group: "Облака", Title: "Google Диск", Kind: "cloud", Why: "бэкапы и обмен файлами (storage_*)"},
	{ID: "yandex", Group: "Облака", Title: "Яндекс Диск", Kind: "cloud", Why: "бэкапы и обмен файлами (storage_*)"},
	{ID: "yandexdav", Group: "Облака", Title: "Яндекс Диск по паролю приложения", Kind: "form", Why: "без регистрации OAuth-приложения: логин и пароль приложения (WebDAV)",
		Hint: "Яндекс ID → Безопасность → Пароли приложений → «Файлы (WebDAV)» → создать; логин — ваш адрес без @yandex.ru.", HintURL: "https://id.yandex.ru/security/app-passwords"},
	{ID: "dropbox", Group: "Облака", Title: "Dropbox", Kind: "cloud", Why: "бэкапы и обмен файлами (storage_*)"},
	{ID: "onedrive", Group: "Облака", Title: "OneDrive", Kind: "cloud", Why: "ещё одно хранилище бэкапов"},
	{ID: "mailru", Group: "Облака", Title: "Облако Mail.ru", Kind: "form", Why: "российское хранилище бэкапов (WebDAV)",
		Hint: "Почта → Настройки → Безопасность → Пароли для внешних приложений → создать с доступом к Облаку (WebDAV).", HintURL: "https://account.mail.ru/user/2-step-auth/passwords/"},
	{ID: "webdav", Group: "Облака", Title: "WebDAV / Nextcloud", Kind: "form", Why: "своё хранилище",
		Hint: "Nextcloud: Файлы → Настройки → адрес WebDAV; пароль — Настройки → Безопасность → пароль приложения."},
	{ID: "s3", Group: "Облака", Title: "S3 (Yandex Object Storage, Backblaze B2)", Kind: "form", Why: "дешёвое хранилище бэкапов с версиями",
		Hint: "Yandex Cloud: сервисный аккаунт с ролью storage.editor → статический ключ доступа.", HintURL: "https://console.yandex.cloud/"},
	{ID: "codex", Group: "ИИ-агенты", Title: "Codex (ChatGPT)", Kind: "agent",
		Why:  "агент agent_task/roadmap_run codex",
		Hint: "Вход по одноразовому коду: откройте ссылку на любом устройстве и введите код — localhost не нужен."},
	{ID: "claude", Group: "ИИ-агенты", Title: "Claude Code (токен агентов)", Kind: "token", Path: "secrets/claude.oauth_token",
		Why:  "отдельный долгоживущий токен агентов, чтобы не трогать вход интерактивной сессии",
		Hint: "На любом компьютере с Claude Code: `claude setup-token` → вставьте токен sk-ant-oat01-…", check: checkClaudeToken},
	{ID: "homeassistant", Group: "Дом", Title: "Home Assistant", Kind: "token", Path: "secrets/ha.token",
		Why:  "ha_* инструменты: состояния, автоматизации, сервисы",
		Hint: "Home Assistant → Профиль → Безопасность → Долгосрочные токены доступа → Создать.",
		check: func(ctx context.Context, c *http.Client, token string) ConnCheck {
			return connSimple(func(string) string { return haBase() + "/api/" }, bearer, "message")(ctx, c, token)
		}},
	{ID: "telegram", Group: "Уведомления", Title: "Telegram-бот", Kind: "token", Path: "secrets/telegram.bot_token",
		Why:     "уведомления и вопросы владельцу (подключение готовится; токен сохраняется уже сейчас)",
		Hint:    "@BotFather → /newbot → токен вида 123456:ABC…",
		HintURL: "https://t.me/BotFather",
		check:   connSimple(func(t string) string { return "https://api.telegram.org/bot" + t + "/getMe" }, func(string) map[string]string { return nil }, "username")},
	{ID: "ntfy", Group: "Уведомления", Title: "ntfy", Kind: "form", Why: "push на телефон без Telegram",
		Hint: "Приложение ntfy → подписаться на свою тему; токен нужен только для закрытого сервера.", HintURL: "https://ntfy.sh"},
	{ID: "lane", Group: "Сеть", Title: "MCP-порт через Lane", Kind: "lane", Why: "публичный адрес для ChatGPT и Claude без ручных туннелей"},
	{ID: "tailscale", Group: "Сеть", Title: "Tailscale", Kind: "form", Why: "ключи узлов и ACL tailnet (удалённый раннер на ноутбуке)",
		Hint: "Admin console → Settings → Keys → Generate API key.", HintURL: "https://login.tailscale.com/admin/settings/keys"},
	{ID: "cloudflare", Group: "Сеть", Title: "Cloudflare", Kind: "form", Why: "туннель и DNS портала",
		Hint: "My Profile → API Tokens → Create Token (Zone DNS Edit, Cloudflare Tunnel Edit).", HintURL: "https://dash.cloudflare.com/profile/api-tokens"},
	{ID: "restic", Group: "Бэкапы", Title: "restic", Kind: "form", Why: "пароль и репозиторий шифрованных бэкапов",
		Hint: "Придумайте длинный пароль и сохраните его ещё и вне H96 — без него бэкап не открыть."},
}

func connFind(id string) (connProvider, bool) {
	for _, p := range connCatalog {
		if p.ID == id {
			return p, true
		}
	}
	return connProvider{}, false
}

// validConnToken rejects what cannot be a token before any network call.
func validConnToken(t string) error {
	if len(t) < 8 || len(t) > 4096 {
		return errors.New("токен должен быть от 8 до 4096 символов")
	}
	for _, r := range t {
		if r <= ' ' || r == 0x7f {
			return errors.New("в токене есть пробелы или управляющие символы")
		}
	}
	return nil
}

// connWriteToken replaces the token file atomically, readable by the service
// only. The previous file is left untouched until the rename.
func connWriteToken(rel, token string) error {
	path := connPath(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".conn-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.WriteString(token); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o400)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func connReadToken(rel string) (string, time.Time, bool) {
	path := connPath(rel)
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 8192 {
		return "", time.Time{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}, false
	}
	t := strings.TrimSpace(string(raw))
	return t, st.ModTime(), t != ""
}

func last4(t string) string {
	if len(t) < 12 {
		return "…"
	}
	return "…" + t[len(t)-4:]
}
