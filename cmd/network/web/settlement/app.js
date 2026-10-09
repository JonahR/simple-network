// Settlement: polls /api/settlement and shows each business day's clearing
// cycle: the end-of-day pipeline, who pays whom, each member's net position,
// what every cleared transaction cost, and the ledger behind it.

const POLL_MS = 2000;
const STAGE_TERMS = {
  "Files received": "clearing-file",
  "Matched to authorizations": "presentment",
  Priced: "interchange",
  "Issuer files": "clearing",
  "Zero-sum check": "ledger",
  Settled: "net-settlement",
  "Acquirers advised": "funding",
};
const POSTING = {
  pending: ["", "…", "Pending"],
  posted: ["good", "✓", "Posted"],
  unmatched: ["", "!", "Posted, no hold"],
  "delivery failed": ["bad", "✕", "Not delivered"],
};

let data = null;
let selectedCycle = null; // Cycle ID, or null for the newest closed cycle
let selectedItem = null; // Network transaction ID of the item shown in the waterfall

function money(minor, currencyCode = "840") {
  const iso = (data && data.currencies[currencyCode]) || "USD";
  return new Intl.NumberFormat(undefined, { style: "currency", currency: iso }).format(minor / 100);
}

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

const nameOf = (id) => (data.names && data.names[id]) || id;
const pct = (bps) => (bps / 100).toFixed(2) + "%";

function cycles() {
  return [data.engine.open, ...data.engine.history];
}

function current() {
  const all = cycles();
  if (selectedCycle) {
    const c = all.find((c) => c.id === selectedCycle);
    if (c) return c;
  }
  // The newest closed cycle, or the open one before any has closed.
  return data.engine.history[0] || data.engine.open;
}

function renderOpen() {
  const o = data.engine.open;
  const records = o.files.reduce((n, f) => n + f.accepted, 0);
  document.getElementById("open").innerHTML = `
    <b>${escapeHTML(o.business_date)}</b> · cycle ${o.number} · open<br>
    <span data-term="clearing-file">${o.files.length} file${o.files.length === 1 ? "" : "s"}</span>, ${records} record${records === 1 ? "" : "s"} received so far<br>
    <span data-term="t-plus">Value date</span> ${escapeHTML(o.value_date)} (T+1)
    ${data.engine.undelivered ? `<br><span class="nonzero">${data.engine.undelivered} issuer file(s) waiting to be redelivered</span>` : ""}`;

  const sel = document.getElementById("cycle-select");
  const opts = cycles()
    .map((c) => `<option value="${escapeHTML(c.id)}"${c.id === current().id ? " selected" : ""}>${escapeHTML(c.id)} · ${escapeHTML(c.status)}</option>`)
    .join("");
  if (sel.innerHTML !== opts) sel.innerHTML = opts;
}

function renderStages(c) {
  const el = document.getElementById("stages");
  if (!c.stages.length) {
    el.innerHTML = `<li class="empty">${c.status === "open" ? "This cycle is still open. Run the end of day to close it." : "No stages recorded."}</li>`;
    return;
  }
  el.innerHTML = c.stages
    .map((s) => `<li class="stage ${s.ok ? "ok" : "fail"}">
      <div class="s-name"><span class="s-icon" aria-label="${s.ok ? "done" : "problem"}">${s.ok ? "✓" : "✕"}</span><span data-term="${STAGE_TERMS[s.name] || "settlement"}">${escapeHTML(s.name)}</span></div>
      <div class="s-detail">${escapeHTML(s.detail)}</div>
    </li>`)
    .join("");
}

