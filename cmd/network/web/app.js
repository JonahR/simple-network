// simple-network operations dashboard.
//
// The switch publishes every step of every transaction over /api/stream. This
// page keeps a map of records, animates each live transaction across the
// topology as its steps arrive, and shows a per-step trace for the selected one.

const records = new Map(); // network_txn_id -> record
let overview = null;
let selectedId = null;
let following = true;

const STEP_NAMES = {
  validate: "Validate",
  route: "Route",
  detokenize: "Token vault",
  translate_pin: "PIN translate",
  breaker: "Breaker",
  send: "Send",
  issuer: "Issuer",
  respond: "Respond",
  reversal: "Reversal 0420",
};
const ENTRY_SHORT = { "010": "Keyed", "901": "Swipe", "051": "Chip", "071": "Contactless", "812": "E-commerce", "100": "On file" };

// --- Formatting --------------------------------------------------------------

const ms = (us) => (us >= 10_000 ? `${Math.round(us / 1000)} ms` : `${(us / 1000).toFixed(1)} ms`);

function money(minor, currency) {
  const iso = overview?.currencies?.[currency] || "USD";
  return new Intl.NumberFormat(undefined, { style: "currency", currency: iso }).format(minor / 100);
}

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

function issuerName(id) {
  return overview?.issuers.find((i) => i.id === id)?.name || id || "—";
}

function modeLabel(req) {
  const parts = [ENTRY_SHORT[req.de22_pos_entry_mode] || req.de22_pos_entry_mode];
  if (req.wallet_provider) parts.push(overview?.wallets?.[req.wallet_provider] || req.wallet_provider);
  if (req.de52_pin_data) parts.push("PIN");
  return parts.join(" · ");
}

// statusHTML renders a response as icon + code + text, never color alone.
function statusHTML(rec) {
  if (rec.status === "pending") return `<span class="status pending">… Pending</span>`;
  const ok = rec.status === "approved";
  return `<span class="status ${ok ? "good" : "critical"}"><span aria-hidden="true">${ok ? "✓" : "✕"}</span><span class="code">${escapeHTML(rec.response_code)}</span> ${escapeHTML(rec.response_text)}</span>`;
}

// --- KPIs, response codes, issuers ----------------------------------------------

function issuerHealth(i) {
  if (i.mode === "unreachable") return { cls: "critical", icon: "✕", text: "Unreachable" };
  if (i.breaker === "open") return { cls: "critical", icon: "✕", text: "Breaker open" };
  if (i.breaker === "half_open") return { cls: "warning", icon: "▲", text: "Breaker testing" };
  if (i.mode === "down") return { cls: "critical", icon: "✕", text: "Down" };
  if (i.mode === "slow") return { cls: "warning", icon: "▲", text: "Slow" };
  return { cls: "good", icon: "●", text: "Healthy" };
}

function renderKPIs() {
  const s = overview.stats;
  const healthy = overview.issuers.filter((i) => issuerHealth(i).cls === "good").length;
  const done = s.approved + s.declined;
  const tiles = [
    ["Authorizations", s.total, s.pending ? `${s.pending} in flight` : "since the network started"],
    ["Approval rate", done ? `${(s.approval_rate * 100).toFixed(1)}%` : "—", `${s.approved} approved · ${s.declined} declined`],
    ["Latency p50", done ? ms(s.p50_us) : "—", "acquirer in → response out"],
    ["Latency p95", done ? ms(s.p95_us) : "—", "includes issuer time"],
    ["Issuers healthy", `${healthy} / ${overview.issuers.length}`, overview.issuers.map((i) => `${i.id} ${issuerHealth(i).text.toLowerCase()}`).join(" · ")],
  ];
  document.getElementById("kpis").innerHTML = tiles
    .map(([label, value, sub]) => `<div class="kpi"><div class="k-label">${label}</div><div class="k-value">${escapeHTML(value)}</div><div class="k-sub">${escapeHTML(sub)}</div></div>`)
    .join("");
}

