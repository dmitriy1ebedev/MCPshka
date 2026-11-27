#!/usr/bin/env python3
"""Bounded local Codex daemon client; no auth/config files or new worker process."""
import hashlib,json,pathlib,re,shlex,socket,sys,time,uuid
from websockets.sync.client import connect
SOCKET='/root/.codex/app-server-control/app-server-control.sock'
MAX_INPUT=12000
MAX_OUTPUT=48000
SOURCE_KINDS=('cli','vscode','exec','appServer')

def redact(value):
 text='' if value is None else str(value)
 text=re.sub(r'-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----','[REDACTED PRIVATE KEY]',text,flags=re.S)
 text=re.sub(r'(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+',r'\1[REDACTED]',text)
 text=re.sub(r'\b(?:sk-|sk-proj-|ghp_|github_pat_|xox[baprs]-)[A-Za-z0-9_-]{12,}','[REDACTED]',text)
 text=re.sub(r'(?i)((?:access_token|refresh_token|api_key|password|secret|authorization)["\s]*[:=]["\s]*)[^\s,"\'}]+',r'\1[REDACTED]',text)
 text=re.sub(r'(?i)((?:--(?:password|token|api-key|secret|user)|-u)\s+)(?:"[^"\n]*"|\x27[^\x27\n]*\x27|[^\s]+)',r'\1[REDACTED]',text)
 text=re.sub(r'(?i)(\btoken\s*=\s*)(?:"[^"\n]*"|\x27[^\x27\n]*\x27|[^\s,;]+)',r'\1[REDACTED]',text)
 return text

def identifier(value):
 return isinstance(value,str) and bool(re.fullmatch(r'[A-Za-z0-9_-]{8,128}',value))

def command_identity(command):
 # Arbitrary arguments can contain credentials with no recognizable secret label.
 try:parts=shlex.split(command)
 except (ValueError,TypeError):return '[unparsed command]'
 if not parts:return '[empty command]'
 name=pathlib.PurePath(parts[0]).name
 allowed={'go':{'test','build','vet','list','version'},'git':{'status','diff','log','show'},
          'python3':set(),'python':set(),'bash':set(),'sh':set(),'make':set(),
          'node':set(),'npm':{'test','run','install'},'curl':set(),'docker':{'ps','inspect'},'sleep':set()}
 if name not in allowed:return '[command arguments withheld]'
 return name+(' '+parts[1] if len(parts)>1 and parts[1] in allowed[name] else '')

class RPC:
 def __enter__(self):
  sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);sock.settimeout(5);sock.connect(SOCKET)
  self.connection=connect('ws://localhost/',sock=sock,open_timeout=5,max_size=2*1024*1024,compression=None)
  self.ws=self.connection.__enter__()
  self.seq=0
  self.call('initialize',{'clientInfo':{'name':'mcpshka','title':'MCPshka','version':'0.1.56'},'capabilities':{'experimentalApi':True}})
  self.ws.send(json.dumps({'method':'initialized','params':{}}));return self
 def __exit__(self,*args):self.connection.__exit__(*args)
 def call(self,method,params):
  self.seq+=1;request_id=self.seq
  self.ws.send(json.dumps({'id':request_id,'method':method,'params':params},separators=(',',':')))
  deadline=time.monotonic()+8
  for _ in range(128):
   remaining=deadline-time.monotonic()
   if remaining<=0:raise TimeoutError('daemon RPC deadline')
   response=json.loads(self.ws.recv(timeout=remaining))
   if response.get('id')==request_id:
    if 'error' in response:raise ValueError('Codex RPC rejected')
    return response['result']
  raise ValueError('RPC notification budget exceeded')

def summary(thread):
 return {'session_id':thread['id'],'name':redact(thread.get('name') or thread.get('preview',''))[:200],'cwd':thread.get('cwd'),'updated_at':thread.get('updatedAt'),'status':thread.get('status',{}),'history_mode':thread.get('historyMode'),'origin':redact(thread.get('source'))[:40],'source':'local_codex','trust':'UNTRUSTED'}

def sessions_list(rpc,args):
 if set(args)-{'cursor','limit','cwd','archived'}:raise ValueError('unknown field')
 limit=args.get('limit',10)
 if not isinstance(limit,int) or not 1<=limit<=30:raise ValueError('invalid page size')
 # Without sourceKinds the daemon lists interactive sessions only, so every
 # agent_task run (codex exec) was invisible and the list came back empty.
 params={'limit':limit,'useStateDbOnly':True,'sortKey':'updated_at','sourceKinds':list(SOURCE_KINDS)}
 for field in ('cursor','cwd','archived'):
  if field in args:
   if isinstance(args[field],str) and len(args[field])>2048:raise ValueError('filter budget')
   params[field]=args[field]
 result=rpc.call('thread/list',params)
 return {'sessions':[summary(t) for t in result['data']],'next_cursor':result.get('nextCursor'),'source':'local_codex','cloud_history':'not_verified'}

