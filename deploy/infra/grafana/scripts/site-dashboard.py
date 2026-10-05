#!/usr/bin/env python3
"""Generates dashboards/poweur-site.json: the poweur.org website and docs, from the same Grafana
Faro stream as the web app dashboard (Loki {source="faro"}), but only the public site.

Rerun after editing: python3 deploy/infra/grafana/scripts/site-dashboard.py
Labels: app="poweur-site" (the marketing site, legal pages) and app="poweur-docs" (/docs).
Custom events from packages/faro/src/static.ts: cta_click (event_data_target = app | github |
docs | legal, event_data_from = the page) and claim_submit (the claim box; the typed name is
never recorded). Everything is anonymous: no IDs, no IPs, no cookies.
"""
import json
import os

LOKI = {"type": "loki", "uid": "loki"}
APP = '{source="faro", app=~"$app"'
PAGE_VIEWS = APP + ', kind="event"} | logfmt | event_name="page_view"'
EVENT = lambda name: APP + f', kind="event"}} | logfmt | event_name="{name}"'
EXCEPTIONS = APP + ', kind="exception"}'
VITALS = APP + ', kind="measurement"} | logfmt | type="web-vitals"'

panels = []
y = 0


def row(title):
    global y
    panels.append({"id": len(panels) + 1, "type": "row", "title": title, "collapsed": False, "gridPos": {"x": 0, "y": y, "w": 24, "h": 1}, "panels": []})
    y += 1


def panel(kind, title, exprs, x, w, h, instant=False, unit=None, options=None, legend=None, description=None, display=None):
    targets = []
    for i, expr in enumerate(exprs if isinstance(exprs, list) else [exprs]):
        t = {"refId": chr(65 + i), "expr": expr, "datasource": LOKI, "queryType": "instant" if instant else "range"}
        if legend:
            t["legendFormat"] = legend
        targets.append(t)
    p = {
        "id": len(panels) + 1,
        "title": title,
        "type": kind,
        "datasource": LOKI,
        "gridPos": {"x": x, "y": y, "w": w, "h": h},
        "targets": targets,
        "fieldConfig": {"defaults": {}, "overrides": []},
        "options": options or {},
    }
    if unit:
        p["fieldConfig"]["defaults"]["unit"] = unit
    if display:
        p["fieldConfig"]["defaults"]["displayName"] = display
    if kind == "table":
        p["transformations"] = [{"id": "organize", "options": {"excludeByName": {"Time": True}, "renameByName": {"Value #A": "Count"}}}]
    if description:
        p["description"] = description
    panels.append(p)
    return p


stat = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}, "colorMode": "value", "graphMode": "none", "textMode": "value"}
table = {"showHeader": True, "sortBy": [{"displayName": "Count", "desc": True}]}
donut = {"reduceOptions": {"calcs": ["sum"], "values": False}, "pieType": "donut", "legend": {"displayMode": "table", "placement": "right", "values": ["value"]}}

row("Visitors")
panel("stat", "Page views", f"sum(count_over_time({PAGE_VIEWS} [$__range]))", 0, 6, 4, instant=True, options=stat,
      description="Every page shown on poweur.org and its docs, including client-side navigation in the docs. There are no cookies or IDs, so this counts views, not people.")
panel("stat", "Home page views", f'sum(count_over_time({PAGE_VIEWS} | event_data_screen="/" [$__range]))', 6, 6, 4, instant=True, options=stat)
panel("stat", "Claim clicks", f'sum(count_over_time({EVENT("claim_submit")} [$__range]))', 12, 6, 4, instant=True, options=stat,
      description="Submissions of the 'Claim' box on the site. What was typed is never recorded.")
panel("stat", "Errors", f"sum(count_over_time({EXCEPTIONS} [$__range]))", 18, 6, 4, instant=True, options=stat)
y += 4
panel("timeseries", "Page views by app", f"sum by (app) (count_over_time({PAGE_VIEWS} [$__interval]))", 0, 16, 8, legend="{{app}}",
      description="poweur-site is the marketing site and legal pages; poweur-docs is /docs.")
