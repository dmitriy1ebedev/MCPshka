package ownerweb

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Multi-field connections (owner 03.11.2026: «всё, что скоро, тоже
// реализовывать»). Each form is stored as one JSON file 0400 under
// /var/lib/mcpshka/secrets/<id>.json after the provider accepted it.

type connField struct {
	Key, Label, Default string
	Secret, Optional    bool
	pattern             *regexp.Regexp
}

// ConnFieldView is what the page sees: secrets only as "set" with last 4.
type ConnFieldView struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Default  string `json:"default,omitempty"`
	Value    string `json:"value,omitempty"`
	Last4    string `json:"last4,omitempty"`
}

type connForm struct {
	fields []connField
	check  func(ctx context.Context, c *http.Client, v map[string]string) ConnCheck
}

var (
	reURL    = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~%/+-]*)?$`)
	reName   = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,128}$`)
	reTopic  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	reSecret = regexp.MustCompile(`^[\x21-\x7e]{8,512}$`)
	reRegion = regexp.MustCompile(`^[a-z0-9-]{2,32}$`)
	reBucket = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	reRepo   = regexp.MustCompile(`^[\x21-\x7e]{3,512}$`)
)

var connForms = map[string]connForm{
	"ntfy": {fields: []connField{
		{Key: "server", Label: "Сервер", Default: "https://ntfy.sh", pattern: reURL},
		{Key: "topic", Label: "Тема (topic)", pattern: reTopic},
		{Key: "token", Label: "Токен доступа", Secret: true, Optional: true, pattern: reSecret},
	}, check: checkNtfy},
	"tailscale": {fields: []connField{
		{Key: "api_key", Label: "API key (tskey-api-…)", Secret: true, pattern: reSecret},
		{Key: "tailnet", Label: "Tailnet", Default: "-", pattern: regexp.MustCompile(`^(-|[A-Za-z0-9._@-]{1,128})$`)},
	}, check: checkTailscale},
	"cloudflare": {fields: []connField{
		{Key: "api_token", Label: "API token", Secret: true, pattern: reSecret},
	}, check: checkCloudflare},
	"webdav": {fields: []connField{
		{Key: "url", Label: "Адрес WebDAV", pattern: reURL},
		{Key: "user", Label: "Пользователь", pattern: reName},
		{Key: "password", Label: "Пароль приложения", Secret: true, pattern: reSecret},
	}, check: func(ctx context.Context, c *http.Client, v map[string]string) ConnCheck {
		return checkWebDAV(ctx, c, v["url"], v["user"], v["password"])
	}},
	"mailru": {fields: []connField{
		{Key: "user", Label: "Почта (логин)", pattern: reName},
		{Key: "password", Label: "Пароль для внешнего приложения", Secret: true, pattern: reSecret},
	}, check: func(ctx context.Context, c *http.Client, v map[string]string) ConnCheck {
		return checkWebDAV(ctx, c, "https://webdav.cloud.mail.ru/", v["user"], v["password"])
	}},
	"yandexdav": {fields: []connField{
		{Key: "user", Label: "Логин Яндекса", pattern: reName},
		{Key: "password", Label: "Пароль приложения «Файлы»", Secret: true, pattern: reSecret},
	}, check: func(ctx context.Context, c *http.Client, v map[string]string) ConnCheck {
		return checkWebDAV(ctx, c, "https://webdav.yandex.ru/", v["user"], v["password"])
	}},
	"s3": {fields: []connField{
		{Key: "endpoint", Label: "Endpoint", Default: "https://storage.yandexcloud.net", pattern: reURL},
		{Key: "region", Label: "Регион", Default: "ru-central1", pattern: reRegion},
		{Key: "bucket", Label: "Бакет", pattern: reBucket},
		{Key: "access_key", Label: "Access key ID", pattern: regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)},
		{Key: "secret_key", Label: "Secret access key", Secret: true, pattern: reSecret},
	}, check: checkS3},
	"restic": {fields: []connField{
		{Key: "repository", Label: "Репозиторий (rclone:yandex:MCPshka/restic, s3:…, sftp:…)", pattern: reRepo},
		{Key: "password", Label: "Пароль репозитория", Secret: true, pattern: regexp.MustCompile(`^[\x20-\x7e]{12,256}$`)},
	}, check: func(context.Context, *http.Client, map[string]string) ConnCheck {
		return ConnCheck{OK: true, Detail: "сохранено; репозиторий проверится при первом бэкапе"}
	}},
}

func connFormPath(id string) string { return "secrets/" + id + ".json" }