function renderKPIs(c) {
  const gross = c.items.reduce((n, i) => n + i.amount, 0);
  const ic = c.items.reduce((n, i) => n + i.interchange, 0);
  const rev = c.items.reduce((n, i) => n + i.acquirer_fee + i.issuer_fee, 0);
  const moved = c.transfers.reduce((n, t) => n + (t.from === data.network_id ? 0 : t.amount), 0);
  const kpis = [
    ["Cleared", "presentment", c.items.length ? money(gross) : "—", `${c.items.length} transaction${c.items.length === 1 ? "" : "s"}${c.rejected.length ? `, ${c.rejected.length} rejected` : ""}`],
    ["Interchange to issuers", "interchange", c.items.length ? money(ic) : "—", gross ? `${((ic / gross) * 100).toFixed(2)}% of sales` : ""],
    ["Network revenue", "network-fees", c.items.length ? money(rev) : "—", "fees from both sides"],
    ["Money moved", "net-settlement", c.transfers.length ? money(moved) : "—", gross && c.transfers.length ? `${c.transfers.length} payments instead of ${c.items.length} gross` : "after netting"],
  ];
  document.getElementById("kpis").innerHTML = kpis
    .map(([label, term, value, sub]) => `<div class="kpi"><small><span data-term="${term}">${label}</span></small><b>${value}</b><span class="sub">${escapeHTML(sub)}</span></div>`)
    .join("");
}

function renderFlow(c) {
  const el = document.getElementById("flow");
  const pays = c.transfers.filter((t) => t.to === data.network_id);
  const gets = c.transfers.filter((t) => t.from === data.network_id);
  if (!c.transfers.length) {
    el.className = "empty";
    el.textContent = c.status === "open" ? "Not settled yet." : "Nothing to settle in this cycle.";
    return;
  }
  el.className = "";
  const revenue = c.checks.reduce((n, k) => n + k.revenue, 0);
  const box = (t, id) => `<div class="flow-box"><b>${escapeHTML(nameOf(id))}</b><span class="amt">${money(t.amount, t.currency)}</span></div>`;
  el.innerHTML = `
    <div class="flow">
      <div class="flow-col">${pays.map((t) => box(t, t.from)).join("") || '<div class="flow-box muted">No payers</div>'}</div>
      <div class="flow-arrow" aria-hidden="true">→</div>
      <div class="flow-col"><div class="flow-box hub"><b data-term="settlement-bank">Network settlement account</b><span class="muted">at the settlement bank</span></div>
        <div class="flow-box"><b data-term="network-fees">Network revenue</b><span class="amt">${money(revenue)}</span></div></div>
      <div class="flow-arrow" aria-hidden="true">→</div>
      <div class="flow-col">${gets.map((t) => box(t, t.to)).join("") || '<div class="flow-box muted">No receivers</div>'}</div>
    </div>
    <div class="flow-foot">Payers on the left fund the network's account; it pays the receivers and keeps its fees. <span data-term="t-plus">Value date</span> ${escapeHTML(c.value_date)}.</div>`;
}

function renderPositions(c) {
  const el = document.getElementById("positions");
  if (!c.positions.length) {
    el.className = "empty";
    el.textContent = "No positions in this cycle.";
    return;
  }
  el.className = "";
  const max = Math.max(...c.positions.map((p) => Math.abs(p.net)), 1);
  const rows = c.positions
    .map((p) => {
      const w = `${(Math.abs(p.net) / max) * 100}%`;
      const dir = p.net < 0 ? "pays" : p.net > 0 ? "receives" : "even";
      return `<tr>
        <td><b>${escapeHTML(p.name)}</b><small data-term="${p.role}">${p.role === "issuer" ? "Issuer" : "Acquirer"}</small></td>
        <td class="num">${p.count}</td>
        <td class="num">${money(p.gross, p.currency)}</td>
        <td class="num">${money(p.interchange, p.currency)}</td>
        <td class="num">${money(p.network_fees, p.currency)}</td>
        <td class="num"><b>${money(Math.abs(p.net), p.currency)}</b><span class="dir">${dir}</span></td>
        <td><div class="pos-bar" role="img" aria-label="${dir} ${money(Math.abs(p.net), p.currency)}">
          <div class="neg">${p.net < 0 ? `<span style="width:${w}"></span>` : ""}</div>
          <div class="pos">${p.net > 0 ? `<span style="width:${w}"></span>` : ""}</div>
        </div></td>
      </tr>`;
    })
    .join("");
  const checks = c.checks
    .map((k) => `<span class="${k.ok ? "zero" : "nonzero"}">${k.ok ? "✓" : "✕"} ${escapeHTML(data.currencies[k.currency] || k.currency)}: members ${money(k.members, k.currency)} + revenue ${money(k.revenue, k.currency)} = ${money(k.sum, k.currency)}</span>`)
    .join(" · ");
  el.innerHTML = `<div class="table-wrap"><table class="list">
      <thead><tr><th data-term="settlement-bank">Member</th><th class="num" data-term="presentment">Txns</th><th class="num" data-term="de4">Gross</th><th class="num" data-term="interchange">Interchange</th><th class="num" data-term="network-fees">Network fees</th><th class="num" data-term="net-settlement">Net</th><th data-term="net-settlement">Pays ◂ ▸ receives</th></tr></thead>
      <tbody>${rows}</tbody></table></div>
    <p class="hint"><span data-term="ledger">Zero-sum check</span>: ${checks || "nothing to check"}</p>`;
}

