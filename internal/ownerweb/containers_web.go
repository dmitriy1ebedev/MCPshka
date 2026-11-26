package ownerweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"mcpshka/internal/containerctl"
)

// «Контейнеры» (owner 08.11: «нативно, просто, удобно, быстро»): every Docker
// container of the host, derived live — no hand-kept list — with state,
// health, restarts and usage; start/stop/restart and a log tail behind the
// same Face ID session as «Подключения». The root broker family "containers"
// (internal/containerctl) does the work and heals a dead containerd task.

type containerRequest struct {
	Container string `json:"container"`
	Action    string `json:"action"`
	Lines     int    `json:"lines"`
}

func (p *connPortal) containersPage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, containersHTML)
}

// containersAPI serves list (GET), container-act and container-logs (POST).
func (p *connPortal) containersAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, action string) {
	var args []string
	switch {
	case action == "containers" && r.Method == http.MethodGet:
		args = []string{"list"}
	case (action == "container-act" || action == "container-logs") && r.Method == http.MethodPost:
		var in containerRequest
		if err := ReadBody(r, &in); err != nil {
			jsonErr(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !containerctl.ValidName(in.Container) {
			jsonErr(w, "неверное имя контейнера", http.StatusBadRequest)
			return
		}
		if action == "container-act" {
			switch in.Action {
			case "start", "stop", "restart":
			default:
				jsonErr(w, "действие: start, stop или restart", http.StatusBadRequest)
				return
			}
			args = []string{"act", in.Container, in.Action}
		} else {
			args = []string{"logs", in.Container, strconv.Itoa(min(max(in.Lines, 1), containerctl.MaxLogLines))}
		}
	default:
		http.NotFound(w, r)
		return
	}
	if p.broker == nil {
		jsonErr(w, "брокер недоступен", http.StatusServiceUnavailable)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	raw, err := p.broker(cctx, "containers", args...)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		jsonErr(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(json.RawMessage(raw))
}

var containersHTML = ownerPage("Контейнеры", `<p class="sub">Все контейнеры H96 прямо из Docker. Упавшие — сверху.</p><div id="status">Загрузка…</div><div id="list"></div>`, `
function badge(c){if(c.state!=='running')return['bad',c.state==='restarting'?'перезапуск':'остановлен'];if(c.health==='unhealthy')return['bad','нездоров'];if(c.health==='starting')return['warn','запуск'];return['ok','работает'];}
function card(c){const box=el('div','card');const row=el('div','row');row.append(el('span','t',c.name));const [cls,txt]=badge(c);row.append(el('span','st '+cls,txt));box.append(row);
const bits=[c.status,c.image];if(c.cpu)bits.push('CPU '+c.cpu);if(c.memory)bits.push('RAM '+c.memory.split(' / ')[0]);if(c.restarts)bits.push('рестартов: '+c.restarts);if(c.compose)bits.push('compose: '+c.compose);box.append(el('div','meta',bits.filter(Boolean).join(' · ')));
const acts=el('div','acts');const msg=el('div','msg');const logs=el('pre');logs.hidden=true;
const act=(a,label)=>{const b=el('button',a==='start'?'':'sec',label);b.onclick=async()=>{if(a==='stop'&&!confirm('Остановить '+c.name+'?'))return;acts.querySelectorAll('button').forEach(x=>x.disabled=true);msg.textContent='Выполняю…';try{const d=await call('/connections/api/container-act',{container:c.name,action:a});msg.textContent=(d.healed?'Убрал зависшую задачу containerd. ':'')+'Готово.';setTimeout(load,800);}catch(e){msg.textContent='Ошибка: '+e.message;acts.querySelectorAll('button').forEach(x=>x.disabled=false);}};return b;};
if(c.state==='running'){acts.append(act('restart','Перезапустить'),act('stop','Остановить'));}else{acts.append(act('start','Запустить'));}
const lb=el('button','sec','Логи');lb.onclick=async()=>{if(!logs.hidden){logs.hidden=true;return;}logs.hidden=false;logs.textContent='Загрузка…';try{const d=await call('/connections/api/container-logs',{container:c.name,lines:200});logs.textContent=d.logs||'(пусто)';logs.scrollTop=logs.scrollHeight;}catch(e){logs.textContent='Ошибка: '+e.message;}};acts.append(lb);
box.append(acts,msg,logs);return box;}
async function load(){try{const d=await call('/connections/api/containers');const list=d.containers||[];const down=list.filter(c=>c.state!=='running').length;$('#status').textContent=list.length+' контейнеров'+(down?', не работают: '+down:', все работают');$('#list').replaceChildren(...list.map(card));}catch(e){if(!needLogin(e))$('#status').textContent='Ошибка: '+e.message;}}
load();setInterval(()=>{if(!document.hidden&&!document.querySelector('pre:not([hidden])'))load();},15000);
`)
