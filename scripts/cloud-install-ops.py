#!/usr/bin/env python3
"""Install reviewed operations scripts independently from application releases.

install --source <release> --base /opt/mcp-gateway installs ONLY the five listed
scripts, keeps immutable checksum manifests under ops-releases, and atomically
switches the ops symlink. Existing managed bundles are retained for restore.
An existing plain ops directory is accepted only when it contains the four
backup/monitor scripts (plus optional cloud-release.py/manifest); it is retained
as a legacy bundle. No secrets, environment files, systemd files or other config
are copied. No services/timers are enabled or restarted. Install the reviewed
systemd units separately; their ExecStart points to /opt/mcp-gateway/ops.

Use list, then restore --version <printed-version> to revert operations code.
Restoration validates the complete bundle and never restores a database or
changes application current/cloud.env. Application rollback does not roll back
these operations scripts. For legacy directory conversion, an interruption
between directory rename and symlink creation can leave ops absent; the printed
version manifests under ops-releases remain recoverable with restore.
"""
from __future__ import annotations
import argparse
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile

FILES = {
    "cloud-release.py": "scripts/cloud-release.py",
    "cloud-backup.py": "scripts/cloud-backup.py",
    "monitor.py": "deploy/cloud/monitor.py",
    "backup.sh": "deploy/cloud/backup.sh",
    "monitor.sh": "deploy/cloud/monitor.sh",
}
REQUIRED = set(FILES) - {"cloud-release.py"}
MANIFEST = "ops-manifest.json"
VERSION = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,99}\Z")
HASH = re.compile(r"[a-f0-9]{64}\Z")
MAX_SCRIPT = 1024 * 1024


class OpsError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise OpsError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def read_scripts(directory, mapping):
    files = {}
    for name, relative in mapping.items():
        path = directory / relative
        require(path.is_file() and not path.is_symlink() and directory.resolve() in path.resolve().parents,
                "Operations source is missing or symlinked")
        require(0 < path.stat().st_size <= MAX_SCRIPT, "Invalid operations script size")
        files[name] = path.read_bytes()
    return files


def manifest(version, files):
    return {"format": 1, "version": version, "files": {name: digest(data) for name, data in sorted(files.items())}}


def validate_bundle(path):
    require(path.is_dir() and not path.is_symlink(), "Bundle must be a real directory")
    record = json.loads((path / MANIFEST).read_text())
    require(record.get("format") == 1 and record.get("version") == path.name and VERSION.fullmatch(path.name), "Invalid operations bundle manifest")
    hashes = record.get("files")
    require(isinstance(hashes, dict) and REQUIRED <= set(hashes) <= set(FILES), "Invalid operations script whitelist")
    require({entry.name for entry in path.iterdir()} == set(hashes) | {MANIFEST}, "Bundle contains unknown files; no configuration or secrets may be installed")
    files = read_scripts(path, {name: name for name in hashes})
    require(all(isinstance(checksum, str) and HASH.fullmatch(checksum) and digest(files[name]) == checksum for name, checksum in hashes.items()), "Operations bundle checksum mismatch")
    return record


def current_bundle(base):
    active = base / "ops"
    if active.is_symlink():
        target = active.resolve()
        require(target.parent == base / "ops-releases", "ops points outside managed bundles")
        return validate_bundle(target)
    if active.exists():
        require(active.is_dir(), "Existing ops is not a directory")
        names = {entry.name for entry in active.iterdir()}
        require(REQUIRED <= names - {MANIFEST} <= set(FILES), "Existing ops contains unknown files; preserve/remove configuration separately")
        files = read_scripts(active, {name: name for name in names - {MANIFEST}})
        return {"legacy_files": files}
    return None


def write_manifest(path, record):
    path.write_text(json.dumps(record, sort_keys=True, indent=2) + "\n")
    path.chmod(0o644)


def version_id(files, suffix=""):
    combined = digest(json.dumps({key: digest(value) for key, value in sorted(files.items())}, sort_keys=True).encode())[:12]
    return datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ") + "-" + combined + suffix


def preserve_legacy(base, existing):
    if not existing or "legacy_files" not in existing:
        return existing["version"] if existing else None
    version = version_id(existing["legacy_files"], "-legacy")
    active, target = base / "ops", base / "ops-releases" / version
    write_manifest(active / MANIFEST, manifest(version, existing["legacy_files"]))
    # The original bytes are retained by rename, never copied from unknown files.
    os.replace(active, target)
    return version


