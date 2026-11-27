#!/usr/bin/env python3
"""Bounded state authority migration and recovery. Never prints record contents."""
import argparse, contextlib, hashlib, json, os, pathlib, sqlite3, stat, sys, tempfile, time
# Same ceiling as the Go store (statestore.maxDatabasePages 65536 x 4 KiB):
# at 64 MiB this helper refused the live 67.8 MB database and the nightly
# backup failed (25.11).
PAGE_SIZE=4096
MAX_PAGES=65536
MAX_BYTES=MAX_PAGES*PAGE_SIZE
MAX_RECORD=4*1024*1024
MAX_RECORDS=250000
# Validation time grows with the database: a fixed 15 s window rolled back a
# deploy at 16k records. Base plus a per-MiB allowance, hard-capped.
BASE_SECONDS=15
SECONDS_PER_MIB=2
MAX_SECONDS=300
NAMES=('jobs','approvals','leases','idempotency','deploys','backups','rescues')

def deadline_seconds(size):
 return min(MAX_SECONDS,BASE_SECONDS+SECONDS_PER_MIB*((size+(1<<20)-1)//(1<<20)))

def safe_path(value):
 p=pathlib.Path(value).absolute()
 if '..' in p.parts:raise ValueError('parent traversal rejected')
 for part in (p,*p.parents):
  if part.is_symlink(): raise ValueError('symlink rejected')
 return p

def key_ok(key):
 p=pathlib.PurePosixPath(key)
 return bool(key) and key!='.' and not p.is_absolute() and '..' not in p.parts and str(p)==key and '\x00' not in key and len(key)<=4096

def atomic(path, data, owner=None):
 path=safe_path(path);path.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
 fd,name=tempfile.mkstemp(prefix='.state-',dir=path.parent)
 try:
  with os.fdopen(fd,'wb') as f:f.write(data);f.flush();os.fsync(f.fileno())
  if owner:os.chown(name,*owner)
  os.replace(name,path)
  fd=os.open(path.parent,os.O_RDONLY|os.O_DIRECTORY)
  try:os.fsync(fd)
  finally:os.close(fd)
 finally:
  if os.path.exists(name):os.unlink(name)

def _connect(path):
 path=safe_path(path)
 info=path.stat() if path.is_file() else None
 if info is None or info.st_size>MAX_BYTES or info.st_mode & 0o077:raise ValueError('private database required')
 uid=info.st_uid;original=os.geteuid()
 if original==0 and uid!=0:os.seteuid(uid)
 try:
  db=sqlite3.connect(path,timeout=4)
  if db.execute('PRAGMA journal_mode').fetchone()[0]!='wal':raise ValueError('WAL required')
  db.execute('PRAGMA max_page_count=%d'%MAX_PAGES)
 finally:
  if os.geteuid()!=original:os.seteuid(original)
 db.execute('PRAGMA trusted_schema=OFF')
 db.setlimit(sqlite3.SQLITE_LIMIT_LENGTH,2*MAX_RECORD+8192)
 deadline=time.monotonic()+deadline_seconds(info.st_size)
 db.set_progress_handler(lambda:time.monotonic()>deadline,1000)
 version=db.execute('PRAGMA user_version').fetchone()[0]
 if version not in (1,2):db.close();raise ValueError('unsupported schema')
 objects=db.execute("SELECT type,name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").fetchall()
 cols=db.execute("PRAGMA table_info(records)").fetchall()
 expected=[('path','TEXT',0,1),('payload','BLOB',1,0),('sha256','TEXT',1,0)]
 if version==2:expected.append(('size_bytes','INTEGER',1,0))
 if objects!=[('table','records')] or [(r[1],r[2],r[3],r[5]) for r in cols]!=expected:db.close();raise ValueError('untrusted schema')
 if db.execute('PRAGMA page_size').fetchone()[0]!=4096:raise ValueError('unsupported page size')
 return db

@contextlib.contextmanager
def connect(path):
 db=_connect(path)
 try:
  with db:yield db
 finally:db.close()

def records(db):
 result=[];total=0
 for key,payload,digest in db.execute('SELECT path,payload,sha256 FROM records ORDER BY path'):
  if not key_ok(key) or not isinstance(payload,bytes) or len(payload)>MAX_RECORD or hashlib.sha256(payload).hexdigest()!=digest:raise ValueError('invalid record')
  total+=len(payload)
  if total>MAX_BYTES or len(result)>=MAX_RECORDS:raise ValueError('record budget exceeded')
  result.append((key,payload,digest))
 return result

def summary(db):
 count=0;total=0
 # Order is irrelevant for a count; skipping the sort keeps status cheap.
 for key,payload,digest in db.execute('SELECT path,payload,sha256 FROM records'):
  if not key_ok(key) or not isinstance(payload,bytes) or len(payload)>MAX_RECORD or hashlib.sha256(payload).hexdigest()!=digest:raise ValueError('invalid record')
  count+=1;total+=len(payload)
  if total>MAX_BYTES:raise ValueError('record budget exceeded')
 return {'records':count,'payload_bytes':total}

def legacy(root):
 result=[];total=0;deadline=time.monotonic()+30
 for name in NAMES:
  folder=root/name
  if not folder.exists():continue
  for base,dirs,files in os.walk(folder,followlinks=False):
   if name=='jobs' and pathlib.Path(base)==folder:dirs[:]=[x for x in dirs if x!='.home']
   for d in dirs:
    if (pathlib.Path(base)/d).is_symlink():raise ValueError('symlink directory')
   for fn in sorted(files):
    p=pathlib.Path(base)/fn;key=p.relative_to(root).as_posix()
    if not key.endswith('.json'):continue
    if name=='jobs' and not key.endswith('/metadata.json'):continue
    if not key_ok(key):raise ValueError('invalid legacy key')
    if time.monotonic()>deadline:raise ValueError('migration deadline exceeded')
    fd=os.open(p,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    try:
     before=os.fstat(fd)
     if not stat.S_ISREG(before.st_mode) or before.st_size>MAX_RECORD:raise ValueError('invalid legacy record')
     with os.fdopen(fd,'rb',closefd=False) as f:data=f.read(MAX_RECORD+1)
     after=os.fstat(fd)
     if len(data)!=before.st_size or (before.st_mtime_ns,before.st_ctime_ns)!=(after.st_mtime_ns,after.st_ctime_ns):raise ValueError('legacy record changed')
    finally:os.close(fd)
    json.loads(data);total+=len(data)
    if total>MAX_BYTES or len(result)>=MAX_RECORDS:raise ValueError('migration budget exceeded')
    result.append((key,data,hashlib.sha256(data).hexdigest()))
 return sorted(result)

def owner(root):
 info=(root/'jobs').stat() if (root/'jobs').exists() else root.stat()
 return info.st_uid,info.st_gid

def quiescent(root):
 if root==pathlib.Path('/var/lib/mcpshka'):
  import subprocess
  if subprocess.run(['/usr/bin/systemctl','is-active','--quiet','mcpshka.service']).returncode==0:raise ValueError('stop service before changing authority')

def activate(root):
 quiescent(root)
 if (root/'state.mode').exists():
  with connect(root/'state.db') as db:return {'mode':'sqlite-v1','records':summary(db)['records']}
 data=legacy(root);uidgid=owner(root)
 fd,tmp=tempfile.mkstemp(prefix='.state-db-',dir=root);os.close(fd)
 try:
  db=sqlite3.connect(tmp);db.execute('PRAGMA journal_mode=WAL');db.execute('CREATE TABLE records(path TEXT PRIMARY KEY,payload BLOB NOT NULL,sha256 TEXT NOT NULL)');db.execute('PRAGMA user_version=1')
  with db:db.executemany('INSERT INTO records VALUES(?,?,?)',data)
  if records(db)!=data:raise ValueError('migration validation failed')
  db.execute('PRAGMA wal_checkpoint(TRUNCATE)');db.close()
  os.chown(tmp,*uidgid);os.chmod(tmp,0o600)
  # Old inactive database is retained outside the authority path for recovery.
  if (root/'state.db').exists():os.replace(root/'state.db',root/('state.db.inactive-'+str(time.time_ns())))
  os.replace(tmp,root/'state.db');atomic(root/'state.mode',b'sqlite-v1\n',uidgid)
 finally:
  for p in (tmp,tmp+'-wal',tmp+'-shm'):
   if os.path.exists(p):os.unlink(p)
 return {'mode':'sqlite-v1','records':len(data)}

def export(root,destination):
 destination=safe_path(destination)
 if destination.exists():raise ValueError('restore target already exists')
 with connect(root/'state.db') as db:data=records(db)
 destination.mkdir(mode=0o700)
 for key,payload,_ in data:atomic(destination/key,payload)
 return {'records':len(data),'mode':'legacy-copy'}

def deactivate(root):
 quiescent(root)
 if not (root/'state.mode').exists():return {'mode':'legacy'}
 with connect(root/'state.db') as db:data=records(db)
 if any(pathlib.PurePosixPath(key).parts[0] not in NAMES or not key.endswith('.json') for key,_,_ in data):raise ValueError('legacy runtime cannot represent current namespaces; preserve SQLite authority')
 # Export validates all bytes first, then writes under quiescence. Keep stale
 # records as private recovery material, never as active legacy state.
 desired={key for key,_,_ in data};uidgid=owner(root)
 stale=[key for key,_,_ in legacy(root) if key not in desired]
 recovery=root/('retired-state-'+str(time.time_ns()))
 for key in stale:
  target=recovery/key;target.parent.mkdir(parents=True,exist_ok=True,mode=0o700);os.replace(root/key,target)
 for key,payload,_ in data:atomic(root/key,payload,uidgid)
 if legacy(root)!=data:raise ValueError('legacy export validation failed')
 (root/'state.mode').unlink()
 return {'mode':'legacy','records':len(data)}

def status(root):
 if not (root/'state.mode').exists():return {'mode':'legacy','schema':0}
 if (root/'state.mode').read_text().strip()!='sqlite-v1':raise ValueError('bad authority marker')
 with connect(root/'state.db') as db:
  data=summary(db);version=db.execute('PRAGMA user_version').fetchone()[0]
 return {'mode':'sqlite-v1','schema':version,**data,'database_bytes':(root/'state.db').stat().st_size}

def migrate(root,version):
 quiescent(root)
 with connect(root/'state.db') as db:
  before=records(db);current=db.execute('PRAGMA user_version').fetchone()[0]
  if current!=version:
   with db:
    if version==2:db.execute('ALTER TABLE records ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0');db.execute('UPDATE records SET size_bytes=length(payload)')
    elif version==1:db.execute('ALTER TABLE records DROP COLUMN size_bytes')
    else:raise ValueError('unsupported migration')
    db.execute('PRAGMA user_version='+str(version))
  if records(db)!=before:raise ValueError('migration changed payloads')
 return {'schema':version,'records':len(before)}

def write(root,key,payload):
 if not key_ok(key) or len(payload)>MAX_RECORD:raise ValueError('invalid record')
 if not (root/'state.mode').exists():atomic(root/key,payload);return
 with connect(root/'state.db') as db:
  version=db.execute('PRAGMA user_version').fetchone()[0]
  columns='path,payload,sha256';args=[key,payload,hashlib.sha256(payload).hexdigest()];updates='payload=excluded.payload,sha256=excluded.sha256'
  if version==2:columns+=',size_bytes';args.append(len(payload));updates+=',size_bytes=excluded.size_bytes'
  db.execute('INSERT INTO records('+columns+') VALUES('+','.join('?' for _ in args)+') ON CONFLICT(path) DO UPDATE SET '+updates,args)

def main():
 os.umask(0o077)
 p=argparse.ArgumentParser();p.add_argument('action',choices=['activate','deactivate','status','export','migrate','write']);p.add_argument('root');p.add_argument('value',nargs='?');a=p.parse_args();root=safe_path(a.root)
 if a.action=='activate':out=activate(root)
 elif a.action=='deactivate':out=deactivate(root)
 elif a.action=='status':out=status(root)
 elif a.action=='export':out=export(root,a.value)
 elif a.action=='migrate':out=migrate(root,int(a.value))
 else:write(root,a.value,sys.stdin.buffer.read(MAX_RECORD+1));out={'written':True}
 print(json.dumps(out))
if __name__=='__main__':
 try:main()
 except Exception:print(json.dumps({'error':'state operation failed; inspect bounded status before retry'}));sys.exit(1)
