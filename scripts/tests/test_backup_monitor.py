import importlib.util
from pathlib import Path
import unittest
spec=importlib.util.spec_from_file_location("monitor",Path(__file__).resolve().parents[2]/"deploy/cloud/monitor.py")
monitor=importlib.util.module_from_spec(spec);spec.loader.exec_module(monitor)
class MonitorFixture(unittest.TestCase):
    def sample(self,count=0,state="SUCCEEDED",duration=0,rejected=0):return {"calls":[{"transport":"mcp","state":state,"count":count,"duration_ms_total":duration}],"rejections":[{"scope":"workspace","reason":"rate","count":rejected}],"observed_at":"2026-01-01T00:00:00Z"}
    def test_counter_deltas_detect_incident_and_ignore_historical_totals(self):
        receipt={"verified_at":"2026-01-01T00:00:00Z"};now=monitor.timestamp(receipt["verified_at"])
        result=monitor.evaluate(self.sample(100),self.sample(100),monitor.DEFAULT_RULES,receipt,receipt,now)
        self.assertEqual(result["alerts"],[])
        result=monitor.evaluate(self.sample(25,"UNKNOWN",300000,21),self.sample(),monitor.DEFAULT_RULES,receipt,receipt,now)
        self.assertEqual({a["code"] for a in result["alerts"]},{"operation_unknown","failure_ratio","mean_duration","capacity_rejections"})
    def test_missing_stale_backups_and_warmup_are_visible(self):
        result=monitor.evaluate(self.sample(),None,monitor.DEFAULT_RULES,now=monitor.timestamp("2026-01-02T00:00:00Z"))
        self.assertTrue(result["warming_up"]);self.assertEqual({a["code"] for a in result["alerts"]},{"metrics_stale","offhost_backup_missing","restore_drill_missing"})
    def test_invalid_dimensions_and_counters_fail_closed(self):
        sample=self.sample();sample["calls"][0]["state"]="arbitrary-upstream-error"
        with self.assertRaises(ValueError):monitor.counters(sample)
        with self.assertRaises(ValueError):monitor.counters(self.sample(-1))

