package ownerweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"mcpshka/internal/runnerhub"
)

// Runners is the pull-runner hub (set by the server). /runner/* is the
// machine side: a runner authenticates with its own key (Bearer), never with
// the owner's cookie; the owner side lives on «Раннеры» behind Face ID.
var Runners *runnerhub.Hub

const runnerPollWait = 25 * time.Second // below proxy idle timeouts

func registerRunnerRoutes(mux *http.ServeMux) { mux.HandleFunc("/runner/", runnerRoute) }

func runnerRoute(w http.ResponseWriter, r *http.Request) {
	origin, err := OriginConfigured()
	if err != nil || Runners == nil {
		http.NotFound(w, r)
		return
	}
	if u, _ := url.Parse(origin); r.Host != u.Host {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	action := strings.TrimPrefix(r.URL.Path, "/runner/")
	if platform, ok := strings.CutPrefix(action, "download/"); ok && r.Method == http.MethodGet {
		runnerhub.Download(w, r, platform)
		return
	}
	if action == "setup" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		_, _ = w.Write(runnerhub.Setup(origin))
		return
	}
	var in struct {
		Token, Name, Error string
		JobID              string            `json:"job_id"`
		ExitCode           int               `json:"exit_code"`
		Output             string            `json:"output"`
		Caps               map[string]string `json:"caps"`
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(io.LimitReader(r.Body, 512<<10)).Decode(&in); err != nil {
			jsonErr(w, "bad json", http.StatusBadRequest)
			return
		}
	}
	if action == "register" && r.Method == http.MethodPost {
		id, key, err := Runners.Register(in.Token, in.Name)
		if err != nil {
			jsonErr(w, err.Error(), http.StatusForbidden)
			return
		}
		JSON(w, map[string]string{"id": id, "key": key}, http.StatusOK)
		return
	}
	runner, err := Runners.Auth(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if err != nil {
		jsonErr(w, err.Error(), refusalStatus(err))
		return
	}
	switch {
	case action == "poll" && r.Method == http.MethodPost:
		job, err := Runners.Poll(runner.ID, in.Caps, runnerPollWait)
		if err != nil {
			jsonErr(w, err.Error(), refusalStatus(err))
			return
		}
		JSON(w, map[string]any{"job": job}, http.StatusOK)
	case action == "heartbeat" && r.Method == http.MethodPost:
		cancel, err := Runners.Heartbeat(runner.ID, in.JobID)
		if err != nil {
			jsonErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
		JSON(w, map[string]bool{"cancel": cancel}, http.StatusOK)
	case action == "archive" && r.Method == http.MethodGet:
		id := r.URL.Query().Get("job_id")
		j, err := Runners.Job(id)
		if err != nil || j.Runner != runner.ID || j.State != runnerhub.Leased {
			http.Error(w, "job is not leased to this runner", http.StatusForbidden)
			return
		}
		f, err := os.Open(Runners.BundlePath(j.ID))
		if err != nil {
			http.Error(w, "archive gone", http.StatusGone)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = io.Copy(w, f)
	case action == "result" && r.Method == http.MethodPost:
		if err := Runners.Result(runner.ID, in.JobID, in.ExitCode, in.Output, in.Error); err != nil {
			jsonErr(w, err.Error(), http.StatusConflict)
			return
		}
		JSON(w, map[string]bool{"ok": true}, http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

// runnersAPI is the owner side (Face ID session): list, new token, disable.
func (p *connPortal) runnersAPI(_ context.Context, w http.ResponseWriter, r *http.Request, action string) {
	if Runners == nil {
		jsonErr(w, "раннеры не включены", http.StatusServiceUnavailable)
		return
	}
	var in struct{ ID string }
	if r.Method == http.MethodPost {
		if err := ReadBody(r, &in); err != nil {
			jsonErr(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	switch action {
	case "runners":
		rs, js, err := Runners.Snapshot()
		if err != nil {
			jsonErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(js) > 20 {
			js = js[:20]
		}
		origin, _ := OriginConfigured()
		JSON(w, map[string]any{"runners": rs, "jobs": js, "origin": origin, "now": time.Now().UTC()}, http.StatusOK)
	case "runner-token":
		tok, exp, err := Runners.NewToken()
		if err != nil {
			jsonErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
		JSON(w, map[string]any{"token": tok, "expires": exp}, http.StatusOK)
	case "runner-disable":
		if err := Runners.Disable(in.ID); err != nil {
			jsonErr(w, err.Error(), http.StatusBadRequest)
			return
		}
		JSON(w, map[string]bool{"ok": true}, http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (p *connPortal) runnersPage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, runnersHTML)
}

var runnersHTML = ownerPage("Раннеры", `<p class="sub">Внешние машины для тяжёлых проверок: облачная IDE (GigaIDE Cloud, Codespaces) или ноутбук. Раннер сам ходит к MCPshka по HTTPS — портов и SSH не нужно. Любой ИИ (ChatGPT, Claude, Kimi, DeepSeek…) ставит задачу инструментом <code>runner_task</code>; если раннера нет 10 минут — задача уходит обратно на H96.</p>
<div id="status"></div>
<h2>Подключить</h2>
<div class="card"><div class="meta">Откройте терминал облачной IDE (GigaIDE Cloud, Codespaces) или ноутбука, нажмите кнопку, затем «Скопировать» и вставьте одну строку. Она ставит раннер, автозапуск и хранит ключ в рабочей папке IDE, которую та не стирает. Токен одноразовый, живёт час.</div><div class="acts"><button id="tok">Команда подключения</button></div><pre id="cmd" hidden></pre><div class="acts"><button id="copy" class="sec" hidden>Скопировать</button></div></div>
<div class="card"><div class="t">Если IDE сбросила настройки</div><div class="meta">Ключ сохранён — повторите без токена:</div><pre id="dev"></pre></div>
<h2>Раннеры</h2><div id="list"></div><h2>Задачи</h2><div id="jobs"></div>`, `
let origin='';
const ago=t=>{const s=Math.round((Date.now()-new Date(t))/1000);return s<90?s+' с назад':s<5400?Math.round(s/60)+' мин назад':Math.round(s/3600)+' ч назад';};
async function load(){try{const d=await call('/connections/api/runners',{});origin=d.origin;
$('#dev').textContent='curl -fsSL '+origin+'/runner/setup | sh';
const rs=(d.runners||[]);$('#list').replaceChildren(...(rs.length?rs.map(x=>{const c=el('div','card');const r=el('div','row');const on=!x.disabled&&(Date.now()-new Date(x.last_seen))<120000;r.append(el('span','t',x.name),el('span','st '+(x.disabled?'bad':on?'ok':'warn'),x.disabled?'отключён':on?(x.job?'работает':'ждёт'):'спит'));c.append(r,el('div','meta',ago(x.last_seen)+' · '+Object.entries(x.caps||{}).map(([k,v])=>k+'='+v).join(' ')+(x.job?' · задача '+x.job:'')));if(!x.disabled){const b=el('button','sec','Отключить');b.onclick=async()=>{if(confirm('Отключить '+x.name+'?')){await call('/connections/api/runner-disable',{id:x.id});load();}};const a=el('div','acts');a.append(b);c.append(a);}return c;}):[el('div','meta','Пока нет ни одного раннера.')]));
$('#jobs').replaceChildren(...(d.jobs||[]).map(j=>{const c=el('div','card');const r=el('div','row');r.append(el('span','t',j.kind+' '+j.project.replace('/root/repos/','')+' @'+j.commit.slice(0,8)),el('span','st '+(j.state==='DONE'?'ok':j.state==='FAILED'||j.state==='NO_RUNNER'?'bad':'warn'),j.state));c.append(r,el('div','meta',j.id+' · '+ago(j.created)+(j.error?' · '+j.error:'')));if(j.output){const p=el('pre','',j.output);p.hidden=true;const b=el('button','sec','Вывод');b.onclick=()=>p.hidden=!p.hidden;c.append(b,p);}return c;}));
$('#status').textContent='';}catch(e){if(!needLogin(e))$('#status').textContent='Ошибка: '+e.message;}}
$('#tok').onclick=async()=>{try{const t=await call('/connections/api/runner-token',{});$('#cmd').textContent='curl -fsSL '+origin+'/runner/setup | sh -s '+t.token;$('#cmd').hidden=false;$('#copy').hidden=false;}catch(e){if(!needLogin(e))$('#status').textContent='Ошибка: '+e.message;}};
$('#copy').onclick=async()=>{try{await navigator.clipboard.writeText($('#cmd').textContent);$('#copy').textContent='Скопировано';}catch(e){$('#copy').textContent='Выделите и скопируйте';}};
load();setInterval(()=>{if(!document.hidden)load();},20000);
`)

// refusalStatus: 401 only for a refused key; a registry the server cannot
// read is 503, which the agent waits out instead of exiting (23.11: a
// root-owned runners.json turned into «key refused» and killed the runner).
func refusalStatus(err error) int {
	if errors.Is(err, runnerhub.ErrRefused) {
		return http.StatusUnauthorized
	}
	return http.StatusServiceUnavailable
}