function renderItems(c) {
  const tbody = document.getElementById("items");
  if (!c.items.length) {
    tbody.innerHTML = `<tr><td colspan="10" class="empty">No cleared transactions in this cycle.</td></tr>`;
    document.getElementById("waterfall").innerHTML = "";
  } else {
    if (!c.items.some((i) => i.network_txn_id === selectedItem)) selectedItem = c.items[0].network_txn_id;
    tbody.innerHTML = c.items
      .map((i) => {
        const [cls, icon, label] = POSTING[i.posting] || ["", "", i.posting];
        return `<tr data-i="${escapeHTML(i.network_txn_id)}" aria-selected="${i.network_txn_id === selectedItem}">
          <td class="mono">${escapeHTML(i.arn)}</td>
          <td>${escapeHTML(i.merchant_name)}</td>
          <td>${escapeHTML(i.issuer_id)}</td>
          <td><span class="mono">${escapeHTML(i.program_id)}</span><small>${escapeHTML(data.product_names[i.product] || i.product)} · ${i.channel === "card_present" ? "card present" : "card not present"}</small></td>
          <td class="num">${money(i.amount, i.currency)}${i.cashback ? `<small>incl. ${money(i.cashback, i.currency)} cash back</small>` : ""}</td>
          <td class="num">${money(i.interchange, i.currency)}</td>
          <td class="num">${money(i.acquirer_fee + i.issuer_fee, i.currency)}</td>
          <td class="num">${money(i.issuer_owes, i.currency)}</td>
          <td class="num">${money(i.acquirer_gets, i.currency)}</td>
          <td><span class="status ${cls}" data-term="clearing"><span aria-hidden="true">${icon}</span> ${label}</span></td>
        </tr>`;
      })
      .join("");
    renderWaterfall(c.items.find((i) => i.network_txn_id === selectedItem));
  }
  const rej = document.getElementById("rejected");
  rej.innerHTML = c.rejected.length
    ? `<h3><span data-term="clearing-file">Rejected records</span></h3>
      <div class="table-wrap"><table class="list wrap-last"><thead><tr><th data-term="network-txn-id">Network txn</th><th class="num" data-term="de4">Amount</th><th data-term="clearing-file">Reason</th></tr></thead>
      <tbody>${c.rejected.map((r) => `<tr><td class="mono">${escapeHTML(r.network_txn_id || "—")}</td><td class="num">${money(r.amount)}</td><td>${escapeHTML(r.reason)}</td></tr>`).join("")}</tbody></table></div>`
    : "";
}

