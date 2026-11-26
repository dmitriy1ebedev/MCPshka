package ownerweb

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mcpshka/internal/mcptransport"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"mcpshka/internal/brokerclient"
)

const PasskeyHTML = `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>MCPshka — подтверждение</title>
<style>body{font:16px -apple-system,system-ui,sans-serif;background:#101317;color:#eee;max-width:520px;margin:0 auto;padding:30px 20px}h1{font-size:25px}section{background:#20252b;border-radius:14px;padding:19px;line-height:1.6}small{color:#afb8c2}button{border:0;border-radius:12px;padding:15px;font-size:17px;margin-top:15px;background:#d8e8ff;color:#101317;font-weight:650;width:100%}button:disabled{opacity:.5}input{box-sizing:border-box;width:100%;font-size:20px;padding:15px;border-radius:12px;border:1px solid #667;background:#101317;color:white}#status{margin-top:20px;white-space:pre-wrap}</style>
</head><body><h1>Подтверждение MCPshka</h1><section id="details">Загрузка плана…</section><div id="enroll" hidden><p>Первичная привязка Apple «Пароли». Одноразовый TOTP вводится только на этой странице.</p><input id="otp" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="Код из Apple «Пароли»"><button id="setup">Подготовить привязку</button><button id="register" hidden>Создать passkey через Face ID</button></div><button id="approve" hidden>Подтвердить через Face ID</button><div id="status" role="status"></div>
<script>
const detail=document.getElementById('details'), status=document.getElementById('status');
const isEnroll=location.pathname==='/passkey/enroll';
let id=isEnroll?'':(location.pathname.split('/')[2]||'').toLowerCase().match(/^[0-9a-f]*/)[0];
const btn=document.getElementById('approve'), enroll=document.getElementById('enroll');
const b64bytes=x=>{const s=x.replace(/-/g,'+').replace(/_/g,'/');const a=atob(s+'='.repeat((4-s.length%4)%4));return Uint8Array.from(a,c=>c.charCodeAt(0));};
const bytesb64=x=>{const b=new Uint8Array(x);let s='';for(const v of b)s+=String.fromCharCode(v);return btoa(s).replace(/=/g,'').replace(/\+/g,'-').replace(/\//g,'_');};
const unwrap=o=>o.publicKey||o;
const createOptions=o=>{const p=structuredClone(unwrap(o));p.challenge=b64bytes(p.challenge);p.user.id=b64bytes(p.user.id);for(const x of (p.excludeCredentials||[]))x.id=b64bytes(x.id);return {publicKey:p};};
const getOptions=o=>{const p=structuredClone(unwrap(o));p.challenge=b64bytes(p.challenge);for(const x of (p.allowCredentials||[]))x.id=b64bytes(x.id);return {publicKey:p};};
const serialize=c=>({id:c.id,rawId:bytesb64(c.rawId),type:c.type,authenticatorAttachment:c.authenticatorAttachment,clientExtensionResults:c.getClientExtensionResults(),response:Object.fromEntries(['clientDataJSON','attestationObject','authenticatorData','signature','userHandle'].filter(k=>c.response[k]).map(k=>[k,bytesb64(c.response[k])]))});
async function request(path,body){const r=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json'}:{},body:body?JSON.stringify(body):undefined,cache:'no-store',credentials:'same-origin'});const d=await r.json();if(!r.ok)throw Error(d.error||'Ошибка подтверждения');return d;}
function show(e){status.textContent=String(e.message||e);}
let opts,exp;
if(isEnroll){
  detail.textContent='Однократная первичная настройка. Код TOTP не сохраняется в ChatGPT.';
  enroll.hidden=false;
  document.getElementById('setup').onclick=async()=>{try{const code=document.getElementById('otp').value;if(!/^\d{6}$/.test(code))throw Error('Нужны шесть цифр');const r=await request('/passkey/enroll/begin',{code});opts=r.options;document.getElementById('otp').value='';document.getElementById('setup').hidden=true;document.getElementById('register').hidden=false;status.textContent='Теперь создайте ключ доступа через Face ID.';}catch(e){show(e);}};
  document.getElementById('register').onclick=async()=>{try{const c=await navigator.credentials.create(createOptions(opts));await request('/passkey/enroll/finish',{credential:serialize(c)});enroll.hidden=true;status.textContent='Passkey создан. Вернитесь в ChatGPT.';}catch(e){show(e);}};
} else if(id.length===32||id.length===0||id.length>=8) {
  // Persist only the attempt flag (never credential/assertion bytes). A mobile
  // browser may reload while the committed /finish reply is lost.
  const pendingKey=()=> 'mcp-approval-finish-v1:'+id;
  const rememberPending=()=>{try{localStorage.setItem(pendingKey(),String(Date.now()));}catch(e){}};
  const pendingFromPreviousPage=()=>{try{const at=Number(localStorage.getItem(pendingKey()));return at>0&&Date.now()-at<48*3600*1000;}catch(e){return false;}};
  const clearPending=()=>{try{localStorage.removeItem(pendingKey());}catch(e){}};
  const load=async()=>{try{const data=await request('/approval/'+id+'/options');opts=data.options;exp=data.expires_at;if(data.state==='authorized'){detail.textContent='Задача '+id;status.textContent='Уже подтверждено через Face ID. Выполнение идёт само — можно вернуться в чат.';return;}if(data.plan_version===2&&data.scope==='mcpshka_administration'&&data.policy==='mcpshka-administration/v1'){detail.textContent=(data.purpose?'Для чего: '+data.purpose+'\n\n':'')+'Face ID откроет MCPshka права на эту задачу до её закрытия, максимум 24 часа. Все проверки и ограничения операций остаются.';detail.textContent+=data.agent_consent?'\n\nРазрешить дополнительных ИИ: '+data.agent_consent.agents.join(', ')+'\nПроекты: '+[data.agent_consent.path].concat(data.agent_consent.paths||[]).join(', ')+'\nДругие агенты и проекты не разрешены.':'\n\nДополнительные ИИ запрещены (native_only).';if(data.state==='active'){status.textContent='Сессия уже открыта. Повторное подтверждение не требуется.';return;}}else if(data.plan_version===1&&data.scope==='homeassistant_automations'){detail.textContent='Версия плана: '+data.plan_version+'\nДействие: изменение автоматизаций Home Assistant (весь automations.yaml).\nИсходный SHA-256: '+data.live_sha+'\nНовый SHA-256: '+data.staged_sha+'\nРазрешение действует только для этой задачи.';}else if(data.plan_version===1&&data.scope==='approved_task'){detail.textContent='Одна команда от root, один раз:\n'+(data.purpose||'')+'\n\nПосле Face ID её выполнит approved_task_execute; если исполняемый файл или закреплённые файлы изменятся, выполнение будет остановлено.';}else if(data.plan_version===1&&data.scope==='mcp_connector'){detail.textContent=(data.purpose||'Подключить Claude к MCPshka')+'\n\nClaude получит инструменты MCPshka от вашего имени; административные действия по-прежнему требуют отдельного Face ID.';}else{throw Error('Неизвестная область разрешения');}btn.hidden=false;}catch(e){show(e);detail.textContent='Подтверждение недоступно';}};
  (async()=>{if(id.length!==32){try{const r=await request('/approval/resolve/'+(id||'latest'));if(!/^[0-9a-f]{32}$/.test(r.task_id||''))throw Error('');id=r.task_id;history.replaceState(null,'','/approval/'+id);}catch(e){detail.textContent='Нет ожидающего подтверждения по этой ссылке. Попросите новую или откройте '+location.origin+'/approval/';return;}}await load();
    if(pendingFromPreviousPage()){
      finishSent=true;
      btn.hidden=true;
      btn.disabled=true;
      status.textContent='Предыдущая отправка Face ID могла исполниться. Проверяю состояние без повторной отправки.';
      void reconcileAfterFinish();
    }
  })();
  // Reconcile the durable grant using GET only. Never resend a WebAuthn
  // assertion after a lost /finish response: the approved task may have run.
  let finishSent=false;
  // stillWaiting: the last GET reached the server and it still issues a fresh
  // challenge, so the earlier /finish produced no grant (MCP-243).
  let stillWaiting=false;
  const recoverAuthorization=async()=>{
    stillWaiting=false;
    try {
      const data=await request('/approval/'+id+'/options');
      if(data.state!=='authorized'&&data.state!=='active'){
        if(data.options){stillWaiting=true;opts=data.options;exp=data.expires_at;}
        return false;
      }
      if(data.scope==='mcp_connector'){location.assign('/oauth/approved/'+id);return true;}
      detail.textContent='Задача '+id;
      status.textContent='Сервер подтвердил Face ID. Выполнение может продолжаться — проверьте результат в ChatGPT.';
      btn.hidden=true;
      btn.disabled=true;
      finishSent=false;
      clearPending();
      return true;
    } catch(e){return false;}
  };
  // A single GET can fail while iOS switches Wi-Fi to LTE. Retry only GETs,
  // with one bounded reconciler and no second WebAuthn POST.
  let recoveryInFlight=false;
  const reconcileAfterFinish=async()=>{
    if(recoveryInFlight)return false;
    recoveryInFlight=true;
    try{
      for(let attempt=0;attempt<8;attempt++){
        if(await recoverAuthorization())return true;
        if(attempt<7)await new Promise(resolve=>setTimeout(resolve,1500));
      }
      if(stillWaiting){
        // The server is the authority: no grant was made, so a NEW assertion
        // over its fresh challenge is safe. The old one is never resent.
        finishSent=false;
        clearPending();
        status.textContent='Сервер не получил прошлое подтверждение. Нажмите кнопку ещё раз — будет новый запрос Face ID.';
        btn.hidden=false;
        btn.disabled=false;
        return true;
      }
      return false;
    }finally{recoveryInFlight=false;}
  };
  window.addEventListener('pageshow',()=>{if(finishSent)void reconcileAfterFinish();});
  window.addEventListener('online',()=>{if(finishSent)void reconcileAfterFinish();});
  document.addEventListener('visibilitychange',()=>{if(!document.hidden&&finishSent)void reconcileAfterFinish();});
  btn.onclick=async()=>{
    btn.disabled=true;
    try{
      if(exp&&Date.now()>Date.parse(exp)-15000){
        const fresh=await request('/approval/'+id+'/options');
        if(fresh.state==='authorized'||fresh.state==='active'){
          await recoverAuthorization();return;
        }
        opts=fresh.options;exp=fresh.expires_at;
      }
      const c=await navigator.credentials.get(getOptions(opts));
      finishSent=true;
      rememberPending();
      const result=await request('/approval/'+id+'/finish',{credential:serialize(c)});
      if(result.scope==='mcp_connector'){clearPending();location.assign('/oauth/approved/'+id);return;}
      detail.textContent='Задача '+id;
      status.textContent='Подтверждено через Face ID. Статус: '+result.state+'. Вернитесь в ChatGPT.';
      btn.hidden=true;
      finishSent=false;
      clearPending();
    }catch(e){
      if(finishSent){
        if(await reconcileAfterFinish())return;
        status.textContent='Face ID выполнен, но ответ сервера не подтверждён. Не повторяйте операцию: откройте эту ссылку снова для проверки состояния.';
        btn.hidden=true;
        return;
      }
      show(e);
      btn.disabled=false;
    }
  };
} else {detail.textContent='Ссылка обрезана. Откройте '+location.origin+'/approval/ — там последнее ожидающее подтверждение.';}
</script></body></html>`

