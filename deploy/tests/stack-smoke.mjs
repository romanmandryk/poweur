#!/usr/bin/env node
// Real isolated Compose stack: ingest, queries, private/public access and alerts.
// Node built-ins only. Test containers/volumes are removed on completion.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';

const root = fileURLToPath(new URL('../../', import.meta.url));
function command(args, capture = false) {
  const r = spawnSync(args[0], args.slice(1), { encoding: 'utf8', stdio: capture ? 'pipe' : 'inherit', maxBuffer: 8 * 1024 * 1024 });
  // Do not include command arguments or captured configuration in exceptions:
  // they can contain test credentials.
  if (r.error || r.status !== 0) throw new Error(`${args[0]} failed (${r.status})`);
  return r.stdout;
}
function request(url, data, auth) {
  const args = ['exec', '-i', 'e13-smoke-http', 'curl', '-sS', '--max-time', '5', '-w', '\n%{http_code}', '-H', 'Content-Type: application/json'];
  if (auth) args.push('-H', `Authorization: ${auth}`);
  if (data !== undefined) args.push('--data-binary', '@-');
  const r = spawnSync('docker', [...args, url], { input: data === undefined ? undefined : JSON.stringify(data), encoding: 'utf8', maxBuffer: 8 * 1024 * 1024 });
  if (r.error || r.status !== 0) throw new Error('Test HTTP endpoint not ready');
  const pos = r.stdout.lastIndexOf('\n');
  return { status: Number(r.stdout.slice(pos + 1)), body: r.stdout.slice(0, pos) };
}
async function waitFor(check, seconds = 90) {
  const deadline = Date.now() + seconds * 1000;
  while (Date.now() < deadline) {
    try { if (check()) return; } catch { /* readiness/network/JSON may be transient */ }
    await delay(1000);
  }
  throw new Error('Timed out waiting for stack condition');
}
const tmp = mkdtempSync(join(tmpdir(), 'poweur-stack-'));
let compose;
try {
  const env = join(tmp, 'secrets');
  command(['bash', join(root, 'deploy/setup-observability.sh'), '--file', env]);
  const config = JSON.parse(command(['docker', 'compose', '-p', 'poweur-e13-smoke', '--env-file', env, '-f', join(root, 'deploy/infra/docker-compose.yml'), 'config', '--format', 'json'], true));
  config.networks.infra_net.name = 'poweur-e13-smoke';
  for (const [name, svc] of Object.entries(config.services)) {
    const original = svc.container_name ?? name;
    svc.container_name = `e13-smoke-${name}`;
    svc.networks = { infra_net: { aliases: [original] } };
    delete svc.ports;
    if (name === 'node-exporter') for (const volume of svc.volumes ?? []) if (volume.bind) delete volume.bind.propagation;
  }
  config.services.grafana.environment.GF_SECURITY_COOKIE_SECURE = 'false'; // isolated HTTP test only
  // Shorten alert timing only in the test; keep production expressions intact.
  const provision = join(tmp, 'provisioning');
  cpSync(join(root, 'deploy/infra/grafana/provisioning'), provision, { recursive: true });
  for (const file of readdirSync(join(provision, 'alerting'))) {
    const path = join(provision, 'alerting', file);
    const alerts = JSON.parse(readFileSync(path, 'utf8'));
    for (const group of alerts.groups ?? []) {
      group.interval = '10s';
      for (const rule of group.rules) rule.for = '0s';
    }
    writeFileSync(path, JSON.stringify(alerts));
  }
  for (const volume of config.services.grafana.volumes) if (volume.target === '/etc/grafana/provisioning') volume.source = provision;
  // Caddy validates separately; never request production ACME certificates here.
  delete config.services.caddy;
  config.services['http-client'] = { image: 'curlimages/curl:8.16.0', container_name: 'e13-smoke-http', entrypoint: ['/bin/sh', '-c', 'sleep 600'], networks: { infra_net: {} } };
  const file = join(tmp, 'compose.json');
  writeFileSync(file, JSON.stringify(config), { mode: 0o600 });
  compose = ['docker', 'compose', '-p', 'poweur-e13-smoke', '-f', file];
  command([...compose, 'up', '-d']);
  const urls = Object.fromEntries(Object.entries({ alloy: 4318, prometheus: 9090, grafana: 3000, loki: 3100 }).map(([svc, port]) => [svc, `http://infra-${svc}:${port}`]));
  await waitFor(() => request(`${urls.grafana}/api/health`).status === 200);
  await waitFor(() => request(`${urls.loki}/ready`).status === 200);
  const auth = readFileSync(env, 'utf8').match(/^OTEL_EXPORTER_OTLP_HEADERS='Authorization=(.*)'$/m)[1];
  const now = BigInt(Date.now()) * 1_000_000n;
  const attrs = obj => Object.entries(obj).map(([key, v]) => ({ key, value: { stringValue: v } }));
  const resource = { attributes: attrs({ 'service.name': 'poweur-relay', 'deployment.environment.name': 'production' }) };
  const log = { resourceLogs: [{ resource, scopeLogs: [{ logRecords: [{ timeUnixNano: String(now), body: { stringValue: JSON.stringify({ kind: 'action', action: 'registration.create', actor_id: 'PRIVATE_CANARY', identity_mode: 'hashed' }) } }] }] }] };
  assert.ok([401, 403].includes(request(`${urls.alloy}/v1/logs`, log).status));
  assert.equal(request(`${urls.alloy}/v1/logs`, log, auth).status, 200);
  const metric = { resourceMetrics: [{ resource, scopeMetrics: [{ metrics: [{ name: 'poweur_actions', sum: { aggregationTemporality: 2, isMonotonic: true, dataPoints: [{ timeUnixNano: String(now), startTimeUnixNano: String(now - 1_000_000_000n), asInt: '1', attributes: attrs({ action: 'registration.create', outcome: 'success' }) }] } }] }] }] };
  assert.equal(request(`${urls.alloy}/v1/metrics`, metric, auth).status, 200);
  const query = expr => request(`${urls.prometheus}/api/v1/query?${new URLSearchParams({ query: expr })}`).body;
  await waitFor(() => JSON.parse(query('poweur_actions_total')).data.result.length > 0);
  assert.ok(!query('poweur_actions_total').includes('PRIVATE_CANARY'));
  await waitFor(() => request(`${urls.loki}/loki/api/v1/query_range?${new URLSearchParams({ query: '{service_name="poweur-relay"}', limit: '10' })}`).body.includes('PRIVATE_CANARY'));
  for (const path of ['/api/dashboards/uid/poweur-ops', '/api/datasources/proxy/uid/loki/loki/api/v1/labels']) assert.ok([401, 403].includes(request(urls.grafana + path).status));
  const admin = `Basic ${Buffer.from(`admin:${config.services.grafana.environment.GF_SECURITY_ADMIN_PASSWORD}`).toString('base64')}`;
  const board = request(`${urls.grafana}/api/dashboards/uid/poweur-growth`, undefined, admin);
  assert.equal(board.status, 200);
  const growthPanels = JSON.parse(board.body).dashboard.panels;
  assert.ok(growthPanels.length > 0);
  for (const p of growthPanels) {
    // Markdown/row panels have no query. Grafana omits datasource there; do not
    // treat that as Loki. Everything that queries must stay on prometheus.
    if (p.type === 'text' || p.type === 'row') continue;
    assert.equal(p.datasource?.uid, 'prometheus', `${p.title || p.id} must query prometheus`);
  }
  const share = request(`${urls.grafana}/api/dashboards/uid/poweur-growth/public-dashboards`, { isEnabled: true, timeSelectionEnabled: false, annotationsEnabled: false, share: 'public' }, admin);
  assert.ok([200, 201].includes(share.status));
  const publicBoard = request(`${urls.grafana}/api/public/dashboards/${JSON.parse(share.body).accessToken}`);
  assert.equal(publicBoard.status, 200);
  assert.ok(!publicBoard.body.includes('PRIVATE_CANARY'));
  const alerts = request(`${urls.grafana}/api/v1/provisioning/alert-rules`, undefined, admin);
  assert.equal(alerts.status, 200);
  assert.ok(JSON.parse(alerts.body).length >= 6);
  command([...compose, 'stop', 'alloy']);
  await waitFor(() => {
    const r = request(`${urls.grafana}/api/prometheus/grafana/api/v1/rules`, undefined, admin);
    return r.status === 200 && JSON.parse(r.body).data.groups.flatMap(g => g.rules).some(r => r.name === 'Alloy is down' && r.state === 'firing');
  });
  console.log('PASS: authenticated ingest, metrics/logs, private/public dashboards, and intake outage alert');
} finally {
  try {
    if (compose) {
      try { command([...compose, 'logs', '--tail', '25', 'postgres', 'alloy', 'loki', 'grafana']); }
      finally { command([...compose, 'down', '-v', '--remove-orphans']); }
    }
  } finally { rmSync(tmp, { recursive: true, force: true }); }
}
