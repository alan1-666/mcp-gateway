#!/usr/bin/env python3
"""Encrypted single-host recovery snapshots and an isolated restore drill.

Requires Python 3, GnuPG 2, Docker Compose, and OpenSSH for offhost copies.
init-key creates an independent private backup passphrase; escrow it separately.
create saves pg_dump + cloud.env + secrets (including raw vault master-key) in
one GPG AES-256/MDC encrypted tar, validates decryption and every entry hash,
and never prunes previous snapshots. Temp plaintext exists only in a private
0700 directory for the duration of this command. Place --temp-dir on tmpfs to
avoid plaintext temporary data on persistent media. Release backups use this
same implementation. Restores never replace production or overwrite secrets.

Scheduled mode requires an explicit offhost JSON configuration and verifies the
remote SHA-256 before updating last-offhost.json. No destination is assumed.
Example config fields: {"host":"backup@example.net","directory":"/srv/gateway-backups",
"identity_file":"/root/.ssh/gateway_backup","known_hosts_file":"/root/.ssh/known_hosts"}.
Configure a restricted SSH account, provision host keys, and protect that JSON.
"""
from __future__ import annotations
import argparse
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import secrets
import shutil
import subprocess
import sys
import tarfile
import tempfile
from datetime import datetime, timezone

class BackupError(Exception): pass

def require(ok, message):
    if not ok: raise BackupError(message)

def run(args, **kw):
    result = subprocess.run(args, stderr=subprocess.PIPE, stdout=kw.pop("stdout", subprocess.PIPE), check=False, timeout=kw.pop("timeout", 900), **kw)
    require(result.returncode == 0, f"{Path(args[0]).name} failed (exit {result.returncode}); previous snapshots are preserved")
    return result.stdout or b""

def sha(path):
    h = hashlib.sha256()
    with Path(path).open("rb") as stream:
        while chunk := stream.read(1024 * 1024): h.update(chunk)
    return h.hexdigest()

def atomic_json(path, value):
    path = Path(path)
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".backup-", delete=False) as stream:
        temporary = Path(stream.name)
        os.fchmod(stream.fileno(), 0o600)
        stream.write((json.dumps(value, sort_keys=True, indent=2)+"\n").encode()); stream.flush(); os.fsync(stream.fileno())
    try: os.replace(temporary, path)
    finally: temporary.unlink(missing_ok=True)

def read_env(base):
    env = {}
    for line in (base / "cloud.env").read_text().splitlines():
        if line and not line.startswith("#"):
            key, separator, value = line.partition("=")
            require(separator and re.fullmatch(r"[A-Z][A-Z0-9_]*", key), "cloud.env must use plain KEY=value syntax")
            env[key] = value
    return env

def compose_for(base):
    values = read_env(base)
    env = {k:v for k,v in os.environ.items() if k not in values}
    def compose(*args, **kw):
        return run(["docker", "compose", "--project-name", "mcp-gateway-cloud", "--env-file", str(base / "cloud.env"), "-f", str(base / "current/deploy/compose/cloud.yaml"), *args], env=env, **kw)
    return compose

def validate_key(key, private):
    require(key.is_file() and not key.is_symlink(), "Backup key must be a regular private file; run init-key first")
    require(key.stat().st_mode & 0o077 == 0, "Backup key must have permissions 0600")
    secret = key.read_bytes()
    require(re.fullmatch(rb"[a-zA-Z0-9_-]{43,128}\n?", secret) is not None, "Backup key must contain at least 43 ASCII passphrase characters")
    require(key.resolve() != private.resolve() and private.resolve() not in key.resolve().parents, "Backup key must be separate from the backed-up secret directory")
    require(secret.rstrip(b"\n") != (private / "master-key").read_bytes(), "Backup key must differ from the vault master key")

def gpg_args(key, home):
    return ["gpg", "--no-options", "--homedir", str(home), "--batch", "--yes", "--no-tty", "--pinentry-mode", "loopback", "--no-symkey-cache", "--passphrase-file", str(key)]

