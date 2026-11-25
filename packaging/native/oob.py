#!/usr/bin/env python3
"""Out-of-band MCPshka management over SSH paths that need neither mcpshka nor LANE.

Each path is one authorized key with a from= restriction and the forced
command `mcpshka-oob.py serve <path>`; sshd and this helper are the whole
path. Only fixed commands exist: status, probe, receipt and the three rescue
modes. Every call through a path is its evidence: a path counts as verified
while its last inbound call is younger than MAX_AGE.
"""
import importlib.util,ipaddress,json,os,pathlib,re,secrets,subprocess,sys,time
sys.dont_write_bytecode=True
RESCUE='/usr/local/libexec/mcpshka-rescue.py'
CONF=pathlib.Path('/etc/mcpshka/oob.json')
EVIDENCE=pathlib.Path('/var/lib/mcpshka-oob')  # root 0700: the rescue unit has no DAC override for the service's 0700 state dir
MAX_AGE=24*3600
NAME=re.compile(r'[a-z][a-z0-9-]{1,30}')
MODES=('reconnect','restart','rollback')
# A path's evidence only counts from the network that path is: the reverse
# tunnel arrives on loopback, Tailscale from the CGNAT tailnet range.
SOURCES={'vps':('127.0.0.0/8','::1/128'),'tailscale':('100.64.0.0/10','fd7a:115c:a1e0::/48')}

def client_allowed(path,client):
 nets=SOURCES.get(path)
 if nets is None:return True
 try:ip=ipaddress.ip_address(client)
 except ValueError:return False
 return any(ip in ipaddress.ip_network(n) for n in nets)

def rescue():
 spec=importlib.util.spec_from_file_location('rescue',RESCUE);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m

def boot_id():
 return pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()

def record(path,command):
 EVIDENCE.mkdir(mode=0o700,parents=True,exist_ok=True)
 tmp=EVIDENCE/('.'+path+'.tmp')
 tmp.write_text(json.dumps({'path':path,'command':command,'at':int(time.time()),'client':os.environ.get('SSH_CLIENT','').split(' ')[0]})+'\n')
 os.replace(tmp,EVIDENCE/(path+'.json'))

def summary(now=None):
 now=now or time.time();paths={}
 for f in sorted(EVIDENCE.glob('*.json')) if EVIDENCE.is_dir() else []:
  try:e=json.loads(f.read_text())
  except ValueError:continue
  if NAME.fullmatch(str(e.get('path',''))):paths[e['path']]={'last_inbound':e.get('at',0),'fresh':now-e.get('at',0)<MAX_AGE}
 return {'verified_independent_oob':sum(p['fresh'] for p in paths.values()),'paths':paths,'max_age_s':MAX_AGE}

def serve(path,line):
 if not NAME.fullmatch(path):raise ValueError('invalid path name')
 client=os.environ.get('SSH_CLIENT','').split(' ')[0]
 if not client_allowed(path,client):raise ValueError('path %s does not accept calls from %r'%(path,client))
 words=line.split()
 if not words:words=['status']
 cmd=words[0]
 if cmd=='status' and len(words)==1:out={'rescue':rescue().snapshot(),'oob':summary()}
 elif cmd=='probe' and len(words)==2 and re.fullmatch('[0-9a-f]{32}',words[1]):out={'nonce':words[1]}
 elif cmd=='receipt' and len(words)==2 and re.fullmatch('[0-9a-f]{32}',words[1]):out=rescue().receipt_status(words[1])
 elif cmd in MODES and len(words)==1:out=rescue().start(secrets.token_hex(16),cmd)
 else:raise ValueError('commands: status | probe <hex32> | receipt <hex32> | reconnect | restart | rollback')
 record(path,cmd)
 return dict(out,path=path,boot_id=boot_id())

def verify(name):
 """Round trip H96 -> VPS -> reverse tunnel -> H96 sshd -> serve <name>."""
 conf={p['name']:p for p in json.loads(CONF.read_text())['paths']}
 p=conf[name]
 if p.get('kind')!='reverse-ssh':raise ValueError('only reverse-ssh paths are verified from here; others by an inbound call')
 nonce=secrets.token_hex(16)
 inner='ssh -o BatchMode=yes -o ConnectTimeout=8 -o UserKnownHostsFile=~/.mcpshka-oob/known_hosts -i ~/.mcpshka-oob/probe -p %d root@127.0.0.1 probe %s'%(int(p['port']),nonce)
 args=['/usr/bin/ssh','-o','BatchMode=yes','-o','ConnectTimeout=10','-o','UserKnownHostsFile='+p['known_hosts'],'-i',p['key'],p['target'],inner]
 r=subprocess.run(['/usr/sbin/runuser','-u','mcpshka','--']+args,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=40,check=False)
 try:out=json.loads(r.stdout.decode().strip().splitlines()[-1])
 except (ValueError,IndexError):return {'path':name,'state':'failed','error':r.stderr.decode()[-300:].strip()}
 ok=out.get('nonce')==nonce and out.get('boot_id')==boot_id() and out.get('path')==name
 return {'path':name,'state':'verified' if ok else 'mismatch','oob':summary()}

def main():
 os.umask(0o077);args=sys.argv[1:]
 if os.geteuid()!=0:raise ValueError('root administrative helper required')
 if len(args)==2 and args[0]=='serve':out=serve(args[1],os.environ.get('SSH_ORIGINAL_COMMAND',''))
 elif args==['status']:out=summary()
 elif len(args)==2 and args[0]=='verify' and NAME.fullmatch(args[1]):out=verify(args[1])
 else:raise ValueError('invalid oob request')
 print(json.dumps(out))

if __name__=='__main__':
 try:main()
 except Exception as error:
  print(json.dumps({'state':'error','error':str(error)[:300]}));sys.exit(1)
