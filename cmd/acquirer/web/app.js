// Acquirer back office: polls /api/overview and shows each authorization's
// path from the terminal through the acquirer to the network and back.

const LANES = [
  { id: "pos", label: "POS", role: "Merchant terminal" },
  { id: "acquirer", label: "Acquirer", role: "This bank" },
  { id: "network", label: "Network", role: "Routes to the issuer" },
];
const LANE_INDEX = Object.fromEntries(LANES.map((l, i) => [l.id, i]));
const POLL_MS = 2000;

let data = null;
let networkURL = "";
// The selected authorization, or null to follow the newest one.
let selectedKey = null;
// A network transaction ID from the URL (#txn=...), as linked from the POS.
let wantedTxn = new URLSearchParams(location.hash.slice(1)).get("txn");

const keyOf = (rec) => `${rec.received}|${rec.terminal_id}|${rec.terminal_stan}`;

function money(minor, currencyCode) {
  const iso = (data && data.currencies[currencyCode]) || "USD";
  return new Intl.NumberFormat(undefined, { style: "currency", currency: iso }).format(minor / 100);
}

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

const fmtMS = (ms) => (ms < 10 ? ms.toFixed(1) : Math.round(ms)) + " ms";

// statusBadge shows the outcome with words and an icon, not color alone.
function statusBadge(rec) {
  const code = rec.response.de39_response_code;
  const text = data.response_codes[code]?.text || "Unknown";
  const [cls, icon] = { APPROVED: ["good", "✓"], PARTIAL: ["", "◐"], DECLINED: ["bad", "✕"] }[rec.status] || ["", ""];
  return `<span class="status ${cls}" title="${escapeHTML(text)}"><span aria-hidden="true">${icon}</span> ${escapeHTML(code)} ${escapeHTML(text)}</span>`;
}

function renderKPIs() {
  const s = data.stats;
  const kpis = [
    ["Authorizations", s.count],
    ["Approval rate", s.count ? Math.round(s.approval_rate * 100) + "%" : "—"],
    ["Avg response time", s.count ? fmtMS(s.avg_ms) : "—"],
    ["Avg network round trip", s.network_ms ? fmtMS(s.network_ms) : "—"],
  ];
  document.getElementById("kpis").innerHTML = kpis
    .map(([label, value]) => `<div class="kpi"><small>${label}</small><b>${value}</b></div>`)
    .join("");
}

function selected() {
  const txns = data.transactions;
  if (!txns.length) return null;
  if (wantedTxn) {
    const hit = txns.find((r) => r.response.network_txn_id === wantedTxn);
    if (hit) {
      selectedKey = keyOf(hit);
      wantedTxn = null;
    }
  }
  return (selectedKey && txns.find((r) => keyOf(r) === selectedKey)) || txns[0];
}

// renderPath draws the selected authorization as a sequence diagram.
function renderPath(rec) {
  const el = document.getElementById("path");
  if (!rec) return;
  el.className = "";
  const r = rec.request;
  document.getElementById("path-title").textContent =
    `${rec.merchant_name || rec.merchant_id} · ${r.de2_pan} · ${money(r.de4_amount, r.de49_currency)}` +
    (selectedKey ? "" : " · following newest");

  const lanes = LANES.map(
    (l) => `<div class="lane${l.id === "acquirer" ? " here" : ""}">${l.label}<small>${l.role}</small></div>`
  ).join("");

  const approved = rec.status !== "DECLINED";
  const rows = rec.steps
    .map((s) => {
      const a = LANE_INDEX[s.from];
      const b = LANE_INDEX[s.to];
      const lo = Math.min(a, b);
      const hi = Math.max(a, b);
      const dir = b > a ? "right" : "left";
      const tone = s.mti === "0110" ? (approved ? "good" : "bad") : "";
      return `<div class="seq-row">
        <div class="msg ${dir} ${tone}" style="grid-column: ${2 * lo + 2} / ${2 * hi + 2}">
          <div class="msg-label"><span class="mti">${escapeHTML(s.mti)}</span>${escapeHTML(s.title)}</div>
          <div class="msg-line"></div>
          <div class="msg-ms">${s.ms ? fmtMS(s.ms) : "&nbsp;"}</div>
        </div>
        <div class="msg-detail">${escapeHTML(s.detail)}</div>
      </div>`;
    })
    .join("");

  const changes = rec.changes.length
    ? `<h3>What the acquirer changed</h3>
      <div class="table-wrap"><table class="changes"><thead><tr><th>Field</th><th>Terminal</th><th></th><th>Network leg</th><th>Why</th></tr></thead><tbody>
      ${rec.changes
        .map(
          (c) => `<tr><td><span class="mono">${escapeHTML(c.field)}</span> ${escapeHTML(c.name)}</td>
            <td class="mono">${escapeHTML(c.before || "—")}</td><td class="arrow">→</td>
            <td class="mono">${escapeHTML(c.after)}</td><td>${escapeHTML(c.why)}</td></tr>`
        )
        .join("")}</tbody></table></div>`
    : "";

  const txnID = rec.response.network_txn_id;
  const link = txnID && networkURL
    ? `<a href="${escapeHTML(networkURL)}/#txn=${encodeURIComponent(txnID)}">See the network and issuer hops →</a>`
    : "";
  el.innerHTML = `
    <div class="seq"><div class="seq-lanes">${lanes}</div>${rows}</div>
    <div class="path-foot">
      <span>Result ${statusBadge(rec)}</span>
      <span>Total at acquirer <b>${fmtMS(rec.total_ms)}</b></span>
      ${rec.response.de38_auth_code ? `<span>Auth code <b class="mono">${escapeHTML(rec.response.de38_auth_code)}</b></span>` : ""}
      ${txnID ? `<span>Network txn <b class="mono">${escapeHTML(txnID)}</b></span>` : ""}
      ${link}
    </div>
    ${changes}`;
}

