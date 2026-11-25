package ownerweb

import (
	"context"
	"errors"
	"html"
	"net/http"
	"strings"

	"mcpshka/internal/mcphub"
)

// Plugins is the MCP hub (set by the server): «Плагины» connects other MCP
// servers to MCPshka so a chat needs only MCPshka (MCP-187). Behind the same
// Face ID session as «Подключения»; tokens are write-only here as well.
var Plugins *mcphub.Hub

// pluginCallback is where an OAuth provider returns the owner's browser.
const pluginCallback = "/connections/plugins/callback"

func (p *connPortal) pluginsPage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, pluginsHTML)
}

// pluginCallbackRoute ends an OAuth sign-in. The session cookie is
// SameSite=Strict and a provider redirect is cross-site, so the single-use
// state of a flow the owner started behind Face ID is the authority here.
func pluginCallbackRoute(w http.ResponseWriter, r *http.Request) {
	if Plugins == nil || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	msg := "Готово: плагин подключён."
	id, err := Plugins.FinishOAuth(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("code"))
	if e := r.URL.Query().Get("error"); e != "" {
		msg = "Вход отменён: " + e
	} else if err != nil {
		msg = "Не получилось: " + err.Error()
	} else if s, err := Plugins.Server(id); err == nil {
		msg = "Готово: «" + s.Title + "» подключён к MCPshka."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_, _ = w.Write([]byte(`<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><body style="font:17px system-ui;padding:24px"><p>` +
		html.EscapeString(msg) + `</p><p><a href="/connections/plugins">Вернуться к плагинам</a></p></body>`))
}

type pluginRow struct {
	mcphub.Server
	Secret mcphub.SecretInfo `json:"secret"`
	Stat   *mcphub.Stat      `json:"stat,omitempty"`
	Tools  int               `json:"tools"`
	Error  string            `json:"error,omitempty"`
}

// pluginsAPI is the owner side: list, probe, add, sign in, token, toggle,
// remove, refresh, store search and the call log.
func (p *connPortal) pluginsAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, action string) {
	if Plugins == nil {
		jsonErr(w, "хаб плагинов не включён", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		ID, URL, Title, Source, Token, Q string
		Enabled                          bool
	}
	if r.Method == http.MethodPost {
		if err := ReadBody(r, &in); err != nil {
			jsonErr(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	fail := func(err error) { jsonErr(w, err.Error(), http.StatusBadRequest) }
	switch action {
	case "plugins":
		servers, err := Plugins.Servers()
		if err != nil {
			fail(err)
			return
		}
		stats := Plugins.Stats()
		rows := make([]pluginRow, 0, len(servers))
		for _, s := range servers {
			row := pluginRow{Server: s, Secret: Plugins.SecretInfo(s.ID), Stat: stats[s.ID]}
			if tools, err := Plugins.Tools(ctx, s.ID, false); err == nil {
				row.Tools = len(tools)
			} else {
				row.Error = err.Error()
			}
			rows = append(rows, row)
		}
		seen, _ := Plugins.Matches()
		JSON(w, map[string]any{"servers": rows, "seen": seen, "calls": Plugins.Calls(r.URL.Query().Get("server"), 50)}, http.StatusOK)
	case "plugin-search":
		found, err := Plugins.Search(ctx, in.Q)
		out := map[string]any{"entries": found}
		if err != nil {
			out["warning"] = "каталог MCP Registry недоступен: показаны только проверенные"
		}
		JSON(w, out, http.StatusOK)
	case "plugin-probe":
		JSON(w, Plugins.Probe(ctx, in.URL), http.StatusOK)
	case "plugin-add":
		pr := Plugins.Probe(ctx, in.URL)
		if pr.Error != "" {
			fail(errors.New(pr.Error))
			return
		}
		source := in.Source
		if source == "" {
			source = "manual"
		}
		s, err := Plugins.Put(mcphub.Server{Title: in.Title, URL: in.URL, Auth: pr.Auth, Source: source})
		if err != nil {
			fail(err)
			return
		}
		p.pluginNext(ctx, w, r, s)
	case "plugin-login":
		s, err := Plugins.Server(in.ID)
		if err != nil {
			fail(err)
			return
		}
		p.pluginNext(ctx, w, r, s)
	case "plugin-token":
		if err := validConnToken(in.Token); err != nil {
			fail(err)
			return
		}
		s, err := Plugins.Server(in.ID)
		if err != nil {
			fail(err)
			return
		}
		if s.Auth != mcphub.AuthToken {
			s.Auth = mcphub.AuthToken
			if s, err = Plugins.Put(s); err != nil {
				fail(err)
				return
			}
		}
		if err := Plugins.PutSecret(s.ID, mcphub.Secret{Token: in.Token}); err != nil {
			fail(err)
			return
		}
		tools, err := Plugins.Tools(ctx, s.ID, true)
		if err != nil {
			fail(errors.New("токен сохранён, но сервер его не принял: " + err.Error()))
			return
		}
		JSON(w, map[string]any{"ok": true, "tools": len(tools)}, http.StatusOK)
	case "plugin-toggle":
		if err := Plugins.SetDisabled(in.ID, !in.Enabled); err != nil {
			fail(err)
			return
		}
		JSON(w, map[string]bool{"ok": true}, http.StatusOK)
	case "plugin-remove":
		if err := Plugins.Remove(in.ID); err != nil {
			fail(err)
			return
		}
		JSON(w, map[string]bool{"ok": true}, http.StatusOK)
	case "plugin-refresh":
		tools, err := Plugins.Tools(ctx, in.ID, true)
		if err != nil {
			fail(err)
			return
		}
		JSON(w, map[string]any{"ok": true, "tools": len(tools)}, http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

// pluginNext answers what the owner does next for s: nothing (open server,
// tools listed), open the sign-in URL, or paste a token.
func (p *connPortal) pluginNext(ctx context.Context, w http.ResponseWriter, r *http.Request, s mcphub.Server) {
	switch s.Auth {
	case mcphub.AuthNone:
		tools, err := Plugins.Tools(ctx, s.ID, true)
		if err != nil {
			jsonErr(w, err.Error(), http.StatusBadGateway)
			return
		}
		JSON(w, map[string]any{"id": s.ID, "next": "done", "tools": len(tools)}, http.StatusOK)
	case mcphub.AuthOAuth:
		origin, err := OriginConfigured()
		if err != nil {
			jsonErr(w, "не настроен публичный адрес MCPshka для возврата после входа", http.StatusServiceUnavailable)
			return
		}
		login, err := Plugins.BeginOAuth(ctx, s.ID, strings.TrimSuffix(origin, "/")+pluginCallback)
		if err != nil {
			JSON(w, map[string]any{"id": s.ID, "next": "token", "note": err.Error()}, http.StatusOK)
			return
		}
		JSON(w, map[string]any{"id": s.ID, "next": "login", "login_url": login}, http.StatusOK)
	default:
		JSON(w, map[string]any{"id": s.ID, "next": "token"}, http.StatusOK)
	}
}

var pluginsHTML = ownerPage("Плагины", `<p class="sub">Другие MCP-серверы, к которым MCPshka ходит сама: в чате достаточно одной MCPshka, без @. Инструменты подключённых серверов находит <code>tool_resolve</code>, вызывает <code>tool_invoke</code>; всё, что может менять данные, — только в окне Face ID. Токены только записываются; в журнал попадают имена полей и размеры, не значения.</p>
<div id="status"></div>
<nav><a href="#on">Подключено</a><a href="#seen">Найдено в чатах</a><a href="#store">Магазин</a><a href="#log">Журнал</a></nav>
<h2 id="on">Подключено</h2><div id="list"></div>
<h2 id="seen">Найдено в чатах</h2><p class="sub">Списки плагинов, которые прислали ChatGPT и Claude (<code>plugin_sync</code>) — только названия, без токенов.</p><div id="found"></div>
<h2 id="store">Магазин</h2><div class="card"><div class="row"><input id="q" placeholder="например, docs, supabase, search" style="flex:1"><button id="go">Найти</button></div><div class="meta">Сначала — проверенные официальные адреса и открытые серверы без входа; дальше — открытый каталог MCP Registry (там встречаются копии под похожими именами — смотрите автора и репозиторий). Перед подключением MCPshka проверяет адрес вживую.</div><div class="row" style="margin-top:8px"><input id="url" placeholder="или свой адрес https://…/mcp" style="flex:1"><button id="add" class="sec">Подключить</button></div></div><div id="entries"></div>
<h2 id="log">Журнал</h2><div id="calls"></div>`, `
const ago=t=>{if(!t||t.startsWith('0001'))return '—';const s=Math.round((Date.now()-new Date(t))/1000);return s<90?s+' с назад':s<5400?Math.round(s/60)+' мин назад':s<172800?Math.round(s/3600)+' ч назад':Math.round(s/86400)+' дн назад';};
const kb=b=>b<1024?b+' Б':(b/1024).toFixed(1)+' КБ';
const verdicts={builtin:['уже умеет MCPshka','ok'],connected:['подключён','ok'],connectable:['можно подключить','warn'],own_login:['нужен свой вход','warn'],client_only:['только в чате','bad'],unknown:['не найден — поищите в магазине','warn']};
function err(e){if(!needLogin(e))$('#status').textContent='Ошибка: '+e.message;}
async function next(d){if(d.next==='login'){$('#status').textContent='Откроется вход у сервиса; после него вернитесь сюда.';location.href=d.login_url;return;}
if(d.next==='token'){const t=prompt((d.note?d.note+'\n':'')+'Вставьте токен доступа этого сервиса (хранится только на H96, страница его не показывает):');if(t){try{const r=await call('/connections/api/plugin-token',{id:d.id,token:t.trim()});$('#status').textContent='Подключено: инструментов '+r.tools;}catch(e){err(e);}}}
else $('#status').textContent='Подключено: инструментов '+d.tools;load();}
async function connect(url,title,source){$('#status').textContent='Проверяю '+url+' …';try{next(await call('/connections/api/plugin-add',{url,title,source}));}catch(e){err(e);}}
function btn(text,cls,fn){const b=el('button',cls,text);b.onclick=fn;return b;}
async function load(){try{const d=await call('/connections/api/plugins');
$('#list').replaceChildren(...(d.servers.length?d.servers.map(s=>{const c=el('div','card');const r=el('div','row');const bad=s.error;const st=s.disabled?['выключен','warn']:bad?(bad.includes('LOGIN_NEEDED')?['нужен вход','warn']:['не отвечает','bad']):['работает','ok'];
r.append(el('span','t',s.title),el('span','st '+st[1],st[0]));c.append(r);
const x=s.stat||{};c.append(el('div','meta',s.url+' · вход: '+({none:'не нужен',token:'токен',oauth:'OAuth'}[s.auth])+(s.secret.present?' '+(s.secret.last4||'')+(s.secret.expiry&&!s.secret.expiry.startsWith('0001')?' до '+new Date(s.secret.expiry).toLocaleString():''):'')+' · инструментов '+s.tools));
c.append(el('div','meta','вызовов '+(x.calls||0)+' (за сутки '+(x.day||0)+') · ошибок '+(x.errors||0)+' · заблокировано секретов '+(x.blocked||0)+' · отправлено '+kb(x.bytes_out||0)+', получено '+kb(x.bytes_in||0)+' · последний '+ago(x.last)));
if(bad)c.append(el('div','msg bad',bad));
if(x.tools&&x.tools.length){const p=el('pre','',x.tools.map(t=>t.tool+' — '+t.calls+(t.errors?' (ошибок '+t.errors+')':'')+', '+ago(t.last)).join('\n'));p.hidden=true;c.append(btn('По инструментам','sec',()=>p.hidden=!p.hidden),p);}
const a=el('div','acts');if(s.auth!=='none')a.append(btn('Войти заново','sec',async()=>{try{next(await call('/connections/api/plugin-login',{id:s.id}));}catch(e){err(e);}}));
a.append(btn('Проверить','sec',async()=>{try{const r=await call('/connections/api/plugin-refresh',{id:s.id});$('#status').textContent=s.title+': инструментов '+r.tools;load();}catch(e){err(e);}}),
btn(s.disabled?'Включить':'Выключить','sec',async()=>{await call('/connections/api/plugin-toggle',{id:s.id,enabled:s.disabled});load();}),
btn('Удалить','sec',async()=>{if(confirm('Удалить «'+s.title+'» и его вход?')){await call('/connections/api/plugin-remove',{id:s.id});load();}}));c.append(a);return c;}):[el('div','meta','Пока ничего не подключено — начните с магазина.')]));
$('#found').replaceChildren(...(d.seen.length?d.seen.map(m=>{const c=el('div','card');const r=el('div','row');const v=verdicts[m.verdict]||[m.verdict,'warn'];r.append(el('span','t',m.name),el('span','st '+v[1],v[0]));c.append(r,el('div','meta',m.client+(m.note?' · '+m.note:'')+(m.entry?' · '+m.entry.url:'')));
if(m.verdict==='connectable'&&m.entry){const a=el('div','acts');a.append(btn('Подключить','',()=>connect(m.entry.url,m.entry.title,m.entry.name)));c.append(a);}return c;}):[el('div','meta','Чаты ещё не присылали список. В ChatGPT или Claude скажите: «синхронизируй плагины с MCPshka».')]));
$('#calls').replaceChildren(...(d.calls.length?d.calls.map(k=>{const c=el('div','card');const r=el('div','row');r.append(el('span','t',k.server+'.'+k.tool),el('span','st '+(k.status==='OK'?'ok':k.status==='SECRET_BLOCKED'?'bad':'warn'),k.status));c.append(r,el('div','meta',ago(k.at)+(k.caller?' · '+k.caller:'')+' · '+k.ms+' мс · ушло '+kb(k.bytes_out)+' · пришло '+kb(k.bytes_in)+' · поля '+k.args));if(k.error)c.append(el('div','msg',k.error));return c;}):[el('div','meta','Вызовов ещё не было.')]));
$('#status').textContent='';}catch(e){err(e);}}
async function search(){try{const d=await call('/connections/api/plugin-search',{q:$('#q').value});$('#entries').replaceChildren(...(d.warning?[el('div','meta warn',d.warning)]:[]),...d.entries.map(e=>{const c=el('div','card');const r=el('div','row');r.append(el('span','t',e.title),el('span','st '+(e.verified?'ok':'warn'),e.verified?'проверенный адрес':'каталог'));c.append(r,el('div','meta',(e.description||'')+' · '+e.url+' · вход: '+({none:'не нужен',oauth:'OAuth',token:'токен'}[e.auth]||'узнаем при проверке')+(e.repo?' · '+e.repo:'')+' · '+e.name));const a=el('div','acts');a.append(btn('Подключить','',()=>connect(e.url,e.title,e.name)));c.append(a);return c;}));}catch(e){err(e);}}
$('#go').onclick=search;$('#q').onkeydown=e=>{if(e.key==='Enter')search();};$('#add').onclick=()=>{const u=$('#url').value.trim();if(u)connect(u,'','manual');};
load();search();setInterval(()=>{if(!document.hidden)load();},30000);
`)
