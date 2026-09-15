import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const root = new URL('../infra/', import.meta.url);
const board = JSON.parse(readFileSync(new URL('grafana/dashboards/poweur-growth.json', root), 'utf8'));
const rules = readFileSync(new URL('prometheus/rules.yml', root), 'utf8');
const recorded = new Set([...rules.matchAll(/record:\s*(\S+)/g)].map(m => m[1]));
const queries = board.panels.flatMap(p => (p.targets ?? []).map(t => ({ panel: p.title, expr: t.expr, ds: t.datasource?.type ?? p.datasource?.type })));

test('public growth dashboard is read-only, unique and fits the grid', () => {
  assert.equal(board.uid, 'poweur-growth');
  assert.equal(board.editable, false);
  const ids = board.panels.map(p => p.id);
  assert.equal(new Set(ids).size, ids.length);
  for (const p of board.panels) assert.ok(p.gridPos.x + p.gridPos.w <= 24, `${p.title} overflows the grid`);
});

test('public growth dashboard only queries production aggregates', () => {
  assert.ok(queries.length > 0);
  for (const { panel, expr, ds } of queries) {
    assert.equal(ds, 'prometheus', `${panel} must not use logs`);
    assert.doesNotMatch(expr, /actor|client_ip|identity_mode|route=|\$__/, `${panel} references private fields`);
    for (const [name] of expr.matchAll(/\bpoweur_[a-z0-9_]+/g)) {
      if (name === 'poweur_heartbeat_timestamp_seconds') {
        assert.match(expr, /deployment_environment_name="production"/, `${panel} must select production`);
        continue;
      }
      assert.ok(name.startsWith('poweur_growth_'), `${panel} queries raw metric ${name}`);
      assert.ok(recorded.has(name), `${panel} uses unrecorded ${name}`);
    }
  }
});

test('every growth recording rule filters production and is used', () => {
  const used = new Set(queries.flatMap(q => [...q.expr.matchAll(/\bpoweur_growth_[a-z0-9_]+/g)].map(m => m[0])));
  for (const name of recorded) {
    if (!name.startsWith('poweur_growth_')) continue;
    const block = rules.slice(rules.indexOf(`record: ${name}\n`)).split('- record:')[0];
    assert.match(block, /deployment_environment_name="production"/, `${name} must exclude dev/test`);
    assert.ok(used.has(name), `${name} is recorded but not shown`);
  }
});