class DeliveryFixture(unittest.TestCase):
    def setUp(self):
        import tempfile
        self.temp = tempfile.TemporaryDirectory()
        self.base = Path(self.temp.name)
        self.state = self.base / 'delivery.json'
        self.config = {'kind':'generic','url':'https://example.invalid/private-hook','cooldown_seconds':3600,'destination_hash':'fixture-destination'}
        self.sent = []
    def tearDown(self): self.temp.cleanup()
    def sender(self, url, body):
        self.sent.append((url,body))
        return b'{"code":0,"errcode":0}'
    def incident(self, code='monitor_failed'):
        return {'status':'alert','alerts':[{'code':code,'detail':'Fixed fixture detail.'}]}
    def test_no_configuration_only_reports_local_and_never_sends_or_marks_sent(self):
        result=monitor.deliver(self.incident(),None,self.state,sender=self.sender)
        self.assertEqual(result,{'status':'local_only','sent':False})
        self.assertEqual(self.sent,[]);self.assertFalse(self.state.exists())
    def test_first_change_cooldown_reminder_and_one_recovery(self):
        self.assertTrue(monitor.deliver(self.incident(),self.config,self.state,now=1000,sender=self.sender)['sent'])
        self.assertFalse(monitor.deliver(self.incident(),self.config,self.state,now=1100,sender=self.sender)['sent'])
        self.assertTrue(monitor.deliver(self.incident('unknown_overdue'),self.config,self.state,now=1200,sender=self.sender)['sent'])
        self.assertTrue(monitor.deliver(self.incident('unknown_overdue'),self.config,self.state,now=4800,sender=self.sender)['sent'])
        healthy={'status':'ok','alerts':[]}
        self.assertEqual(monitor.deliver(healthy,self.config,self.state,now=4900,sender=self.sender)['event'],'resolved')
        self.assertFalse(monitor.deliver(healthy,self.config,self.state,now=5000,sender=self.sender)['sent'])
        self.assertEqual(len(self.sent),4)
        self.assertEqual(self.sent[-1][1]['status'],'resolved')
        self.assertEqual(self.sent[-1][1]['alerts'],[])
    def test_transport_and_application_failures_never_mark_sent_and_retry_succeeds(self):
        def fail(*_): raise TimeoutError('fixture transport failed')
        with self.assertRaises(TimeoutError): monitor.deliver(self.incident(),self.config,self.state,now=1000,sender=fail)
        self.assertFalse(self.state.exists())
        monitor.deliver(self.incident(),self.config,self.state,now=1001,sender=self.sender)
        previous=self.state.read_bytes()
        with self.assertRaises(TimeoutError):monitor.deliver(self.incident('unknown_overdue'),self.config,self.state,now=1002,sender=fail)
        self.assertEqual(self.state.read_bytes(),previous)
        config={**self.config,'kind':'feishu'}
        with self.assertRaises(ValueError):monitor.deliver(self.incident('unknown_overdue'),config,self.state,now=1003,sender=lambda *_:b'{"code":19001}')
        self.assertEqual(self.state.read_bytes(),previous)
    def test_provider_bodies_are_fixed_plain_text_and_acknowledged(self):
        for kind in ['generic','feishu','wecom']:
            state=self.base/(kind+'.json')
            result=monitor.deliver(self.incident(),{**self.config,'kind':kind},state,now=1000,sender=self.sender)
            self.assertTrue(result['sent'])
        self.assertEqual(set(self.sent[1][1]),{'msg_type','content'})
        self.assertEqual(set(self.sent[2][1]),{'msgtype','text'})
        self.assertNotIn('mentioned_list',self.sent[2][1]['text'])
    def test_secret_file_https_validation_and_no_redirect(self):
        import json
        secret=self.base/'url';secret.write_text('https://example.invalid/hook?key=fixture-private');secret.chmod(0o600)
        config=self.base/'config.json';config.write_text(json.dumps({'kind':'wecom','url_file':str(secret),'cooldown_seconds':3600}))
        parsed=monitor.delivery_config(config);self.assertIn('destination_hash',parsed)
        secret.chmod(0o644)
        with self.assertRaises(ValueError):monitor.delivery_config(config)
        secret.chmod(0o600);secret.write_text('http://example.invalid/hook')
        with self.assertRaises(ValueError):monitor.delivery_config(config)
        with self.assertRaises(ValueError):monitor.NoRedirect().redirect_request(None,None,None,None,None,None)
    def test_post_uses_bounded_timeout_and_checks_http_status(self):
        class Response:
            status=200
            def __enter__(self): return self
            def __exit__(self,*_):pass
            def read(self,limit):
                if limit!=16385: raise AssertionError('response must be bounded')
                return b'{}'
        class Opener:
            def open(inner,request,timeout):
                self.assertEqual(timeout,10);self.assertEqual(request.method,'POST')
                self.assertEqual(request.get_header('Content-type'),'application/json')
                return Response()
        self.assertEqual(monitor.post_webhook(self.config['url'],{'fixed':'fixture'},opener=Opener()),b'{}')
        Response.status=500
        with self.assertRaises(ValueError):monitor.post_webhook(self.config['url'],{},opener=Opener())
    def test_local_collector_uses_fixed_readonly_query_and_environment_isolation(self):
        import json, subprocess
        (self.base/'cloud.env').write_text('RELEASE_ID=fixture\nPOSTGRES_PASSWORD=fixture-private\n')
        sample={'calls':[],'rejections':[],'active_leases':0,'unknown_count':1,'unknown_oldest_age_seconds':600,'observed_at':'2026-01-01T00:00:00Z'}
        def runner(args,**kwargs):
            self.assertIn('workspace=team',args)
            self.assertEqual(kwargs['input'],monitor.COLLECT_SQL.encode())
            self.assertIn('BEGIN READ ONLY;',monitor.COLLECT_SQL)
            self.assertIn('statement_timeout',monitor.COLLECT_SQL)
            self.assertNotIn('POSTGRES_PASSWORD',kwargs['env'])
            self.assertEqual(kwargs['timeout'],15)
            return subprocess.CompletedProcess(args,0,json.dumps(sample).encode(),b'')
        self.assertEqual(monitor.collect_local(self.base,'team',runner=runner),sample)
        result=monitor.evaluate(sample,None,monitor.DEFAULT_RULES,now=monitor.timestamp(sample['observed_at']))
        self.assertIn('unknown_overdue',{v['code'] for v in result['alerts']})
        with self.assertRaises(ValueError):monitor.collect_local(self.base,"team'; DROP TABLE tools;--",runner=runner)
    def test_local_backup_default_and_offhost_are_explicit_shell_modes(self):
        import os, subprocess
        source=Path(__file__).resolve().parents[2]/'deploy/cloud/backup.sh'
        stub=self.base/'ops/cloud-backup.py';stub.parent.mkdir(parents=True);stub.write_text('import json,sys;print(json.dumps(sys.argv[1:]))')
        script=self.base/'ops/backup.sh';script.write_text(source.read_text())
        env={k:v for k,v in os.environ.items() if not k.startswith('GATEWAY_BACKUP_')};env['GATEWAY_BASE']=str(self.base)
        result=subprocess.run(['sh',str(script)],env=env,check=True,capture_output=True,text=True)
        self.assertNotIn('--scheduled',result.stdout);self.assertNotIn('--offhost-config',result.stdout)
        env['GATEWAY_BACKUP_OFFHOST_CONFIG']=str(self.base/'offhost.json')
        result=subprocess.run(['sh',str(script)],env=env,check=True,capture_output=True,text=True)
        self.assertIn('--scheduled',result.stdout);self.assertIn('--offhost-config',result.stdout)

if __name__ == "__main__":
    unittest.main()