panel("piechart", "Browsers", f"sum by (browser_name) (count_over_time({PAGE_VIEWS} [$__interval]))", 16, 8, 8, legend="{{browser_name}}", options=donut)
y += 8
panel("table", "Top pages", f"topk(25, sum by (app, event_data_screen) (count_over_time({PAGE_VIEWS} [$__range])))", 0, 12, 10, instant=True, options=table)
panel("table", "Calls to action", f'topk(20, sum by (event_data_target, event_data_from) (count_over_time({EVENT("cta_click")} [$__range])))', 12, 12, 10, instant=True, options=table,
      description="Clicks to the app (app), GitHub, the docs and the legal pages, with the page they were on.")
y += 10
panel("piechart", "Mobile vs desktop", f'sum by (device) (label_replace(label_replace(count_over_time({PAGE_VIEWS} [$__interval]), "device", "mobile", "browser_mobile", "true"), "device", "desktop", "browser_mobile", "false"))', 0, 8, 7, legend="{{device}}", options=donut)
panel("table", "Operating systems", f"topk(10, sum by (browser_os) (count_over_time({PAGE_VIEWS} [$__range])))", 8, 8, 7, instant=True, options=table)
panel("table", "Languages", f"topk(10, sum by (browser_language) (count_over_time({PAGE_VIEWS} [$__range])))", 16, 8, 7, instant=True, options=table)
y += 7

row("Performance (web vitals, p75)")
for i, (metric, title, unit) in enumerate([
    ("lcp", "Largest contentful paint", "ms"),
    ("inp", "Interaction to next paint", "ms"),
    ("cls", "Cumulative layout shift", "none"),
    ("ttfb", "Time to first byte", "ms"),
    ("fcp", "First contentful paint", "ms"),
]):
    panel(
        "timeseries",
        f"{title} (p75)",
        f'quantile_over_time(0.75, {VITALS} | value_{metric}!="" | unwrap value_{metric} | __error__="" [$__interval]) by (app)',
        (i % 3) * 8, 8, 7, unit=unit, legend="{{app}}",
    )
    if i % 3 == 2:
        y += 7
y += 7

row("Errors")
panel("table", "Top errors", f"topk(25, sum by (app, type, value) (count_over_time({EXCEPTIONS} | logfmt [$__range])))", 0, 24, 8, instant=True, options=table)
y += 8
panel("logs", "Recent errors", EXCEPTIONS, 0, 24, 9,
      options={"showTime": True, "wrapLogMessage": True, "sortOrder": "Descending", "enableLogDetails": True, "dedupStrategy": "none"})
y += 9

dashboard = {
    "uid": "poweur-site",
    "title": "Poweur Site: poweur.org and docs",
    "description": "Anonymous analytics for the public website and the docs (Grafana Faro). The web app, the OAuth bridge and the relay have their own dashboards.",
    "schemaVersion": 39,
    "version": 1,
    "refresh": "1m",
    "timezone": "utc",
    "editable": False,
    "annotations": {"list": []},
    "time": {"from": "now-7d", "to": "now"},
    "templating": {
        "list": [
            {
                "name": "app",
                "label": "Part of the site",
                "type": "custom",
                "query": "poweur-site,poweur-docs",
                "includeAll": True,
                "multi": True,
                "allValue": "poweur-site|poweur-docs",
                "current": {"selected": True, "text": ["All"], "value": ["$__all"]},
                "options": [
                    {"selected": True, "text": "All", "value": "$__all"},
                    {"selected": False, "text": "poweur-site", "value": "poweur-site"},
                    {"selected": False, "text": "poweur-docs", "value": "poweur-docs"},
                ],
            }
        ]
    },
    "panels": panels,
}

out = os.path.join(os.path.dirname(__file__), "..", "dashboards", "poweur-site.json")
with open(out, "w") as f:
    json.dump(dashboard, f, indent=2)
    f.write("\n")
print(f"wrote {os.path.normpath(out)} ({len(panels)} panels)")
