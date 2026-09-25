#!/usr/bin/env python3
"""Generates dashboards/poweur-web.json: browser analytics and errors from
Grafana Faro (packages/faro -> Alloy faro.receiver -> Loki {source="faro"}).

Rerun after editing: python3 deploy/infra/grafana/scripts/web-dashboard.py
Labels: source="faro", kind (event|exception|measurement|log), app
(poweur-web|poweur-oauth|poweur-site|poweur-docs). Everything else is logfmt.
"""
import json
import os

LOKI = {"type": "loki", "uid": "loki"}
SEL = '{source="faro", app=~"$app"'
PAGE_VIEWS = SEL + ', kind="event"} | logfmt | event_name="page_view"'
EXCEPTIONS = SEL + ', kind="exception"}'
VITALS = SEL + ', kind="measurement"} | logfmt | type="web-vitals"'

panels = []
y = 0


def row(title):
    global y
    panels.append({"type": "row", "title": title, "collapsed": False, "gridPos": {"x": 0, "y": y, "w": 24, "h": 1}, "panels": []})
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
        # Instant queries ignore legendFormat; name slices from the label.
        p["fieldConfig"]["defaults"]["displayName"] = display
    if kind == "table":
        # One row per label set: no time column, a readable count.
        p["transformations"] = [{"id": "organize", "options": {"excludeByName": {"Time": True}, "renameByName": {"Value #A": "Count"}}}]
    if description:
        p["description"] = description
    panels.append(p)
    return p


stat = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}, "colorMode": "value", "graphMode": "none", "textMode": "value"}
table = {"showHeader": True, "sortBy": [{"displayName": "Count", "desc": True}]}

row("Traffic")
panel("stat", "Page views", f"sum(count_over_time({PAGE_VIEWS} [$__range]))", 0, 5, 4, instant=True, options=stat)
panel("stat", "Errors", f"sum(count_over_time({EXCEPTIONS} [$__range]))", 5, 5, 4, instant=True, options=stat)
panel(
    "stat",
    "Errors per 100 page views",
    f"100 * sum(count_over_time({EXCEPTIONS} [$__range])) / sum(count_over_time({PAGE_VIEWS} [$__range]))",
    10, 5, 4, instant=True, options=stat,
)
panel(
    "stat",
    "Signals with an ID (opted in)",
    f'sum(count_over_time({SEL}}} |= "user_id=" [$__range])) / sum(count_over_time({SEL}}} [$__range]))',
    15, 5, 4, instant=True, unit="percentunit", options=stat,
    description="Share of signals from users who ticked 'Include my ID in diagnostics'. Everything else is anonymous.",
)
panel("stat", "Apps reporting", f"count(sum by (app) (count_over_time({SEL}}} [$__range])))", 20, 4, 4, instant=True, options=stat)
y += 4
panel("timeseries", "Page views by app", f"sum by (app) (count_over_time({PAGE_VIEWS} [$__interval]))", 0, 16, 8, legend="{{app}}")
# Pies: range queries (one series per label, named by the legend) summed over
# the dashboard's range. Instant Loki queries arrive as a single table instead.
panel("piechart", "Browsers", f"sum by (browser_name) (count_over_time({PAGE_VIEWS} [$__interval]))", 16, 8, 8, legend="{{browser_name}}",
      options={"reduceOptions": {"calcs": ["sum"], "values": False}, "pieType": "donut", "legend": {"displayMode": "table", "placement": "right", "values": ["value"]}})
y += 8
panel("table", "Top screens and pages", f"topk(20, sum by (app, event_data_screen) (count_over_time({PAGE_VIEWS} [$__range])))", 0, 12, 10, instant=True, options=table)
panel("table", "Top actions (web app)", f'topk(20, sum by (event_name) (count_over_time({SEL}, kind="event"}} | logfmt | event_name!="page_view" | event_name!="view_changed" | event_name!="app-start" [$__range])))',
      12, 12, 10, instant=True, options=table)
y += 10
panel("piechart", "Mobile vs desktop", f'sum by (device) (label_replace(label_replace(count_over_time({PAGE_VIEWS} [$__interval]), "device", "mobile", "browser_mobile", "true"), "device", "desktop", "browser_mobile", "false"))', 0, 8, 7, legend="{{device}}",
      options={"reduceOptions": {"calcs": ["sum"], "values": False}, "pieType": "donut", "legend": {"displayMode": "table", "placement": "right", "values": ["value"]}})
panel("table", "App versions seen", f"sum by (app, app_version) (count_over_time({SEL}}} | logfmt [$__range]))", 8, 8, 7, instant=True, options=table)
panel("table", "Operating systems", f"topk(10, sum by (browser_os) (count_over_time({PAGE_VIEWS} [$__range])))", 16, 8, 7, instant=True, options=table)
y += 7

row("Errors")
panel("timeseries", "Errors by app", f"sum by (app) (count_over_time({EXCEPTIONS} [$__interval]))", 0, 12, 8, legend="{{app}}")
panel("timeseries", "Errors by type", f"sum by (type) (count_over_time({EXCEPTIONS} | logfmt [$__interval]))", 12, 12, 8, legend="{{type}}")
y += 8
panel("table", "Top errors", f"topk(25, sum by (app, type, value) (count_over_time({EXCEPTIONS} | logfmt [$__range])))", 0, 24, 10, instant=True, options=table,
      description="Messages are scrubbed: Poweur IDs and domains show as <id> unless the user opted in.")
y += 10
panel("logs", "Recent errors", EXCEPTIONS, 0, 24, 10,
      options={"showTime": True, "wrapLogMessage": True, "sortOrder": "Descending", "enableLogDetails": True, "dedupStrategy": "none"})
y += 10

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

dashboard = {
    "uid": "poweur-web",
    "title": "Poweur Web: analytics & errors",
    "description": "Browser telemetry from Grafana Faro (packages/faro): the web app, the OAuth bridge's pages, the website and the docs. Anonymous unless a user opted in.",
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
                "label": "App",
                "type": "query",
                "datasource": LOKI,
                "query": {"label": "app", "stream": '{source="faro"}', "type": 1, "refId": "LokiVariableQueryEditor-VariableQuery"},
                "definition": 'label_values({source="faro"}, app)',
                "includeAll": True,
                "multi": True,
                "allValue": ".+",
                "current": {"selected": True, "text": ["All"], "value": ["$__all"]},
                "refresh": 2,
                "sort": 1,
            }
        ]
    },
    "panels": panels,
}

out = os.path.join(os.path.dirname(__file__), "..", "dashboards", "poweur-web.json")
with open(out, "w") as f:
    json.dump(dashboard, f, indent=2)
    f.write("\n")
print(f"wrote {os.path.normpath(out)} ({len(panels)} panels)")
