import assert from 'node:assert/strict';
import { test } from 'node:test';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
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
function fake(root, name, js) { write(join(root, 'bin', name), '#!/usr/bin/env node\n' + js, 0o755); }
function fakeEnv(root) { return { PATH: `${join(root, 'bin')}:${process.env.PATH}`, TEST_ROOT: root }; }
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

for (const envFile of ['apps/api/.env.prod', '${RELAY_ENV_FILE:-apps/api/.env.prod}']) {
  test(`baseline pins image, preserves web, and excludes secrets (${envFile})`, t => {
    const root = fixture(t), infra = join(root, 'infra'), web = join(root, 'web');
    write(join(infra, '.env'), 'SECRET=do-not-copy');
    write(join(infra, 'nested', '.env.secret'), 'SECRET=do-not-copy');
    write(join(infra, 'tls.key'), 'private');
    write(join(infra, 'docker-compose.yml'), 'services: {}');
    write(join(web, 'index.html'), 'old web');
    write(join(root, 'docker-compose.prod.yml'), `services:\n  relay:\n    image: example:latest\n    env_file: [${envFile}]\n  unrelated:\n    image: keep:this\n`);
    const image = `sha256:${'b'.repeat(64)}`;
    fake(root, 'docker', `process.stdout.write(process.argv[4] === '{{.Image}}' ? '${image}' : process.env.TEST_ROOT + '/web');`);
    success(run('capture-baseline.sh', ['--root', root, '--infra', infra], fakeEnv(root)));
    const baseline = realpathSync(join(root, '.current'));
    assert.equal(readFileSync(join(baseline, '.relay-image'), 'utf8').trim(), image);
    assert.equal(readFileSync(join(baseline, 'apps/web/index.html'), 'utf8'), 'old web');
    assert.ok(!existsSync(join(baseline, 'deploy/infra/.env')));
    assert.ok(!existsSync(join(baseline, 'deploy/infra/nested/.env.secret')));
    assert.ok(!existsSync(join(baseline, 'deploy/infra/tls.key')));
    const compose = readFileSync(join(baseline, 'docker-compose.prod.yml'), 'utf8');
    assert.ok(compose.includes('${RELAY_IMAGE:?}'));
    assert.ok(compose.includes('${RELAY_ENV_FILE}'));
    assert.ok(compose.includes('image: keep:this'));
    assert.ok(!compose.includes('latest'));
    assert.notEqual(run('capture-baseline.sh', ['--root', root, '--infra', infra], fakeEnv(root)).status, 0);
  });
}

test('baseline refuses missing web mount without recording a release', t => {
  const root = fixture(t);
  fake(root, 'docker', 'process.stdout.write("missing");');
  assert.notEqual(run('capture-baseline.sh', ['--root', root], fakeEnv(root)).status, 0);
  assert.ok(!existsSync(join(root, '.current')));
});

const sha = 'a'.repeat(40), image = `ghcr.io/example/poweur-relay@sha256:${'b'.repeat(64)}`;
for (const health of ['healthy', 'http-failure', 'wrong-sha', 'readonly', 'invalid-json']) {
  test(`deployment health gate: ${health}`, t => {
    const root = fixture(t), previous = join(root, '.releases', 'c'.repeat(40));
    write(join(root, 'apps/api/.env.prod'));
    write(join(root, '.observability.env'));
    write(join(root, '.smoke/config.toml'));
    write(join(previous, '.relay-image'), image);
    symlinkSync(previous, join(root, '.current'));
    write(join(root, 'archive/docker-compose.prod.yml'), 'services: {}');
    write(join(root, 'archive/deploy/infra/docker-compose.yml'), 'services: {}');
    assert.equal(spawnSync('tar', ['-cf', join(root, 'release.tar'), '-C', join(root, 'archive'), '.']).status, 0);
    fake(root, 'git', `if (process.argv[2] === 'archive') process.stdout.write(require('node:fs').readFileSync(process.env.TEST_ROOT + '/release.tar'));`);
    fake(root, 'flock', 'process.exit(0);');
    fake(root, 'sleep', 'process.exit(0);');
    fake(root, 'mv', `require('node:fs').renameSync(process.argv.at(-2), process.argv.at(-1));`);
    fake(root, 'docker', `
      const fs = require('node:fs'), args = process.argv.slice(2);
      fs.appendFileSync(process.env.TEST_ROOT + '/commands', args.join(' ') + '\\n');
      if (args[0] === 'inspect' && args[1] === '-f') process.stdout.write('172.18.0.2');
      if (args.includes('http://poweur-relay:8080/health')) {
        const mode = process.env.TEST_HEALTH;
        if (mode === 'http-failure') process.exit(1);
        if (mode === 'invalid-json') process.stdout.write('invalid');
        else process.stdout.write(JSON.stringify({versionHash: mode === 'wrong-sha' ? 'old' : process.env.TEST_SHA, storage: {writable: mode !== 'readonly'}}));
      }
    `);
    const env = { ...fakeEnv(root), POWEUR_APP_DIR: root, TEST_SHA: sha, TEST_HEALTH: health };
    const result = run('release.sh', [sha, image], env);
    const calls = readFileSync(join(root, 'commands'), 'utf8');
    if (health === 'healthy') {
      success(result);
      assert.equal(realpathSync(join(root, '.current')), join(root, '.releases', sha));
      assert.equal(readFileSync(join(root, '.previous-release'), 'utf8').trim(), previous);
      assert.ok(calls.includes('--entrypoint /poweur-smoke'));
      success(run('rollback.sh', [], env));
      assert.equal(realpathSync(join(root, '.current')), previous);
    } else {
      assert.notEqual(result.status, 0);
      assert.equal(realpathSync(join(root, '.current')), previous);
      assert.ok(calls.includes(`${previous}/docker-compose.prod.yml up`));
      assert.ok(result.stderr.includes('Restored'));
    }
  });
}
