package ownerweb

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
)

// The connect link is a small wizard, so the owner never carries secrets
// through the chat or a terminal:
//   - the provider already has the owner's OAuth app → straight to consent;
//   - no app yet → a page with the exact redirect address, a form for the
//     app's id and secret, and (Dropbox) a quick way through rclone's own
//     app: consent ends on a localhost address the phone cannot open, the
//     owner pastes that address back and rclone on H96 finishes the exchange
//     (the same cloudWeb flow as the «Подключения» page).
//
// Google Drive has no quick way: rclone's shared Google client is being
// retired in 2026 and is rate-limited (MCP-165). Yandex neither: since
// 11.10.2026 its REST API answers 410 GoneError to rclone's app on every
// listing (rclone issue #10067; seen here 13.11 right after consent).

// storageQuick lists providers that rclone can still authorize with its own
// app; the «Подключения» page offers its paste flow only for these.
var storageQuick = map[string]bool{"dropbox": true, "onedrive": true}

// storageNoQuickWhy says on the wizard why rclone's key does not do here.
var storageNoQuickWhy = map[string]string{
	"drive":  "для Google отключают в 2026 году, и он уже упирается в лимиты",
	"yandex": "Яндекс с 11.10.2026 не пускает к файлам (ошибка 410)",
}

const storageCSS = `body{font:17px -apple-system,system-ui,sans-serif;margin:5vh 6vw;color:#222;max-width:680px}h1{font-size:23px}h2{font-size:18px;margin-top:28px}input{font:16px ui-monospace,monospace;width:100%;box-sizing:border-box;padding:10px;margin:6px 0;border:1px solid #bbb;border-radius:8px}button,a.btn{display:inline-block;font:17px -apple-system,system-ui,sans-serif;background:#1463d8;color:#fff;border:0;border-radius:10px;padding:12px 18px;margin:8px 0;text-decoration:none}li{margin:7px 0}.note{color:#666;font-size:15px}.err{color:#b00020}@media(prefers-color-scheme:dark){body{background:#111;color:#eee}input{background:#1c1c1c;color:#eee;border-color:#555}.note{color:#aaa}}`