def validate_archive(archive_path):
    with tarfile.open(archive_path, "r:") as archive:
        members = archive.getmembers()
        require(len(members) <= 70 and len({m.name for m in members}) == len(members), "Invalid backup member set")
        for m in members:
            p = PurePosixPath(m.name)
            require(m.isfile() and not p.is_absolute() and ".." not in p.parts and str(p) == m.name, "Unsafe backup member")
        names = {m.name for m in members}
        require({"backup.json", "database.dump", "cloud.env", "secrets/master-key"} <= names, "Backup lacks required recovery material")
        record = json.load(archive.extractfile("backup.json"))
        require(record.get("format") == 1 and isinstance(record.get("files"), dict), "Invalid backup manifest")
        require(set(record["files"]) == names - {"backup.json"}, "Incomplete backup manifest")
        for name, checksum in record["files"].items():
            require(name in {"database.dump", "cloud.env"} or re.fullmatch(r"secrets/[A-Za-z0-9._-]+", name), "Unexpected backup member")
            stream = archive.extractfile(name); h = hashlib.sha256()
            while chunk := stream.read(1024 * 1024): h.update(chunk)
            require(h.hexdigest() == checksum, "Backup member checksum mismatch")
        require(archive.getmember("secrets/master-key").size == 32, "Backup vault master key must be 32 raw bytes")
        return record

def decrypt_validate(snapshot, key, directory):
    home = directory / "gpg"; home.mkdir(mode=0o700, exist_ok=True)
    plain = directory / "restore.tar"
    run(gpg_args(key, home) + ["--output", str(plain), "--decrypt", str(snapshot)])
    return plain, validate_archive(plain)

