#!/usr/bin/env python3
"""Real Docker regression: service IP changes recover without reloading console.

Uses only an existing console image; never pulls/builds images or accesses any
application services. Creates and cleans an isolated network and named fixtures.
Compile the synthetic fixture for the Docker host architecture before running:

  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/console-dns-fixture ./test/console-dns-fixture
  python3 scripts/tests/verify_console_dns.py --console-image EXISTING_IMAGE \
      --config deploy/nginx.conf --fixture-bin /tmp/console-dns-fixture

Docker must run on this Linux host so the read-only bind mounts and private
bridge address are accessible. No ports are published. Run against the previous
nginx.conf as a negative control: it must
fail to recover after the IP change. No company data or real tokens are used.
"""

import argparse
import hashlib
import ipaddress
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid


HEADERS = {
    "Host": "gateway.example", "Authorization": "Bearer synthetic-dns-fixture",
    "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
    "Origin": "https://gateway.example", "MCP-Protocol-Version": "2025-06-18",
    "Mcp-Session-Id": "synthetic-session", "Last-Event-ID": "event-1", "X-Trace-Id": "synthetic-trace",
}
BODY = b'{"jsonrpc":"2.0","id":9223372036854775807,"method":"tools/list","params":{}}'
ROUTES = {"api": "/api/v1/dns-probe/a%2Fb?tag=a%2Bb&tag=x%2Fy", "gateway": "/mcp?tag=a%2Bb&tag=x%2Fy"}


class Failure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise Failure(message)


def docker(*args):
    result = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise Failure("docker " + " ".join(args[:2]) + " failed: " + result.stderr.strip()[-2000:])
    return result.stdout.strip()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def verify_static_routes(opener, origin):
    for path, marker in (("/", "public-website"), ("/en/", "english-website"),
                         ("/cn/", "chinese-website"), ("/console/", "private-workspace"),
                         ("/console/reload", "private-workspace"), ("/assets/probe.js", "static-asset")):
        with opener.open(origin + path, timeout=3) as response:
            require(response.status == 200 and response.read(4096).decode() == marker,
                    "incorrect static route: " + path)
            require(response.headers.get("Referrer-Policy") == "no-referrer", "missing referrer protection")
            require("frame-ancestors 'none'" in response.headers.get("Content-Security-Policy", ""),
                    "missing static content security policy")
    for path in ("/missing-page", "/cn/missing", "/en/missing", "/assets/missing.js", "/internal/private"):
        try:
            opener.open(origin + path, timeout=3)
        except urllib.error.HTTPError as response:
            require(response.code == 404, "incorrect missing-route status: " + path)
        else:
            raise Failure("missing route returned a page: " + path)
    for entry in ("console", "en", "cn"):
        try:
            opener.open(origin + f"/{entry}?view=test", timeout=3)
        except urllib.error.HTTPError as response:
            require(response.code == 308 and response.headers.get("Location") == f"/{entry}/?view=test",
                    entry + " canonical redirect lost its path or query")
        else:
            raise Failure(entry + " canonical redirect missing")
    print(json.dumps({"case": "public_website_workspace_and_missing_assets", "status": "pass"}), flush=True)


def probe(opener, origin, role, generation):
    req = urllib.request.Request(origin + ROUTES[role], data=BODY, headers=HEADERS, method="POST")
    with opener.open(req, timeout=3) as response:
        raw = response.read(65537)
        require(len(raw) <= 65536, "unexpected response size")
        value = json.loads(raw)
    # Retry only connectivity/stale-generation states while Docker DNS converges.
    # A corrupt route/body/header fails immediately even during that window.
    require(value.get("request_uri") == ROUTES[role], role + " URI/query was rewritten")
    require(value.get("method") == "POST", role + " method was changed")
    require(value.get("body_bytes") == len(BODY) and value.get("body_sha256") == hashlib.sha256(BODY).hexdigest(),
            role + " body changed")
    require(value.get("headers") == HEADERS, role + " headers changed")
    return value.get("generation") == role + "-" + generation


def await_generation(opener, origin, generation):
    deadline = time.monotonic() + 25
    ready = set()
    attempts = 0
    while time.monotonic() < deadline:
        attempts += 1
        for role in ROUTES:
            try:
                if probe(opener, origin, role, generation):
                    ready.add(role)
                else:
                    ready.discard(role)
            except (urllib.error.URLError, TimeoutError):
                ready.discard(role)
        if ready == set(ROUTES):
            return attempts
        time.sleep(1)
    raise Failure("API/MCP did not reach " + generation + " within 25 seconds")


