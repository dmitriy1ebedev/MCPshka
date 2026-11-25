package ownerweb

import (
	"io"
	"net/http"
	"strings"
)

// Shared frame of the owner pages («Контейнеры», «Ресурсы», «Код», «IDE»):
// one stylesheet, one fetch helper, the same nav. Every page is served with a
// per-response script nonce and no third-party resources.

const pageCSS = `:root{color-scheme:light dark;--bg:#f6f7f9;--card:#fff;--ink:#16181d;--mute:#5d6470;--line:#dde1e7;--ok:#137a3a;--bad:#b42318;--warn:#9a6700;--acc:#2457d6}
@media (prefers-color-scheme:dark){:root{--bg:#111318;--card:#1a1d24;--ink:#e8eaee;--mute:#9aa1ad;--line:#2c313b;--ok:#4cc27a;--bad:#ff7b6e;--warn:#e0b341;--acc:#7aa2ff}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.45 -apple-system,system-ui,Segoe UI,Roboto,sans-serif}
main{max-width:960px;margin:0 auto;padding:16px}h1{font-size:22px;margin:4px 0 2px}h2{font-size:14px;margin:18px 0 8px;color:var(--mute);text-transform:uppercase;letter-spacing:.04em}.sub{color:var(--mute);margin:0 0 12px}a{color:var(--acc)}
nav{display:flex;gap:12px;flex-wrap:wrap;margin:0 0 10px;font-size:14px}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:12px 14px;margin:0 0 10px}
.row{display:flex;gap:8px;align-items:baseline;justify-content:space-between;flex-wrap:wrap}.t{font-weight:600;overflow-wrap:anywhere}.meta{color:var(--mute);font-size:13px;margin-top:4px;overflow-wrap:anywhere}
.st{font-size:12px;font-weight:600;padding:2px 8px;border-radius:999px;border:1px solid currentColor;white-space:nowrap}.ok{color:var(--ok)}.bad{color:var(--bad)}.warn{color:var(--warn)}
.acts{display:flex;gap:8px;margin-top:8px;flex-wrap:wrap}button{min-height:40px;padding:9px 14px;border:0;border-radius:8px;background:var(--acc);color:#fff;font:inherit;font-weight:600;cursor:pointer}button.sec{background:transparent;color:var(--acc);border:1px solid var(--acc)}button:disabled{opacity:.5}
input,select,textarea{padding:9px 10px;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--ink);font:inherit;min-width:0}
pre,textarea.code{background:var(--bg);border:1px solid var(--line);border-radius:8px;padding:8px;font:12px/1.45 ui-monospace,Menlo,monospace;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;margin:8px 0 0}
pre{max-height:420px}textarea.code{width:100%;min-height:60vh;white-space:pre;tab-size:4}
#status{min-height:1.4em;color:var(--mute)}.msg{font-size:13px;margin-top:6px;overflow-wrap:anywhere}
.bar{height:8px;border-radius:4px;background:var(--line);overflow:hidden;margin-top:6px}.bar>i{display:block;height:100%;background:var(--acc)}.bar.hot>i{background:var(--bad)}
@media (max-width:560px){.acts button{flex:1 1 40%}}`

const pageJS = `const $=s=>document.querySelector(s);
async function call(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json'}:{},body:body?JSON.stringify(body):undefined,cache:'no-store',credentials:'same-origin'});let d={};try{d=await r.json();}catch(e){}if(!r.ok){const e=Error(d.error||('HTTP '+r.status));e.status=r.status;throw e;}return d;}
function tool(name,args){return call('/connections/api/tool',{tool:name,args:args||{}});}
function el(tag,cls,text){const e=document.createElement(tag);if(cls)e.className=cls;if(text!==undefined)e.textContent=text;return e;}
function needLogin(e){if(e&&e.status===401){$('#status').replaceChildren('Нужен вход через Face ID: ',Object.assign(el('a','','открыть «Подключения»'),{href:'/connections'}));return true;}return false;}
function gib(b){return b>=1<<30?(b/(1<<30)).toFixed(1)+' ГиБ':(b/(1<<20)).toFixed(0)+' МиБ';}
`

const pageNav = `<nav><a href="/connections">Подключения</a><a href="/connections/resources">Ресурсы</a><a href="/connections/containers">Контейнеры</a><a href="/connections/code">Код</a><a href="/connections/ide">IDE</a><a href="/connections/runners">Раннеры</a><a href="/connections/issues">Проблемы</a><a href="/connections/memory">Память</a><a href="/connections/plugins">Плагины</a></nav>`

// ownerPage assembles a page: title, body HTML, page script.
func ownerPage(title, body, script string) string {
	return `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + title + ` — MCPshka</title><style>` + pageCSS + `</style></head><body><main>` + pageNav + `<h1>` + title + `</h1>` + body + `<script>` + pageJS + script + `</script></main></body></html>`
}

// servePage writes an owner page with a fresh script nonce.
func servePage(w http.ResponseWriter, r *http.Request, html string) { servePageCSP(w, r, html, "") }

// servePageCSP adds one extra CSP directive (e.g. img-src data:).
func servePageCSP(w http.ResponseWriter, r *http.Request, html, extra string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nonce := RandomHex(16)
	csp := "default-src 'none'; script-src 'nonce-" + nonce + "'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	if extra != "" {
		csp += "; " + extra
	}
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, strings.Replace(html, "<script>", "<script nonce=\""+nonce+"\">", 1))
}