function renderCodes() {
  const entries = Object.entries(overview.stats.by_code).sort((a, b) => b[1] - a[1]);
  const el = document.getElementById("codes");
  if (!entries.length) {
    el.className = "empty";
    el.textContent = "No responses yet.";
    return;
  }
  const total = entries.reduce((n, [, c]) => n + c, 0);
  const max = entries[0][1];
  el.className = "bars";
  el.innerHTML = entries
    .map(([code, n]) => {
      const text = overview.response_codes[code]?.text || "Unknown";
      const tip = `${code} ${text}: ${n} of ${total} (${((n / total) * 100).toFixed(0)}%)`;
      return `<div class="b-label"><span class="mono">${escapeHTML(code)}</span> ${escapeHTML(text)}</div>
        <div class="b-track" data-tip="${escapeHTML(tip)}"><div class="b-bar" style="width:${(n / max) * 100}%"></div></div>
        <div class="b-value">${n}</div>`;
    })
    .join("");
}

function renderIssuers() {
  document.getElementById("timeout").textContent = overview.issuer_timeout;
  document.getElementById("issuers").innerHTML = `
    <div class="table-wrap"><table>
      <thead><tr><th>Issuer</th><th>Health</th><th class="num">Auths</th><th class="num">Approved</th><th class="num">Avg time</th><th>Simulate</th></tr></thead>
      <tbody>${overview.issuers
        .map((i) => {
          const h = issuerHealth(i);
          const rate = i.stats.total ? `${((i.stats.approved / i.stats.total) * 100).toFixed(0)}%` : "—";
          const seg = ["normal", "slow", "down"]
            .map((m) => `<button type="button" data-issuer="${i.id}" data-mode="${m}" aria-pressed="${i.mode === m}">${m[0].toUpperCase() + m.slice(1)}</button>`)
            .join("");
          return `<tr>
            <td><b>${escapeHTML(i.name)}</b> <span class="muted">${i.id}</span></td>
            <td><span class="status ${h.cls}"><span aria-hidden="true">${h.icon}</span>${h.text}</span></td>
            <td class="num">${i.stats.total}</td>
            <td class="num">${rate}</td>
            <td class="num">${i.stats.total ? ms(i.stats.avg_issuer_us) : "—"}</td>
            <td><span class="seg" role="group" aria-label="Simulate ${escapeHTML(i.name)}">${seg}</span></td>
          </tr>`;
        })
        .join("")}</tbody>
    </table></div>`;
}

document.getElementById("issuers").addEventListener("click", async (e) => {
  const b = e.target.closest("button[data-mode]");
  if (!b) return;
  await fetch(`/api/sim/issuers/${b.dataset.issuer}/mode`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ mode: b.dataset.mode }),
  });
  refreshOverview();
});

function renderRouting() {
  document.getElementById("bin-version").textContent = `version ${overview.bins.version}`;
  document.getElementById("bins").innerHTML = overview.bins.entries
    .map((e) => `<tr>
      <td class="mono">${e.prefix}</td>
      <td>${e.token_range ? "Token vault" : escapeHTML(issuerName(e.issuer_id))}</td>
      <td>${e.token_range ? "—" : escapeHTML(overview.product_names[e.product] || e.product)}</td>
      <td>${escapeHTML(e.label)}</td>
    </tr>`)
    .join("");
  document.getElementById("tokens").innerHTML = overview.tokens
    .map((t) => `<tr>
      <td class="mono">${t.token_masked}</td>
      <td>${escapeHTML(t.wallet)}</td>
      <td class="mono">${t.pan}</td>
      <td class="mono">${t.token_expiry.slice(2)}/${t.token_expiry.slice(0, 2)}</td>
      <td>${t.active ? `<span class="status good"><span aria-hidden="true">●</span>Active</span>` : `<span class="status critical"><span aria-hidden="true">✕</span>Suspended</span>`}</td>
      <td><button type="button" class="small" data-token="${t.token}" data-active="${!t.active}">${t.active ? "Suspend" : "Reactivate"}</button></td>
    </tr>`)
    .join("");
}

document.getElementById("tokens").addEventListener("click", async (e) => {
  const b = e.target.closest("button[data-token]");
  if (!b) return;
  await fetch(`/api/tokens/${b.dataset.token}/active`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ active: b.dataset.active === "true" }),
  });
  refreshOverview();
});

let overviewTimer = null;
async function refreshOverview() {
  overview = await (await fetch("/api/overview")).json();
  renderKPIs();
  renderCodes();
  renderIssuers();
  renderRouting();
  renderTopologyNodes();
}
// Coalesce refreshes when many transactions complete at once.
function scheduleOverview() {
  clearTimeout(overviewTimer);
  overviewTimer = setTimeout(refreshOverview, 250);
}

// --- Transaction feed ------------------------------------------------------------

