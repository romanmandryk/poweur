#!/usr/bin/env python3
"""Run the real, isolated Compose stack and verify ingest, queries and access.
Requires Docker. All test volumes/containers are removed on completion.
"""
import base64
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[2]


def command(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def request(url, data=None, auth=None):
    headers = {"Content-Type": "application/json"}
    if auth:
        headers["Authorization"] = auth
    try:
        with urllib.request.urlopen(urllib.request.Request(url, json.dumps(data).encode() if data is not None else None, headers), timeout=5) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def wait_for(check, seconds=90):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except (OSError, ValueError):
            pass
        time.sleep(1)
    raise AssertionError("Timed out waiting for stack condition")


def main():
    with tempfile.TemporaryDirectory(prefix="poweur-stack-") as tmp:
        tmp = Path(tmp)
        env = tmp / "secrets"
        command(sys.executable, str(ROOT / "deploy/setup-observability.py"), "--file", str(env))
        raw = command("docker", "compose", "-p", "poweur-e13-smoke", "--env-file", str(env), "-f", str(ROOT / "deploy/infra/docker-compose.yml"), "config", "--format", "json", capture_output=True)
        config = json.loads(raw.stdout)
        config["networks"]["infra_net"]["name"] = "poweur-e13-smoke"
        for name, svc in config["services"].items():
            original = svc.get("container_name", name)
            svc["container_name"] = "e13-smoke-" + name
            svc["networks"] = {"infra_net": {"aliases": [original]}}
            svc.pop("ports", None)
            if name in {"alloy", "prometheus", "grafana", "loki"}:
                port = {"alloy": 4318, "prometheus": 9090, "grafana": 3000, "loki": 3100}[name]
                svc["ports"] = [{"target": port, "host_ip": "127.0.0.1", "published": "0", "protocol": "tcp"}]
        # Validate Caddy config, but don't start a local ACME issuer for prod DNS.
        config["services"].pop("caddy")
        file = tmp / "compose.json"
        file.write_text(json.dumps(config))
        os.chmod(file, 0o600)
        compose = ["docker", "compose", "-p", "poweur-e13-smoke", "-f", str(file)]
        try:
            command(*compose, "up", "-d")
            urls = {}
            for svc, port in [("alloy", 4318), ("prometheus", 9090), ("grafana", 3000), ("loki", 3100)]:
                addr = command(*compose, "port", svc, str(port), capture_output=True).stdout.strip()
                urls[svc] = "http://" + addr
            wait_for(lambda: request(urls["grafana"] + "/api/health")[0] == 200)
            wait_for(lambda: request(urls["loki"] + "/ready")[0] == 200)
            auth = next(line.split("=", 1)[1].strip("'") for line in env.read_text().splitlines() if line.startswith("OTEL_EXPORTER_OTLP_HEADERS=" )).split("=", 1)[1]
            now = str(time.time_ns())
            attrs = lambda d: [{"key": k, "value": {"stringValue": v}} for k, v in d.items()]
            resource = {"attributes": attrs({"service.name": "poweur-relay", "deployment.environment.name": "production"})}
            log = {"resourceLogs": [{"resource": resource, "scopeLogs": [{"logRecords": [{"timeUnixNano": now, "body": {"stringValue": json.dumps({"kind": "action", "action": "registration.create", "actor_id": "PRIVATE_CANARY", "identity_mode": "hashed"})}}]}]}]}
            assert request(urls["alloy"] + "/v1/logs", log)[0] in (401, 403)
            assert request(urls["alloy"] + "/v1/logs", log, auth)[0] == 200
            metric = {"resourceMetrics": [{"resource": resource, "scopeMetrics": [{"metrics": [{"name": "poweur_actions", "sum": {"aggregationTemporality": 2, "isMonotonic": True, "dataPoints": [{"timeUnixNano": now, "startTimeUnixNano": str(int(now)-10**9), "asInt": "1", "attributes": attrs({"action": "registration.create", "outcome": "success"})}]}}]}]}]}
            assert request(urls["alloy"] + "/v1/metrics", metric, auth)[0] == 200
            query = lambda expr: request(urls["prometheus"] + "/api/v1/query?" + urllib.parse.urlencode({"query": expr}))[1]
            wait_for(lambda: bool(json.loads(query("poweur_actions_total"))["data"]["result"]))
            assert "PRIVATE_CANARY" not in query("poweur_actions_total")
            wait_for(lambda: "PRIVATE_CANARY" in request(urls["loki"] + "/loki/api/v1/query_range?" + urllib.parse.urlencode({"query": '{service_name="poweur-relay"}', "limit": "10"}))[1])
            # Private dashboard and datasource proxy must deny anonymous access.
            for path in ["/api/dashboards/uid/poweur-ops", "/api/datasources/proxy/uid/loki/loki/api/v1/labels"]:
                assert request(urls["grafana"] + path)[0] in (401, 403)
            pw = config["services"]["grafana"]["environment"]["GF_SECURITY_ADMIN_PASSWORD"]
            admin = "Basic " + base64.b64encode(("admin:" + pw).encode()).decode()
            code, body = request(urls["grafana"] + "/api/dashboards/uid/poweur-growth", auth=admin)
            assert code == 200, body
            board = json.loads(body)["dashboard"]
            assert all(p["datasource"]["uid"] == "prometheus" for p in board["panels"])
            # Exercise actual externally-shared dashboard creation and anonymous query.
            code, body = request(urls["grafana"] + "/api/dashboards/uid/poweur-growth/public-dashboards", {"isEnabled": True, "timeSelectionEnabled": False, "annotationsEnabled": False, "share": "public"}, admin)
            assert code in (200, 201), body
            token = json.loads(body)["accessToken"]
            code, body = request(urls["grafana"] + "/api/public/dashboards/" + token)
            assert code == 200 and "PRIVATE_CANARY" not in body, body
            code, body = request(urls["grafana"] + "/api/v1/provisioning/alert-rules", auth=admin)
            assert code == 200 and len(json.loads(body)) >= 6, body
            print("PASS: authenticated OTLP -> metrics/logs, provisioned dashboards/alerts, private access denial, public aggregate sharing")
        finally:
            command(*compose, "logs", "--tail", "8", "alloy", "loki", "grafana")
            command(*compose, "down", "-v", "--remove-orphans")


if __name__ == "__main__":
    main()