func OriginConfigured() (string, error) {
	v := strings.TrimSuffix(strings.TrimSpace(os.Getenv("MCPSHKA_PASSKEY_ORIGIN")), "/")
	u, e := url.Parse(v)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return "", errors.New("passkey HTTPS origin is not configured")
	}
	return v, nil
}
func portalGuard(w http.ResponseWriter, r *http.Request) bool {
	origin, e := OriginConfigured()
	if e != nil {
		http.NotFound(w, r)
		return false
	}
	u, _ := url.Parse(origin)
	if r.Host != u.Host {
		http.Error(w, "invalid host", http.StatusForbidden)
		return false
	}
	if r.Method == http.MethodPost && r.Header.Get("Origin") != origin {
		http.Error(w, "origin mismatch", http.StatusForbidden)
		return false
	}
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	return true
}

// jsonErr answers {"error": msg} with status.
func jsonErr(w http.ResponseWriter, msg string, status int) {
	JSON(w, map[string]string{"error": msg}, status)
}

func JSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ValidApprovalPrefix accepts "latest" or 8..31 lowercase hex characters of
// an approval ID.
func ValidApprovalPrefix(v string) bool {
	if v == "latest" {
		return true
	}
	if len(v) < 8 || len(v) > 31 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func portalBroker(ctx context.Context, args ...string) (json.RawMessage, error) {
	raw, e := brokerclient.Run(ctx, "passkey", args...)
	if e != nil {
		return nil, e
	}
	if !json.Valid(raw) {
		return nil, errors.New("invalid broker response")
	}
	return raw, nil
}
func portalBody(r *http.Request) (string, error) {
	if r.ContentLength > 16384 {
		return "", errors.New("request too large")
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 16385))
	if e != nil || len(b) > 16384 {
		return "", errors.New("invalid JSON body")
	}
	var data struct {
		Code       string          `json:"code"`
		Credential json.RawMessage `json:"credential"`
	}
	if e = json.Unmarshal(b, &data); e != nil {
		return "", errors.New("malformed JSON")
	}
	if data.Code != "" {
		return data.Code, nil
	}
	if len(data.Credential) > 12000 || len(data.Credential) == 0 {
		return "", errors.New("missing credential")
	}
	return string(data.Credential), nil
}