def session_read(rpc,args):
 if set(args)-{'session_id','cursor','limit','output_limit_bytes'} or not identifier(args.get('session_id')):raise ValueError('invalid session request')
 limit=args.get('limit',3);budget=args.get('output_limit_bytes',16000)
 if not isinstance(limit,int) or not 1<=limit<=10 or not isinstance(budget,int) or not 1024<=budget<=32000:raise ValueError('read budget')
 thread=rpc.call('thread/read',{'threadId':args['session_id'],'includeTurns':False})['thread']
 params={'threadId':args['session_id'],'limit':limit,'itemsView':'full','sortDirection':'desc'}
 if args.get('cursor'):params['cursor']=args['cursor']
 page=rpc.call('thread/turns/list',params)
 out={'session':summary(thread),'turns':[],'next_cursor':page.get('nextCursor'),'truncated':False,'trust':'UNTRUSTED'}
 remaining=budget
 for turn in page.get('data',[]):
  entry={'turn_id':turn.get('id'),'status':turn.get('status'),'items':[]}
  items=turn.get('items',[])
  if len(items)>64:out['truncated']=True
  # Reserve the bounded view for newest evidence, then retain chronological display.
  for item in reversed(items[-64:]):
   kind=item.get('type')
   if kind=='userMessage':text='\n'.join((x.get('text') or '') for x in item.get('content',[]) if x.get('type')=='text')
   elif kind=='agentMessage':text=item.get('text','')
   elif kind=='commandExecution':text='status='+redact(item.get('status'))+'\nexit='+redact(item.get('exitCode'))+'\ncommand='+command_identity(item.get('command') or '')+'\n'+(item.get('aggregatedOutput') or '')
   else:continue
   text=redact(text);raw=text.encode();truncated=len(raw)>remaining
   text=raw[:remaining].decode(errors='ignore');remaining-=len(text.encode())
   entry['items'].append({'type':kind,'text':text,'truncated':truncated});out['truncated']|=truncated
   if remaining<=0:break
  entry['items'].reverse()
  out['turns'].append(entry)
  if remaining<=0:out['truncated']=True;break
 return out

def task_submit(rpc,args):
 if set(args)-{'session_id','message','request_id','mode','model','reasoning_effort'} or not identifier(args.get('session_id')):raise ValueError('invalid submission')
 message=args.get('message');request_id=args.get('request_id');mode=args.get('mode','queue')
 if not isinstance(message,str) or not message.strip() or len(message.encode())>4096 or not isinstance(request_id,str) or not re.fullmatch('[0-9a-f]{32}',request_id) or mode not in ('queue','start'):raise ValueError('invalid task')
 model=args.get('model');effort=args.get('reasoning_effort')
 if model is not None or effort is not None:
  if mode!='start' or not isinstance(model,str) or not re.fullmatch(r'[A-Za-z0-9._-]{1,80}',model):raise ValueError('model settings require read-only start and explicit model')
  if effort is not None and effort not in ('minimal','low','medium','high','xhigh'):raise ValueError('invalid reasoning effort')
  catalog=rpc.call('model/list',{'limit':100,'includeHidden':False})
  matches=[item for item in catalog.get('data',[]) if item.get('model')==model and not item.get('hidden')]
  if len(matches)!=1:raise ValueError('model not present in bounded live catalog')
  supported=[item.get('reasoningEffort') for item in matches[0].get('supportedReasoningEfforts',[]) if isinstance(item,dict)]
  if effort is not None and effort not in supported:raise ValueError('effort unsupported by selected model')
  raise ValueError('per-turn model override is not enabled in this bridge release')
 thread=rpc.call('thread/read',{'threadId':args['session_id'],'includeTurns':False})['thread']
 status=thread.get('status',{}).get('type')
 if status not in ('active','idle'):raise ValueError('target session must already be loaded; bridge never spawns/resumes a worker')
 if mode=='start':
  if status!='idle':raise ValueError('target busy; queue instead')
  result=rpc.call('turn/start',{'threadId':args['session_id'],'input':[{'type':'text','text':message}],'clientUserMessageId':request_id,'approvalPolicy':'never','sandboxPolicy':{'type':'readOnly'}})
  return {'session_id':args['session_id'],'turn_id':result['turn']['id'],'state':result['turn']['status'],'request_id':request_id,'mode':'read-only'}
 result=rpc.call('thread/queue/add',{'threadId':args['session_id'],'input':[{'type':'text','text':message}],'clientUserMessageId':request_id})
 return {'session_id':args['session_id'],'queue_id':result['queuedSubmission']['id'],'state':'queued','request_id':request_id,'note':'existing session owner processes queue'}

