"""Operations code installation fixtures: real local files, no service/network calls."""
import importlib.util
import json
import shutil
import subprocess
import sys
from pathlib import Path
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('install_ops',Path(__file__).resolve().parents[1]/'cloud-install-ops.py')
ops=importlib.util.module_from_spec(spec);spec.loader.exec_module(ops)

class InstallOpsFixture(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.root=Path(self.temp.name).resolve();self.base=self.root/'host';self.base.mkdir();self.source=self.root/'source';self.source.mkdir()
        for name,relative in ops.FILES.items():
            file=self.source/relative;file.parent.mkdir(parents=True,exist_ok=True);file.write_text('# reviewed '+name+'\n')
        (self.source/'secrets').mkdir();(self.source/'secrets/private').write_text('must never be copied')
        (self.base/'cloud.env').write_text('RELEASE_ID=cloud.7\nPRIVATE=unchanged\n');(self.base/'secrets').mkdir();(self.base/'secrets/master-key').write_bytes(b'm'*32)
    def tearDown(self):self.temp.cleanup()
    def test_install_repeat_and_application_rollback_do_not_modify_config_or_ops(self):
        first=ops.install(self.base,self.source);target=(self.base/'ops').resolve()
        self.assertTrue((self.base/'ops').is_symlink());self.assertEqual(target.parent,self.base/'ops-releases')
        self.assertEqual({p.name for p in target.iterdir()},set(ops.FILES)|{ops.MANIFEST})
        self.assertEqual((self.base/'cloud.env').read_text(),'RELEASE_ID=cloud.7\nPRIVATE=unchanged\n')
        self.assertEqual((self.base/'secrets/master-key').read_bytes(),b'm'*32)
        second=ops.install(self.base,self.source);self.assertEqual(second['status'],'unchanged');self.assertEqual(first['version'],second['version'])
        for release in ['cloud.8','cloud.7']:
            directory=self.base/'releases'/release;directory.mkdir(parents=True)
            current=self.base/'current';current.unlink(missing_ok=True);current.symlink_to(directory)
            self.assertEqual((self.base/'ops').resolve(),target)
    def test_upgrade_keeps_previous_and_explicit_restore_checks_integrity(self):
        first=ops.install(self.base,self.source)
        (self.source/ops.FILES['monitor.py']).write_text('# second monitor\n')
        second=ops.install(self.base,self.source);self.assertEqual(second['previous_version'],first['version'])
        self.assertEqual(len(ops.list_versions(self.base)['items']),2)
        ops.restore(self.base,first['version']);self.assertEqual((self.base/'ops').resolve().name,first['version'])
        damaged=self.base/'ops-releases'/second['version'];(damaged/'monitor.py').write_text('# modified outside installer\n')
        with self.assertRaises(ops.OpsError):ops.restore(self.base,second['version'])
        self.assertEqual((self.base/'ops').resolve().name,first['version'])
    def test_valid_prior_bundle_can_repair_damaged_active_without_erasing_it(self):
        first=ops.install(self.base,self.source);(self.source/ops.FILES['monitor.py']).write_text('# changed\n');second=ops.install(self.base,self.source)
        current=(self.base/'ops').resolve();(current/'monitor.py').write_text('# corruption\n')
        ops.restore(self.base,first['version']);self.assertEqual((self.base/'ops').resolve().name,first['version']);self.assertEqual((current/'monitor.py').read_text(),'# corruption\n')
    def test_plain_legacy_ops_is_preserved_as_a_restorable_bundle(self):
        legacy=self.base/'ops';legacy.mkdir()
        for name in ops.REQUIRED:(legacy/name).write_text('# legacy '+name+'\n')
        result=ops.install(self.base,self.source);previous=result['previous_version'];self.assertTrue(previous.endswith('-legacy'))
        ops.restore(self.base,previous)
        for name in ops.REQUIRED:self.assertEqual((self.base/'ops'/name).read_text(),'# legacy '+name+'\n')
        self.assertFalse((self.base/'ops/cloud-release.py').exists())
    def test_secret_like_unknown_files_and_symlink_sources_are_never_copied(self):
        legacy=self.base/'ops';legacy.mkdir()
        for name in ops.REQUIRED:(legacy/name).write_text('# legacy\n')
        (legacy/'secret.env').write_text('fixture-private')
        with self.assertRaises(ops.OpsError):ops.install(self.base,self.source)
        self.assertFalse((self.base/'ops-releases').exists());self.assertEqual((legacy/'secret.env').read_text(),'fixture-private')
        (legacy/'secret.env').unlink();source=self.source/ops.FILES['monitor.py'];source.unlink();source.symlink_to(self.base/'cloud.env')
        with self.assertRaises(ops.OpsError):ops.install(self.base,self.source)
    def test_release_manifest_mismatch_and_parallel_install_fail_before_activation(self):
        (self.source/'release.json').write_text(json.dumps({'files':{}}))
        with self.assertRaises(ops.OpsError):ops.install(self.base,self.source)
        (self.source/'release.json').unlink()
        with ops.Locked(self.base):
            with self.assertRaises(ops.OpsError):ops.install(self.base,self.source)
        self.assertFalse((self.base/'ops').exists())
    @unittest.skipUnless(shutil.which("gpg"), "GnuPG required for installed backup fixture")
    def test_installed_release_imports_backup_without_mutating_immutable_bundle(self):
        repository = Path(__file__).resolve().parents[2]
        installed = ops.install(self.base, repository)
        key = self.base / "backup-key"
        key.write_text("b" * 64)
        key.chmod(0o600)
        code = """
import pathlib,runpy,sys,types
base=pathlib.Path(sys.argv[1])
namespace=runpy.run_path(str(base/'ops/cloud-release.py'),run_name='ops_fixture')
def compose(directory,*args,**kwargs):
    if 'pg_dump' in args: kwargs['stdout'].write(b'PGDMP-import-fixture')
    elif 'pg_restore' in args: assert kwargs['stdin'].read()==b'PGDMP-import-fixture'
    else: raise AssertionError('unexpected fixture command')
host=types.SimpleNamespace(base=base,previous=base/'current',compose=compose)
namespace['Host'].backup(host,'import-fixture')
"""
        subprocess.run([sys.executable, '-c', code, str(self.base)], check=True, capture_output=True)
        bundle = (self.base / 'ops').resolve()
        self.assertFalse((bundle / '__pycache__').exists())
        self.assertEqual(ops.validate_bundle(bundle)['version'], installed['version'])
        self.assertEqual(ops.install(self.base, repository)['status'], 'unchanged')
        ops.restore(self.base, installed['version'])
        self.assertTrue(list((self.base/'backups').glob('*.tar.gpg')))

    def test_restore_can_repair_an_interrupted_legacy_conversion_with_no_active_link(self):
        first=ops.install(self.base,self.source);(self.base/'ops').unlink()
        ops.restore(self.base,first['version']);self.assertEqual((self.base/'ops').resolve().name,first['version'])

if __name__=='__main__':unittest.main()
