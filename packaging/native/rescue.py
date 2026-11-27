#!/usr/bin/env python3
"""Independent bounded recovery of the two pinned MCPshka units; no network edits."""
import contextlib,fcntl,hashlib,importlib.util,json,os,pathlib,re,subprocess,sys,time,urllib.request
sys.dont_write_bytecode=True
ROOT=pathlib.Path('/var/lib/mcpshka');SELF='/usr/local/libexec/mcpshka-rescue.py'
MAIN='mcpshka.service';TUNNEL='mcpshka-tunnel.service';MODES=('reconnect','restart','rollback')
spec=importlib.util.spec_from_file_location('state','/usr/local/libexec/mcpshka-state-store.py');state=importlib.util.module_from_spec(spec);spec.loader.exec_module(state)

def run(args,timeout=8):
 r=subprocess.run(args,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=timeout,check=False)
 if len(r.stdout)>65536:raise ValueError('command output budget')
 return r.returncode,r.stdout

def unit(name):
 code,raw=run(['/usr/bin/systemctl','is-active',name],3)
 return raw.decode().strip() if code in (0,3) else 'unknown'

def probe(url):
 try:
  with urllib.request.urlopen(url,timeout=2) as response:return response.status==200 and len(response.read(2049))<=2048
 except Exception:return False

def active_jobs():
 with state.connect(ROOT/'state.db') as db:
  rows=db.execute("SELECT payload,sha256 FROM records WHERE path LIKE 'jobs/%/metadata.json'").fetchall()
 if len(rows)>state.MAX_RECORDS:raise ValueError('job budget')
 total=0
 for raw,digest in rows:
  if hashlib.sha256(raw).hexdigest()!=digest:raise ValueError('job checksum')
  if json.loads(raw).get('state') not in ('done','failed','cancelled','canceled','timed_out'):total+=1
 return total

OOB=pathlib.Path('/var/lib/mcpshka-oob')

def oob_paths(now=None):
 # Written by mcpshka-oob.py on every inbound call through a path; a path
 # counts only while that evidence is fresh (MAX_AGE in oob.py). Unreadable
 # evidence means zero verified paths, never a failed recovery check.
 now=now or time.time();fresh=[]
 try:files=sorted(OOB.glob('*.json')) if OOB.is_dir() else []
 except OSError:return fresh
 for f in files:
  try:e=json.loads(f.read_text())
  except ValueError:continue
  if now-e.get('at',0)<24*3600:fresh.append(str(e.get('path','')))
 return fresh

def snapshot():
 main=unit(MAIN);tunnel=unit(TUNNEL);paths=oob_paths()
 return {'main':main,'tunnel':tunnel,'health':main=='active' and probe('http://127.0.0.1:8765/healthz'),'ready':tunnel=='active' and probe('http://127.0.0.1:8768/readyz'),'verified_independent_oob':len(paths),'oob_paths':paths,'oob_state':'verified by inbound calls within 24 h' if paths else 'no OOB path used within 24 h'}

def require_active():
 marker=state.safe_path(ROOT/'state.mode')
 if not marker.is_file() or marker.read_bytes().strip()!=b'sqlite-v1':raise ValueError('active SQLite authority required')

def get(identifier):
 require_active()
 with state.connect(ROOT/'state.db') as db:row=db.execute('SELECT payload,sha256 FROM records WHERE path=?',('rescues/'+identifier+'.json',)).fetchone()
 if not row:raise FileNotFoundError('rescue receipt missing')
 if hashlib.sha256(row[0]).hexdigest()!=row[1]:raise ValueError('receipt checksum')
 return json.loads(row[0])

def receipt_status(identifier):
 result=get(identifier)
 if result.get('state') in ('queued','running'):
  observed=unit('mcpshka-rescue-'+identifier+'.service')
  result=dict(result,worker_state=observed)
  if observed not in ('active','activating','reloading'):result.update(recorded_state=result['state'],state='unknown',reason='worker not active; reconcile without replay')
 return result

def save(identifier,data):
 require_active();state.write(ROOT,'rescues/'+identifier+'.json',(json.dumps(data,sort_keys=True)+'\n').encode())

