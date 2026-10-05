#!/usr/bin/env python3
"""Collect gateway health, evaluate alerts, and optionally deliver notifications.

Default collection uses a fixed read-only PostgreSQL query via the local cloud
Compose project. --metrics-file and --url support fixture/export collection.
Without --delivery-config this writes local JSON only: no external delivery.
Trusted host configuration can select generic JSON, Feishu or WeCom webhooks.
Keep the HTTPS URL in a separate 0600 file. URLs, bearer tokens and webhook
responses are never logged; redirects are refused. No UI/request can set these
host-owned destinations. First incidents, changed incident codes and cooldown
reminders are delivered, plus one recovery; failed sends never mark sent.
The monitor must have a persistent writable --state file for deduplication.
Webhook acknowledgement and the local receipt cannot be atomic: a crash between
them can produce a duplicate; the generic payload includes an incident key.
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
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request

DEFAULT_RULES = {
    "minimum_calls": 20, "failure_ratio": 0.2, "mean_duration_ms": 10000,
    "unknown_calls": 1, "unknown_oldest_age_seconds": 300,
    "limit_rejections": 20, "metrics_max_age_seconds": 300,
    "local_backup_max_age_seconds": 129600, "offhost_max_age_seconds": 129600,
    "restore_max_age_seconds": 604800,
}
# Parameters are passed as a psql variable and quoted by psql, never SQL text.
COLLECT_SQL = """BEGIN READ ONLY;
SET LOCAL statement_timeout='8s';
SELECT json_build_object(
 'calls', COALESCE((SELECT json_agg(json_build_object('transport',transport,'state',state,'count',count,'duration_ms_total',duration_ms_total) ORDER BY transport,state) FROM capacity_call_metrics WHERE workspace_id=:'workspace'),'[]'::json),
 'rejections', COALESCE((SELECT json_agg(json_build_object('scope',scope,'reason',reason,'count',count) ORDER BY scope,reason) FROM capacity_rejection_metrics WHERE workspace_id=:'workspace'),'[]'::json),
 'active_leases',(SELECT count(*) FROM capacity_leases WHERE workspace_id=:'workspace' AND expires_at>clock_timestamp()),
 'unknown_count',(SELECT count(*) FROM operations o WHERE workspace_id=:'workspace' AND state='UNKNOWN' AND COALESCE((SELECT outcome FROM operation_reconciliations r WHERE (r.workspace_id,r.operation_id)=(o.workspace_id,o.id) ORDER BY r.id DESC LIMIT 1),'inconclusive')='inconclusive'),
 'unknown_oldest_age_seconds',COALESCE((SELECT extract(epoch FROM clock_timestamp()-min(updated_at)) FROM operations o WHERE workspace_id=:'workspace' AND state='UNKNOWN' AND COALESCE((SELECT outcome FROM operation_reconciliations r WHERE (r.workspace_id,r.operation_id)=(o.workspace_id,o.id) ORDER BY r.id DESC LIMIT 1),'inconclusive')='inconclusive'),0),
 'observed_at',clock_timestamp());