// renderWaterfall splits one transaction's amount the way the network sees
// it: what the acquirer receives, interchange to the issuer, and the
// acquirer's network fee. The issuer's own network fee is added on its side.
function renderWaterfall(i) {
  const el = document.getElementById("waterfall");
  if (!i) {
    el.innerHTML = "";
    return;
  }
  const w = (v) => `${Math.max((v / i.amount) * 100, v > 0 ? 0.6 : 0)}%`;
  el.innerHTML = `<div class="waterfall">
    <h3><span data-term="interchange">Where ${money(i.amount, i.currency)} went</span> · <span class="mono">${escapeHTML(i.arn)}</span> · ${escapeHTML(i.merchant_name)} → ${escapeHTML(nameOf(i.issuer_id))}</h3>
    <div class="wf-bar" role="img" aria-label="Acquirer ${money(i.acquirer_gets)}, interchange ${money(i.interchange)}, network fee ${money(i.acquirer_fee)}">
      <span class="acq" style="width:${w(i.acquirer_gets)}"></span><span class="ic" style="width:${w(i.interchange)}"></span><span class="fee" style="width:${w(i.acquirer_fee)}"></span>
    </div>
    <div class="legend">
      <span><i class="acq"></i><span data-term="acquirer">Acquirer receives</span> <b>${money(i.acquirer_gets, i.currency)}</b></span>
      <span><i class="ic"></i><span data-term="interchange">Interchange to issuer</span> <b>${money(i.interchange, i.currency)}</b> (${escapeHTML(i.program_id)})</span>
      <span><i class="fee"></i><span data-term="network-fees">Network fee, acquirer side</span> <b>${money(i.acquirer_fee, i.currency)}</b></span>
      <span><span data-term="network-fees">Network fee, issuer side</span> <b>${money(i.issuer_fee, i.currency)}</b>: the issuer pays ${money(i.issuer_owes, i.currency)} in all</span>
    </div>
    <p class="hint">The merchant's own share depends on its <span data-term="mdr">discount rate</span> with the acquirer: see <span data-term="funding">merchant funding</span> on the acquirer's page.</p>
  </div>`;
}

function renderLedger() {
  const e = data.engine;
  const opening = data.opening;
  document.getElementById("bank").innerHTML = e.bank.length
    ? e.bank
        .map((b) => {
          const change = b.balance - b.opening;
          return `<tr><td><b>${escapeHTML(b.name)}</b></td><td class="num">${money(b.opening, b.currency)}</td><td class="num">${change >= 0 ? "+" : "−"}${money(Math.abs(change), b.currency)}</td><td class="num"><b>${money(b.balance, b.currency)}</b></td></tr>`;
        })
        .join("")
    : `<tr><td colspan="4" class="empty">Accounts open at the first settlement, each funded with ${money(opening)}.</td></tr>`;

  document.getElementById("balances").innerHTML = e.balances.length
    ? `<div class="table-wrap"><table class="list"><thead><tr><th data-term="ledger">Account</th><th class="num" data-term="ledger">Debits</th><th class="num" data-term="ledger">Credits</th><th class="num" data-term="ledger">Balance</th></tr></thead><tbody>${e.balances
        .map((b) => {
          const isDue = b.account.startsWith("due_");
          const cls = isDue ? (b.net === 0 ? "zero" : "nonzero") : "";
          return `<tr><td class="mono">${escapeHTML(b.account)}</td><td class="num">${money(b.debit, b.currency)}</td><td class="num">${money(b.credit, b.currency)}</td><td class="num ${cls}">${money(b.net, b.currency)}</td></tr>`;
        })
        .join("")}</tbody></table></div>`
    : `<p class="empty">No entries yet.</p>`;

  document.getElementById("journal-count").textContent = e.entry_count ? `${e.entry_count} entries, newest first (showing ${e.entries.length})` : "";
  const journal = document.getElementById("journal");
  if (!e.entries.length) return;
  journal.className = "journal";
  journal.innerHTML = e.entries
    .map((en) => `<div class="entry">
      <div class="entry-head"><b>#${en.seq} ${escapeHTML(en.memo)}</b><span>${escapeHTML(en.cycle_id)} · ${new Date(en.time).toLocaleTimeString()}</span></div>
      <table><tbody>${en.lines
        .map((l) => `<tr><td class="acct${l.credit ? " cr" : ""}">${l.debit ? "DR" : "CR"} ${escapeHTML(l.account)}</td><td class="num">${money(l.debit || l.credit, en.currency)}</td></tr>`)
        .join("")}</tbody></table>
    </div>`)
    .join("");
}

