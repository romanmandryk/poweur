#!/usr/bin/env python3
"""Create local deployment secrets without printing them or changing existing values.
Run as the deploy user. Optionally import the original /opt/infra/.env first.
"""
import argparse
import base64
import hashlib
import os
from pathlib import Path
import secrets


def initialize(target: Path, original: Path | None = None):
    text = target.read_text() if target.exists() else (original.read_text() if original else "")
    values = {line.split("=", 1)[0] for line in text.splitlines() if "=" in line and not line.startswith("#")}
    password = secrets.token_hex(32)
    # Alloy's htpasswd reader supports SHA hashes. The random 256-bit password
    # is not human-memorable; store only its hash at the receiver.
    digest = base64.b64encode(hashlib.sha1(password.encode()).digest()).decode()
    auth = base64.b64encode(("relay:" + password).encode()).decode()
    defaults = {
        "POSTGRES_PASSWORD": secrets.token_hex(32),
        "GRAFANA_DB_PASSWORD": secrets.token_hex(32),
        "GRAFANA_PASSWORD": secrets.token_hex(24),
        "OTLP_HTPASSWD": "relay:{SHA}" + digest,
        "OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Basic " + auth,
        "OTEL_EXPORTER_OTLP_ENDPOINT": "http://infra-alloy:4318",
        "TELEMETRY_ALLOW_HTTP": "1",
        "TELEMETRY_HASH_KEY": secrets.token_hex(32),
        "ALERT_EMAIL": "admin@poweur.net",
        "GRAFANA_SMTP_ENABLED": "false",
    }
    if ("OTLP_HTPASSWD" in values) != ("OTEL_EXPORTER_OTLP_HEADERS" in values):
        raise ValueError("Both intake credential fields must be present or absent; refusing mismatched credentials")
    additions = [f"{key}='{value}'" for key, value in defaults.items() if key not in values]
    content = text.rstrip() + "\n" + "\n".join(additions) + "\n"
    target.parent.mkdir(parents=True, exist_ok=True)
    tmp = target.with_suffix(".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as out:
        out.write(content)
    os.chmod(tmp, 0o600)
    os.replace(tmp, target)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--file", type=Path, default=Path(".observability.env"))
    parser.add_argument("--import-env", type=Path)
    args = parser.parse_args()
    initialize(args.file, args.import_env)
    print(f"Wrote {args.file} (0600); existing settings preserved. Configure SMTP before expecting email alerts.")
