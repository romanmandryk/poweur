import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const script = new URL('../digest/poweur-digest.sh', import.meta.url).pathname;
const dir = mkdtempSync(join(tmpdir(), 'digest-'));

// A fake Prometheus: values come from env (IDS, NEW, DAY, MONTH); every query
// is matched on its text like the real expressions.
const prom = join(dir, 'prom.sh');
writeFileSync(prom, `#!/usr/bin/env bash
case "$1" in
  *hosted_identities*) v=\${IDS:-0} ;;
  *registration.create*) v=\${NEW:-0} ;;
  *"[30d]"*) v=\${MONTH:-0} ;;
  *) v=\${DAY:-0} ;;
esac
echo "{\\"data\\":{\\"result\\":[{\\"value\\":[0,\\"$v\\"]}]}}"
`);
chmodSync(prom, 0o755);

// A fake curl that records what would be sent.
const bin = join(dir, 'bin');
execFileSync('mkdir', [bin]);
writeFileSync(join(bin, 'curl'), `#!/usr/bin/env bash\nprintf '%s\\n' "$@" > "${dir}/curl.args"\n`);
chmodSync(join(bin, 'curl'), 0o755);

const run = (env, ...args) => spawnSync('bash', [script, ...args], {
  encoding: 'utf8',
  env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, PROM_QUERY_CMD: prom, DIGEST_ENV: '/nonexistent', NTFY_URL: 'https://ntfy.example/topic', ...env },
});

test('dry run lists every metric with totals and today', () => {
  const r = run({ IDS: '1204', NEW: '7', DAY: '3', MONTH: '12345' }, '--dry-run');
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /^IDs: 1,204 total, \+7 today$/m);
  for (const name of ['Messages', 'Relay sign-ins \\(CLI \\+ web\\)', 'OAuth sign-ins', 'Guestbook sign-ins', 'Guestbook entries', 'Hello bot messages']) {
    assert.match(r.stdout, new RegExp(`^${name}: \\+3 today, 12,345 in 30d$`, 'm'));
  }
});

test('stays quiet when nothing grew', () => {
  const r = run({ IDS: '1204', MONTH: '50' });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /nothing grew/);
  assert.throws(() => execFileSync('test', ['-e', join(dir, 'curl.args')]), 'no push was sent');
});

test('sends one push to the topic when something grew, and --force sends anyway', () => {
  let r = run({ IDS: '10', NEW: '1' });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /sent/);
  const args = execFileSync('cat', [join(dir, 'curl.args')], { encoding: 'utf8' });
  assert.match(args, /https:\/\/ntfy\.example\/topic/);
  assert.match(args, /IDs: 10 total, \+1 today/);
  execFileSync('rm', [join(dir, 'curl.args')]);
  r = run({ IDS: '10' }, '--force');
  assert.match(r.stdout, /sent/);
});