function renderFees() {
  const f = data.engine.fees;
  document.getElementById("fee-version").textContent = `version ${f.version}`;
  document.getElementById("programs").innerHTML = f.programs
    .map((p) => `<tr>
      <td><span class="mono">${escapeHTML(p.id)}</span><small>${escapeHTML(p.name)}</small></td>
      <td>${p.products ? p.products.map((x) => escapeHTML(data.product_names[x] || x)).join(", ") : "Any"}</td>
      <td>${p.channels ? p.channels.map((c) => (c === "card_present" ? "Card present" : "Card not present")).join(", ") : "Any"}</td>
      <td class="num">${pct(p.rate_bps)}</td>
      <td class="num">${money(p.fixed_minor)}</td>
      <td class="num">${money(p.rate_bps + p.fixed_minor) /* On $100.00, basis points are cents */}</td>
    </tr>`)
    .join("");
  document.getElementById("network-fees").innerHTML = `<div class="table-wrap"><table class="list"><thead><tr><th data-term="network-fees">Fee</th><th data-term="network-fees">Paid by</th><th class="num" data-term="network-fees">Rate</th></tr></thead><tbody>
    <tr><td>Assessment</td><td data-term="acquirer">Acquirer</td><td class="num">${pct(f.acquirer_assessment_bps)} + ${money(f.acquirer_per_item_minor)} per transaction</td></tr>
    <tr><td>Assessment</td><td data-term="issuer">Issuer</td><td class="num">${pct(f.issuer_assessment_bps)}</td></tr>
  </tbody></table></div>`;
}

function render() {
  const c = current();
  renderOpen();
  renderStages(c);
  renderKPIs(c);
  renderFlow(c);
  renderPositions(c);
  renderItems(c);
  renderLedger();
  renderFees();
}

document.getElementById("cycle-select").addEventListener("change", (e) => {
  selectedCycle = e.target.value;
  render();
});

document.getElementById("items").addEventListener("click", (e) => {
  const tr = e.target.closest("tr[data-i]");
  if (!tr) return;
  selectedItem = tr.dataset.i;
  render();
});

document.getElementById("run").addEventListener("click", async () => {
  const btn = document.getElementById("run");
  const msg = document.getElementById("run-msg");
  btn.disabled = true;
  msg.className = "run-msg";
  msg.textContent = "Collecting clearing files, clearing, and settling…";
  try {
    const res = await (await fetch("/api/settlement/run", { method: "POST" })).json();
    selectedCycle = res.cycle.id;
    msg.className = res.warning ? "run-msg warn" : "run-msg";
    msg.textContent = res.warning
      ? `Cycle ${res.cycle.id} ${res.cycle.status}, but ${res.warning}`
      : `Cycle ${res.cycle.id} ${res.cycle.status}: ${res.cycle.items.length} cleared, ${res.cycle.transfers.length} payment(s).`;
  } catch {
    msg.className = "run-msg warn";
    msg.textContent = "The network did not answer. Try again.";
  } finally {
    btn.disabled = false;
    lastBody = "";
    poll();
  }
});

document.querySelectorAll(".tabs button").forEach((b) =>
  b.addEventListener("click", () => {
    document.querySelectorAll(".tabs button").forEach((x) => x.setAttribute("aria-selected", String(x === b)));
    document.querySelectorAll(".tab").forEach((t) => (t.hidden = t.id !== `tab-${b.dataset.tab}`));
  })
);

// lastBody is the previous poll's response; the page redraws only when it
// changes, so a label being hovered in Help mode isn't replaced under the pointer.
let lastBody = "";
let timer = 0;

async function poll() {
  clearTimeout(timer);
  timer = setTimeout(poll, POLL_MS);
  let body;
  try {
    body = await (await fetch("/api/settlement")).text();
  } catch {
    lastBody = "";
    document.getElementById("open").textContent = "Network not reachable. Retrying…";
    return;
  }
  if (body === lastBody) return;
  lastBody = body;
  data = JSON.parse(body);
  render();
}

poll();