def create_backup(base, label="scheduled", *, key=None, temp_dir=None, compose=None):
    base = Path(base).resolve(); key = Path(key or base / "backup-key")
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,99}", label), "Invalid backup label")
    values = read_env(base); private = Path(values.get("GATEWAY_SECRETS_DIR", str(base / "secrets")))
    require(private.is_dir() and not private.is_symlink(), "Secret directory missing or symlinked")
    master = private / "master-key"
    require(master.is_file() and not master.is_symlink() and master.stat().st_size == 32, "A raw 32-byte vault master-key is required for a recoverable snapshot")
    validate_key(key, private)
    directory = base / "backups"; directory.mkdir(mode=0o700, exist_ok=True); os.chmod(directory, 0o700)
    target = directory / ("gateway-"+label+"-"+datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")+".tar.gpg")
    compose = compose or compose_for(base)
    with (directory / ".lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with tempfile.TemporaryDirectory(prefix="gateway-backup-", dir=temp_dir) as temporary:
            work = Path(temporary); os.chmod(work, 0o700)
            dump = work / "database.dump"
            with dump.open("xb") as stream:
                os.fchmod(stream.fileno(), 0o600)
                compose("exec", "-T", "postgres", "pg_dump", "-U", "gateway", "-d", "gateway", "-Fc", stdout=stream)
            require(dump.stat().st_size > 0, "Database dump is empty")
            with dump.open("rb") as stream: compose("exec", "-T", "postgres", "pg_restore", "--list", stdin=stream)
            files = {"database.dump":dump, "cloud.env":base / "cloud.env"}
            entries = list(private.iterdir()); require(len(entries) <= 64, "Too many secret files")
            for entry in entries:
                require(entry.is_file() and not entry.is_symlink() and re.fullmatch(r"[A-Za-z0-9._-]+", entry.name) and entry.stat().st_size <= 1024*1024, "Unsupported secret directory entry")
                files["secrets/"+entry.name] = entry
            record = {"format":1, "created_at":datetime.now(timezone.utc).isoformat(), "release_id":values.get("RELEASE_ID"), "files":{name:sha(path) for name,path in files.items()}}
            manifest = json.dumps(record, sort_keys=True).encode()
            home = work / "gpg"; home.mkdir(mode=0o700)
            pending = target.with_suffix(".pending")
            try:
                with pending.open("xb") as output:
                    os.fchmod(output.fileno(), 0o600)
                    process = subprocess.Popen(gpg_args(key, home) + ["--symmetric", "--cipher-algo", "AES256", "--force-mdc", "--compress-algo", "none", "--s2k-mode", "3", "--s2k-digest-algo", "SHA256", "--s2k-count", "65011712"], stdin=subprocess.PIPE, stdout=output, stderr=subprocess.PIPE)
                    try:
                        with tarfile.open(fileobj=process.stdin, mode="w|") as archive:
                            item=tarfile.TarInfo("backup.json");item.size=len(manifest);item.mode=0o600;archive.addfile(item,io.BytesIO(manifest))
                            for name,path in files.items(): archive.add(path, arcname=name, recursive=False)
                        process.stdin.close(); process.stderr.read(); process.stderr.close(); code=process.wait(timeout=900)
                        require(code==0, "GPG encryption failed; previous snapshots preserved")
                    finally:
                        if process.poll() is None: process.kill(); process.wait()
                        if not process.stdin.closed: process.stdin.close()
                        if not process.stderr.closed: process.stderr.close()
                    output.flush(); os.fsync(output.fileno())
                plain, checked = decrypt_validate(pending,key,work)
                require(checked==record, "Encrypted backup round-trip failed")
                os.replace(pending,target)
                atomic_json(target.with_suffix(target.suffix+".json"), {"sha256":sha(target),"created_at":record["created_at"],"release_id":record["release_id"],"encryption":"GPG AES256 + MDC","restore_verified":False})
                atomic_json(directory / "last-local.json", {"file":target.name,"sha256":sha(target),"created_at":record["created_at"]})
            finally: pending.unlink(missing_ok=True)
    return target

def transfer_offhost(snapshot, config, *, runner=run):
    snapshot=Path(snapshot);config=json.loads(Path(config).read_text())
    require(set(config)=={"host","directory","identity_file","known_hosts_file"}, "Offhost config requires host, directory, identity_file and known_hosts_file")
    host,directory=config["host"],config["directory"]
    require(isinstance(host,str) and re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]*@[A-Za-z0-9][A-Za-z0-9.-]*",host), "Invalid offhost SSH host")
    require(isinstance(directory,str) and re.fullmatch(r"/[A-Za-z0-9_./-]+",directory) and ".." not in PurePosixPath(directory).parts, "Offhost directory must be an absolute shell-safe path")
    for name in ["identity_file","known_hosts_file"]: require(Path(config[name]).is_file(), "SSH identity and pinned known_hosts files must exist")
    require(re.fullmatch(r"[A-Za-z0-9._-]+",snapshot.name), "Invalid snapshot filename")
    checksum=sha(snapshot);remote=directory.rstrip("/")+"/"+snapshot.name
    # No shell expansion is accepted in config; host key checking cannot be disabled.
    ssh=["ssh","-o","BatchMode=yes","-o","StrictHostKeyChecking=yes","-o","IdentitiesOnly=yes","-o","UserKnownHostsFile="+config["known_hosts_file"],"-i",config["identity_file"],host]
    command=f"umask 077; test -d '{directory}' && test ! -e '{remote}' && cat > '{remote}.partial' && echo '{checksum}  {remote}.partial' | sha256sum -c - >/dev/null && mv '{remote}.partial' '{remote}' && sha256sum '{remote}'"
    with snapshot.open("rb") as stream: result=runner(ssh+[command],stdin=stream,timeout=900).decode().split()
    require(result and result[0]==checksum, "Offhost checksum verification failed; local and previous snapshots preserved")
    receipt={"file":snapshot.name,"sha256":checksum,"verified_at":datetime.now(timezone.utc).isoformat(),"host":host}
    atomic_json(snapshot.parent / "last-offhost.json",receipt)
    return receipt

def restore_verify(snapshot,key,*,temp_dir=None,runner=run):
    """Use a disposable PostgreSQL 16 container without publishing ports/volumes."""
    name="mcp-backup-drill-"+secrets.token_hex(8)
    with tempfile.TemporaryDirectory(prefix="gateway-restore-",dir=temp_dir) as temporary:
        work=Path(temporary);plain,record=decrypt_validate(Path(snapshot),Path(key),work)
        dump=work / "database.dump"
        with tarfile.open(plain,"r:") as archive, dump.open("xb") as output:
            os.fchmod(output.fileno(),0o600);shutil.copyfileobj(archive.extractfile("database.dump"),output)
        started=False
        try:
            # It has no network, no host ports and no host/production volume mounts.
            runner(["docker","run","--detach","--rm","--name",name,"--network","none","--tmpfs","/var/lib/postgresql/data:rw,noexec,nosuid", "-e","POSTGRES_HOST_AUTH_METHOD=trust","-e","POSTGRES_DB=gateway_restore_check","postgres:16-alpine"]);started=True
            runner(["docker","exec",name,"sh","-ec","i=0; until pg_isready -h 127.0.0.1 -U postgres -d gateway_restore_check >/dev/null; do i=$((i+1)); [ $i -lt 60 ]; sleep 1; done"],timeout=70)
            with dump.open("rb") as stream:runner(["docker","exec","-i",name,"pg_restore","-U","postgres","--no-owner","--no-privileges","--exit-on-error","-d","gateway_restore_check"],stdin=stream)
            result=runner(["docker","exec",name,"psql","-XAt","-U","postgres","-d","gateway_restore_check","-c","SELECT count(*) FROM schema_migrations; SELECT count(*) FROM operations;"]).decode().splitlines()
            require(len(result)==2 and all(x.isdigit() for x in result) and int(result[0])>0,"Restored database schema check failed")
            return {"verified_at":datetime.now(timezone.utc).isoformat(),"snapshot_sha256":sha(snapshot),"release_id":record.get("release_id"),"migrations":int(result[0]),"operations":int(result[1]),"isolated_database":"gateway_restore_check","vault_key_present":True}
        finally:
            if started:runner(["docker","rm","--force",name],timeout=60)

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__,formatter_class=argparse.RawDescriptionHelpFormatter)
    sub=parser.add_subparsers(dest="command",required=True)
    init=sub.add_parser("init-key",help="Create a private independent backup key; never overwrites");init.add_argument("--key",type=Path,required=True)
    create=sub.add_parser("create",help="Encrypt a snapshot, optionally transfer it offhost")
    create.add_argument("--base",type=Path,default=Path("/opt/mcp-gateway"));create.add_argument("--key",type=Path);create.add_argument("--label",default="scheduled");create.add_argument("--temp-dir",type=Path);create.add_argument("--offhost-config",type=Path);create.add_argument("--scheduled",action="store_true",help="Require offhost config before creating a scheduled snapshot")
    restore=sub.add_parser("restore-verify",help="Restore into a disposable isolated Docker database; never into production");restore.add_argument("--snapshot",type=Path,required=True);restore.add_argument("--key",type=Path,required=True);restore.add_argument("--temp-dir",type=Path);restore.add_argument("--receipt",type=Path,required=True)
    args=parser.parse_args(argv)
    try:
        if args.command=="init-key":
            fd=os.open(args.key,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
            with os.fdopen(fd,"w") as stream:stream.write(secrets.token_urlsafe(48)+"\n")
            print("Backup key created. Escrow it independently before scheduling backups.")
        elif args.command=="create":
            require(not args.scheduled or args.offhost_config is not None,"Scheduled backup requires --offhost-config; schedule is not active without a destination")
            target=create_backup(args.base,args.label,key=args.key,temp_dir=args.temp_dir)
            if args.offhost_config:transfer_offhost(target,args.offhost_config)
            print(target)
        else:
            receipt=restore_verify(args.snapshot,args.key,temp_dir=args.temp_dir);atomic_json(args.receipt,receipt);print("Isolated restore verified; production database and keys were not changed.")
        return 0
    except (BackupError,OSError,ValueError,tarfile.TarError,subprocess.SubprocessError) as exc:
        # Do not render captured process stderr, file contents or credentials.
        print(f"Backup operation failed: {exc if isinstance(exc,BackupError) else type(exc).__name__}",file=sys.stderr);return 1
if __name__=="__main__":sys.exit(main())
