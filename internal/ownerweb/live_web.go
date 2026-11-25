package ownerweb

import (
	"encoding/base64"
	"net/http"

	"mcpshka/internal/browser"
)

// «Вход на сайт» (owner 23.11, GigaIDE runner): the owner logs in to a site
// from the phone in the host's browser — live picture, taps and typing —
// so its profile keeps the session for browser_run and the GigaIDE wake.
// Same Face ID session as «Подключения»; passwords are never logged.

type liveRequest struct {
	Site, Text, Key, URL string
	X, Y, DY             float64
}

func (p *connPortal) livePage(w http.ResponseWriter, r *http.Request) {
	servePageCSP(w, r, liveHTML, "img-src data:")
}

func (p *connPortal) liveAPI(w http.ResponseWriter, r *http.Request, action string) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var in liveRequest
	if err := ReadBody(r, &in); err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if action == "live-start" {
		if _, err := browser.StartLive(in.Site); err != nil {
			jsonErr(w, err.Error(), http.StatusBadGateway)
			return
		}
		action = "live-frame"
	}
	if action == "live-stop" {
		browser.StopLive()
		JSON(w, map[string]bool{"ok": true}, http.StatusOK)
		return
	}
	l := browser.CurrentLive()
	if l == nil {
		jsonErr(w, "страница закрыта; откройте сайт заново", http.StatusConflict)
		return
	}
	var err error
	switch action {
	case "live-frame":
	case "live-tap":
		err = l.Tap(in.X, in.Y)
	case "live-scroll":
		err = l.Scroll(in.DY)
	case "live-type":
		err = l.Type(in.Text)
	case "live-key":
		err = l.Key(in.Key)
	case "live-goto":
		err = l.Goto(in.URL)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	jpeg, href, title, err := l.Frame()
	if err != nil {
		jsonErr(w, err.Error(), http.StatusBadGateway)
		return
	}
	JSON(w, map[string]any{"site": l.Site, "url": href, "title": title, "w": browser.LiveWidth, "h": browser.LiveHeight,
		"frame": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)}, http.StatusOK)
}

var liveHTML = ownerPage("Вход на сайт", `<p class="sub">Браузер H96 на вашем экране: войдите в сайт сами (пароль и коды видит только этот браузер). Вход сохранится в профиле сайта — MCPshka дальше работает с ним без вас (GigaIDE-раннер, GitVerse, MosHub).</p>
<div id="status"></div>
<div class="card"><div class="acts"><input id="site" value="gitverse.ru" style="flex:1"><button id="open">Открыть</button></div>
<div class="acts"><button class="sec" data-s="gitverse.ru">GitVerse</button><button class="sec" data-s="hub.mos.ru">MosHub</button><button class="sec" data-s="gigaide">GigaIDE</button></div></div>
<div id="view" hidden><div class="meta" id="url"></div><img id="shot" alt="" style="width:100%;border:1px solid #8884;border-radius:8px;touch-action:manipulation">
<div class="acts"><input id="txt" placeholder="текст в поле, на которое нажали" autocomplete="off" autocapitalize="off" style="flex:1"><button id="send">Ввести</button></div>
<div class="acts"><button class="sec" data-k="Enter">Enter</button><button class="sec" data-k="Backspace">⌫</button><button class="sec" data-k="Tab">Tab</button><button class="sec" data-d="-500">▲</button><button class="sec" data-d="500">▼</button><button class="sec" id="back">Назад</button><button id="done">Готово</button></div></div>`, `
let W=412,busy=false;
function show(d){W=d.w;$('#shot').src=d.frame;$('#url').textContent=d.title+' · '+d.url;$('#view').hidden=false;$('#status').textContent='';}
async function act(a,b){if(busy)return;busy=true;try{show(await call('/connections/api/'+a,b||{}));}catch(e){if(!needLogin(e))$('#status').textContent='Ошибка: '+e.message;}busy=false;}
function open(s){$('#site').value=s;act('live-start',{site:s});}
$('#open').onclick=()=>open($('#site').value.trim());
document.querySelectorAll('[data-s]').forEach(b=>b.onclick=()=>open(b.dataset.s));
document.querySelectorAll('[data-k]').forEach(b=>b.onclick=()=>act('live-key',{key:b.dataset.k}));
document.querySelectorAll('[data-d]').forEach(b=>b.onclick=()=>act('live-scroll',{dy:+b.dataset.d}));
$('#shot').onclick=e=>{const r=e.target.getBoundingClientRect(),k=W/r.width;act('live-tap',{x:(e.clientX-r.left)*k,y:(e.clientY-r.top)*k});};
$('#send').onclick=()=>{const t=$('#txt').value;$('#txt').value='';act('live-type',{text:t});};
$('#back').onclick=()=>act('live-goto',{url:'back'});
$('#done').onclick=async()=>{try{await call('/connections/api/live-stop',{});}catch(e){}$('#view').hidden=true;$('#status').textContent='Готово: вход сохранён в профиле сайта.';};
setInterval(()=>{if(!document.hidden&&!$('#view').hidden)act('live-frame');},1500);
const q=new URLSearchParams(location.search).get('site');if(q)open(q);
`)