let feedFrame = 0;
const freshIds = new Set();

function sortedRecords() {
  return [...records.values()].sort((a, b) => (a.network_txn_id < b.network_txn_id ? 1 : -1));
}

function scheduleFeed() {
  if (!feedFrame) feedFrame = requestAnimationFrame(renderFeed);
}

function renderFeed() {
  feedFrame = 0;
  const rows = sortedRecords().slice(0, 200);
  document.getElementById("feed-count").textContent = rows.length ? `${records.size} shown` : "";
  if (!rows.length) return;
  document.getElementById("feed").innerHTML = rows
    .map((r) => {
      const req = r.request;
      const amount = r.response && r.response.de4_amount && r.response.de4_amount !== req.de4_amount
        ? `${money(r.response.de4_amount, req.de49_currency)} <span class="muted">of ${money(req.de4_amount, req.de49_currency)}</span>`
        : money(req.de4_amount, req.de49_currency);
      const cls = [r.network_txn_id === selectedId ? "selected" : "", freshIds.has(r.network_txn_id) ? "fresh" : ""].join(" ");
      return `<tr data-id="${r.network_txn_id}" class="${cls}">
        <td>${new Date(r.received).toLocaleTimeString()}</td>
        <td class="mono">${escapeHTML(req.de2_pan)}</td>
        <td>${escapeHTML(modeLabel(req))}</td>
        <td>${escapeHTML(r.issuer_id || "—")}</td>
        <td class="num">${amount}</td>
        <td>${statusHTML(r)}</td>
        <td class="num">${r.status === "pending" ? "" : ms(r.latency_us)}</td>
      </tr>`;
    })
    .join("");
  freshIds.clear();
}

document.getElementById("feed").addEventListener("click", (e) => {
  const tr = e.target.closest("tr[data-id]");
  if (!tr) return;
  select(tr.dataset.id, false);
});

document.getElementById("follow").addEventListener("click", () => {
  following = true;
  const latest = sortedRecords()[0];
  if (latest) select(latest.network_txn_id, true);
});

function select(id, follow) {
  selectedId = id;
  following = follow;
  document.getElementById("follow").hidden = follow;
  if (!follow) history.replaceState(null, "", `#txn=${id}`);
  renderTrace();
  scheduleFeed();
}

// --- Trace -------------------------------------------------------------------

function renderTrace() {
  const el = document.getElementById("trace");
  const r = records.get(selectedId);
  if (!r) return;
  el.className = "";
  const req = r.request;
  // An in-flight transaction is scaled to the time elapsed so far.
  const elapsed = r.status === "pending" ? (Date.now() - new Date(r.received)) * 1000 : r.latency_us;
  const total = Math.max(elapsed, ...r.steps.map((s) => s.start_us + s.duration_us), 1);

  const rows = r.steps
    .map((s) => {
      const left = (s.start_us / total) * 100;
      const width = Math.max((s.duration_us / total) * 100, 0.4);
      const name = STEP_NAMES[s.name] || s.name;
      const tip = `${name}: ${ms(s.duration_us)}, starting at +${ms(s.start_us)}`;
      return `<div class="w-name">${escapeHTML(name)}</div>
        <div class="w-track" data-tip="${escapeHTML(tip)}"><div class="w-bar ${s.ok ? "" : "fail"}" style="left:${Math.min(left, 99.6)}%;width:${width}%"></div></div>
        <div class="w-detail">${s.ok ? "" : `<span class="fail-icon" aria-label="failed">✕ </span>`}${escapeHTML(s.detail)}</div>`;
    })
    .join("");

  el.innerHTML = `
    <div class="trace-head">
      <span class="amount">${money(req.de4_amount, req.de49_currency)}</span>
      ${statusHTML(r)}
      <span class="muted">${escapeHTML(modeLabel(req))} · ${r.issuer_id ? escapeHTML(issuerName(r.issuer_id)) : "not routed"}${r.status === "pending" ? "" : ` · ${ms(r.latency_us)}`}</span>
    </div>
    <div class="trace-id mono">${r.network_txn_id}${r.duplicates ? ` · ${r.duplicates} duplicate${r.duplicates > 1 ? "s" : ""} answered from cache` : ""}</div>
    <div class="waterfall">${rows}<div class="w-axis"><span>0</span><span>${ms(total)}</span></div></div>
    ${legsTable(r)}`;
}

