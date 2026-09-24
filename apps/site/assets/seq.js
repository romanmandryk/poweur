// Tiny sequence-diagram renderer. Usage:
// <figure class="diagram" data-seq><script type="application/json">{ lanes: [...], steps: [...] }</script></figure>
// lanes: [{ id, title, sub?, tone? }]   tone: violet | indigo | teal | amber | ink
// steps: { from, to, label, sub?, tone?, dashed? }   arrow between lanes (from === to draws a self-action)
//        { note, over: [laneA, laneB], tone? }       note box spanning lanes
const TONES = {
  violet: { fill: "rgba(114,76,207,.22)", stroke: "#A890FE", text: "#DBD5FF" },
  indigo: { fill: "rgba(59,74,164,.28)", stroke: "#889EF8", text: "#CED9FF" },
  teal: { fill: "rgba(5,154,148,.18)", stroke: "#53B8B2", text: "#A7E9E4" },
  amber: { fill: "rgba(213,151,64,.16)", stroke: "#EDB774", text: "#FFD29B" },
  ink: { fill: "rgba(255,255,255,.05)", stroke: "#86868A", text: "#DADADE" },
};
const esc = (s) => String(s).replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" })[c]);

function render(fig) {
  const spec = JSON.parse(fig.querySelector("script").textContent);
  const W = 1000, pad = 20, headH = 64, rowH = 58, top = headH + 36;
  const n = spec.lanes.length;
  const laneW = (W - pad * 2) / n;
  const x = {};
  spec.lanes.forEach((l, i) => (x[l.id] = pad + laneW * i + laneW / 2));
  const H = top + spec.steps.length * rowH + 30;
  const uid = Math.random().toString(36).slice(2, 7);
  let s = `<svg viewBox="0 0 ${W} ${H}" xmlns="http://www.w3.org/2000/svg" font-family="Inter, system-ui, sans-serif" role="img" aria-label="${esc(spec.title || "Sequence diagram")}"><defs>`;
  for (const [k, t] of Object.entries(TONES))
    s += `<marker id="a-${k}-${uid}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" fill="${t.stroke}"/></marker>`;
  s += `</defs>`;

  // Lane headers + lifelines
  spec.lanes.forEach((l) => {
    const t = TONES[l.tone || "ink"], bw = Math.min(laneW - 16, 190), cx = x[l.id];
    s += `<line x1="${cx}" y1="${headH + 10}" x2="${cx}" y2="${H - 10}" stroke="rgba(255,255,255,.12)" stroke-dasharray="3 5"/>`;
    s += `<rect x="${cx - bw / 2}" y="10" width="${bw}" height="${headH}" rx="12" fill="${t.fill}" stroke="${t.stroke}" stroke-opacity=".6"/>`;
    s += `<text x="${cx}" y="${l.sub ? 38 : 47}" text-anchor="middle" fill="#fff" font-size="14" font-weight="600">${esc(l.title)}</text>`;
    if (l.sub) s += `<text x="${cx}" y="58" text-anchor="middle" fill="${t.text}" font-size="11.5" font-family="JetBrains Mono, monospace">${esc(l.sub)}</text>`;
  });

  spec.steps.forEach((st, i) => {
    const y = top + i * rowH + 20;
    const t = TONES[st.tone || "violet"];
    if (st.note) {
      const xs = st.over.map((id) => x[id]);
      const cx = (Math.min(...xs) + Math.max(...xs)) / 2;
      const w = Math.max(Math.max(...xs) - Math.min(...xs) + 140, st.note.length * 7 + 32);
      const x1 = Math.max(pad, Math.min(cx - w / 2, W - pad - w)), x2 = x1 + w;
      s += `<rect x="${x1}" y="${y - 20}" width="${x2 - x1}" height="40" rx="10" fill="${t.fill}" stroke="${t.stroke}" stroke-opacity=".5" stroke-dasharray="4 4"/>`;
      s += `<text x="${(x1 + x2) / 2}" y="${y + 5}" text-anchor="middle" fill="${t.text}" font-size="13">${esc(st.note)}</text>`;
      return;
    }
    if (st.from === st.to) {
      const cx = x[st.from];
      const w = Math.max(st.label.length * 6.8, (st.sub || "").length * 6.4) + 20;
      s += `<rect x="${cx - w / 2}" y="${y - 17}" width="${w}" height="${st.sub ? 40 : 30}" rx="8" fill="#0B0A10" stroke="${t.stroke}" stroke-opacity=".7"/>`;
      s += `<text x="${cx}" y="${y + 2}" text-anchor="middle" fill="${t.text}" font-size="12.5">${esc(st.label)}</text>`;
      if (st.sub) s += `<text x="${cx}" y="${y + 17}" text-anchor="middle" fill="#86868A" font-size="10.5" font-family="JetBrains Mono, monospace">${esc(st.sub)}</text>`;
      return;
    }
    const x1 = x[st.from], x2 = x[st.to], dir = x2 > x1 ? 1 : -1;
    const mid = (x1 + x2) / 2;
    s += `<line x1="${x1 + dir * 6}" y1="${y + 6}" x2="${x2 - dir * 6}" y2="${y + 6}" stroke="${t.stroke}" stroke-width="1.6" ${st.dashed ? 'stroke-dasharray="6 5"' : ""} marker-end="url(#a-${st.tone || "violet"}-${uid})"/>`;
    s += `<circle cx="${x1}" cy="${y + 6}" r="3.5" fill="${t.stroke}"/>`;
    s += `<text x="${mid}" y="${y - 3}" text-anchor="middle" fill="#fff" font-size="13" font-weight="500">${esc(st.label)}</text>`;
    if (st.sub) s += `<text x="${mid}" y="${y + 24}" text-anchor="middle" fill="${t.text}" font-size="11" font-family="JetBrains Mono, monospace">${esc(st.sub)}</text>`;
  });
  s += `</svg>`;
  fig.insertAdjacentHTML("beforeend", s);
}

document.querySelectorAll("[data-seq]").forEach(render);