// The public HTTPS origin is served on a separate loopback socket.
// /artifacts and the control-plane health API are NEVER mounted here; /mcp
// only behind the OAuth connector (Face ID per authorization) when configured.
func PasskeyOnlyHandler(connector *OAuthServer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/approval/", PasskeyPortal)
	mux.HandleFunc("/passkey/enroll", PasskeyPortal)
	mux.HandleFunc("/passkey/enroll/begin", PasskeyPortal)
	mux.HandleFunc("/passkey/enroll/finish", PasskeyPortal)
	registerStorageConnectRoutes(mux)
	registerConnectionsRoutes(mux)
	registerRunnerRoutes(mux)
	if connector != nil {
		connector.register(mux)
	}
	return mux
}
func OpenPasskeyListener() (net.Listener, error) {
	if strings.TrimSpace(os.Getenv("MCPSHKA_PASSKEY_ORIGIN")) == "" {
		return nil, nil
	}
	if _, err := OriginConfigured(); err != nil {
		return nil, err
	}
	addr := strings.TrimSpace(os.Getenv("MCPSHKA_PASSKEY_LISTEN"))
	if addr == "" {
		addr = "127.0.0.1:8799"
	}
	if err := mcptransport.RequireLoopbackAddr(addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", addr)
}

func PasskeyPortal(w http.ResponseWriter, r *http.Request) {
	if !portalGuard(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Path
	if (path == "/passkey/enroll" || strings.HasPrefix(path, "/approval/")) && r.Method == http.MethodGet && !strings.HasSuffix(path, "/options") && !strings.HasPrefix(path, "/approval/resolve/") {
		nonceBytes := make([]byte, 18)
		if _, e := rand.Read(nonceBytes); e != nil {
			http.Error(w, "secure random unavailable", 503)
			return
		}
		nonce := base64.RawStdEncoding.EncodeToString(nonceBytes)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, strings.Replace(PasskeyHTML, "<script>", "<script nonce=\""+nonce+"\">", 1))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	var out json.RawMessage
	var err error
	switch {
	case path == "/passkey/enroll/begin" && r.Method == http.MethodPost:
		var code string
		code, err = portalBody(r)
		if err == nil && len(code) == 6 {
			out, err = portalBroker(ctx, "enroll-begin", code)
		} else if err == nil {
			err = errors.New("six digits required")
		}
	case path == "/passkey/enroll/finish" && r.Method == http.MethodPost:
		var body string
		body, err = portalBody(r)
		if err == nil {
			out, err = portalBroker(ctx, "enroll-finish", body)
		}
	case strings.HasPrefix(path, "/approval/resolve/") && r.Method == http.MethodGet:
		// Short and bookmarked links: phone terminals wrap the long link and
		// open only its first part. The broker maps the prefix to one pending
		// approval; Face ID is still required to finish it.
		prefix := strings.TrimPrefix(path, "/approval/resolve/")
		if !ValidApprovalPrefix(prefix) {
			http.NotFound(w, r)
			return
		}
		out, err = portalBroker(ctx, "resolve", prefix)
	case strings.HasPrefix(path, "/approval/"):
		tail := strings.TrimPrefix(path, "/approval/")
		parts := strings.Split(tail, "/")
		if len(parts) != 2 || len(parts[0]) != 32 {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		for _, c := range id {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				http.NotFound(w, r)
				return
			}
		}
		switch {
		case parts[1] == "options" && r.Method == http.MethodGet:
			out, err = portalBroker(ctx, "options", id)
		case parts[1] == "finish" && r.Method == http.MethodPost:
			var body string
			body, err = portalBody(r)
			if err == nil {
				out, err = portalBroker(ctx, "finish", id, body)
				if err == nil {
					if ContinueApproval != nil {
						ContinueApproval(ctx, out)
					}
				}
			}
		default:
			http.NotFound(w, r)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		jsonErr(w, err.Error(), http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}