// legsTable compares what the acquirer sent with what the network forwarded,
// so detokenization and PIN translation are visible.
function legsTable(r) {
  const a = r.request;
  const i = r.issuer_request || {};
  const resp = r.response || {};
  const fields = [
    ["DE2 Card / token", a.de2_pan, i.de2_pan],
    ["Token", "", i.token],
    ["DE14 Expiry", a.de14_expiry, i.de14_expiry],
    ["DE22 Entry mode", a.de22_pos_entry_mode, i.de22_pos_entry_mode],
    ["DE52 PIN block", a.de52_pin_data ? "under acquirer key" : "", i.de52_pin_data ? `under ${r.issuer_id} key` : ""],
    ["DE55 Cryptogram", a.de55_arqc, i.de55_arqc],
    ["Network txn ID", "", i.network_txn_id ? "added" : ""],
  ].filter(([, x, y]) => x || y);
  const response = [
    ["DE39 Response", resp.de39_response_code ? `${resp.de39_response_code} ${resp.response_text}` : ""],
    ["DE38 Auth code", resp.de38_auth_code],
    ["DE4 Approved", resp.de4_amount ? money(resp.de4_amount, a.de49_currency) : ""],
    ["DE2 Returned", resp.de2_pan],
  ].filter(([, v]) => v);
  return `<details class="legs" open>
    <summary>Messages</summary>
    <div class="table-wrap"><table>
      <thead><tr><th>Field</th><th>From acquirer</th><th>To issuer</th></tr></thead>
      <tbody>${fields
        .map(([k, x, y]) => {
          const sent = r.issuer_request ? y || "—" : r.status === "pending" ? "…" : "not sent";
          return `<tr class="${r.issuer_request && x !== y ? "changed" : ""}"><td>${k}</td><td class="mono">${escapeHTML(x || "—")}</td><td class="mono">${escapeHTML(sent)}</td></tr>`;
        })
        .join("")}</tbody>
    </table></div>
    ${response.length ? `<div class="table-wrap"><table><thead><tr><th>Field</th><th>0110 to acquirer</th></tr></thead><tbody>${response
      .map(([k, v]) => `<tr><td>${k}</td><td class="mono">${escapeHTML(v)}</td></tr>`)
      .join("")}</tbody></table></div>` : ""}
  </details>`;
}

// --- Topology ------------------------------------------------------------------

const SVG_NS = "http://www.w3.org/2000/svg";
const topo = document.getElementById("topo");
const P = {
  acq: { x: 200, y: 150 },
  swIn: { x: 300, y: 150 },
  swOut: { x: 700, y: 150 },
};
const ISSUER_POS = { FSB: { x: 800, y: 75 }, UCB: { x: 800, y: 225 } };
const STAGES = [
  ["validate", "Validate"],
  ["route", "Route"],
  ["detokenize", "Vault"],
  ["translate_pin", "PIN"],
  ["send", "Issuer"],
  ["respond", "Respond"],
];
const stageEls = {};

function el(tag, attrs, parent = topo) {
  const e = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  parent.append(e);
  return e;
}

function text(parent, x, y, cls, content, anchor = "start") {
  const t = el("text", { x, y, class: cls, "text-anchor": anchor }, parent);
  t.textContent = content;
  return t;
}

let topoNodes = {};
function buildTopology() {
  for (const p of Object.values(ISSUER_POS)) el("line", { x1: P.swOut.x, y1: P.swOut.y, x2: p.x, y2: p.y, class: "edge" });
  el("line", { x1: P.acq.x, y1: P.acq.y, x2: P.swIn.x, y2: P.swIn.y, class: "edge" });

  const acq = el("g", { class: "node" });
  el("rect", { x: 20, y: 105, width: 180, height: 90, rx: 10 }, acq);
  topoNodes.acqTitle = text(acq, 36, 136, "node-title", "Acquirer");
  topoNodes.acqSub = text(acq, 36, 158, "node-sub", "Merchant POS");
  topoNodes.acqCount = text(acq, 36, 178, "node-sub", "");

  const sw = el("g", { class: "node switch" });
  el("rect", { x: 300, y: 70, width: 400, height: 160, rx: 12 }, sw);
  text(sw, 320, 100, "node-title", "simple-network switch");
  topoNodes.swSub = text(sw, 320, 210, "counter", "");
  STAGES.forEach(([name, label], i) => {
    const g = el("g", { class: "stage" }, sw);
    const x = 316 + i * 62;
    el("rect", { x, y: 136, width: 56, height: 28, rx: 6 }, g);
    text(g, x + 28, 154, "", label, "middle");
    stageEls[name] = g;
  });

  for (const [id, p] of Object.entries(ISSUER_POS)) {
    const g = el("g", { class: "node" });
    el("rect", { x: 800, y: p.y - 45, width: 180, height: 90, rx: 10 }, g);
    topoNodes[id] = {
      title: text(g, 816, p.y - 18, "node-title", id),
      health: text(g, 816, p.y + 4, "health", ""),
      sub: text(g, 816, p.y + 26, "node-sub", ""),
    };
  }
  topoNodes.packets = el("g", {});
}

