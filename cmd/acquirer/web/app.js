// Acquirer back office: polls /api/overview and shows each authorization's
// path from the terminal through the acquirer to the network and back.

const LANES = [
  { id: "pos", label: "POS", role: "Merchant terminal", term: "pos-terminal" },
  { id: "acquirer", label: "Acquirer", role: "This bank", term: "acquirer" },
  { id: "network", label: "Network", role: "Routes to the issuer", term: "network" },
];
// Glossary terms for the labels Help mode explains.
const MTI_TERMS = { "0100": "msg-0100", "0110": "msg-0110", "0420": "reversal", "0430": "reversal" };
const FIELD_TERMS = { DE11: "de11", DE18: "de18", DE32: "de32-acquirer-id", DE37: "de37" };
const REVERSAL_TEXT = { pending: "0420 sent, waiting for the 0430", acknowledged: "acknowledged by the network", stopped: "not acknowledged; retries stopped at shutdown", rejected: "refused by the network; needs manual cleanup" };
const LANE_INDEX = Object.fromEntries(LANES.map((l, i) => [l.id, i]));
const POLL_MS = 2000;

let data = null;
let networkURL = "";
// The selected authorization, or null to follow the newest one.
let selectedKey = null;
// The authorization the URL asks for, as linked from the POS: the terminal's
// own keys (#tid=&stan=&de7=), which every record has, or a network
// transaction ID (#txn=), which only records the network answered have.
let wanted = parseHash();

function parseHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  const w = { tid: p.get("tid"), stan: p.get("stan"), de7: p.get("de7"), txn: p.get("txn") };
  return (w.tid && w.stan) || w.txn ? w : null;
}

function matches(rec, w) {
  if (w.tid && w.stan) {
    return rec.terminal_id === w.tid && rec.terminal_stan === w.stan && (!w.de7 || rec.request.de7_transmission_datetime === w.de7);
  }
  return rec.response.network_txn_id === w.txn;
}

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
  return `<span class="status ${cls}" data-term="de39"><span aria-hidden="true">${icon}</span> ${escapeHTML(code)} ${escapeHTML(text)}</span>`;
}

function renderKPIs() {
  const s = data.stats;
  const kpis = [
    ["Authorizations", "authorization", s.count],
    ["Approval rate", "approval-rate", s.count ? Math.round(s.approval_rate * 100) + "%" : "—"],
    ["Avg response time", "latency", s.count ? fmtMS(s.avg_ms) : "—"],
    ["Avg network round trip", "latency", s.network_ms ? fmtMS(s.network_ms) : "—"],
  ];
  document.getElementById("kpis").innerHTML = kpis
    .map(([label, term, value]) => `<div class="kpi"><small><span data-term="${term}">${label}</span></small><b>${value}</b></div>`)
    .join("");
}

function selected() {
  const txns = data.transactions;
  if (!txns.length) return null;
  if (wanted) {
    const hit = txns.find((r) => matches(r, wanted));
    if (hit) {
      selectedKey = keyOf(hit);
      wanted = null;
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
    (l) => `<div class="lane${l.id === "acquirer" ? " here" : ""}"><span data-term="${l.term}">${l.label}</span><small>${l.role}</small></div>`
  ).join("");

  const approved = rec.status !== "DECLINED";
  const rows = rec.steps
    .map((s) => {
      // A step within one participant (the acquirer giving up on the network,
      // or waiting to resend a reversal) is a note on its lifeline, not an arrow.
      if (s.from === s.to) {
        const i = LANE_INDEX[s.from];
        return `<div class="seq-row">
          <div class="msg note" style="grid-column: ${2 * i + 1} / ${2 * i + 3}">
            <div class="msg-label"><span aria-hidden="true">⏱</span> ${escapeHTML(s.title)}</div>
            <div class="msg-ms">${s.ms ? fmtMS(s.ms) : "&nbsp;"}</div>
          </div>
          <div class="msg-detail">${escapeHTML(s.detail)}</div>
        </div>`;
      }
      const a = LANE_INDEX[s.from];
      const b = LANE_INDEX[s.to];
      const lo = Math.min(a, b);
      const hi = Math.max(a, b);
      const dir = b > a ? "right" : "left";
      const tone = s.mti === "0110" ? (approved ? "good" : "bad") : "";
      return `<div class="seq-row">
        <div class="msg ${dir} ${tone}" style="grid-column: ${2 * lo + 2} / ${2 * hi + 2}">
          <div class="msg-label"><span class="mti" data-term="${MTI_TERMS[s.mti] || "mti"}">${escapeHTML(s.mti)}</span>${escapeHTML(s.title)}</div>
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
          (c) => `<tr><td><span class="mono" data-term="${FIELD_TERMS[c.field] || "iso8583"}">${escapeHTML(c.field)}</span> ${escapeHTML(c.name)}</td>
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
      <span><span data-term="latency">Total at acquirer</span> <b>${fmtMS(rec.total_ms)}</b></span>
      ${rec.response.de38_auth_code ? `<span><span data-term="de38">Auth code</span> <b class="mono">${escapeHTML(rec.response.de38_auth_code)}</b></span>` : ""}
      ${txnID ? `<span><span data-term="network-txn-id">Network txn</span> <b class="mono">${escapeHTML(txnID)}</b></span>` : ""}
      ${rec.reversal ? `<span><span data-term="reversal">Reversal</span> <b>${escapeHTML(REVERSAL_TEXT[rec.reversal] || rec.reversal)}</b></span>` : ""}
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

// lastBody is the previous poll's response; the page redraws only when it
// changes, so a label being hovered in Help mode isn't replaced under the pointer.
let lastBody = "";

async function poll() {
  try {
    const body = await (await fetch("/api/overview")).text();
    if (body !== lastBody) {
      lastBody = body;
      data = JSON.parse(body);
      document.getElementById("bank").textContent = `${data.bank.name} · Acquirer ID (DE32) ${data.bank.id}`;
      document.getElementById("bank").dataset.term = "de32-acquirer-id";
      render();
    }
  } catch {
    lastBody = "";
    document.getElementById("bank").textContent = "Acquirer not reachable. Retrying…";
  }
  setTimeout(poll, POLL_MS);
}

window.addEventListener("hashchange", () => {
  wanted = parseHash();
  if (data) render();
});

fetch("/ui/pages")
  .then((r) => r.json())
  .then((pages) => (networkURL = pages.find((p) => p.id === "network")?.url || ""))
  .catch(() => {});

poll();
