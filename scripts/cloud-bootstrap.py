#!/usr/bin/env python3
"""Create private cloud configuration on the deployment host; never print secrets."""
import argparse
import os
from pathlib import Path
import secrets
from urllib.parse import urlparse

parser = argparse.ArgumentParser()
parser.add_argument("--origin", required=True)
parser.add_argument("--release", required=True)
parser.add_argument("--directory", default="/opt/mcp-gateway")
args = parser.parse_args()
origin = urlparse(args.origin)
if origin.scheme != "https" or not origin.netloc or origin.path or origin.query or origin.fragment or origin.username:
    parser.error("origin must be HTTPS without a path, query, or user information")
if not all(c.isalnum() or c in "._-" for c in args.release):
    parser.error("invalid release name")
base = Path(args.directory).resolve()
base.mkdir(parents=True, exist_ok=True)
private = base / "secrets"
private.mkdir(exist_ok=True)
private.chmod(0o750)
os.chown(private, 0, 65532)
for name, value in (("bootstrap", secrets.token_urlsafe(32) + "\n"), ("credentials.json", "[]\n"), ("runner-token", secrets.token_urlsafe(48) + "\n")):
    path = private / name
    if not path.exists():
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o640)
        with os.fdopen(fd, "w") as stream:
            stream.write(value)
        os.chown(path, 0, 65532)
    path.chmod(0o640)
env = base / "cloud.env"
if not env.exists():
    with os.fdopen(os.open(env, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
        stream.write(f"RELEASE_ID={args.release}\nPUBLIC_ORIGIN={args.origin}\nGATEWAY_SECRETS_DIR={private}\nPOSTGRES_PASSWORD={secrets.token_hex(32)}\nHTTP_ALLOWED_ORIGINS=\nHTTP_ALLOWED_CIDRS=\n")
setup = base / "owner-setup.txt"
if not setup.exists():
    with os.fdopen(os.open(setup, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
        stream.write("One-time administrator setup (expires seven days after first startup):\n" + args.origin + "/#invite=" + (private / "bootstrap").read_text().strip() + "\n")
print("Private configuration is ready. Existing values were preserved.")