function renderTopologyNodes() {
  if (!overview) return;
  const latest = sortedRecords()[0];
  const acqID = latest?.acquirer_id || "100001";
  topoNodes.acqTitle.textContent = `Acquirer ${acqID}`;
  const merchant = latest?.request?.de43_merchant_name_location?.slice(0, 25).trim();
  topoNodes.acqSub.textContent = merchant ? `${merchant} POS` : "Merchant POS";
  topoNodes.acqCount.textContent = `${overview.stats.total} sent`;
  for (const i of overview.issuers) {
    const n = topoNodes[i.id];
    if (!n) continue;
    const h = issuerHealth(i);
    n.title.textContent = i.name;
    n.health.textContent = `${h.icon} ${h.text}`;
    n.health.setAttribute("class", `health ${h.cls}`);
    n.sub.textContent = `${i.stats.total} auths · BINs ${i.bins.length}`;
  }
  updateInFlight();
}

function updateInFlight() {
  const n = [...records.values()].filter((r) => r.status === "pending").length;
  topoNodes.swSub.textContent = n ? `${n} in flight` : "idle";
}

const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;
const LEG_MS = reduceMotion ? 0 : 420;

// move animates a packet from a to b, calling onStep with each x position.
function move(dot, a, b, duration = LEG_MS, onStep) {
  return new Promise((resolve) => {
    if (!duration) {
      dot.setAttribute("cx", b.x);
      dot.setAttribute("cy", b.y);
      onStep?.(b.x);
      return resolve();
    }
    const t0 = performance.now();
    const frame = (now) => {
      const t = Math.min((now - t0) / duration, 1);
      const e = t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2;
      const x = a.x + (b.x - a.x) * e;
      dot.setAttribute("cx", x);
      dot.setAttribute("cy", a.y + (b.y - a.y) * e);
      onStep?.(x);
      t < 1 ? requestAnimationFrame(frame) : resolve();
    };
    requestAnimationFrame(frame);
  });
}

// lightStages marks the switch's stages as the packet passes over them.
function lightStages(rec, x) {
  STAGES.forEach(([name], i) => {
    const center = 316 + i * 62 + 28;
    const g = stageEls[name];
    if (x < center) return;
    const names = name === "send" ? ["breaker", "send"] : [name];
    const step = rec.steps.find((s) => names.includes(s.name));
    g.setAttribute("class", step ? `stage ${step.ok ? "lit" : "fail"}` : "stage");
  });
}

function clearStages() {
  for (const g of Object.values(stageEls)) g.setAttribute("class", "stage");
}

// flows tracks the animation for each live transaction.
const flows = new Map();

