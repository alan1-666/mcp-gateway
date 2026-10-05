#!/usr/bin/env python3
"""Package committed sources and operate explicit, checked single-host releases.

This upgrades an already bootstrapped cloud host. It never deletes volumes,
rewrites secrets, restores a database, or silently rolls back a failed migration.
Run --help, and each subcommand's --help, before using it on a host.
"""
from __future__ import annotations

import argparse
import fcntl
import gzip
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import tarfile
import tempfile
from datetime import datetime, timezone
import urllib.request


MANIFEST = "release.json"
MAX_ARCHIVE_BYTES = 100 * 1024 * 1024
SERVICES = ["api", "gateway", "worker", "pi-runner", "console"]
IMAGES = ["mcp-gateway-backend", "mcp-gateway-console", "mcp-gateway-pi-runner"]
NAME = re.compile(r"[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}\Z")
HASH = re.compile(r"[a-f0-9]{64}\Z")
COMMIT = re.compile(r"[a-f0-9]{40}\Z")


class ReleaseError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise ReleaseError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def json_bytes(value):
    return (json.dumps(value, sort_keys=True, indent=2) + "\n").encode()


def safe_path(name):
    path = PurePosixPath(name)
    require(not path.is_absolute() and name and ".." not in path.parts and str(path) == name,
            "Artifact contains an unsafe path")
    return path


def private_path(name):
    return any(part in {".git", ".local", ".gateway", ".gateway-worker", "node_modules", "dist", "bin", "coverage", "output", ".playwright-cli", "secrets", "__pycache__", ".DS_Store"}
               or part == ".env" or part.startswith(".env.") and part != ".env.example"
               for part in PurePosixPath(name).parts)