def select(base, version):
    target = base / "ops-releases" / version
    validate_bundle(target)
    staged = base / ".ops-next"
    require(not staged.exists() and not staged.is_symlink(), "Stale .ops-next exists; inspect/remove only that link before retrying")
    try:
        staged.symlink_to(target)
        os.replace(staged, base / "ops")
    finally:
        if staged.is_symlink():
            staged.unlink()


class Locked:
    def __init__(self, base):
        self.base = base.resolve()
        self.stream = None
    def __enter__(self):
        self.base.mkdir(parents=True, exist_ok=True)
        self.stream = (self.base / ".ops-install.lock").open("a+b")
        try:
            fcntl.flock(self.stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            self.stream.close()
            raise OpsError("Another operations installation is in progress") from error
        return self.base
    def __exit__(self, *_):
        self.stream.close()


def install(base, source):
    source = source.resolve()
    files = read_scripts(source, FILES)
    # If installed from a release artifact, ensure the files match its manifest.
    release = source / "release.json"
    if release.exists():
        hashes = json.loads(release.read_text()).get("files", {})
        require(all(hashes.get(relative) == digest(files[name]) for name, relative in FILES.items()), "Operations scripts differ from the release artifact")
    with Locked(base) as base:
        existing = current_bundle(base)
        hashes = {name: digest(data) for name, data in sorted(files.items())}
        if existing and existing.get("files") == hashes:
            return {"status": "unchanged", "version": existing["version"], "previous_version": existing["version"]}
        releases = base / "ops-releases"
        releases.mkdir(mode=0o755, exist_ok=True)
        require(not releases.is_symlink(), "ops-releases must not be a symlink")
        version = version_id(files)
        staging = Path(tempfile.mkdtemp(prefix=".ops-stage-", dir=releases))
        try:
            staging.chmod(0o755)
            for name, data in files.items():
                (staging / name).write_bytes(data)
                (staging / name).chmod(0o755)
            write_manifest(staging / MANIFEST, manifest(version, files))
            os.replace(staging, releases / version)
        finally:
            if staging.exists():
                shutil.rmtree(staging)
        previous = preserve_legacy(base, existing)
        select(base, version)
        return {"status": "installed", "version": version, "previous_version": previous}


def restore(base, version):
    require(VERSION.fullmatch(version), "Invalid operations version")
    with Locked(base) as base:
        require((base / "ops-releases").is_dir() and not (base / "ops-releases").is_symlink(), "Managed bundles are missing")
        validate_bundle(base / "ops-releases" / version)
        active = base / "ops"
        if active.is_symlink():
            # A verified retained target can repair a damaged current bundle;
            # preserve that directory untouched for inspection.
            prior = active.resolve()
            require(prior.parent == base / "ops-releases", "ops points outside managed bundles")
            previous = prior.name
        else:
            previous = preserve_legacy(base, current_bundle(base))
        select(base, version)
        return {"status": "restored", "version": version, "previous_version": previous}


def list_versions(base):
    base = base.resolve()
    active = current_bundle(base)
    versions = []
    directory = base / "ops-releases"
    if directory.exists():
        require(directory.is_dir() and not directory.is_symlink(), "Managed bundle directory is invalid")
        for path in sorted(directory.iterdir()):
            if path.name.startswith("."):
                continue
            record = validate_bundle(path)
            versions.append({"version": record["version"], "current": bool(active and active.get("version") == record["version"])})
    return {"items": versions}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = parser.add_subparsers(dest="command", required=True)
    create = commands.add_parser("install", help="Install reviewed scripts, preserve prior bundle; never enable units")
    create.add_argument("--source", type=Path, required=True)
    restore_parser = commands.add_parser("restore", help="Switch ops to a retained verified bundle; never changes application/DB")
    restore_parser.add_argument("--version", required=True)
    commands.add_parser("list", help="List retained bundle versions")
    for command in commands.choices.values():
        command.add_argument("--base", type=Path, default=Path("/opt/mcp-gateway"))
    args = parser.parse_args(argv)
    try:
        if args.command == "install":
            result = install(args.base, args.source)
        elif args.command == "restore":
            result = restore(args.base, args.version)
        else:
            result = list_versions(args.base)
        print(json.dumps(result, sort_keys=True))
        return 0
    except (OpsError, OSError, ValueError, KeyError) as error:
        print("Operations installation failed: " + (str(error) if isinstance(error, OpsError) else type(error).__name__), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