@contextlib.contextmanager
def lock(path):
 fd=os.open(path,os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
 try:
  info=os.fstat(fd)
  if info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe recovery lock')
  fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
  yield
 finally:os.close(fd)

def main_start_safe():
 if (ROOT/'state.mode').exists():
  code,raw=run(['/usr/sbin/runuser','-u','mcpshka','--','/usr/local/bin/mcpshka','state-store-support'],4)
  if code or raw.strip()!=b'sqlite-v1':raise ValueError('installed binary/state mismatch; no stale-state startup')

def reconnect(before):
 action='restart' if before['tunnel']=='active' and not before['ready'] else 'start'
 if run(['/usr/bin/systemctl',action,TUNNEL],20)[0]:raise ValueError('tunnel reconnect failed')
 for _ in range(15):
  after=snapshot()
  if after['health'] and after['ready']:return {'before':before,'after':after,'state':'verified'}
  time.sleep(1)
 raise ValueError('recovery acceptance failed')

def recover(mode):
 if mode not in MODES:raise ValueError('unsupported recovery mode')
 before=snapshot()
 with lock('/run/mcpshka-self-deploy.lock'):
  if mode=='restart':
   if active_jobs():raise ValueError('active jobs require reconciliation before restart')
   main_start_safe()
   if run(['/usr/bin/systemctl','restart',MAIN],20)[0]:raise ValueError('main restart failed')
   return reconnect(snapshot())
  if mode=='reconnect':
   if not before['health']:raise ValueError('main unhealthy; reconcile before reconnect')
   return reconnect(before)
  if active_jobs():raise ValueError('active jobs require reconciliation before rollback')
 # The guarded updater takes its own lock and verifies expected installed
 # identity. Release ours before scheduling; never nest a detached updater.
 # Releases are controller+broker pairs: roll the pair back through the
 # coordinated updater's journal of the installed version (BUG-20261026-127:
 # rolling back only the controller left a mismatched broker).
 code,raw=run(['/usr/sbin/runuser','-u','mcpshka','--','/usr/local/bin/mcpshka','version'],4)
 installed=raw.decode().strip().removeprefix('mcpshka ')
 plan=pathlib.Path('/root/repos/mcpshka/bin')/('mcpshka-coordinated-release-'+installed+'.json')
 if not code and re.fullmatch(r'0\.[0-9]{1,3}\.[0-9]{1,6}',installed) and plan.is_file():
  code,raw=run(['/usr/local/libexec/mcpshka-coordinated-upgrade','--version',installed,'--rollback'],600)
  if code:raise ValueError('paired rollback failed; inspect the coordinated journal, never repeat blindly')
  return {'before':before,'state':'rolled_back','paired':True,'from':installed,'result':json.loads(raw.decode().strip().splitlines()[-1])}
 previous=pathlib.Path('/usr/local/bin/mcpshka.previous')
 info=state.safe_path(previous).stat()
 if info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe rollback binary')
 code,raw=run(['/usr/sbin/runuser','-u','mcpshka','--',str(previous),'version'],4)
 version=raw.decode().strip().removeprefix('mcpshka ')
 if code or not re.fullmatch(r'[0-9][0-9a-z.]{1,40}',version):raise ValueError('invalid rollback version')
 # Releases name candidates mcpshka-<version>.<sha12>.candidate; the hash
 # comparison below still decides which one is the previous binary.
 bin_dir=pathlib.Path('/root/repos/mcpshka/bin')
 names=[bin_dir/('mcpshka-'+version+'.candidate')]+sorted(bin_dir.glob('mcpshka-'+version+'.*.candidate'))
 names=[c for c in names if re.fullmatch(r'mcpshka-'+re.escape(version)+r'(\.[0-9a-f]{12})?\.candidate',c.name) and c.is_file()]
 want=hashlib.sha256(previous.read_bytes()).digest()
 candidate=next((c for c in names if hashlib.sha256(state.safe_path(c).read_bytes()).digest()==want),None)
 if candidate is None:raise ValueError('retained committed rollback candidate required')
 code,raw=run(['/usr/local/bin/mcpshka','deploy-self',str(candidate),version],8)
 if code:raise ValueError('guarded rollback scheduling failed')
 deployment=json.loads(raw);return {'before':before,'state':'scheduled','deployment_id':deployment['deployment_id']}

def worker(identifier,mode):
 receipt=get(identifier)
 if receipt.get('state')!='queued':return receipt
 receipt.update(state='running');save(identifier,receipt)
 try:
  with lock('/run/mcpshka-rescue.lock'):receipt.update(recover(mode))
 except Exception as error:receipt.update(state='unknown',error_class=type(error).__name__,error='recovery requires reconciliation; never blindly repeat')
 receipt['finished_at']=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime());save(identifier,receipt)
 return receipt

def start(identifier,mode):
 if mode not in MODES:raise ValueError('unsupported recovery mode')
 try:return get(identifier)
 except FileNotFoundError:pass
 receipt={'id':identifier,'mode':mode,'state':'queued'};save(identifier,receipt)
 args=['/usr/bin/systemd-run','--quiet','--collect','--no-block','--unit=mcpshka-rescue-'+identifier,'--property=Type=oneshot','--property=TimeoutStartSec=900s','--property=MemoryMax=384M','--property=CPUQuota=25%','--property=TasksMax=32','--property=UMask=0077','--property=NoNewPrivileges=yes','/usr/bin/python3',SELF,'worker',mode,identifier]
 if run(args,5)[0]:raise ValueError('rescue scheduling unknown')
 return receipt

def watch():
 current=snapshot()
 # Never fight a deliberate healthy main shutdown or restart jobs. systemd
 # owns main on-failure restart; this independent watcher restores its tunnel.
 if not current['health']:return dict(current,state='hold_main_reconcile')
 if current['ready']:return dict(current,state='ready')
 try:
  with lock('/run/mcpshka-rescue.lock'):return recover('reconnect')
 except BlockingIOError:return dict(current,state='guarded_operation_in_progress')

def main():
 os.umask(0o077);args=sys.argv[1:]
 if os.geteuid()!=0:raise ValueError("root administrative helper required")
 if args==['watch']:out=watch()
 elif args==['status']:out=snapshot()
 elif len(args)==2 and args[0]=='status' and re.fullmatch('[0-9a-f]{32}',args[1]):out=receipt_status(args[1])
 elif len(args)==3 and args[0] in ('start','worker') and args[1] in MODES and re.fullmatch('[0-9a-f]{32}',args[2]):out=start(args[2],args[1]) if args[0]=='start' else worker(args[2],args[1])
 else:raise ValueError('invalid rescue request')
 print(json.dumps(out))
if __name__=='__main__':
 try:main()
 except Exception as error:print(json.dumps({'error':'bounded rescue operation rejected','error_class':type(error).__name__}));sys.exit(1)