def atomic_write(path, data, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".release-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            os.fchmod(stream.fileno(), mode)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


class Runner:
    def run(self, args, *, env=None, input=None, stdin=None, stdout=None, timeout=900):
        # Commands may inherit deployment credentials. Never render argv,
        # environment, or captured stderr in an exception.
        result = subprocess.run(args, env=env, input=input, stdin=stdin, stdout=stdout or subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=timeout, check=False)
        require(result.returncode == 0, f"Command failed: {args[0]} (exit {result.returncode}); inspect operator logs")
        return result.stdout or b""


def validate_manifest(manifest):
    require(isinstance(manifest, dict), "Release manifest must be a JSON object")
    require(manifest.get("format") == 1, "Unsupported release manifest format")
    require(isinstance(manifest.get("release_id"), str) and NAME.fullmatch(manifest["release_id"]), "Invalid release ID")
    require(isinstance(manifest.get("commit"), str) and COMMIT.fullmatch(manifest["commit"]), "Manifest requires a full Git commit SHA")
    files = manifest.get("files")
    require(isinstance(files, dict) and files, "Manifest has no source file hashes")
    for name, checksum in files.items():
        safe_path(name)
        require(name != MANIFEST and not private_path(name) and isinstance(checksum, str) and HASH.fullmatch(checksum),
                "Manifest has an invalid source entry")
    require("deploy/compose/cloud.yaml" in files, "Cloud Compose configuration is missing")
    migrations = manifest.get("migrations")
    require(isinstance(migrations, dict) and migrations, "Manifest has no migration hashes")
    for name, checksum in migrations.items():
        require(files.get(f"migrations/{name}") == checksum and name.endswith(".sql") and "/" not in name,
                "Migration hashes do not match packaged source")
    actual = {PurePosixPath(name).name: checksum for name, checksum in files.items()
              if PurePosixPath(name).parent == PurePosixPath("migrations") and name.endswith(".sql")}
    require(actual == migrations, "Migration manifest is incomplete")
    return manifest


def package(source, commit, release_id, output, runner=None):
    runner = runner or Runner()
    require(NAME.fullmatch(release_id), "Invalid release ID")
    prefix = ["git", "-C", str(source)]
    resolved = runner.run(prefix + ["rev-parse", "--verify", "--end-of-options", f"{commit}^{{commit}}"], timeout=30).decode().strip()
    require(COMMIT.fullmatch(resolved), "Unable to resolve a complete Git commit")
    raw = runner.run(prefix + ["archive", "--format=tar", resolved], timeout=60)
    contents, modes = {}, {}
    with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
        for entry in archive:
            if entry.isdir():
                continue
            require(entry.isfile(), "Release packages do not support source symlinks or special files")
            safe_path(entry.name)
            require(not private_path(entry.name) and entry.name != MANIFEST,
                    "A tracked private/generated path cannot be included in a release")
            require(entry.name not in contents, "Duplicate archive entry")
            contents[entry.name] = archive.extractfile(entry).read()
            modes[entry.name] = 0o755 if entry.mode & 0o111 else 0o644
    require(sum(map(len, contents.values())) <= MAX_ARCHIVE_BYTES, "Release sources exceed 100 MiB")
    files = {name: digest(data) for name, data in contents.items()}
    manifest = validate_manifest({
        "format": 1, "release_id": release_id, "commit": resolved, "files": files,
        "migrations": {PurePosixPath(name).name: checksum for name, checksum in files.items()
                       if PurePosixPath(name).parent == PurePosixPath("migrations") and name.endswith(".sql")},
    })
    contents[MANIFEST], modes[MANIFEST] = json_bytes(manifest), 0o644
    buffer = io.BytesIO()
    with gzip.GzipFile(fileobj=buffer, mode="wb", filename="", mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as archive:
            for name in sorted(contents):
                info = tarfile.TarInfo(name)
                info.mode, info.size, info.mtime = modes[name], len(contents[name]), 0
                archive.addfile(info, io.BytesIO(contents[name]))
    atomic_write(output, buffer.getvalue())
    return manifest, digest(buffer.getvalue())


def read_artifact(path):
    require(path.stat().st_size <= MAX_ARCHIVE_BYTES, "Compressed artifact exceeds 100 MiB")
    contents, modes, total = {}, {}, 0
    with tarfile.open(path, mode="r:gz") as archive:
        for entry in archive:
            safe_path(entry.name)
            require(entry.isfile() and entry.name not in contents, "Artifact has duplicate or non-file entries")
            total += entry.size
            require(total <= MAX_ARCHIVE_BYTES, "Expanded artifact exceeds 100 MiB")
            contents[entry.name] = archive.extractfile(entry).read()
            modes[entry.name] = 0o755 if entry.mode & 0o111 else 0o644
    require(MANIFEST in contents, "Artifact lacks release.json")
    manifest = validate_manifest(json.loads(contents.pop(MANIFEST)))
    require(set(contents) == set(manifest["files"]), "Artifact files differ from manifest")
    for name, data in contents.items():
        require(digest(data) == manifest["files"][name], "Artifact source checksum mismatch")
    return manifest, contents, modes


def verify_directory(directory, manifest):
    validate_manifest(manifest)
    for name, checksum in manifest["files"].items():
        path = directory / name
        require(path.is_file() and not path.is_symlink() and directory.resolve() in path.resolve().parents,
                "Release source is missing or contains a symlink")
        require(digest(path.read_bytes()) == checksum, f"Release file differs from manifest: {name}")
    actual = {str(path.relative_to(directory)) for path in directory.rglob("*") if path.is_file()
              and str(path.relative_to(directory)) != MANIFEST and not private_path(str(path.relative_to(directory)))}
    require(actual == set(manifest["files"]), "Release contains unrecorded source files")


def compatibility(path, manifest):
    if path is None:
        return {}
    record = json.loads(path.read_text())
    require(isinstance(record, dict), "Compatibility declaration must be a JSON object")
    require(record.get("release_id") == manifest["release_id"] and record.get("commit") == manifest["commit"],
            "Compatibility declaration must name the exact target release and commit")
    require(isinstance(record.get("reason"), str) and record["reason"].strip(), "Compatibility declaration requires a review reason")
    allowed = record.get("allowed_migrations")
    require(isinstance(allowed, dict), "Compatibility declaration requires explicit allowed_migrations hashes")
    for name, checksum in allowed.items():
        require(isinstance(name, str) and name.endswith(".sql") and "/" not in name and isinstance(checksum, str) and HASH.fullmatch(checksum),
                "Compatibility declaration contains invalid migration checksums")
        require(name not in manifest["migrations"], "Compatibility declarations cannot replace a target migration")
    return allowed


def check_schema(actual, manifest, allowed, require_complete):
    require(isinstance(actual, dict), "Database returned invalid migration records")
    accepted = {**manifest["migrations"], **allowed}
    for name, checksum in actual.items():
        require(accepted.get(name) == checksum,
                f"Database migration {name} is not checksum-compatible with target release; explicit reviewed compatibility is required")
    if require_complete:
        require(all(actual.get(name) == checksum for name, checksum in manifest["migrations"].items()),
                "Database is missing target release migrations")


def public_health(origin):
    require(origin.startswith("https://") and "/" not in origin[8:] and "?" not in origin and "#" not in origin,
            "PUBLIC_ORIGIN must be an HTTPS origin")
    with urllib.request.urlopen(origin + "/api/v1/auth/status", timeout=20) as response:
        require(response.status == 200 and json.loads(response.read(8192)).get("mode") == "cloud",
                "Public HTTPS cloud authentication health check failed")


class Host:
    def __init__(self, base, runner=None, health=public_health):
        self.base, self.runner, self.health = base.resolve(), runner or Runner(), health
        self.env_path = self.base / "cloud.env"
        require(self.env_path.is_file() and not self.env_path.is_symlink(), "Bootstrap cloud.env before using release tooling")
        self.env_text = self.env_path.read_text()
        self.values = {}
        for line in self.env_text.splitlines():
            if line and not line.lstrip().startswith("#") and "=" in line:
                key, value = line.split("=", 1)
                require(key not in self.values, "cloud.env contains duplicate keys")
                self.values[key] = value.strip().strip('"').strip("'")
        require(NAME.fullmatch(self.values.get("RELEASE_ID", "")), "cloud.env has no valid RELEASE_ID")
        self.current = self.base / "current"
        require(self.current.is_symlink(), "current must point to an existing release directory")
        self.previous = self.current.resolve()
        require(self.previous.parent == self.base / "releases" and self.previous.name == self.values["RELEASE_ID"],
                "current, release directory and cloud.env RELEASE_ID disagree; repair explicitly before continuing")
        self.lock = None

    def __enter__(self):
        self.lock = open(self.base / ".release.lock", "a+b")
        try:
            fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            self.lock.close()
            self.lock = None
            raise ReleaseError("Another release operation is in progress") from error
        # Detect changes between construction and acquiring the lock.
        if self.env_path.read_text() != self.env_text or self.current.resolve() != self.previous:
            self.lock.close()
            self.lock = None
            raise ReleaseError("Release state changed while acquiring the lock")
        return self

    def __exit__(self, *_):
        if self.lock:
            self.lock.close()

    def compose(self, directory, *args, input=None, stdin=None, stdout=None, timeout=900):
        env = os.environ.copy()
        # Only explicit cloud.env owns secret/configuration values. An operator's
        # unrelated shell variable must not silently override deployment config.
        for key in self.values:
            env.pop(key, None)
        env["RELEASE_ID"] = directory.name
        return self.runner.run(["docker", "compose", "--project-name", "mcp-gateway-cloud", "--env-file", str(self.env_path),
                                "-f", str(directory / "deploy/compose/cloud.yaml"), *args],
                               env=env, input=input, stdin=stdin, stdout=stdout, timeout=timeout)

    def schema(self):
        sql = "SELECT COALESCE(json_object_agg(name,checksum),'{}'::json) FROM schema_migrations"
        return json.loads(self.compose(self.previous, "exec", "-T", "postgres", "psql", "-XAt", "-U", "gateway", "-d", "gateway", "-c", sql, timeout=30))

    def image_ids(self, release_id):
        result = {}
        for name in IMAGES:
            image = f"{name}:{release_id}"
            image_id = self.runner.run(["docker", "image", "inspect", "--format", "{{.Id}}", image], timeout=30).decode().strip()
            require(re.fullmatch(r"sha256:[a-f0-9]{64}", image_id), "Docker returned an invalid image ID")
            result[image] = image_id
        return result

    def backup(self, release_id):
        directory = self.base / "backups"
        directory.mkdir(mode=0o700, exist_ok=True)
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
        target = directory / f"pre-release-{release_id}-{stamp}.dump"
        temporary = target.with_suffix(".tmp")
        try:
            with open(temporary, "xb") as stream:
                os.chmod(temporary, 0o600)
                self.compose(self.previous, "exec", "-T", "postgres", "pg_dump", "-U", "gateway", "-d", "gateway", "-Fc", stdout=stream)
            require(temporary.stat().st_size > 0, "Database backup is empty")
            with open(temporary, "rb") as stream:
                # pg_restore --list validates a dump catalog; it never restores.
                self.compose(self.previous, "exec", "-T", "postgres", "pg_restore", "--list", stdin=stream)
            os.replace(temporary, target)
        finally:
            temporary.unlink(missing_ok=True)
        return str(target)

    def check_health(self, directory):
        output = self.compose(directory, "ps", "--all", "--format", "json", timeout=30).decode().strip()
        rows = json.loads(output) if output.startswith("[") else [json.loads(line) for line in output.splitlines() if line]
        by_service = {row.get("Service"): row for row in rows}
        images = self.image_ids(directory.name)
        for name in ["postgres", *SERVICES]:
            require(by_service.get(name, {}).get("State") == "running", f"Service {name} is not running")
            if name in {"postgres", "api", "gateway", "console"}:
                require(by_service[name].get("Health") == "healthy", f"Service {name} is not healthy")
            if name in SERVICES:
                image = f"mcp-gateway-{'console' if name == 'console' else 'pi-runner' if name == 'pi-runner' else 'backend'}:{directory.name}"
                row = by_service[name]
                require(row.get("Image") == image and re.fullmatch(r"[a-f0-9]{12,64}", row.get("ID", "")),
                        f"Service {name} is running a different release")
                running = self.runner.run(["docker", "container", "inspect", "--format", "{{.Image}}", row["ID"]], timeout=30).decode().strip()
                require(running == images[image], f"Service {name} image differs from the recorded tag")
        self.health(self.values.get("PUBLIC_ORIGIN", ""))

    def state_path(self, release_id):
        return self.base / "release-state" / f"{release_id}.json"

    def finalize(self, directory, state):
        temporary = self.base / ".current-next"
        require(not temporary.exists() and not temporary.is_symlink(), "Stale current transition exists; inspect before replacing")
        atomic_write(self.state_path(directory.name), json_bytes(state))
        # Neither file contains a source-controlled credential. Preserve every
        # cloud.env line except the one RELEASE_ID assignment.
        updated = "\n".join(f"RELEASE_ID={directory.name}" if line.startswith("RELEASE_ID=") else line
                            for line in self.env_text.splitlines()) + "\n"
        atomic_write(self.env_path, updated.encode())
        temporary.symlink_to(directory)
        os.replace(temporary, self.current)
        atomic_write(self.base / "last-release.json", json_bytes(state))
        (self.base / "release-transition.json").unlink(missing_ok=True)


def install_artifact(host, manifest, contents, modes):
    directory = host.base / "releases" / manifest["release_id"]
    if directory.exists():
        verify_directory(directory, manifest)
        existing = directory / MANIFEST
        require(existing.is_file() and json.loads(existing.read_text()) == manifest, "Existing release lacks the exact artifact manifest; compare/adopt it explicitly")
        return directory
    directory.parent.mkdir(parents=True, exist_ok=True)
    staging = Path(tempfile.mkdtemp(prefix=".release-stage-", dir=directory.parent))
    try:
        for name, data in contents.items():
            path = staging / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
            path.chmod(modes[name])
        (staging / MANIFEST).write_bytes(json_bytes(manifest))
        os.replace(staging, directory)
    finally:
        if staging.exists():
            import shutil
            shutil.rmtree(staging)
    return directory


def operate(host, directory, manifest, allowed, *, rollback=False, wait=180):
    verify_directory(directory, manifest)
    before = host.schema()
    check_schema(before, manifest, allowed, require_complete=rollback)
    state_path = host.state_path(directory.name)
    prior_state = json.loads(state_path.read_text()) if state_path.exists() else None
    previous_manifest_path = host.previous / MANIFEST
    previous_state_path = host.state_path(host.previous.name)
    require(previous_manifest_path.is_file() and previous_state_path.is_file(),
            "Record the current legacy release with adopt --record before replacing services")
    previous_manifest = validate_manifest(json.loads(previous_manifest_path.read_text()))
    previous_state = json.loads(previous_state_path.read_text())
    require(previous_state.get("commit") == previous_manifest["commit"] and host.image_ids(host.previous.name) == previous_state.get("images"),
            "Current release image provenance changed; repair explicitly before deployment")
    if rollback or directory == host.previous:
        require(prior_state and prior_state.get("commit") == manifest["commit"], "Target release has no recorded image provenance; compare/adopt first")
        require(host.image_ids(directory.name) == prior_state.get("images"), "Image tags differ from the recorded release; rollback refused")
    if directory == host.previous:
        try:
            host.check_health(directory)
            return "Release is already current and healthy; no mutations performed"
        except (ReleaseError, OSError):
            if not rollback:
                raise
    if not rollback:
        require(not prior_state, "Release ID has already been used; use rollback for a retained release or package a new ID")
        host.compose(directory, "build", "api", "console", "pi-runner")
    images = host.image_ids(directory.name)
    transition = {"target_release": directory.name, "target_commit": manifest["commit"],
                  "previous_release": host.previous.name, "database_before": before, "phase": "stopping"}
    atomic_write(host.base / "release-transition.json", json_bytes(transition))
    # Stop admission and workers before backup/migration. Existing calls receive
    # the Compose grace period. PostgreSQL, credentials and Pi volumes remain.
    host.compose(host.previous, "stop", "--timeout", "140", *SERVICES, timeout=180)
    backup = host.backup(directory.name)
    transition.update(phase="backed_up", backup=backup)
    atomic_write(host.base / "release-transition.json", json_bytes(transition))
    if not rollback:
        host.compose(directory, "run", "--rm", "--no-deps", "migrate")
    check_schema(host.schema(), manifest, allowed, require_complete=True)
    host.compose(directory, "up", "-d", "--no-build", "--no-deps", "--wait", "--wait-timeout", str(wait), *SERVICES, timeout=wait + 30)
    host.check_health(directory)
    require(host.image_ids(directory.name) == images, "Image tags changed during release")
    state = {"release_id": directory.name, "commit": manifest["commit"], "images": images,
             "previous_release": host.previous.name, "backup": backup,
             "database_migrations": host.schema(), "allowed_migrations": allowed,
             "action": "rollback" if rollback else "deploy", "verified_at": datetime.now(timezone.utc).isoformat()}
    host.finalize(directory, state)
    return f"{state['action']} verified: {directory.name} at {manifest['commit']}"


def adopt(host, manifest, allowed, record):
    directory = host.base / "releases" / manifest["release_id"]
    require(directory == host.previous, "Legacy adoption must describe the current release")
    verify_directory(directory, manifest)
    actual = host.schema()
    check_schema(actual, manifest, allowed, require_complete=True)
    images = host.image_ids(directory.name)
    host.check_health(directory)
    require(host.current.resolve() == host.previous and host.env_path.read_text() == host.env_text,
            "Current release changed during legacy comparison; compare again")
    if record:
        require(not (directory / MANIFEST).exists() and not host.state_path(directory.name).exists(), "Legacy release is already recorded")
        atomic_write(directory / MANIFEST, json_bytes(manifest), 0o644)
        atomic_write(host.state_path(directory.name), json_bytes({"release_id": directory.name, "commit": manifest["commit"],
                     "images": images, "database_migrations": actual, "allowed_migrations": allowed,
                     "action": "adopt", "verified_at": datetime.now(timezone.utc).isoformat()}))
    return "Legacy source, schema and health matched; " + ("manifest and current image IDs recorded" if record else "read-only comparison complete (use --record to adopt)")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="Failures never restore the DB or erase volumes. After a stopped/failed deployment, inspect the actual schema and use an explicit compatible rollback. Retain artifacts and images. A manifest binds source hashes; it is not an image signature or a guarantee of business health.")
    sub = parser.add_subparsers(dest="command", required=True)
    pack = sub.add_parser("package", help="Create a deterministic archive from a Git commit, excluding all working-tree changes")
    pack.add_argument("--source", type=Path, default=Path("."))
    pack.add_argument("--commit", required=True, help="Reviewed commit/ref; resolved and stored as its full SHA")
    pack.add_argument("--release", required=True)
    pack.add_argument("--output", type=Path, required=True)
    for command in ["deploy", "rollback", "adopt"]:
        p = sub.add_parser(command, help={"deploy": "Build and deploy a packaged release to an existing host", "rollback": "Switch to recorded images after checking actual DB migrations; never run reverse migrations", "adopt": "Compare a legacy current directory against a committed artifact; read-only by default"}[command])
        p.add_argument("--base", type=Path, default=Path("/opt/mcp-gateway"))
        p.add_argument("--compatibility", type=Path, help='Reviewed JSON: {"release_id":"target","commit":"full SHA","reason":"review evidence","allowed_migrations":{"006_example.sql":"sha256"}}. Only exact additional DB migration hashes are permitted.')
        if command == "rollback":
            p.add_argument("--release", required=True)
        else:
            p.add_argument("--artifact", type=Path, required=True)
        if command == "adopt":
            p.add_argument("--record", action="store_true", help="Write the compared source manifest and observed image IDs. Does not attest how legacy images were built.")
        else:
            p.add_argument("--wait", type=int, default=180, help="Compose health wait in seconds (10–900)")
    args = parser.parse_args(argv)
    try:
        if args.command == "package":
            manifest, checksum = package(args.source, args.commit, args.release, args.output)
            print(f"Packaged committed source {manifest['commit']} as {manifest['release_id']}; artifact SHA256 {checksum}")
            return 0
        if args.command == "adopt" and not args.record:
            manifest, _, _ = read_artifact(args.artifact)
            print(adopt(Host(args.base), manifest, compatibility(args.compatibility, manifest), False))
            return 0
        with Host(args.base) as host:
            if args.command == "rollback":
                require(NAME.fullmatch(args.release), "Invalid release ID")
                directory = host.base / "releases" / args.release
                manifest = validate_manifest(json.loads((directory / MANIFEST).read_text()))
                require(manifest["release_id"] == args.release, "Target directory and manifest release ID disagree")
            else:
                manifest, contents, modes = read_artifact(args.artifact)
            allowed = compatibility(args.compatibility, manifest)
            if args.command == "adopt":
                print(adopt(host, manifest, allowed, args.record))
            else:
                require(10 <= args.wait <= 900, "Health wait must be 10–900 seconds")
                if args.command == "deploy":
                    directory = install_artifact(host, manifest, contents, modes)
                print(operate(host, directory, manifest, allowed, rollback=args.command == "rollback", wait=args.wait))
        return 0
    except (ReleaseError, OSError, ValueError, tarfile.TarError, subprocess.TimeoutExpired) as error:
        # Exception text from OS/network may include a path, never secret file
        # contents or subprocess environment. Subprocess stderr is suppressed.
        print(f"Release stopped: {error}. No database restore was attempted.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
