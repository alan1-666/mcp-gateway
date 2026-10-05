"""GPG round-trip + injected Docker/SSH fixtures; no production/network access."""
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import tarfile
import tempfile
import unittest

spec=importlib.util.spec_from_file_location("backup",Path(__file__).resolve().parents[1]/"cloud-backup.py")
backup=importlib.util.module_from_spec(spec);spec.loader.exec_module(backup)

@unittest.skipUnless(shutil.which("gpg"),"GnuPG is required for encryption fixtures")
class BackupFixture(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.base=Path(self.temp.name)
        private=self.base/"secrets";private.mkdir();(private/"master-key").write_bytes(b"v"*32);(private/"runner-token").write_bytes(b"fixture-private-runner")
        (self.base/"cloud.env").write_text(f"RELEASE_ID=fixture.1\nGATEWAY_SECRETS_DIR={private}\nPOSTGRES_PASSWORD=fixture-private-db\n")
        self.key=self.base/"backup-key";self.key.write_text("b"*64);self.key.chmod(0o600)
        self.calls=[]
    def tearDown(self):self.temp.cleanup()
    def compose(self,*args,stdout=None,stdin=None,**_):
        self.calls.append(args)
        if "pg_dump" in args:stdout.write(b"PGDMP-fixture-data")
        else:self.assertEqual(args[-1],"--list");self.assertEqual(stdin.read(),b"PGDMP-fixture-data")
    def create(self):return backup.create_backup(self.base,compose=self.compose)
    def test_encrypted_snapshot_contains_recovery_material_and_no_plaintext_survives(self):
        path=self.create();raw=path.read_bytes()
        for secret in [b"fixture-private",b"PGDMP",b"v"*32]:self.assertNotIn(secret,raw)
        with tempfile.TemporaryDirectory() as d:
            plain,record=backup.decrypt_validate(path,self.key,Path(d))
            self.assertIn("secrets/master-key",record["files"])
            with tarfile.open(plain) as archive:self.assertEqual(archive.extractfile("secrets/master-key").read(),b"v"*32)
        self.assertFalse(list((self.base/"backups").glob("*.dump")))
        self.assertFalse(list((self.base/"backups").glob("*.pending")))
        self.assertEqual(json.loads((self.base/"backups/last-local.json").read_text())["sha256"],backup.sha(path))
    def test_failure_keeps_previous_snapshot_and_tampering_is_rejected(self):
        path=self.create();before=path.read_bytes()
        def fail(*args,**kwargs):raise backup.BackupError("fixture dump failure")
        with self.assertRaises(backup.BackupError):backup.create_backup(self.base,compose=fail)
        self.assertEqual(path.read_bytes(),before)
        damaged=self.base/"damaged.gpg";raw=bytearray(before);raw[-12]^=1;damaged.write_bytes(raw)
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(backup.BackupError):backup.decrypt_validate(damaged,self.key,Path(d))
        self.assertFalse(list((self.base/"backups").glob("*.pending")))
    def test_key_separation_permissions_and_missing_vault_are_required(self):
        self.key.chmod(0o644)
        with self.assertRaisesRegex(backup.BackupError,"0600"):self.create()
        self.key.chmod(0o600);(self.base/"secrets/master-key").unlink()
        with self.assertRaisesRegex(backup.BackupError,"32-byte"):self.create()
    def config(self):
        identity=self.base/"identity";identity.write_text("fixture");known=self.base/"known_hosts";known.write_text("fixture")
        config=self.base/"offhost.json";config.write_text(json.dumps({"host":"backup@example.net","directory":"/srv/backups","identity_file":str(identity),"known_hosts_file":str(known)}));return config
    def test_offhost_hash_failure_keeps_prior_receipt_then_verified_transfer_updates(self):
        path=self.create();config=self.config();receipt=self.base/"backups/last-offhost.json";receipt.write_text('{"prior":true}')
        def bad(args,**kwargs):return b"wrong /srv/file"
        with self.assertRaises(backup.BackupError):backup.transfer_offhost(path,config,runner=bad)
        self.assertEqual(json.loads(receipt.read_text()),{"prior":True});self.assertTrue(path.exists())
        def good(args,stdin=None,**kwargs):
            self.assertIn("StrictHostKeyChecking=yes",args);self.assertIn(".partial",args[-1]);self.assertIn("sha256sum -c",args[-1]);self.assertEqual(stdin.read(),path.read_bytes());return (backup.sha(path)+"  snapshot\n").encode()
        result=backup.transfer_offhost(path,config,runner=good);self.assertEqual(result["sha256"],backup.sha(path))
    def test_isolated_restore_never_mounts_production_and_cleanup_runs_on_failure(self):
        path=self.create();calls=[]
        def docker(args,stdin=None,**kwargs):
            calls.append(args)
            if "pg_restore" in args:self.assertEqual(stdin.read(),b"PGDMP-fixture-data")
            if "psql" in args:return b"10\n5\n"
            return b"fixture"
        receipt=backup.restore_verify(path,self.key,runner=docker)
        self.assertEqual(receipt["migrations"],10);self.assertTrue(receipt["vault_key_present"])
        first=calls[0];self.assertIn("none",first);self.assertNotIn("-v",first);self.assertNotIn("-p",first)
        self.assertEqual(calls[-1][:3],["docker","rm","--force"])
        self.assertIn("pg_isready -h 127.0.0.1",calls[1][-1])
        calls=[]
        def fail(args,**kwargs):
            calls.append(args)
            if "pg_restore" in args:raise backup.BackupError("restore failed")
            return b""
        with self.assertRaises(backup.BackupError):backup.restore_verify(path,self.key,runner=fail)
        self.assertEqual(calls[-1][:3],["docker","rm","--force"])
    def test_schedule_requires_destination_before_dump_and_rejects_ssh_injection(self):
        self.assertEqual(backup.main(["create","--base",str(self.base),"--scheduled"]),1)
        self.assertFalse((self.base/"backups").exists())
        path=self.create();config=self.config();value=json.loads(config.read_text());value["directory"]="/srv/$(touch bad)";config.write_text(json.dumps(value))
        with self.assertRaises(backup.BackupError):backup.transfer_offhost(path,config,runner=lambda *_:self.fail("must not invoke SSH"))
if __name__=="__main__":unittest.main()
