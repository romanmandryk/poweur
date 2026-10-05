import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const root = new URL('../infra/', import.meta.url);
const board = JSON.parse(readFileSync(new URL('grafana/dashboards/poweur-site.json', root), 'utf8'));
const exprs = board.panels.flatMap(p => (p.targets ?? []).map(t => t.expr));

test('the site dashboard is read-only, unique and fits the grid', () => {
  assert.equal(board.uid, 'poweur-site');
  assert.equal(board.editable, false);
  const ids = board.panels.map(p => p.id);
  assert.equal(new Set(ids).size, ids.length);
  for (const p of board.panels) assert.ok(p.gridPos.x + p.gridPos.w <= 24, `${p.title} overflows the grid`);
});

test('it only reads the website and docs streams, never the app, the bridge or an ID', () => {
  assert.ok(exprs.length > 10);
  for (const expr of exprs) {
    assert.match(expr, /source="faro"/);
    assert.match(expr, /app=~"\$app"/);
    assert.doesNotMatch(expr, /poweur-web|poweur-oauth|user_id/);
  }
  const app = board.templating.list.find(v => v.name === 'app');
  assert.equal(app.allValue, 'poweur-site|poweur-docs');
  assert.deepEqual(app.options.map(o => o.value).filter(v => v !== '$__all'), ['poweur-site', 'poweur-docs']);
});

test('the claim and call-to-action events the site sends are charted', () => {
  assert.ok(exprs.some(e => e.includes('event_name="claim_submit"')));
  assert.ok(exprs.some(e => e.includes('event_name="cta_click"')));
});