COMMIT;
"""


def timestamp(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()


def counters(sample):
    out = {"calls": 0, "failures": 0, "unknown": 0, "duration_ms": 0, "rejections": 0}
    for value in sample["calls"]:
        if value["transport"] not in ["http", "mcp"] or value["state"] not in ["SUCCEEDED", "FAILED", "UNKNOWN"]:
            raise ValueError("unbounded metric dimension")
        count, duration = value["count"], value["duration_ms_total"]
        if type(count) != int or type(duration) != int or count < 0 or duration < 0:
            raise ValueError("invalid counter")
        out["calls"] += count
        out["duration_ms"] += duration
        if value["state"] in ["FAILED", "UNKNOWN"]:
            out["failures"] += count
        if value["state"] == "UNKNOWN":
            out["unknown"] += count
    for value in sample["rejections"]:
        if value["scope"] not in ["workspace", "client", "upstream"] or value["reason"] not in ["rate", "concurrency"] or type(value["count"]) != int or value["count"] < 0:
            raise ValueError("invalid rejection counter")
        out["rejections"] += value["count"]
    return out


def evaluate(sample, previous, rules, offhost=None, restore=None, now=None, *, local=None, check_local=False):
    now = now if now is not None else datetime.now(timezone.utc).timestamp()
    alerts, delta = [], None
    current = counters(sample)

    def alert(code, detail):
        alerts.append({"code": code, "detail": detail})

    age = now - timestamp(sample["observed_at"])
    if age > rules["metrics_max_age_seconds"] or age < -60:
        alert("metrics_stale", "Metrics sample is stale or from a future clock.")
    if previous:
        before = counters(previous)
        delta = {key: max(0, current[key] - before[key]) for key in current}
        if delta["unknown"] >= rules["unknown_calls"]:
            alert("operation_unknown", "New calls need reconciliation.")
        if delta["calls"] >= rules["minimum_calls"]:
            if delta["failures"] / delta["calls"] >= rules["failure_ratio"]:
                alert("failure_ratio", "Call failure ratio exceeded the configured threshold.")
            if delta["duration_ms"] / delta["calls"] >= rules["mean_duration_ms"]:
                alert("mean_duration", "Mean call duration exceeded the configured threshold.")
        if delta["rejections"] >= rules["limit_rejections"]:
            alert("capacity_rejections", "Admission rejections exceeded the configured threshold.")
    unknown_count = sample.get("unknown_count", 0)
    unknown_age = sample.get("unknown_oldest_age_seconds", 0)
    if type(unknown_count) != int or unknown_count < 0 or type(unknown_age) not in [int, float] or unknown_age < 0:
        raise ValueError("invalid UNKNOWN age")
    if unknown_count and unknown_age >= rules["unknown_oldest_age_seconds"]:
        alert("unknown_overdue", "An unresolved UNKNOWN operation exceeded the review age threshold.")
    receipts = [("offhost_backup", offhost, rules["offhost_max_age_seconds"], "verified_at"),
                ("restore_drill", restore, rules["restore_max_age_seconds"], "verified_at")]
    if check_local:
        receipts.append(("local_backup", local, rules["local_backup_max_age_seconds"], "created_at"))
    for name, receipt, threshold, field in receipts:
        if receipt is None:
            alert(name + "_missing", "A verified receipt is missing.")
        else:
            age = now - timestamp(receipt[field])
            if age > threshold or age < -60:
                alert(name + "_stale", "The last verified receipt is too old or from a future clock.")
    return {"status": "alert" if alerts else "ok", "warming_up": previous is None, "alerts": alerts, "delta": delta}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise ValueError("monitor redirects are refused")


def read(path):
    if not path or not path.exists():
        return None
    if path.stat().st_size > 65536:
        raise ValueError("monitor input exceeds limit")
    return json.loads(path.read_text())


def atomic_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".monitor-", delete=False) as stream:
        temporary = Path(stream.name)
        os.fchmod(stream.fileno(), 0o600)
        stream.write(json.dumps(value, sort_keys=True).encode())
        stream.flush()
        os.fsync(stream.fileno())
    try:
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def collect_local(base, workspace, *, runner=subprocess.run):
    if not re.fullmatch(r"[A-Za-z0-9_.-]{1,128}", workspace):
        raise ValueError("invalid workspace")
    # Avoid caller environment silently overriding the selected cloud.env.
    values = {}
    for line in (base / "cloud.env").read_text().splitlines():
        key, sep, value = line.partition("=")
        if sep and not line.startswith("#"):
            values[key] = value
    env = {key: value for key, value in os.environ.items() if key not in values}
    result = runner(["docker", "compose", "--project-name", "mcp-gateway-cloud", "--env-file", str(base / "cloud.env"),
                     "-f", str(base / "current/deploy/compose/cloud.yaml"), "exec", "-T", "postgres", "psql", "-XAtq", "-U", "gateway", "-d", "gateway",
                     "-v", "ON_ERROR_STOP=1", "-v", "workspace=" + workspace],
                    input=COLLECT_SQL.encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, timeout=15, check=False)
    if result.returncode != 0 or len(result.stdout) > 65536:
        raise ValueError("local collection failed")
    return json.loads(result.stdout)


def collect_url(url, token_file):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.query or parsed.fragment or not token_file:
        raise ValueError("a direct HTTPS URL and token file are required")
    token = token_file.read_text().strip()
    if not token or "\n" in token or "\r" in token:
        raise ValueError("invalid monitor token")
    request = urllib.request.Request(url, headers={"Authorization": "Bearer " + token, "Accept": "application/json"})
    with urllib.request.build_opener(NoRedirect).open(request, timeout=15) as response:
        body = response.read(65537)
        if len(body) > 65536:
            raise ValueError("metrics response too large")
        return json.loads(body)


def delivery_config(path):
    value = read(path)
    if not isinstance(value, dict) or set(value) != {"kind", "url_file", "cooldown_seconds"}:
        raise ValueError("invalid host delivery configuration")
    if value["kind"] not in ["generic", "feishu", "wecom"] or type(value["cooldown_seconds"]) != int or not 60 <= value["cooldown_seconds"] <= 86400:
        raise ValueError("invalid delivery kind or cooldown")
    secret = Path(value["url_file"])
    if not secret.is_absolute() or not secret.is_file() or secret.is_symlink() or secret.stat().st_mode & 0o077 or secret.stat().st_size > 4096:
        raise ValueError("webhook URL requires a private regular file")
    url = secret.read_text().strip()
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.fragment or any(c in url for c in "\r\n\t "):
        raise ValueError("webhook requires a direct HTTPS URL")
    return {**value, "url": url, "destination_hash": hashlib.sha256((value["kind"] + "\n" + url).encode()).hexdigest()}


def webhook_body(kind, status, key, alerts):
    # Only fixed evaluator messages enter a notification. No SQL, event payloads,
    # secrets, arbitrary JSON templates, recipient lists or mentions are accepted.
    if kind == "generic":
        return {"source": "mcp-gateway", "status": status, "incident_key": key, "alerts": alerts}
    lines = ["MCP Gateway " + ("RECOVERED" if status == "resolved" else "ALERT"), "Incident: " + key]
    lines.extend(value["code"] + ": " + value["detail"] for value in alerts)
    text = "\n".join(lines)
    if kind == "feishu":
        return {"msg_type": "text", "content": {"text": text}}
    return {"msgtype": "text", "text": {"content": text}}


def post_webhook(url, body, *, opener=None):
    request = urllib.request.Request(url, data=json.dumps(body, ensure_ascii=False).encode(), headers={"Content-Type": "application/json"}, method="POST")
    opener = opener or urllib.request.build_opener(NoRedirect)
    with opener.open(request, timeout=10) as response:
        raw = response.read(16385)
        if not 200 <= response.status < 300 or len(raw) > 16384:
            raise ValueError("webhook acknowledgement failed")
        return raw


def deliver(result, config, state_path, *, now=None, sender=post_webhook):
    if config is None:
        return {"status": "local_only", "sent": False}
    now = now if now is not None else datetime.now(timezone.utc).timestamp()
    previous = read(state_path) or {}
    alerts = sorted(result["alerts"], key=lambda value: value["code"])
    key = hashlib.sha256("\n".join(value["code"] for value in alerts).encode()).hexdigest()[:16]
    same_destination = previous.get("destination_hash") == config["destination_hash"]
    if alerts:
        status = "firing"
        if same_destination and previous.get("status") == status and previous.get("incident_key") == key and now - previous.get("sent_at", 0) < config["cooldown_seconds"]:
            return {"status": "suppressed", "sent": False, "reason": "cooldown", "incident_key": key}
    else:
        if not same_destination or previous.get("status") != "firing":
            return {"status": "idle", "sent": False}
        status, key = "resolved", previous["incident_key"]
    body = webhook_body(config["kind"], status, key, alerts)
    raw = sender(config["url"], body)
    if config["kind"] in ["feishu", "wecom"]:
        ack = json.loads(raw)
        field = "code" if config["kind"] == "feishu" else "errcode"
        if type(ack.get(field)) != int or ack[field] != 0:
            raise ValueError("webhook application acknowledgement failed")
    atomic_json(state_path, {"status": status, "incident_key": key, "destination_hash": config["destination_hash"], "sent_at": now})
    return {"status": "sent", "sent": True, "event": status, "incident_key": key}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    src = parser.add_mutually_exclusive_group()
    src.add_argument("--metrics-file", type=Path)
    src.add_argument("--url")
    parser.add_argument("--base", type=Path, default=Path("/opt/mcp-gateway"))
    parser.add_argument("--workspace", default="team")
    parser.add_argument("--token-file", type=Path)
    parser.add_argument("--state", type=Path, required=True)
    parser.add_argument("--rules", type=Path)
    parser.add_argument("--local-receipt", type=Path)
    parser.add_argument("--offhost-receipt", type=Path)
    parser.add_argument("--restore-receipt", type=Path)
    parser.add_argument("--delivery-config", type=Path)
    args = parser.parse_args(argv)
    try:
        args.state.parent.mkdir(parents=True, exist_ok=True)
        with args.state.with_suffix(".lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            sample = None
            try:
                rules = {**DEFAULT_RULES, **(read(args.rules) or {})}
                if set(rules) != set(DEFAULT_RULES) or any(type(v) not in [int, float] or not 0 < v < float("inf") for v in rules.values()) or rules["failure_ratio"] > 1:
                    raise ValueError("invalid alert rules")
                if args.metrics_file:
                    sample = read(args.metrics_file)
                elif args.url:
                    sample = collect_url(args.url, args.token_file)
                else:
                    sample = collect_local(args.base, args.workspace)
                result = evaluate(sample, read(args.state), rules,
                                  read(args.offhost_receipt or args.base / "backups/last-offhost.json"),
                                  read(args.restore_receipt or args.base / "backups/last-restore.json"),
                                  local=read(args.local_receipt or args.base / "backups/last-local.json"), check_local=True)
            except Exception:
                sample = None
                result = {"status": "alert", "alerts": [{"code": "monitor_failed", "detail": "Metrics, receipt, or configuration check failed."}]}
            try:
                config = delivery_config(args.delivery_config) if args.delivery_config else None
                delivery_state = args.state.with_name(args.state.stem + ".delivery.json")
                result["delivery"] = deliver(result, config, delivery_state)
            except Exception:
                result["delivery"] = {"status": "failed", "sent": False}
                print(json.dumps(result, sort_keys=True))
                return 1
            # Keep the prior sample on delivery failure so interval incidents can
            # retry on the next successful collection. Never persist credentials.
            if sample is not None:
                atomic_json(args.state, sample)
            print(json.dumps(result, sort_keys=True))
            return 2 if result["alerts"] else 0
    except Exception:
        print(json.dumps({"status": "alert", "alerts": [{"code": "monitor_failed", "detail": "Monitor state or lock failed."}], "delivery": {"status": "failed", "sent": False}}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
