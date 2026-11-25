package ownerweb

import "net/http"

// «Код»: browse and edit a project from any browser, the phone included,
// through the same tools ChatGPT uses. Editing takes the project's
// single-writer lease (shown if ChatGPT or an agent holds it — then the page
// stays read-only); saves are SHA-pinned fs_write (≤64 KiB), commits are
// project_commit of the chosen files, tests go through the heavy queue.

func (p *connPortal) codePage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, codeHTML)
}

var codeHTML = ownerPage("Код", `<p class="sub">Проекты H96: просмотр, правка, diff, коммит и тесты через очередь MCPshka.</p>
<div class="card"><div class="acts"><select id="proj"></select><button id="take">Править</button><button id="free" class="sec" hidden>Отпустить</button></div><div id="lease" class="msg"></div></div>
<div id="status"></div>
<div class="card"><div class="row"><span class="t" id="cwd">—</span><button id="up" class="sec">↑ Вверх</button></div><div id="tree"></div></div>
<div class="card" id="editor" hidden><div class="row"><span class="t" id="fname"></span><span class="meta" id="finfo"></span></div><textarea class="code" id="text" spellcheck="false"></textarea><div class="acts"><button id="save">Сохранить</button><button id="revert" class="sec">Отменить правки</button></div><div class="msg" id="emsg"></div></div>
<div class="card"><div class="row"><span class="t">Изменения</span><button id="refresh" class="sec">Обновить</button></div><div id="changes" class="meta"></div><pre id="diff" hidden></pre>
<div class="acts"><input id="cmsg" placeholder="Сообщение коммита" style="flex:1 1 240px"><button id="commit">Коммит выбранных</button><button id="test" class="sec">Тесты</button></div><div class="msg" id="cres"></div><pre id="tout" hidden></pre></div>`, `
let root='',cwd='',file=null,held=false,hb=null;
const enc=s=>{const b=new TextEncoder().encode(s);let x='';for(let i=0;i<b.length;i+=8192)x+=String.fromCharCode.apply(null,b.subarray(i,i+8192));return btoa(x);};
async function projects(){try{const d=await tool('fs_list',{path:'/root/repos',depth:1,max_entries:200});const dirs=(d.entries||[]).filter(e=>e.type==='dir'&&!/\/\./.test(e.path)).map(e=>e.path).sort();$('#proj').replaceChildren(...dirs.map(p=>Object.assign(el('option','',p.replace('/root/repos/','')),{value:p})));const last=localStorage.getItem('code.proj');if(last&&dirs.includes(last))$('#proj').value=last;await pick();}catch(e){if(!needLogin(e))$('#status').textContent='Ошибка: '+e.message;}}
async function pick(){root=$('#proj').value;try{localStorage.setItem('code.proj',root);}catch(e){}setHeld(false);await leaseInfo();await ls(root);await changes();}
async function leaseInfo(){try{const s=await tool('project_lease_status',{path:root});$('#lease').textContent=s.held?(s.owner==='owner-web'?'Правите вы':'Сейчас проект ведёт '+(s.owner||'?')+' — только просмотр'):'Проект свободен';$('#take').disabled=s.held&&s.owner!=='owner-web';}catch(e){$('#lease').textContent='';}}
function setHeld(v){held=v;$('#free').hidden=!v;$('#take').hidden=v;$('#save').disabled=!v;$('#commit').disabled=!v;clearInterval(hb);if(v)hb=setInterval(()=>call('/connections/api/code-heartbeat',{path:root}).catch(()=>{}),60000);}
async function ls(dir){cwd=dir;$('#cwd').textContent=dir.replace(root,'')||'/';try{const d=await tool('fs_list',{path:dir,depth:1,max_entries:500});const es=(d.entries||[]).filter(e=>e.path!==dir&&!e.path.endsWith('/.git')).sort((a,b)=>(a.type==='dir')===(b.type==='dir')?a.path.localeCompare(b.path):(a.type==='dir'?-1:1));$('#tree').replaceChildren(...es.map(e=>{const a=el('div','row');const n=el('a','',(e.type==='dir'?'📁 ':'')+e.path.split('/').pop());n.href='#';n.onclick=ev=>{ev.preventDefault();e.type==='dir'?ls(e.path):open(e.path);};a.append(n,el('span','meta',e.type==='dir'?'':gib(e.size||0).replace(' МиБ',' МиБ')));return a;}));}catch(e){$('#tree').textContent='Ошибка: '+e.message;}}
async function open(path){try{const d=await tool('fs_read',{path:path,limit:65536});file={path:path,sha:d.sha256,text:d.text||''};$('#editor').hidden=false;$('#fname').textContent=path.replace(root+'/','');const big=d.truncated||d.encoding!=='utf-8'&&d.encoding!=='utf8'&&!d.text;$('#finfo').textContent=(d.total_bytes||0)+' байт'+(d.truncated?' · показано 64 КиБ, только чтение':'');$('#text').value=d.text||'(двоичный файл)';$('#text').readOnly=!!(big||!d.sha256);$('#emsg').textContent='';$('#editor').scrollIntoView({behavior:'smooth'});}catch(e){$('#status').textContent='Ошибка: '+e.message;}}
$('#save').onclick=async()=>{if(!file)return;$('#emsg').textContent='Сохраняю…';try{const r=await tool('fs_write',{path:file.path,content_base64:enc($('#text').value),expected_sha256:file.sha});file.sha=r.sha256||file.sha;file.text=$('#text').value;$('#emsg').textContent='Сохранено.';changes();}catch(e){$('#emsg').textContent='Ошибка: '+e.message;}};
$('#revert').onclick=()=>{if(file)$('#text').value=file.text;};
$('#text').addEventListener('keydown',e=>{if(e.key==='Tab'){e.preventDefault();const t=e.target,s=t.selectionStart;t.setRangeText('\t',s,t.selectionEnd,'end');}if((e.ctrlKey||e.metaKey)&&e.key==='s'){e.preventDefault();if(held)$('#save').click();}});
async function changes(){try{const s=await tool('git_status',{path:root});const list=(s.entries||[]);const box=$('#changes');box.replaceChildren();if(!list.length){box.textContent='Нет изменений · ветка '+(s.branch||'?');$('#diff').hidden=true;return;}box.append(el('div','','Ветка '+(s.branch||'?')+':'));list.forEach(line=>{const f=line.slice(3);const l=el('label','row');const c=Object.assign(el('input'),{type:'checkbox',checked:true,value:f});l.append(c,el('span','',line));box.append(l);});const d=await tool('git_diff',{path:root,limit:131072});$('#diff').hidden=!d.text;$('#diff').textContent=d.text+(d.truncated?'\n… (обрезано)':'');}catch(e){$('#changes').textContent='Ошибка: '+e.message;}}
$('#refresh').onclick=changes;
$('#commit').onclick=async()=>{const files=[...document.querySelectorAll('#changes input:checked')].map(c=>c.value);const msg=$('#cmsg').value.trim();if(!files.length||!msg){$('#cres').textContent='Выберите файлы и напишите сообщение.';return;}$('#cres').textContent='Коммичу…';try{const r=await tool('project_commit',{path:root,files:files,message:msg});$('#cres').textContent='Коммит '+(r.commit||r.sha||'').slice(0,8)+' готов.';$('#cmsg').value='';changes();}catch(e){$('#cres').textContent='Ошибка: '+e.message;}};
$('#test').onclick=async()=>{$('#cres').textContent='Ставлю тесты в очередь…';$('#tout').hidden=true;try{const r=await tool('project_test',{path:root});const id=(r.job||{}).id;if(!id)throw Error('нет id задачи');const poll=async()=>{const s=await tool('job_status',{job_id:id});const q=(s.queue||{}).state;$('#cres').textContent='Тесты '+id+': '+(s.state==='running'&&q?q:s.state);if(['running','QUEUED','queued'].includes(s.state)){setTimeout(poll,5000);return;}const o=await tool('job_output',{job_id:id,limit:65536});$('#tout').hidden=false;$('#tout').textContent=o.text||o.data||JSON.stringify(o).slice(0,4000);};poll();}catch(e){$('#cres').textContent='Ошибка: '+e.message;}};
$('#take').onclick=async()=>{try{await call('/connections/api/code-open',{path:root});setHeld(true);leaseInfo();}catch(e){$('#lease').textContent='Не удалось: '+e.message;}};
$('#free').onclick=async()=>{await call('/connections/api/code-close',{path:root}).catch(()=>{});setHeld(false);leaseInfo();};
$('#up').onclick=()=>{if(cwd!==root)ls(cwd.replace(/\/[^/]+$/,''));};
$('#proj').onchange=pick;setHeld(false);projects();
`)
