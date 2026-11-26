package ownerweb

import (
	"net"
	"net/http"
	"strings"
)

// «IDE»: how to reach MCPshka from an editor. The owner works from iPhone and
// iPad (08.11), so the browser routes come first: the native «Код» page,
// GigaIDE Cloud on GitVerse (repositories are mirrored there), full VS Code
// (code-server) inside the home network. Desktop editors connect to /mcp by
// OAuth with dynamic registration; their loopback callbacks are allowed by the
// «IDE» brain on «Подключения», and every sign-in still waits for Face ID.

func (p *connPortal) idePage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, strings.ReplaceAll(ideHTML, "{{LAN}}", lanAddress()))
}

// lanAddress is the host's current private IPv4, read live (never hard-coded).
func lanAddress() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() && !strings.HasPrefix(n.IP.String(), "172.") {
			return n.IP.String()
		}
	}
	return "h96.local"
}

var ideHTML = ownerPage("IDE", `<p class="sub">Как работать с кодом H96 из редактора. Сначала — варианты для iPad и телефона.</p>
<h2>iPad и телефон</h2>
<div class="card"><div class="t">«Код» в MCPshka</div><div class="meta">Просмотр, правка, diff, коммит и тесты прямо здесь, вход тот же Face ID. На iPad: Поделиться → «На экран Домой».</div><div class="acts"><a href="/connections/code"><button>Открыть «Код»</button></a></div></div>
<div class="card"><div class="t">GigaIDE Cloud (GitVerse)</div><div class="meta">VS Code в браузере от Сбера. Откройте репозиторий в GitVerse → «Открыть в GigaIDE Cloud». Если он умеет подключать MCP-серверы — укажите адрес ниже; при первом входе MCPshka запомнит его адрес возврата, и на «Подключениях» появится кнопка «разрешить».</div><div class="acts"><a href="https://gitverse.ru/dmitriy_1ebedev" target="_blank" rel="noopener"><button class="sec">Репозитории в GitVerse</button></a></div></div>
<div class="card"><div class="t">VS Code на H96 (только дома)</div><div class="meta">Полный VS Code (code-server) работает на приставке, но с правами root — поэтому наружу не выставлен. Открывается из домашнего Wi-Fi; браузер предупредит о самоподписанном сертификате.</div><div class="acts"><a id="lan" href="https://{{LAN}}:8443" target="_blank" rel="noopener"><button class="sec">Открыть дома</button></a></div></div>
<div class="card"><div class="t">Облачная IDE как раннер</div><div class="meta">GigaIDE Cloud или Codespaces могут брать у MCPshka тяжёлые тесты и сборки, чтобы не грузить H96. Подключение — одной командой в терминале IDE.</div><div class="acts"><a href="/connections/runners"><button class="sec">Раннеры</button></a></div></div>
<h2>Адрес MCP</h2>
<div class="card"><div class="row"><code id="url"></code><button id="copy" class="sec">Скопировать</button></div><div class="meta">Streamable HTTP, вход по OAuth (регистрация клиента автоматическая), подтверждение — Face ID.</div></div>
<h2>Компьютер</h2>
<div class="card"><div class="t">VS Code — .vscode/mcp.json</div><pre id="vscode"></pre></div>
<div class="card"><div class="t">Cursor — ~/.cursor/mcp.json</div><pre id="cursor"></pre></div>
<div class="card"><div class="t">Claude Code</div><pre id="claude"></pre></div>
<div class="card"><div class="t">Codex (CLI и расширение VS Code) — ~/.codex/config.toml</div><pre id="codex"></pre><div class="meta">Потом в терминале: <code>codex mcp login mcpshka</code> — откроется вход, подтвердите Face ID.</div></div>
<div class="card"><div class="t">GigaIDE / JetBrains</div><div class="meta">Настройки → AI Assistant → Model Context Protocol → добавить сервер HTTP с адресом выше.</div></div>`, `
const u=location.origin+'/mcp';$('#url').textContent=u;
$('#vscode').textContent=JSON.stringify({servers:{mcpshka:{type:'http',url:u}}},null,2);
$('#cursor').textContent=JSON.stringify({mcpServers:{mcpshka:{url:u}}},null,2);
$('#claude').textContent='claude mcp add --transport http mcpshka '+u;
$('#codex').textContent='[mcp_servers.mcpshka]\nurl = "'+u+'"';
$('#copy').onclick=async()=>{try{await navigator.clipboard.writeText(u);$('#copy').textContent='Скопировано';}catch(e){$('#copy').textContent='Выделите и скопируйте';}};
`)