// formValues validates owner input against the form: defaults fill empty
// optional/default fields, secrets left empty keep the stored value.
func formValues(id string, in map[string]string) (map[string]string, error) {
	f, ok := connForms[id]
	if !ok {
		return nil, errors.New("неизвестная форма")
	}
	old, _ := readForm(id)
	out := map[string]string{}
	for _, fd := range f.fields {
		v := strings.TrimSpace(in[fd.Key])
		if v == "" && fd.Secret {
			v = old[fd.Key]
		}
		if v == "" {
			v = fd.Default
		}
		if v == "" {
			if fd.Optional {
				continue
			}
			return nil, fmt.Errorf("заполните «%s»", fd.Label)
		}
		if fd.pattern != nil && !fd.pattern.MatchString(v) {
			return nil, fmt.Errorf("«%s»: неверный формат", fd.Label)
		}
		out[fd.Key] = v
	}
	for k := range in {
		if !formHas(f, k) {
			return nil, fmt.Errorf("лишнее поле %q", k)
		}
	}
	return out, nil
}

func formHas(f connForm, k string) bool {
	for _, fd := range f.fields {
		if fd.Key == k {
			return true
		}
	}
	return false
}

func readForm(id string) (map[string]string, time.Time) {
	raw, mod, ok := connReadToken(connFormPath(id))
	if !ok {
		return nil, time.Time{}
	}
	var v map[string]string
	if json.Unmarshal([]byte(raw), &v) != nil {
		return nil, time.Time{}
	}
	return v, mod
}

func writeForm(id string, v map[string]string) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return connWriteToken(connFormPath(id), string(raw))
}

func formView(id string, v map[string]string) []ConnFieldView {
	var out []ConnFieldView
	for _, fd := range connForms[id].fields {
		fv := ConnFieldView{Key: fd.Key, Label: fd.Label, Secret: fd.Secret, Optional: fd.Optional, Default: fd.Default}
		if s := v[fd.Key]; s != "" {
			if fd.Secret {
				fv.Last4 = last4(s)
			} else {
				fv.Value = s
			}
		}
		out = append(out, fv)
	}
	return out
}

func connDo(ctx context.Context, c *http.Client, method, u string, h map[string]string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, nil, err
	}
	connHeaders(req, h)
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, errors.New("сервис недоступен")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, nil
}

func httpVerdict(code int, ok ...int) (bool, string) {
	for _, o := range ok {
		if code == o {
			return true, ""
		}
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return false, fmt.Sprintf("сервис отклонил данные входа (HTTP %d)", code)
	}
	return false, fmt.Sprintf("HTTP %d", code)
}

func checkNtfy(ctx context.Context, c *http.Client, v map[string]string) ConnCheck {
	h := map[string]string{}
	if t := v["token"]; t != "" {
		h["Authorization"] = "Bearer " + t
	}
	code, _, err := connDo(ctx, c, http.MethodGet, strings.TrimRight(v["server"], "/")+"/"+v["topic"]+"/json?poll=1&since=1m", h, nil)
	if err != nil {
		return ConnCheck{Detail: err.Error()}
	}
	if ok, d := httpVerdict(code, http.StatusOK); !ok {
		return ConnCheck{Detail: d}
	}
	return ConnCheck{OK: true, Account: v["topic"]}
}

func checkTailscale(ctx context.Context, c *http.Client, v map[string]string) ConnCheck {
	code, raw, err := connDo(ctx, c, http.MethodGet, "https://api.tailscale.com/api/v2/tailnet/"+url.PathEscape(v["tailnet"])+"/devices", bearer(v["api_key"]), nil)
	if err != nil {
		return ConnCheck{Detail: err.Error()}
	}
	if ok, d := httpVerdict(code, http.StatusOK); !ok {
		return ConnCheck{Detail: d}
	}
	var r struct {
		Devices []json.RawMessage `json:"devices"`
	}
	_ = json.Unmarshal(raw, &r)
	return ConnCheck{OK: true, Detail: fmt.Sprintf("устройств в tailnet: %d", len(r.Devices))}
}

func checkCloudflare(ctx context.Context, c *http.Client, v map[string]string) ConnCheck {
	code, body, _, err := connGet(ctx, c, "https://api.cloudflare.com/client/v4/user/tokens/verify", bearer(v["api_token"]))
	if err != nil {
		return ConnCheck{Detail: err.Error()}
	}
	if ok, d := httpVerdict(code, http.StatusOK); !ok {
		return ConnCheck{Detail: d}
	}
	r, _ := body["result"].(map[string]any)
	if st := connStr(r, "status"); st != "active" {
		return ConnCheck{Detail: "токен не активен: " + st}
	}
	return ConnCheck{OK: true, Expires: connStr(r, "expires_on")}
}

