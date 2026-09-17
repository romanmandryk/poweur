import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const root = new URL('../infra/', import.meta.url);
const board = JSON.parse(readFileSync(new URL('grafana/dashboards/poweur-host.json', root), 'utf8'));
const queries = board.panels.flatMap((p) => (p.targets ?? []).map((t) => ({ panel: p.title, expr: t.expr })));

test('host dashboard is read-only, unique and fits the grid', () => {
  assert.equal(board.uid, 'poweur-host');
  assert.equal(board.editable, false);
  const ids = board.panels.map((p) => p.id);
  assert.equal(new Set(ids).size, ids.length);
  for (const p of board.panels) assert.ok(p.gridPos.x + p.gridPos.w <= 24, `${p.title} overflows the grid`);
});

test('host dashboard only queries node-exporter and blackbox', () => {
  assert.ok(queries.length > 0);
  for (const p of board.panels) {
    assert.equal(p.datasource?.uid, 'prometheus', `${p.title} must query prometheus`);
  }
  for (const { panel, expr } of queries) {
    assert.match(expr, /job="(node|blackbox)"/, `${panel} must select job node or blackbox`);
    assert.doesNotMatch(expr, /poweur_|actor|loki/, `${panel} must stay on host/probe metrics`);
  }
});
