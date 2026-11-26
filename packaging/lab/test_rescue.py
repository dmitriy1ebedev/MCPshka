"""Three isolated process-death cases, no production unit/network mutations."""
import contextlib,importlib.util,multiprocessing,pathlib,tempfile,types,unittest,time
SOURCE=pathlib.Path(__file__).parents[1]/'native/rescue.py'
class RescueTests(unittest.TestCase):
 def module(self):
  source=SOURCE.read_text().replace('/usr/local/libexec/mcpshka-state-store.py',str(SOURCE.with_name('state-store.py').resolve()))
  m=types.ModuleType('rescue');exec(compile(source,str(SOURCE),'exec'),m.__dict__)
  @contextlib.contextmanager
  def lock(path):yield
  m.lock=lock;m.active_jobs=lambda:0;m.main_start_safe=lambda:None
  return m
 def test_three_process_deaths_recover_without_other_units(self):
  for component in ('tunnel','main','tunnel'):
   with self.subTest(component=component):
    m=self.module();processes={};calls=[]
    def spawn(key):
     p=multiprocessing.Process(target=time.sleep,args=(20,));p.start();processes[key]=p
    spawn('main');spawn('tunnel')
    def snapshot():
     live={key:p.is_alive() for key,p in processes.items()}
     return {'main':'active' if live['main'] else 'failed','tunnel':'active' if live['tunnel'] else 'failed','health':live['main'],'ready':live['tunnel'],'verified_independent_oob':0}
    def run(args,timeout=8):
     calls.append(args);self.assertEqual(args[0],'/usr/bin/systemctl');self.assertIn(args[-1],(m.MAIN,m.TUNNEL))
     key='main' if args[-1]==m.MAIN else 'tunnel'
     if args[1]=='restart' and processes[key].is_alive():processes[key].terminate();processes[key].join(2)
     if not processes[key].is_alive():spawn(key)
     return 0,b''
    m.snapshot=snapshot;m.run=run
    try:
     processes[component].terminate();processes[component].join(2)
     result=m.recover('restart' if component=='main' else 'reconnect')
     self.assertEqual(result['state'],'verified');self.assertTrue(result['after']['ready'])
     self.assertEqual(result['after']['verified_independent_oob'],0)
    finally:
     for p in processes.values():p.terminate();p.join(2)
 def test_active_job_prevents_restart(self):
  m=self.module();m.snapshot=lambda:{'health':True,'ready':True,'tunnel':'active'};m.active_jobs=lambda:1
  m.run=lambda *args: self.fail('restart reached systemd with active jobs')
  with self.assertRaises(ValueError):m.recover('restart')
 def test_unknown_modes_do_not_execute(self):
  m=self.module();m.run=lambda *args:self.fail('unknown mode executed')
  for mode in ('restart; rm -rf /','service-other','worker'):
   with self.assertRaises(ValueError):m.recover(mode)
 def test_deployment_lock_prevents_reconnect(self):
  m=self.module();m.snapshot=lambda:{'health':True,'ready':False,'tunnel':'failed'}
  @contextlib.contextmanager
  def lock(path):raise BlockingIOError();yield
  m.lock=lock;m.run=lambda *args:self.fail('interfered with guarded deploy')
  self.assertEqual(m.watch()['state'],'guarded_operation_in_progress')
 def test_packaged_sandbox_preserves_bounded_capture(self):
  folder=SOURCE.parents[1]/'systemd'
  main=(folder/'mcpshka.service').read_text()
  self.assertIn('AF_NETLINK',main)
  self.assertIn('CapabilityBoundingSet=\n',main)
  self.assertIn('AmbientCapabilities=\n',main)
  self.assertIn('NoNewPrivileges=true',main)
  self.assertNotIn('AF_PACKET',main)
  broker=(folder/'mcpshka-broker.service').read_text()
  self.assertIn('RestrictAddressFamilies=AF_UNIX AF_NETLINK AF_PACKET',broker)
  self.assertIn('CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW',broker)
  self.assertIn('AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW',broker)
  self.assertIn('NoNewPrivileges=true',broker)
  self.assertNotIn('CAP_SYS_ADMIN',broker)
 def test_rollback_takes_the_coordinated_pair(self):
  m=self.module();calls=[]
  import pathlib as pl
  real_is_file=pl.Path.is_file
  def run(args,timeout=8):
   calls.append(args)
   if args[-1]=='version':return 0,b'mcpshka 0.2.1\n'
   if '--rollback' in args:return 0,b'{"state":"ROLLED_BACK","version":"0.1.197"}\n'
   raise AssertionError(args)
  m.run=run;m.snapshot=lambda:{'health':True}
  pl.Path.is_file=lambda self:self.name=='mcpshka-coordinated-release-0.2.1.json' or real_is_file(self)
  try:out=m.recover('rollback')
  finally:pl.Path.is_file=real_is_file
  self.assertEqual(out['state'],'rolled_back');self.assertTrue(out['paired'])
  self.assertIn(['/usr/local/libexec/mcpshka-coordinated-upgrade','--version','0.2.1','--rollback'],calls)

if __name__=='__main__':unittest.main()