func checkWebDAV(ctx context.Context, c *http.Client, u, user, pass string) ConnCheck {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", u, strings.NewReader(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`))
	if err != nil {
		return ConnCheck{Detail: "неверный адрес"}
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := c.Do(req)
	if err != nil {
		return ConnCheck{Detail: "сервис недоступен"}
	}
	resp.Body.Close()
	if ok, d := httpVerdict(resp.StatusCode, http.StatusMultiStatus); !ok {
		return ConnCheck{Detail: d}
	}
	return ConnCheck{OK: true, Account: user}
}

// checkS3 lists at most one object with an AWS Signature V4 request.
func checkS3(ctx context.Context, c *http.Client, v map[string]string) ConnCheck {
	req, err := s3ListRequest(ctx, v, time.Now().UTC())
	if err != nil {
		return ConnCheck{Detail: err.Error()}
	}
	resp, err := c.Do(req)
	if err != nil {
		return ConnCheck{Detail: "сервис недоступен"}
	}
	resp.Body.Close()
	if ok, d := httpVerdict(resp.StatusCode, http.StatusOK); !ok {
		return ConnCheck{Detail: d}
	}
	return ConnCheck{OK: true, Account: v["bucket"]}
}

func s3ListRequest(ctx context.Context, v map[string]string, now time.Time) (*http.Request, error) {
	base, err := url.Parse(strings.TrimRight(v["endpoint"], "/") + "/" + v["bucket"] + "/")
	if err != nil {
		return nil, errors.New("неверный endpoint")
	}
	base.RawQuery = "list-type=2&max-keys=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	amzDate, day := now.Format("20060102T150405Z"), now.Format("20060102")
	empty := sha256.Sum256(nil)
	payload := hex.EncodeToString(empty[:])
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payload)
	headers := map[string]string{"host": base.Host, "x-amz-content-sha256": payload, "x-amz-date": amzDate}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	canon := strings.Join([]string{http.MethodGet, base.EscapedPath(), base.RawQuery, canonHeaders.String(), signed, payload}, "\n")
	scope := day + "/" + v["region"] + "/s3/aws4_request"
	ch := sha256.Sum256([]byte(canon))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(ch[:])
	mac := func(key []byte, s string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(s))
		return h.Sum(nil)
	}
	k := mac([]byte("AWS4"+v["secret_key"]), day)
	k = mac(k, v["region"])
	k = mac(k, "s3")
	k = mac(k, "aws4_request")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+v["access_key"]+"/"+scope+", SignedHeaders="+signed+", Signature="+hex.EncodeToString(mac(k, toSign)))
	return req, nil
}

// checkClaudeToken validates the shape of a long-lived Claude Code token
// (`claude setup-token`); agents start using it in a later release.
func checkClaudeToken(_ context.Context, _ *http.Client, token string) ConnCheck {
	if !regexp.MustCompile(`^sk-ant-oat01-[A-Za-z0-9_-]{40,}$`).MatchString(token) {
		return ConnCheck{Detail: "ожидается токен вида sk-ant-oat01-… из `claude setup-token`"}
	}
	return ConnCheck{OK: true, Detail: "формат верный; агенты начнут использовать его в следующем выпуске"}
}

// gitverseSecretURL is the Actions secret the MosHub image workflow pushes with.
var gitverseSecretURL = "https://api.gitverse.ru/repos/dmitriy_1ebedev/mcpshka-ci/actions/secrets/MOSHUB_REGISTRY_TOKEN"

// pushMosHubSecret copies the saved MosHub token into the GitVerse Actions
// secret on the owner's press (Face ID session), using the saved GitVerse token.
func pushMosHubSecret(ctx context.Context, c *http.Client) error {
	mos, _, ok := connReadToken("moshub/api_token")
	if !ok {
		return errors.New("сначала сохраните токен MosHub")
	}
	gv, _, ok := connReadToken("gitverse/api_token")
	if !ok {
		return errors.New("сначала сохраните токен GitVerse")
	}
	body, _ := json.Marshal(map[string]string{"data": mos})
	code, _, err := connDo(ctx, c, http.MethodPut, gitverseSecretURL, map[string]string{
		"Authorization": "Bearer " + gv, "Accept": "application/vnd.gitverse.object+json;version=1", "Content-Type": "application/json"}, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	if ok, d := httpVerdict(code, http.StatusOK, http.StatusCreated, http.StatusNoContent); !ok {
		return errors.New("GitVerse не принял секрет: " + d)
	}
	return nil
}
