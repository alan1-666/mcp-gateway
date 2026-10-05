"""Local filesystem + fake Docker fixtures; never contacts a host or real database."""
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "cloud-release.py"
spec = importlib.util.spec_from_file_location("cloud_release", SCRIPT)
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


def source_files(extra=False):
    files = {"deploy/compose/cloud.yaml": b"name: mcp-gateway-cloud\n", "migrations/001_base.sql": b"SELECT 1;\n", "app.go": b"package main\n"}
    if extra:
        files["migrations/002_extra.sql"] = b"SELECT 2;\n"
    return files


def manifest(name, files, sha="a" * 40):
    hashes = {path: release.digest(data) for path, data in files.items()}
    return {"format": 1, "release_id": name, "commit": sha, "files": hashes,
            "migrations": {Path(path).name: checksum for path, checksum in hashes.items() if path.startswith("migrations/")}}


def directory(base, name, files):
    root = base / "releases" / name
    for path, data in files.items():
        target = root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    return root


class FakeGit:
    def __init__(self, files):
        self.files, self.calls = files, []

    def run(self, args, **_):
        self.calls.append(args)
        if "rev-parse" in args:
            return ("a" * 40 + "\n").encode()
        assert args[-3:] == ["archive", "--format=tar", "a" * 40]
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode="w") as archive:
            for name, data in self.files.items():
                item = tarfile.TarInfo(name)
                item.mode, item.size = 0o644, len(data)
                archive.addfile(item, io.BytesIO(data))
        return raw.getvalue()


class FakeDocker:
    def __init__(self, base, initial):
        self.base, self.schema = base, dict(initial["migrations"])
        self.running_release, self.running = initial["release_id"], True
        self.images, self.calls, self.environments = {}, [], []
        self.fail_backup, self.fail_build, self.fail_health, self.fail_migrate = False, False, False, False
        self.build(initial["release_id"])

    def build(self, name):
        for image in release.IMAGES:
            tag = f"{image}:{name}"
            self.images[tag] = "sha256:" + release.digest(tag.encode())

    def rows(self):
        rows = []
        for service in ["postgres", *release.SERVICES]:
            image = "postgres:16-alpine" if service == "postgres" else f"mcp-gateway-{'console' if service == 'console' else 'pi-runner' if service == 'pi-runner' else 'backend'}:{self.running_release}"
            running = self.running or service == "postgres"
            rows.append({"ID": release.digest(f"{service}-{self.running_release}".encode())[:12], "Service": service,
                         "Image": image, "State": "running" if running else "exited",
                         "Health": "unhealthy" if self.fail_health and service == "api" else "healthy" if service in {"postgres", "api", "gateway", "console"} and running else ""})
        return rows

    def run(self, args, *, env=None, input=None, stdin=None, stdout=None, **_):
        self.calls.append(args)
        if args[:3] == ["docker", "image", "inspect"]:
            if args[-1] not in self.images:
                raise release.ReleaseError("Fixture image is missing")
            return self.images[args[-1]].encode()
        if args[:3] == ["docker", "container", "inspect"]:
            for row in self.rows():
                if row["ID"] == args[-1]:
                    return self.images[row["Image"]].encode()
            raise AssertionError("Unknown fixture container")
        assert args[:2] == ["docker", "compose"]
        file_index = args.index("-f")
        root = Path(args[file_index + 1]).parents[2]
        command = args[file_index + 2:]
        self.environments.append(env)
        assert env["RELEASE_ID"] == root.name
        if command[0] == "build":
            if self.fail_build:
                raise release.ReleaseError("Fixture build failure")
            self.build(root.name)
        elif "psql" in command:
            return json.dumps(self.schema).encode()
        elif "pg_dump" in command:
            if self.fail_backup:
                raise release.ReleaseError("Fixture backup failure")
            stdout.write(b"PGDMP-fixture")
        elif "pg_restore" in command:
            assert command[-1] == "--list", "Database restore is forbidden"
            assert stdin.read() == b"PGDMP-fixture"
        elif command[0] == "stop":
            self.running = False
        elif command[0] == "run":
            assert command[-1] == "migrate"
            if self.fail_migrate:
                raise release.ReleaseError("Fixture migration failure")
            self.schema.update(json.loads((root / release.MANIFEST).read_text())["migrations"])
        elif command[0] == "up":
            assert "--no-deps" in command and "--no-build" in command
            self.running, self.running_release = True, root.name
        elif command[0] == "ps":
            return json.dumps(self.rows()).encode()
        else:
            raise AssertionError(command)
        return b""


class ReleaseFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.base = Path(self.temp.name).resolve()
        self.files = source_files()
        self.old = manifest("cloud.7", self.files)
        self.old_dir = directory(self.base, "cloud.7", self.files)
        (self.base / "current").symlink_to(self.old_dir)
        self.env = "RELEASE_ID=cloud.7\nPUBLIC_ORIGIN=https://example.test\nPOSTGRES_PASSWORD=fixture-private-value\nPI_MODEL=unchanged-model\n"
        (self.base / "cloud.env").write_text(self.env)
        secret = self.base / "secrets" / "credentials.json"
        secret.parent.mkdir()
        secret.write_text("fixture credentials preserved")
        (secret.parent / "master-key").write_bytes(b"m" * 32)
        (self.base / "backup-key").write_text("b" * 64)
        (self.base / "backup-key").chmod(0o600)
        self.docker = FakeDocker(self.base, self.old)

    def tearDown(self):
        self.temp.cleanup()

    def host(self):
        return release.Host(self.base, self.docker, lambda origin: self.assertEqual(origin, "https://example.test"))

    def adopt(self):
        with self.host() as host:
            release.adopt(host, self.old, {}, True)

    def new_release(self, extra=False):
        files = source_files(extra)
        files["app.go"] = b"package main // new\n"
        record = manifest("cloud.8", files, "b" * 40)
        root = release.install_artifact(self.host(), record, files, {name: 0o644 for name in files})
        return root, record

    def test_package_is_deterministic_and_pins_full_commit_and_file_hashes(self):
        first, second = self.base / "one.tar.gz", self.base / "two.tar.gz"
        git = FakeGit(self.files)
        record, checksum = release.package(self.base, "reviewed-ref", "cloud.8", first, git)
        release.package(self.base, "reviewed-ref", "cloud.8", second, git)
        self.assertEqual(first.read_bytes(), second.read_bytes())
        self.assertEqual(checksum, release.digest(first.read_bytes()))
        loaded, files, _ = release.read_artifact(first)
        self.assertEqual(loaded, record)
        self.assertEqual(files, self.files)
        self.assertEqual(record["commit"], "a" * 40)
        with self.assertRaisesRegex(release.ReleaseError, "private/generated"):
            release.package(self.base, "HEAD", "cloud.9", second, FakeGit({**self.files, ".env": b"private"}))

    def test_malformed_manifests_and_archive_paths_fail_before_host_mutation(self):
        for data in [[], {}, {**self.old, "release_id": 7}, {**self.old, "commit": None}]:
            with self.assertRaises(release.ReleaseError):
                release.validate_manifest(data)
        for path in ["../cloud.env", "/etc/passwd", "a/../../secret", "a//b"]:
            with self.assertRaises(release.ReleaseError):
                release.safe_path(path)
        self.assertEqual((self.base / "cloud.env").read_text(), self.env)

    def test_legacy_comparison_is_read_only_and_recording_is_explicit(self):
        before = sorted(str(path.relative_to(self.base)) for path in self.base.rglob("*"))
        result = release.adopt(self.host(), self.old, {}, False)
        self.assertIn("read-only", result)
        self.assertEqual(sorted(str(path.relative_to(self.base)) for path in self.base.rglob("*")), before)
        self.assertFalse((self.old_dir / release.MANIFEST).exists())
        self.adopt()
        self.assertTrue((self.old_dir / release.MANIFEST).exists())
        self.assertEqual((self.base / "cloud.env").read_text(), self.env)

    def test_legacy_revision_is_only_accepted_when_it_matches_the_artifact_commit(self):
        (self.old_dir / "REVISION").write_text(self.old["commit"] + "\n")
        release.adopt(self.host(), self.old, {}, False)
        (self.old_dir / "REVISION").write_text("f" * 40)
        with self.assertRaisesRegex(release.ReleaseError, "REVISION"):
            release.adopt(self.host(), self.old, {}, False)

    def test_deploy_binds_metadata_after_health_and_repeated_deploy_does_not_mutate(self):
        self.adopt()
        target, record = self.new_release(extra=True)
        with self.host() as host:
            release.operate(host, target, record, {})
        self.assertEqual((self.base / "current").resolve(), target)
        self.assertEqual((self.base / "cloud.env").read_text(), self.env.replace("cloud.7", "cloud.8"))
        self.assertEqual((self.base / "secrets/credentials.json").read_text(), "fixture credentials preserved")
        self.assertTrue(list((self.base / "backups").glob("*.tar.gpg")))
        self.assertFalse((self.base / "release-transition.json").exists())
        calls = len(self.docker.calls)
        with self.host() as host:
            self.assertIn("no mutations", release.operate(host, target, record, {}))
        later = self.docker.calls[calls:]
        self.assertFalse(any("build" in cmd or "stop" in cmd or "pg_dump" in cmd or "up" in cmd for cmd in later))

    def test_build_backup_migration_and_health_failures_do_not_switch_current(self):
        for fault in ["fail_build", "fail_backup", "fail_migrate", "fail_health"]:
            with self.subTest(fault=fault):
                self.adopt() if not (self.old_dir / release.MANIFEST).exists() else None
                target, record = self.new_release()
                setattr(self.docker, fault, True)
                with self.host() as host, self.assertRaises(release.ReleaseError):
                    release.operate(host, target, record, {})
                self.assertEqual((self.base / "current").resolve(), self.old_dir)
                self.assertEqual((self.base / "cloud.env").read_text(), self.env)
                self.assertFalse((self.base / "release-state/cloud.8.json").exists())
                setattr(self.docker, fault, False)
                self.docker.running, self.docker.running_release = True, "cloud.7"

    def test_metadata_crash_repair_requires_verified_health_and_does_not_restart_services(self):
        self.adopt()
        target, record = self.new_release(extra=True)
        with self.host() as host:
            release.operate(host, target, record, {})
        # Reproduce a crash after the new state/env was persisted, before the
        # pointer was replaced. The process must not guess from the directory.
        (self.base / "current").unlink()
        (self.base / "current").symlink_to(self.old_dir)
        transition = {"target_release": "cloud.8", "target_commit": record["commit"], "previous_release": "cloud.7"}
        (self.base / "release-transition.json").write_text(json.dumps(transition))
        with self.assertRaisesRegex(release.ReleaseError, "disagree"):
            self.host()
        before = len(self.docker.calls)
        self.docker.fail_health = True
        with release.Host(self.base, self.docker, lambda _: None, allow_incomplete=True) as host:
            with self.assertRaisesRegex(release.ReleaseError, "not healthy"):
                release.repair_metadata(host)
        self.assertEqual((self.base / "current").resolve(), self.old_dir)
        self.docker.fail_health = False
        with release.Host(self.base, self.docker, lambda _: None, allow_incomplete=True) as host:
            release.repair_metadata(host)
        self.assertEqual((self.base / "current").resolve(), target)
        self.assertFalse((self.base / "release-transition.json").exists())
        self.assertFalse(any("stop" in cmd or "up" in cmd or "migrate" in cmd or "pg_dump" in cmd for cmd in self.docker.calls[before:]))

    def test_failed_deployment_cannot_be_misrepresented_as_verified_metadata(self):
        self.adopt()
        target, record = self.new_release()
        self.docker.fail_health = True
        with self.host() as host, self.assertRaises(release.ReleaseError):
            release.operate(host, target, record, {})
        with self.host() as host, self.assertRaises(OSError):
            release.repair_metadata(host)
        self.assertEqual((self.base / "current").resolve(), self.old_dir)

    def test_rollback_uses_actual_database_and_requires_exact_extra_migration_approval(self):
        self.adopt()
        target, record = self.new_release(extra=True)
        with self.host() as host:
            release.operate(host, target, record, {})
        before = len(self.docker.calls)
        with self.host() as host, self.assertRaisesRegex(release.ReleaseError, "not checksum-compatible"):
            release.operate(host, self.old_dir, self.old, {}, rollback=True)
        self.assertFalse(any("stop" in cmd for cmd in self.docker.calls[before:]))
        allowed = {"002_extra.sql": record["migrations"]["002_extra.sql"]}
        with self.host() as host:
            release.operate(host, self.old_dir, self.old, allowed, rollback=True)
        self.assertEqual((self.base / "current").resolve(), self.old_dir)
        self.assertIn("002_extra.sql", self.docker.schema)
        self.assertFalse(any("migrate" in cmd for cmd in self.docker.calls[before:]))
        self.assertTrue(all(cmd[-1] == "--list" for cmd in self.docker.calls if "pg_restore" in cmd))

    def test_failed_release_can_restore_metadata_current_images_only_with_actual_schema_compatibility(self):
        self.adopt()
        target, record = self.new_release(extra=True)
        self.docker.fail_health = True
        with self.host() as host, self.assertRaises(release.ReleaseError):
            release.operate(host, target, record, {})
        self.assertEqual(self.docker.running_release, "cloud.8")
        self.assertEqual((self.base / "current").resolve(), self.old_dir)
        self.docker.fail_health = False
        allowed = {"002_extra.sql": record["migrations"]["002_extra.sql"]}
        with self.host() as host:
            release.operate(host, self.old_dir, self.old, allowed, rollback=True)
        self.assertEqual(self.docker.running_release, "cloud.7")

    def test_mutated_sources_image_tags_and_migration_checksums_are_rejected(self):
        self.adopt()
        target, record = self.new_release()
        (target / "app.go").write_text("changed source")
        with self.host() as host, self.assertRaisesRegex(release.ReleaseError, "differs from manifest"):
            release.operate(host, target, record, {})
        self.docker.images["mcp-gateway-backend:cloud.7"] = "sha256:" + "f" * 64
        with self.host() as host, self.assertRaisesRegex(release.ReleaseError, "provenance changed"):
            release.operate(host, self.old_dir, self.old, {}, rollback=True)
        with self.assertRaisesRegex(release.ReleaseError, "not checksum-compatible"):
            release.check_schema({"001_base.sql": "f" * 64}, self.old, {}, True)

    def test_compatibility_declaration_is_specific_to_target_and_hashes(self):
        path = self.base / "compatibility.json"
        allowed = {"002_extra.sql": "b" * 64}
        declaration = {"release_id": "cloud.7", "commit": self.old["commit"], "reason": "Reviewed additive table", "allowed_migrations": allowed}
        path.write_text(json.dumps(declaration))
        self.assertEqual(release.compatibility(path, self.old), allowed)
        path.write_text(json.dumps({**declaration, "commit": "c" * 40}))
        with self.assertRaisesRegex(release.ReleaseError, "exact target"):
            release.compatibility(path, self.old)

    def test_inconsistent_release_metadata_and_parallel_operators_are_rejected(self):
        with self.host(), self.assertRaisesRegex(release.ReleaseError, "in progress"):
            with self.host():
                pass
        (self.base / "cloud.env").write_text(self.env.replace("cloud.7", "cloud.8"))
        with self.assertRaisesRegex(release.ReleaseError, "disagree"):
            self.host()

    def test_cli_help_is_available_without_docker_or_host_configuration(self):
        result = subprocess.run([os.sys.executable, str(SCRIPT), "rollback", "--help"], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        self.assertIn("--compatibility", result.stdout)
        self.assertIn("allowed_migrations", result.stdout)


if __name__ == "__main__":
    unittest.main()