def task_status(rpc,args):
 if set(args)-{'session_id','queue_id','request_id','turn_id','queue_cursor','turn_cursor'} or not identifier(args.get('session_id')):raise ValueError('invalid task probe')
 for name in ('queue_id','turn_id'):
  if args.get(name) and not identifier(args[name]):raise ValueError('invalid task identity')
 request=args.get('request_id')
 if request is not None and (not isinstance(request,str) or not re.fullmatch('[0-9a-f]{32}',request)):raise ValueError('invalid request identity')
 for name in ('queue_cursor','turn_cursor'):
  if args.get(name) and (not isinstance(args[name],str) or len(args[name])>2048):raise ValueError('cursor budget')
 thread=rpc.call('thread/read',{'threadId':args['session_id'],'includeTurns':False})['thread']
 if thread.get('id')!=args['session_id']:raise ValueError('session identity mismatch')
 params={'threadId':args['session_id'],'limit':30}
 if args.get('queue_cursor'):params['cursor']=args['queue_cursor']
 page=rpc.call('thread/queue/list',params);data=page.get('data',[])
 queue=args.get('queue_id');turn_id=args.get('turn_id')
 matches=[x for x in data if (queue or request) and (not queue or x.get('id')==queue) and (not request or x.get('clientUserMessageId')==request)]
 out={'session':summary(thread),'state':'unknown','queued':None,'queue_count':len(data),
      'queue_next_cursor':page.get('nextCursor'),'result_tool':'codex_session_read','trust':'UNTRUSTED',
      'request_id':request,'observation_scope':'submission and linked turns; not workflow DONE or writer authorization'}
 if len(matches)>1:raise ValueError('ambiguous submission identity')
 if matches:
  out.update(state='queued',queued=True,queue_id=matches[0]['id']);return out
 if queue or request:out['queued']=None if page.get('nextCursor') or args.get('queue_cursor') else False
 if not request and not turn_id:return out
 # Summary keeps userMessage.clientId; full items of a long session exceed the 2 MiB frame.
 params={'threadId':args['session_id'],'limit':10,'itemsView':'summary','sortDirection':'desc'}
 latest_page=rpc.call('thread/turns/list',params)
 history=latest_page
 if args.get('turn_cursor'):
  params['cursor']=args['turn_cursor'];history=rpc.call('thread/turns/list',params)
 turns=latest_page.get('data',[])
 known={t.get('id') for t in turns}
 turns=turns+[t for t in history.get('data',[]) if t.get('id') not in known]
 out['turn_next_cursor']=history.get('nextCursor')
 matched=[t for t in turns if (not request and t.get('id')==turn_id) or
          (request and any(i.get('type')=='userMessage' and i.get('clientId')==request for i in t.get('items',[])))]
 if len(matched)>1:raise ValueError('ambiguous turn identity')
 if not matched:return out
 root=matched[0].get('rootTurnId') or matched[0]['id']
 if turn_id and not any(t.get('id')==turn_id and (t.get('id')==matched[0]['id'] or t.get('rootTurnId')==root) for t in turns):return out
 linked=[t for t in latest_page.get('data',[]) if t.get('id')==matched[0]['id'] or t.get('rootTurnId')==root]
 if not linked:return out
 latest=linked[0];state={'inProgress':'running','completed':'completed','failed':'failed','interrupted':'blocked'}.get(latest.get('status'),'unknown')
 flags=thread.get('status',{}).get('activeFlags',[])
 if state=='running' and any(x in flags for x in ('waitingOnApproval','waitingOnUserInput')):state='blocked'
 out.update(state=state,turn_id=latest['id'],root_turn_id=root,turn_status=latest.get('status'))
 return out

def task_cancel(rpc,args):
 if set(args)-{'session_id','queue_id'} or not identifier(args.get('session_id')) or not identifier(args.get('queue_id')):raise ValueError('only queued submissions can be cancelled')
 result=rpc.call('thread/queue/delete',{'threadId':args['session_id'],'queuedSubmissionId':args['queue_id']})
 return {'deleted':result['deleted'],'session_id':args['session_id'],'queue_id':args['queue_id']}

def failure_class(e):
 # A fixed class name only: messages may echo session content or paths.
 name=type(e).__name__
 return name if re.fullmatch('[A-Za-z][A-Za-z0-9_]{0,63}',name) else 'Exception'

def main():
 if len(sys.argv)!=2:raise ValueError('operation required')
 action=sys.argv[1];functions={'sessions_list':sessions_list,'session_read':session_read,'task_submit':task_submit,'task_status':task_status,'task_cancel':task_cancel}
 if action not in functions:raise ValueError('unsupported operation')
 raw=sys.stdin.buffer.read(MAX_INPUT+1)
 if len(raw)>MAX_INPUT:raise ValueError('request budget')
 args=json.loads(raw)
 if not isinstance(args,dict):raise ValueError('object required')
 with RPC() as rpc:out=functions[action](rpc,args)
 encoded=json.dumps(out,ensure_ascii=False)
 if len(encoded.encode())>MAX_OUTPUT:raise ValueError('response budget')
 print(encoded)
if __name__=='__main__':
 try:main()
 except Exception as e:print(json.dumps({'error':'Codex bridge unavailable or request rejected; probe before retry','reason':failure_class(e)}));sys.exit(1)