function renderTransactions(current) {
  const body = document.getElementById("txns");
  if (!data.transactions.length) return;
  body.innerHTML = data.transactions
    .map((rec, i) => {
      const r = rec.request;
      const stan = r.de11_stan === rec.terminal_stan ? r.de11_stan : `${rec.terminal_stan} → ${r.de11_stan}`;
      return `<tr data-i="${i}" aria-selected="${rec === current}">
        <td>${new Date(rec.received).toLocaleTimeString()}</td>
        <td>${escapeHTML(rec.merchant_name || "Unknown merchant")}<small class="mono">${escapeHTML(rec.merchant_id)}</small></td>
        <td class="mono">${escapeHTML(rec.terminal_id)}</td>
        <td class="mono">${escapeHTML(stan)}</td>
        <td class="mono">${escapeHTML(r.de2_pan)}</td>
        <td class="num">${money(r.de4_amount, r.de49_currency)}</td>
        <td>${statusBadge(rec)}</td>
        <td class="num">${fmtMS(rec.total_ms)}</td>
      </tr>`;
    })
    .join("");
}

document.getElementById("txns").addEventListener("click", (e) => {
  const row = e.target.closest("tr[data-i]");
  if (!row) return;
  const rec = data.transactions[Number(row.dataset.i)];
  // Clicking the selected row again goes back to following the newest.
  selectedKey = selectedKey === keyOf(rec) ? null : keyOf(rec);
  render();
});

function renderMerchants() {
  const lines = (totals, fn) => (totals.length ? totals.map(fn).join("<br>") : "—");
  document.getElementById("merchants").innerHTML = data.merchants
    .map((m) => {
      const t = m.totals;
      const mcc = data.mccs[m.mcc] ? `${m.mcc} (${data.mccs[m.mcc]})` : m.mcc;
      return `<tr>
        <td>${escapeHTML(m.name)}<small><span class="mono">${escapeHTML(m.merchant_id)}</span> · ${escapeHTML(m.city)}, ${escapeHTML(m.country)}</small></td>
        <td>${escapeHTML(mcc)}</td>
        <td class="mono">${m.terminals.map(escapeHTML).join("<br>")}</td>
        <td class="num">${(m.discount_bps / 100).toFixed(2)}%</td>
        <td class="num">${lines(t, (x) => `${x.approved} / ${x.declined}`)}</td>
        <td class="num">${lines(t, (x) => money(x.sales, x.currency))}</td>
        <td class="num">${lines(t, (x) => money(x.refunds, x.currency))}</td>
        <td class="num">${lines(t, (x) => money(x.fees, x.currency))}</td>
        <td class="num"><b>${lines(t, (x) => money(x.net, x.currency))}</b></td>
      </tr>`;
    })
    .join("");
}

function render() {
  const current = selected();
  renderKPIs();
  renderPath(current);
  renderTransactions(current);
  renderMerchants();
}

async function poll() {
  try {
    data = await (await fetch("/api/overview")).json();
    document.getElementById("bank").textContent = `${data.bank.name} · Acquirer ID (DE32) ${data.bank.id}`;
    render();
  } catch {
    document.getElementById("bank").textContent = "Acquirer not reachable. Retrying…";
  }
  setTimeout(poll, POLL_MS);
}

window.addEventListener("hashchange", () => {
  wantedTxn = new URLSearchParams(location.hash.slice(1)).get("txn");
  if (data) render();
});

fetch("/ui/pages")
  .then((r) => r.json())
  .then((pages) => (networkURL = pages.find((p) => p.id === "network")?.url || ""))
  .catch(() => {});

poll();