func storageHTML(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title><style>%s</style></head><body><h1>%s</h1>%s</body></html>`,
		html.EscapeString(title), storageCSS, html.EscapeString(title), body)
}

var storageNames = map[string]string{"drive": "Google Drive", "yandex": "Яндекс.Диск", "dropbox": "Dropbox"}

// storageAppSteps are the provider-side steps, each a link plus what to do.
func storageAppSteps(provider, redirect string) string {
	r := `<input readonly value="` + html.EscapeString(redirect) + `" onclick="this.select()">`
	switch provider {
	case "drive":
		return `<ol><li><a href="https://console.cloud.google.com/projectcreate" target="_blank">Создать проект</a> в Google Cloud (любое имя).</li>` +
			`<li><a href="https://console.cloud.google.com/apis/library/drive.googleapis.com" target="_blank">Включить Google Drive API</a>.</li>` +
			`<li><a href="https://console.cloud.google.com/auth/overview" target="_blank">Google Auth Platform</a>: тип External, затем в «Audience» нажать «Publish app» — иначе доступ пропадёт через 7 дней.</li>` +
			`<li><a href="https://console.cloud.google.com/auth/clients" target="_blank">Clients → Create client</a>: Web application, в «Authorized redirect URIs» вставить:` + r + `</li>` +
			`<li>Скопировать Client ID и Client secret в форму ниже.</li></ol>` +
			`<p class="note">При входе Google покажет «приложение не проверено» — это ваше собственное приложение: «Дополнительные настройки» → «Перейти». MCPshka получит весь Диск, как вы просили.</p>`
	case "yandex":
		return `<ol><li><a href="https://oauth.yandex.ru/client/new" target="_blank">Создать приложение</a>: платформа «Веб-сервисы», Redirect URI:` + r + `</li>` +
			`<li>Доступы: «Яндекс.Диск REST API» — чтение, запись, информация о Диске.</li>` +
			`<li>Скопировать ClientID и Client secret в форму ниже.</li></ol>`
	case "dropbox":
		return `<ol><li><a href="https://www.dropbox.com/developers/apps/create" target="_blank">Создать приложение</a>: Scoped access → Full Dropbox.</li>` +
			`<li>Settings → Redirect URIs:` + r + `</li>` +
			`<li>Permissions: files.metadata.read/write, files.content.read/write → Submit.</li>` +
			`<li>Скопировать App key и App secret в форму ниже.</li></ol>`
	}
	return ""
}

// setupPage offers the ways to connect a provider that has no OAuth app yet.
func (s *storageConnectState) setupPage(w http.ResponseWriter, state string, p storageConnectPending, problem string) {
	redirect, _ := storageRedirectURI()
	base := "/storage/connect/" + url.PathEscape(state)
	var b strings.Builder
	if problem != "" {
		b.WriteString(`<p class="err">` + html.EscapeString(problem) + `</p>`)
	}
	if storageQuick[p.Provider] {
		b.WriteString(`<h2>Быстро — без настройки</h2><p>Вход через приложение rclone. В конце браузер покажет «не удаётся открыть страницу» — это ожидаемо: нужно будет скопировать адрес из адресной строки.</p><a class="btn" href="` + base + `/quick">Подключить быстро</a>`)
		b.WriteString(`<h2>Надёжно — своё приложение (один раз)</h2>`)
	} else {
		b.WriteString(`<p>Нужно один раз завести своё приложение в облаке: общий ключ rclone ` + storageNoQuickWhy[p.Provider] + `. Дальше подключение — в одно нажатие.</p>`)
	}
	b.WriteString(storageAppSteps(p.Provider, redirect))
	b.WriteString(`<form method="post" action="` + base + `/app"><input name="client_id" placeholder="Client ID" autocomplete="off" autocapitalize="off" required><input name="client_secret" placeholder="Client secret" autocomplete="off" autocapitalize="off" required><button>Сохранить и подключить</button></form><p class="note">Ключи сохраняются только на H96 и в чат не попадают.</p>`)
	storageHTML(w, http.StatusOK, "Подключение: "+storageNames[p.Provider], b.String())
}

// saveApp stores the app typed into the wizard and goes on to consent.
func (s *storageConnectState) saveApp(w http.ResponseWriter, r *http.Request, state string, p storageConnectPending) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	app := StorageOAuthApp{ClientID: strings.TrimSpace(r.PostFormValue("client_id")), ClientSecret: strings.TrimSpace(r.PostFormValue("client_secret"))}
	if err := SaveStorageApp(p.Provider, app); err != nil {
		s.setupPage(w, state, p, "Ключи не приняты: проверьте, что скопированы целиком, без пробелов.")
		return
	}
	http.Redirect(w, r, "/storage/connect/"+url.PathEscape(state), http.StatusSeeOther)
}

// quickStart launches rclone authorize and shows the two-step page.
func (s *storageConnectState) quickStart(w http.ResponseWriter, state string, p storageConnectPending) {
	if !storageQuick[p.Provider] {
		s.setupPage(w, state, p, "Для этого облака быстрого способа нет.")
		return
	}
	consent, err := cloudWeb.begin(p.Provider)
	if err != nil {
		s.setupPage(w, state, p, "Быстрый способ сейчас недоступен: "+err.Error())
		return
	}
	s.quickPage(w, state, p, consent, "")
}

func (s *storageConnectState) quickPage(w http.ResponseWriter, state string, p storageConnectPending, consent, problem string) {
	base := "/storage/connect/" + url.PathEscape(state)
	body := ""
	if problem != "" {
		body = `<p class="err">` + html.EscapeString(problem) + `</p>`
	}
	step1 := `<a class="btn" href="` + base + `/quick">Начать заново</a>`
	if consent != "" {
		step1 = `<a class="btn" href="` + html.EscapeString(consent) + `" target="_blank" rel="noopener">Войти в ` + html.EscapeString(storageNames[p.Provider]) + `</a><br>Разрешите доступ.`
	}
	body += `<ol><li>` + step1 + `</li>` +
		`<li>Откроется страница с ошибкой «не удаётся открыть» — так и должно быть. Нажмите на адресную строку и скопируйте адрес целиком (в нём есть <b>53682</b>).</li>` +
		`<li>Вернитесь сюда и вставьте адрес:<form method="post" action="` + base + `/paste"><input name="url" placeholder="127.0.0.1:53682/?state=…&code=…" autocomplete="off" autocapitalize="off" required><button>Готово</button></form></li></ol>`
	storageHTML(w, http.StatusOK, "Подключение: "+storageNames[p.Provider], body)
}

// quickPaste finishes the rclone flow and stores the token in the vault.
func (s *storageConnectState) quickPaste(w http.ResponseWriter, r *http.Request, state string, p storageConnectPending) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	pasted := r.PostFormValue("url")
	if _, err := landedQuery(pasted); err != nil {
		s.quickPage(w, state, p, "", "Это не тот адрес: нужен адрес страницы с ошибкой, в нём есть 53682, state= и code=. Если он потерялся — начните заново.")
		return
	}
	token, err := cloudWeb.finish(p.Provider, pasted)
	if err == nil {
		s.take(state, true)
		err = s.accept(r.Context(), p.Provider, p.Owner, token)
	}
	if err != nil {
		storagePage(w, http.StatusBadGateway, "Не получилось", err.Error()+". Попросите в чате новую ссылку: «подключи облако».")
		return
	}
	storagePage(w, http.StatusOK, "Готово", "Облако подключено к MCPshka. Окно можно закрыть и вернуться в чат.")
}
