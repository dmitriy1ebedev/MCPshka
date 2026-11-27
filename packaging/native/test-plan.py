#!/usr/bin/env python3
"""Dependency-aware test tiers. Cache compilation, never a passing test verdict."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import signal
import stat

TIERS=('FAST','INTEGRATION','RELEASE','PRODUCTION')
# CI owns compiler-heavy suites. Avoid resource contention with HA/Zigbee/MQTT on H96.
# The local allowlist is deliberately small and measurable; everything else
# is accepted only by the pinned cloud jobs, not silently marked as tested.
HOST_SMOKE_PACKAGES={'internal/filesystem','internal/policy','internal/audit','internal/trust'}
def constrained_arm_host():
    import platform
    if platform.machine().lower() not in ('aarch64','arm64'):
        return False
    try:
        with open('/proc/meminfo',encoding='ascii') as source:
            for line in source:
                if line.startswith('MemTotal:'):
                    return int(line.split()[1]) < 5*1024*1024
    except OSError:
        pass
    # Missing memory evidence must not authorize heavy compilation on ARM.
    return True

GLOBAL={'go.mod','go.sum','packaging/native/test-plan.py','.gitverse/workflows/ci.yml'}

def select(changed,graph):
    reasons={}
    if any(p in GLOBAL or p.startswith(('.github/workflows/', '.gitverse/workflows/')) for p in changed):
        return sorted(graph),{p:['toolchain_or_gate_changed'] for p in graph}
    if any(p.startswith('packaging/') and not p.startswith('packaging/native/test_') for p in changed):
        return sorted(graph),{p:['packaging_contract_dependencies:conservative_full_suite'] for p in graph}
    seeds={str(Path(p).parent) for p in changed if p.endswith('.go')}
    known=set(graph)|{p for imports in graph.values() for p in imports}
    if seeds-known:
        return sorted(graph),{p:['unmapped_go_change:conservative_full_suite'] for p in graph}
    pending=list(seeds);selected=set(seeds)
    for p in seeds:reasons[p]=['changed_package']
    while pending:
        dependency=pending.pop()
        for package,imports in graph.items():
            if dependency not in imports:continue
            reasons.setdefault(package,[]).append('dependency:'+dependency)
            if package not in selected:selected.add(package);pending.append(package)
    return sorted(selected.intersection(graph)),{p:sorted(set(reasons[p])) for p in sorted(selected.intersection(graph))}

def commands(tier,packages,reasons,changed):
    steps=[]
    def add(argv,reason):steps.append({'argv':argv,'reason':reason})
    if tier=='PRODUCTION':
        add(['/usr/local/bin/mcpshka','verify-production'],'installed version/hash, health, tunnel, DNS and rollback; no cache')
        return steps
    full=tier=='RELEASE'
    selected=['./...'] if full else ['./'+p for p in packages]
    if selected:
        add(['go','test','-count=1','-p','1','-json',*selected],
            'complete fixed release SHA' if full else reasons)
        if tier in ('INTEGRATION','RELEASE'):
            add(['go','vet','-p','1',*selected],'static checks of selected dependency closure')
        # Do not duplicate race in RELEASE: a separate mandatory pinned cloud
        # job owns it and the release is incomplete until all five jobs pass.
    native=[p for p in changed if p.startswith(('packaging/native/', '.github/workflows/', '.gitverse/workflows/')) or p in GLOBAL]
    if full or tier=='INTEGRATION' and native or 'packaging/native/test-plan.py' in changed:
        add(['python3','-I','-B','-m','unittest','discover','-s','packaging/native','-p','test_*.py'],
            'full native release contracts' if full else 'native helper or test gate changed')
    elif native:
        # Helpers invoke one another; native Python dependencies are not a Go graph.
        add(['python3','-I','-B','-m','unittest','discover','-s','packaging/native','-p','test_*.py'],
            'native dependency graph unknown: conservative complete native contracts')
    return steps

def capture(argv,cwd,timeout=60):
    result=subprocess.run(argv,cwd=cwd,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=timeout)
    if len(result.stdout)>16<<20:raise ValueError('inspection output budget exceeded')
    if result.returncode:raise RuntimeError('inspection failed: '+argv[0]+': '+result.stderr[-1024:].decode(errors='replace'))
    return result.stdout

def wait_bounded(process,timeout):
    try:
        return process.wait(timeout=timeout)
    finally:
        # A completed leader does not prove its descendants have stopped.
        try:os.killpg(process.pid,signal.SIGKILL)
        except ProcessLookupError:pass
        process.wait()

def interrupted(signum,frame):
    raise KeyboardInterrupt

def graph(root):
    raw=capture(['go','list','-json','./...'],root,120).decode()
    decoder=json.JSONDecoder();packages=[]
    while raw.strip():
        value,end=decoder.raw_decode(raw.lstrip());packages.append(value);raw=raw.lstrip()[end:]
    module=json.loads(capture(['go','list','-m','-json'],root))['Path']
    edges={}
    for p in packages:
        directory=Path(p['Dir']).resolve().relative_to(root).as_posix()
        imports=p.get('Imports',[])+p.get('TestImports',[])+p.get('XTestImports',[])
        edges[directory]=['.' if x==module else x[len(module)+1:] for x in imports if x==module or x.startswith(module+'/')]
    return edges

def main():
    shared_cache=Path('/var/lib/mcpshka-dev/go-build')
    parent=shared_cache.parent
    try:
        owner=parent.lstat();cache_owner=shared_cache.lstat()
        trusted_cache=(stat.S_ISDIR(owner.st_mode) and owner.st_uid==0 and owner.st_mode&0o022==0
                       and stat.S_ISDIR(cache_owner.st_mode) and cache_owner.st_uid==os.geteuid()
                       and cache_owner.st_mode&0o077==0 and os.access(shared_cache,os.W_OK))
    except OSError:trusted_cache=False
    if trusted_cache:
        os.environ.setdefault('GOCACHE',str(shared_cache))
        os.environ.setdefault('GOPATH','/root/go')
        os.environ.setdefault('GOMODCACHE','/root/go/pkg/mod')
    os.environ.setdefault('GOMAXPROCS','2')
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('tier',choices=TIERS);parser.add_argument('--base',default='HEAD')
    parser.add_argument('--run',action='store_true');parser.add_argument('--timeout',type=int,default=1200)
    args=parser.parse_args()
    if args.run and args.tier!='PRODUCTION' and os.geteuid()==0:
        raise ValueError('run code tests through an unprivileged managed job')
    if not 1<=args.timeout<=3600:parser.error('timeout must be 1..3600 seconds')
    root=Path(capture(['git','rev-parse','--show-toplevel'],Path.cwd()).decode().strip()).resolve()
    head=capture(['git','rev-parse','HEAD'],root).decode().strip()
    dirty=capture(['git','status','--porcelain'],root)
    if args.tier=='RELEASE' and dirty:raise ValueError('RELEASE requires a clean fixed commit; preserve and integrate dirty work first')
    base=capture(['git','rev-parse','--verify','--end-of-options',args.base+'^{commit}'],root).decode().strip()
    changed=set(p.decode() for p in capture(['git','diff','--name-only','-z','--no-renames',base,'--'],root).split(b'\0') if p)
    changed.update(p.decode() for p in capture(['git','ls-files','-z','--others','--exclude-standard'],root).split(b'\0') if p)
    dependencies={} if args.tier in ('RELEASE','PRODUCTION') else graph(root)
    packages,reasons=select(sorted(changed),dependencies)
    host_constrained=constrained_arm_host()
    if host_constrained and args.tier=='RELEASE' and args.run:
        raise ValueError('H96 is a production host: RELEASE must run on pinned cloud CI; local release tests are prohibited')
    # Full repository validation is deferred to cloud CI below. The local
    # package allowlist is applied before constructing runnable steps.
    deferred_remote=[]
    if host_constrained and args.tier in ('FAST','INTEGRATION'):
        deferred_remote=sorted(set(packages)-HOST_SMOKE_PACKAGES)
        packages=[package for package in packages if package in HOST_SMOKE_PACKAGES]
    # Production ARM hosts must not compile the entire repository locally.
    # Heavy full-repository test and vet remain cloud release responsibilities.
    if host_constrained and args.run and args.tier in ('FAST', 'INTEGRATION'):
        os.environ['GOMAXPROCS'] = '1'
        os.environ['GOFLAGS'] = '-p=1'
        # Keep compiler heap pressure bounded even when Go's cache is cold.
        os.environ['GOMEMLIMIT'] = '320MiB'
    steps=commands(args.tier,packages,reasons,sorted(changed))
    inputs=capture(['git','ls-files','-z'],root).split(b'\0')
    fingerprint=hashlib.sha256()
    for name in sorted(set(n.decode() for n in inputs if n)|changed):
        path=root/name
        if path.is_file() and not path.is_symlink():
            fingerprint.update(name.encode()+b'\0'+hashlib.sha256(path.read_bytes()).digest())
    fingerprint.update(capture(['go','version'],root))
    # Environment influences subprocess contracts; values never enter the report.
    fingerprint.update(json.dumps(dict(os.environ),sort_keys=True).encode())
    report={'tier':args.tier,'head':head,'dirty':bool(dirty),'input_sha256':fingerprint.hexdigest(),
            'changed':sorted(changed),'packages':packages,'steps':steps,
            'cache':'Go content/toolchain/flag keyed compilation only; test results always rerun',
            'heavy_tests':'extended race/fuzz/chaos use isolated cloud CI; never enabled by local tiers',
            'constrained_host':host_constrained,'deferred_remote':deferred_remote,
            'acceptance':'cloud_release_required' if host_constrained and args.tier=='RELEASE' else 'local_smoke_only_cloud_required' if deferred_remote else 'selected_local_checks_only',
            'cloud_release_jobs':['release','race','fuzz','build','release-arm64']}
    print(json.dumps(report),flush=True)
    if not args.run:return
    signal.signal(signal.SIGTERM,interrupted)
    if args.tier=='PRODUCTION' and root!=Path('/root/repos/mcpshka'):raise ValueError('PRODUCTION is H96 control-plane only')
    for step in steps:
        started=time.monotonic()
        with tempfile.TemporaryDirectory(prefix='mcpshka-test-metrics-') as temporary:
            metrics=Path(temporary)/'resources.json'
            argv=step['argv']
            if Path('/usr/bin/time').is_file():
                argv=['/usr/bin/time','-o',str(metrics),'-f',
                      '{"cpu_user_seconds":%U,"cpu_system_seconds":%S,"peak_rss_kib":%M,"input_blocks":%I,"output_blocks":%O}',*argv]
            process=subprocess.Popen(argv,cwd=root,start_new_session=True)
            code=wait_bounded(process,args.timeout)
            usage={}
            if metrics.exists():
                # GNU time may prepend a diagnostic for failed commands.
                for line in metrics.read_text().splitlines():
                    if line.startswith('{'):usage=json.loads(line)
            print(json.dumps({'argv':step['argv'],'exit':code,'seconds':round(time.monotonic()-started,3),'resources':usage}),flush=True)
            if code:raise SystemExit(code)

if __name__=='__main__':
    try:main()
    except KeyboardInterrupt:
        print(json.dumps({'error':'test job interrupted'}),file=sys.stderr);sys.exit(130)
    except (OSError,ValueError,RuntimeError,subprocess.TimeoutExpired) as error:
        print(json.dumps({'error':str(error)}),file=sys.stderr);sys.exit(1)
