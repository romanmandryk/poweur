import assert from 'node:assert/strict';
import { test } from 'node:test';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const deploy = fileURLToPath(new URL('../', import.meta.url));
function fixture(t) {
  const root = realpathSync(mkdtempSync(join(tmpdir(), 'poweur-deploy-')));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}
function write(path, content = '', mode = 0o600) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content, { mode });
}
function run(script, args = [], env = {}) {
  return spawnSync('bash', [join(deploy, script), ...args], { env: { ...process.env, ...env }, encoding: 'utf8' });
}
function success(result) { assert.equal(result.status, 0, result.stderr); }
function settings(path) {
  return Object.fromEntries([...readFileSync(path, 'utf8').matchAll(/^([A-Z_]+)='(.*)'$/gm)].map(m => [m[1], m[2]]));
}

test('setup creates matching credentials, permissions, and is idempotent', t => {
  const target = join(fixture(t), 'secrets');
  success(run('setup-observability.sh', ['--file', target]));
  const first = readFileSync(target, 'utf8');
  success(run('setup-observability.sh', ['--file', target]));
  assert.equal(readFileSync(target, 'utf8'), first);
  assert.equal(statSync(target).mode & 0o777, 0o600);
  const values = settings(target);
  const credential = Buffer.from(values.OTEL_EXPORTER_OTLP_HEADERS.slice('Authorization=Basic '.length), 'base64').toString();
  assert.match(credential, /^relay:[0-9a-f]{64}$/);
  const hash = createHash('sha1').update(credential.slice('relay:'.length)).digest('base64');
  assert.equal(values.OTLP_HTPASSWD, `relay:{SHA}${hash}`);
  assert.match(values.TELEMETRY_HASH_KEY, /^[0-9a-f]{64}$/);
});

test('setup preserves imported dotenv literally without executing it', t => {
  const root = fixture(t), original = join(root, 'original'), target = join(root, 'secrets');
  const text = `GRAFANA_DB_PASSWORD='original'\nPOSTGRES_PASSWORD='original2'\nCUSTOM=$(touch ${root}/executed)\n`;
  write(original, text);
  success(run('setup-observability.sh', ['--file', target, '--import-env', original]));
  assert.ok(readFileSync(target, 'utf8').startsWith(text));
  assert.equal((readFileSync(target, 'utf8').match(/GRAFANA_DB_PASSWORD=/g) ?? []).length, 1);
  assert.ok(!existsSync(join(root, 'executed')));
});

for (const field of ['OTLP_HTPASSWD', 'OTEL_EXPORTER_OTLP_HEADERS']) {
  test(`setup refuses a partial ${field} without changing the file`, t => {
    const target = join(fixture(t), 'secrets'), text = `${field}='incomplete'\n`;
    write(target, text);
    assert.notEqual(run('setup-observability.sh', ['--file', target]).status, 0);
    assert.equal(readFileSync(target, 'utf8'), text);
  });
}

test('setup refuses missing imports and invalid arguments without creating secrets', t => {
  const target = join(fixture(t), 'secrets');
  assert.notEqual(run('setup-observability.sh', ['--file', target, '--import-env', `${target}-missing`]).status, 0);
  assert.ok(!existsSync(target));
  assert.notEqual(run('setup-observability.sh', ['--unknown']).status, 0);
});

test('deploys skip CI unless asked, so a master push only builds and ships', () => {
  const relay = readFileSync(new URL('../../.github/workflows/deploy.yml', import.meta.url), 'utf8');
  const bridge = readFileSync(new URL('../../.github/workflows/deploy-oauth.yml', import.meta.url), 'utf8');
  const ci = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8');
  const health = readFileSync(new URL('../../.github/workflows/health-monitor.yml', import.meta.url), 'utf8');
  for (const yml of [relay, bridge]) {
    assert.match(yml, /inputs\.run_tests \|\| vars\.RUN_CI == 'true'/);
    assert.match(yml, /needs\.test\.result == 'skipped'/);
  }
  assert.match(ci, /workflow_dispatch:/);
  assert.doesNotMatch(ci, /^ {2}push:/m);
  assert.doesNotMatch(ci, /^ {2}pull_request:/m);
  assert.doesNotMatch(health, /^ {2}schedule:/m);
  assert.match(health, /workflow_dispatch:/);
});

