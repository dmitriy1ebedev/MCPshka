package ownerweb

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// «Ресурсы»: what H96 has and who eats it — CPU load, memory, disk, temperature
// from health_snapshot; the pressure verdict that gates heavy work from
// host_triage; the largest directories; containers by usage; cleanup
// of the fixed safe places (hostclean), measured live, one button each.

func (p *connPortal) resourcesPage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, resourcesHTML)
}

// cleanupAPI: the plan is measured live by the broker (root); apply empties
// one listed place. The Face ID session is the confirmation.
func (p *connPortal) cleanupAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, action string) {
	args := []string{"plan"}
	if action == "cleanup-apply" {
		var in struct{ ID string }
		if r.Method != http.MethodPost || ReadBody(r, &in) != nil {
			jsonErr(w, "нужен POST {id}", http.StatusBadRequest)
			return
		}
		switch in.ID {
		case "go-cache", "docker", "journal":
		default:
			jsonErr(w, "неизвестная чистка", http.StatusBadRequest)
			return
		}
		args = []string{"apply", in.ID}
	}
	if p.broker == nil {
		jsonErr(w, "брокер недоступен", http.StatusServiceUnavailable)
		return
	}
	// Emptying a large Go cache on eMMC can outlive the 40 s API budget.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	raw, err := p.broker(cctx, "cleanup", args...)
	if err != nil {
		jsonErr(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(json.RawMessage(raw))
}

var resourcesHTML = ownerPage("Ресурсы", `<p class="sub">Что есть у H96 и кто это ест. Обновляется раз в 30 с.</p>
<div id="status">Загрузка…</div><div id="host"></div><h2>Нагрузка и очередь</h2><div id="pressure"></div>
<h2>Контейнеры по памяти</h2><div id="ctr"></div><h2>Крупное на диске</h2><div id="disk"></div><h2>Почистить</h2><div id="clean"></div><h2>Состояние подсистем</h2><div id="subs"></div>`, `
function meter(label,used,total,hot,text){const c=el('div','card');const r=el('div','row');r.append(el('span','t',label),el('span','meta',text));const b=el('div','bar'+(hot?' hot':''));const i=el('i');i.style.width=Math.min(100,Math.round(100*used/Math.max(total,1)))+'%';b.append(i);c.append(r,b);return c;}
function mem(s){const m=/([\d.]+)\s*([KMG]i?B)/.exec(s||'');if(!m)return 0;const k={KiB:1/1024,KB:1/1024,MiB:1,MB:1,GiB:1024,GB:1024}[m[2]]||1;return parseFloat(m[1])*k;}
async function load(){try{
const [h,t,c]=await Promise.all([tool('health_snapshot'),tool('host_triage'),call('/connections/api/containers')]);
const items={};(h.items||[]).forEach(i=>items[i.name]=i);const res=(items.resources||{}).data||{};
const host=$('#host');host.replaceChildren();
const load1=parseFloat((res.load_average||'0').split(' ')[0]);host.append(meter('Нагрузка CPU',load1,4,load1>6,(res.load_average||'?')+' (4 ядра)'));
const mt=res.memory_total_kb||1,ma=res.memory_available_kb||0;host.append(meter('Память',mt-ma,mt,ma/mt<0.1,'свободно '+gib(ma*1024)+' из '+gib(mt*1024)));
const dt=res.disk_total_bytes||1,df=res.disk_free_bytes||0;host.append(meter('Диск',dt-df,dt,df/dt<0.1,'свободно '+gib(df)+' из '+gib(dt)));
if(res.cpu_temp_c)host.append(meter('Температура CPU',res.cpu_temp_c,90,res.cpu_temp_c>75,res.cpu_temp_c+' °C'));
const st=res.storage||{};if(st.hours_to_reserve)host.append(el('div','meta','Прогноз: до резерва '+Math.round(st.hours_to_reserve)+' ч при текущем росте ('+st.confidence+')'));
const p=t.pressure||{};const pc=el('div','card');const pr=el('div','row');pr.append(el('span','t','Тяжёлые задачи'),el('span','st '+(p.status==='PASS'?'ok':'warn'),p.status==='PASS'?'пускаются':'ждут в очереди'));pc.append(pr,el('div','meta',p.detail||''));$('#pressure').replaceChildren(pc);
const list=(c.containers||[]).slice().sort((a,b)=>mem(b.memory)-mem(a.memory)).slice(0,8);$('#ctr').replaceChildren(...list.map(x=>{const d=el('div','card');const r=el('div','row');r.append(el('span','t',x.name),el('span','meta',(x.memory||'—').split(' / ')[0]+' · CPU '+(x.cpu||'—')));d.append(r);return d;}));
const top=(st.top||[]).slice(0,12);const dc=el('div','card');top.forEach(x=>{const r=el('div','row');r.append(el('span','',x.path),el('span','meta',gib(x.bytes)));dc.append(r);});if(st.reason)dc.append(el('div','meta',st.reason));$('#disk').replaceChildren(dc);
const sc=el('div','card');(h.items||[]).forEach(i=>{const r=el('div','row');const cls=i.state==='ok'?'ok':(i.state==='degraded'?'bad':'warn');r.append(el('span','',i.name),el('span','st '+cls,i.state));sc.append(r);});$('#subs').replaceChildren(sc);
const down=(c.containers||[]).filter(x=>x.state!=='running').length;$('#status').textContent=(t.healthy?'Хост здоров':'Хост нездоров')+(down?' · не работают контейнеры: '+down:'')+' · '+new Date().toLocaleTimeString('ru');
}catch(e){if(!needLogin(e))$('#status').textContent='Ошибка: '+e.message;}}
async function cleanPlan(){try{const d=await call('/connections/api/cleanup-plan');$('#clean').replaceChildren(...(d.items||[]).map(it=>{const c=el('div','card');const r=el('div','row');r.append(el('span','t',it.title),el('span','meta',gib(it.bytes)));const m=el('div','msg');const b=el('button','sec','Почистить');b.disabled=!!it.busy||it.bytes<1<<20;b.onclick=async()=>{if(!confirm(it.title+': освободить ~'+gib(it.bytes)+'?'))return;b.disabled=true;m.textContent='Чищу…';try{const x=await call('/connections/api/cleanup-apply',{id:it.id});m.textContent='Освобождено '+gib(x.freed||0);load();setTimeout(cleanPlan,1500);}catch(e){m.textContent='Ошибка: '+e.message;b.disabled=false;}};const a=el('div','acts');a.append(b);c.append(r,el('div','meta',it.busy||it.detail),a,m);return c;}));}catch(e){if(!needLogin(e))$('#clean').textContent='Ошибка: '+e.message;}}
cleanPlan();load();setInterval(()=>{if(!document.hidden)load();},30000);
`)