function animate(rec) {
  const id = rec.network_txn_id;
  let f = flows.get(id);
  if (!f) {
    f = { rec, q: Promise.resolve(), sent: false, back: false, done: false };
    flows.set(id, f);
    f.dot = el("circle", { r: 7, cx: P.acq.x, cy: P.acq.y, class: "packet req" }, topoNodes.packets);
    f.q = f.q.then(() => move(f.dot, P.acq, P.swIn));
  }
  f.rec = rec;
  const names = rec.steps.map((s) => s.name);
  const target = ISSUER_POS[rec.issuer_id];

  if (!f.sent && names.includes("send") && target) {
    f.sent = true;
    f.q = f.q
      .then(() => move(f.dot, P.swIn, P.swOut, LEG_MS, (x) => lightStages(f.rec, x)))
      .then(() => move(f.dot, P.swOut, target))
      .then(() => {
        // A ring shows a request that is waiting on a slow issuer.
        if (!f.back) f.wait = el("circle", { r: 12, cx: target.x, cy: target.y, class: "waiting" }, topoNodes.packets);
      });
  }
  if (!f.back && names.includes("issuer") && target) {
    f.back = true;
    const ok = rec.steps.find((s) => s.name === "issuer").ok;
    f.q = f.q.then(() => {
      f.wait?.remove();
      f.dot.setAttribute("class", `packet ${ok ? "ok" : "bad"}`);
      return move(f.dot, target, P.swOut);
    });
  }
  if (!f.done && rec.status !== "pending") {
    f.done = true;
    const ok = rec.status === "approved";
    f.q = f.q.then(async () => {
      f.dot.setAttribute("class", `packet ${ok ? "ok" : "bad"}`);
      if (f.sent) {
        await move(f.dot, P.swOut, P.swIn, LEG_MS, (x) => lightStages(f.rec, Math.max(x, 680)));
      } else {
        // Declined inside the network: travel to the failing stage and back.
        const failed = STAGES.findIndex(([n]) => f.rec.steps.some((s) => !s.ok && (s.name === n || (n === "send" && s.name === "breaker"))));
        const stop = { x: failed >= 0 ? 316 + failed * 62 + 28 : 600, y: 150 };
        await move(f.dot, P.swIn, stop, LEG_MS, (x) => lightStages(f.rec, x));
        await move(f.dot, stop, P.swIn);
      }
      await move(f.dot, P.swIn, P.acq);
      f.dot.remove();
      flows.delete(id);
      if (!flows.size) setTimeout(() => !flows.size && clearStages(), 600);
    });
  }
}

// --- Live updates ----------------------------------------------------------------

function onRecord(rec, live) {
  rec.steps ||= [];
  const isNew = !records.has(rec.network_txn_id);
  records.set(rec.network_txn_id, rec);
  if (isNew) freshIds.add(rec.network_txn_id);
  if (live) animate(rec);
  if (following && (isNew || rec.network_txn_id === selectedId)) {
    if (isNew) selectedId = rec.network_txn_id;
    renderTrace();
  } else if (rec.network_txn_id === selectedId) {
    renderTrace();
  }
  scheduleFeed();
  updateInFlight();
  if (rec.status !== "pending") scheduleOverview();
}

async function loadRecent() {
  const recent = await (await fetch("/api/transactions?limit=200")).json();
  for (const r of recent.reverse()) records.set(r.network_txn_id, r);
  const hash = location.hash.match(/txn=([\w-]+)/);
  if (hash) {
    if (!records.has(hash[1])) {
      const res = await fetch(`/api/transactions/${hash[1]}`);
      if (res.ok) records.set(hash[1], await res.json());
    }
    if (records.has(hash[1])) select(hash[1], false);
  } else if (records.size) {
    select(sortedRecords()[0].network_txn_id, true);
  }
  scheduleFeed();
}

function connect() {
  const conn = document.getElementById("conn");
  const es = new EventSource("/api/stream");
  es.onopen = () => {
    conn.className = "conn live";
    conn.querySelector(".label").textContent = "Live";
  };
  es.onerror = () => {
    conn.className = "conn down";
    conn.querySelector(".label").textContent = "Reconnecting…";
  };
  es.addEventListener("txn", (e) => onRecord(JSON.parse(e.data).record, true));
  es.addEventListener("health", scheduleOverview);
}

// --- Tabs and tooltips -------------------------------------------------------------

document.querySelectorAll(".tabs button").forEach((b) =>
  b.addEventListener("click", () => {
    document.querySelectorAll(".tabs button").forEach((x) => x.setAttribute("aria-selected", String(x === b)));
    document.querySelectorAll(".tab").forEach((t) => (t.hidden = t.id !== `tab-${b.dataset.tab}`));
  })
);

const tooltip = document.getElementById("tooltip");
document.addEventListener("mousemove", (e) => {
  const target = e.target.closest?.("[data-tip]");
  if (!target) {
    tooltip.hidden = true;
    return;
  }
  tooltip.textContent = target.dataset.tip;
  tooltip.hidden = false;
  const x = Math.min(e.clientX + 12, innerWidth - tooltip.offsetWidth - 8);
  tooltip.style.left = `${x}px`;
  tooltip.style.top = `${e.clientY + 14}px`;
});

async function init() {
  buildTopology();
  await refreshOverview();
  await loadRecent();
  renderTopologyNodes();
  connect();
  // Breakers move from open to testing on a timer, so poll for health too.
  setInterval(refreshOverview, 5000);
}

init();