test('deploy workflow bakes release identity into the remote script', () => {
  const yml = readFileSync(new URL('../../.github/workflows/deploy.yml', import.meta.url), 'utf8');
  assert.match(yml, /printf 'export RELEASE_SHA=%q\\n'/);
  assert.match(yml, /printf 'export RELEASE_IMAGE=%q\\n'/);
  assert.match(yml, /cat > \/tmp\/poweur-release\.sh/);
  assert.match(yml, /versionHash/);
  assert.doesNotMatch(yml, /bash -s <</);
  assert.doesNotMatch(yml, /ssh "[^"]+" env RELEASE_SHA=/);
});

test('bridge deploy runs only for bridge changes and verifies the release', () => {
  const yml = readFileSync(new URL('../../.github/workflows/deploy-oauth.yml', import.meta.url), 'utf8');
  // Path-filtered: a relay-only or web-only push does not redeploy the bridge.
  assert.match(yml, /paths:\n(\s+- .+\n)*\s+- apps\/oauth\/\*\*/);
  assert.match(yml, /- packages\/identity\/\*\*/);
  assert.match(yml, /file: apps\/oauth\/Dockerfile/);
  assert.match(yml, /VERSION_HASH=\$\{\{ github\.sha \}\}/);
  // Its own directory, script and lock: never the relay's checkout.
  assert.match(yml, /cd \/opt\/apps\/poweur-oauth/);
  assert.doesNotMatch(yml, /git reset/);
  assert.match(yml, /cat > \/tmp\/poweur-oauth-release\.sh/);
  assert.match(yml, /group: poweur-oauth-deploy/);
  assert.match(yml, /versionHash/);
  assert.doesNotMatch(yml, /bash -s <</);
  const compose = readFileSync(new URL('../../apps/oauth/deploy/docker-compose.prod.yml', import.meta.url), 'utf8');
  assert.match(compose, /OAUTH_ISSUER: https:\/\/oauth\.poweur\.org/);
  assert.match(compose, /container_name: poweur-oauth/);
  assert.match(compose, /- \.env\.prod/);
  assert.doesNotMatch(compose, /OAUTH_KEY_ENCRYPTION_KEY:/);
});

test('caddy config is mounted as a directory so deploy reloads see new files', () => {
  const compose = readFileSync(new URL('../infra/docker-compose.yml', import.meta.url), 'utf8');
  assert.match(compose, /- \.\/caddy:\/etc\/caddy:ro/);
  assert.doesNotMatch(compose, /\.\/caddy\/Caddyfile:/);
});

test('website (with docs under /docs) is static files outside the relay checkout, served by Caddy', () => {
  const caddy = readFileSync(new URL('../infra/caddy/Caddyfile', import.meta.url), 'utf8');
  assert.match(caddy, /tmpwww\.poweur\.org[\s\S]*?root \* \/srv\/web\/www/);
  assert.match(caddy, /redir \/docs \/docs\/ 308/);
  assert.doesNotMatch(caddy, /tmpdocs/);
  const compose = readFileSync(new URL('../infra/docker-compose.yml', import.meta.url), 'utf8');
  assert.match(compose, /\/opt\/apps\/poweur-web\}:\/srv\/web:ro/);
  const deploy = readFileSync(new URL('../../.github/workflows/deploy.yml', import.meta.url), 'utf8');
  assert.match(deploy, /mkdir -p \/opt\/apps\/poweur-web/);
  const web = readFileSync(new URL('../../.github/workflows/deploy-web.yml', import.meta.url), 'utf8');
  assert.match(web, /group: poweur-web-deploy/);
  assert.match(web, /--exclude=\.\/social/);
  assert.match(web, /cp -R apps\/docs\/build out\/docs/);
  assert.doesNotMatch(web, /bash -s <</);
});