def verify(args):
    config, binary = Path(args.config).resolve(), Path(args.fixture_bin).resolve()
    require(config.is_file() and binary.is_file(), "config and fixture binary must exist")
    require("," not in str(config) and "," not in str(binary), "bind mount paths cannot contain commas")
    docker("image", "inspect", args.console_image)
    prefix = "console-dns-" + uuid.uuid4().hex[:12]
    network, console = prefix + "-net", prefix + "-console"
    containers = []
    created_network = False
    cleanup_failed = False
    site = tempfile.TemporaryDirectory(prefix="gateway-website-fixture-")
    try:
        site_root = Path(site.name)
        # nginx's non-root user must traverse the synthetic fixture directory.
        site_root.chmod(0o755)
        (site_root / "console").mkdir()
        (site_root / "assets").mkdir()
        (site_root / "index.html").write_text("public-website")
        for locale, marker in (("en", "english-website"), ("cn", "chinese-website")):
            (site_root / locale).mkdir()
            (site_root / locale / "index.html").write_text(marker)
        (site_root / "console/index.html").write_text("private-workspace")
        (site_root / "assets/probe.js").write_text("static-asset")
        docker("network", "create", "--internal", network)
        created_network = True
        ipam = json.loads(docker("network", "inspect", "--format", "{{json .IPAM.Config}}", network))
        subnet = next(ipaddress.ip_network(row["Subnet"]) for row in ipam if ":" not in row["Subnet"])
        require(subnet.num_addresses > 32, "fixture network subnet is too small")
        # Some Docker versions reject static endpoints on an automatically
        # allocated subnet. Recreate only our empty network with Docker's own
        # chosen subnet made explicit. A concurrent allocation fails safely.
        docker("network", "rm", network)
        created_network = False
        docker("network", "create", "--internal", "--subnet", str(subnet), network)
        created_network = True

        def start_backend(role, generation, offset):
            name = prefix + "-" + role + "-" + generation
            containers.append(name)
            address = str(subnet.network_address + offset)
            docker("run", "-d", "--pull=never", "--name", name, "--network", network,
                   "--network-alias", role, "--ip", address, "--read-only", "--cap-drop=ALL",
                   "--security-opt=no-new-privileges", "--entrypoint", "/dns-fixture",
                   "--mount", "type=bind,src=" + str(binary) + ",dst=/dns-fixture,readonly",
                   args.console_image, "-listen", ":" + ("8090" if role == "api" else "8091"),
                   "-generation", role + "-" + generation)
            return name, address

        first = {role: start_backend(role, "v1", offset) for role, offset in (("api", 11), ("gateway", 12))}
        containers.append(console)
        console_ip = str(subnet.network_address + 13)
        docker("run", "-d", "--pull=never", "--name", console, "--network", network,
               "--ip", console_ip, "--read-only", "--tmpfs", "/tmp",
               "--cap-drop=ALL", "--security-opt=no-new-privileges",
               "--mount", "type=bind,src=" + str(config) + ",dst=/etc/nginx/conf.d/default.conf,readonly",
               "--mount", "type=bind,src=" + str(site_root) + ",dst=/usr/share/nginx/html,readonly",
               args.console_image)
        docker("exec", console, "nginx", "-t")
        origin = "http://" + console_ip + ":8080"
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        before = docker("inspect", "--format", "{{.Id}} {{.State.StartedAt}}", console)
        await_generation(opener, origin, "v1")
        verify_static_routes(opener, origin)
        workers_before = docker("top", console, "-eo", "pid,comm")
        print(json.dumps({"case": "initial_routes_and_payload", "status": "pass"}), flush=True)

        for name, _ in first.values():
            docker("rm", "-f", name)
            containers.remove(name)
        second = {role: start_backend(role, "v2", offset) for role, offset in (("api", 21), ("gateway", 22))}
        require(all(first[role][1] != second[role][1] for role in ROUTES), "backend IP did not change")
        started = time.monotonic()
        attempts = await_generation(opener, origin, "v2")
        # Recheck both paths once convergence completes.
        require(all(probe(opener, origin, role, "v2") for role in ROUTES), "backend became stale after recovery")
        require(docker("inspect", "--format", "{{.Id}} {{.State.StartedAt}}", console) == before,
                "console was recreated or restarted")
        require(docker("top", console, "-eo", "pid,comm") == workers_before, "console worker processes changed")
        print(json.dumps({"case": "new_backend_ips_without_console_reload", "status": "pass",
                          "convergence_seconds": round(time.monotonic() - started, 3), "attempts": attempts,
                          "api_ip_changed": True, "mcp_ip_changed": True}), flush=True)
    finally:
        for name in reversed(containers):
            result = subprocess.run(["docker", "rm", "-f", name], capture_output=True, timeout=30)
            cleanup_failed = cleanup_failed or result.returncode != 0
        if created_network:
            result = subprocess.run(["docker", "network", "rm", network], capture_output=True, timeout=30)
            cleanup_failed = cleanup_failed or result.returncode != 0
        site.cleanup()
        if cleanup_failed:
            print(json.dumps({"case": "cleanup", "status": "fail", "resource_prefix": prefix}), file=sys.stderr)
            raise Failure("fixture cleanup failed; inspect only the reported resource prefix")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--console-image", required=True, help="existing local console image; never pulled")
    parser.add_argument("--config", required=True, help="nginx.conf to mount into the test console")
    parser.add_argument("--fixture-bin", required=True, help="static Linux fixture binary for the Docker host architecture")
    args = parser.parse_args()
    try:
        verify(args)
    except (Failure, subprocess.TimeoutExpired, OSError, ValueError, StopIteration) as error:
        print(json.dumps({"status": "fail", "error": str(error)}), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
