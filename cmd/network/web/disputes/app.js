// Disputes: the network's dispute desk. Issuers file chargebacks against
// cleared sales; acquirers accept or represent with evidence; issuers accept
// or go to pre-arbitration with a rebuttal; the network arbitrates. Both
// sides act here, labelled, as a simulation of the messages they'd send;
// each can also act on its own cases from its own page (internal/ui/disputes.js).

const POLL_MS = 2000;
const STAGES = [
  ["chargeback", "First chargeback", "Acquirer to answer", "chargeback"],
  ["represented", "Representment", "Issuer to answer", "representment"],
  ["pre_arbitration", "Pre-arbitration", "Acquirer to answer", "pre-arbitration"],
  ["closed", "Closed", "Decided", "chargeback"],
];
const STATE_TERMS = { chargeback: "chargeback", represented: "representment", pre_arbitration: "pre-arbitration", closed: "chargeback" };
const OUTCOMES = {
  merchant_liable: ["lost", "Cardholder won: the chargeback stands"],
  cardholder_rebilled: ["won", "Merchant won: the cardholder is rebilled"],
};

let data = null;
let selectedID = new URLSearchParams(location.hash.slice(1)).get("dispute");
let lastCase = "";

function money(minor, currencyCode = "840") {
  const iso = (data && data.currencies[currencyCode]) || "USD";
  return new Intl.NumberFormat(undefined, { style: "currency", currency: iso }).format(minor / 100);
}

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

const desk = () => data.desk;
const reasonOf = (code) => desk().reasons.find((r) => r.code === code) || { code, name: code, responses: [] };
const evidenceName = (id) => desk().evidence.find((e) => e.id === id)?.name || id;
const rebuttalName = (id) => desk().rebuttals.find((r) => r.id === id)?.name || "No new information";

function stageBadge(d) {
  if (d.state === "closed") {
    const [cls, text] = OUTCOMES[d.outcome] || ["", d.outcome];
    return `<span class="badge ${cls}" data-term="chargeback">${escapeHTML(text)}</span>`;
  }
  const s = STAGES.find(([id]) => id === d.state);
  const turn = d.state === "represented" ? "issuer" : "acquirer";
  return `<span class="badge ${turn}" data-term="${STATE_TERMS[d.state]}">${escapeHTML(s ? s[1] : d.state)} · ${turn}'s turn</span>`;
}

function renderKPIs() {
  const ds = desk().disputes;
  const open = ds.filter((d) => d.state !== "closed");
  const kpis = [
    ["Open disputes", "chargeback", open.length, `${money(open.reduce((n, d) => n + d.amount, 0))} in dispute`],
    ["Cardholder won", "chargeback", ds.filter((d) => d.outcome === "merchant_liable").length, "the chargeback stood"],
    ["Merchant won", "representment", ds.filter((d) => d.outcome === "cardholder_rebilled").length, "evidence stood; cardholder rebilled"],
    ["Arbitrated", "pre-arbitration", ds.filter((d) => d.events.some((e) => e.stage === "Arbitration decided")).length, `loser pays ${money(desk().limits.Fee)}`],
  ];
  document.getElementById("kpis").innerHTML = kpis
    .map(([label, term, value, sub]) => `<div class="kpi" data-term="${term}"><div class="k-label">${label}</div><div class="k-value">${value}</div><div class="k-sub">${escapeHTML(sub)}</div></div>`)
    .join("");
}

function renderFlow() {
  const ds = desk().disputes;
  const sel = ds.find((d) => d.id === selectedID);
  document.getElementById("flow").innerHTML = STAGES.map(([id, label, sub, term]) => {
    const n = ds.filter((d) => d.state === id).length;
    return `<li class="${sel && sel.state === id ? "now" : ""}" data-term="${term}"><span>${label}</span><b>${n}</b><span>${sub}</span></li>`;
  }).join("");
  document.getElementById("today").textContent = desk().today;
  document.getElementById("offset").textContent = desk().offset_days ? `(+${desk().offset_days} simulated days)` : "";
  const l = desk().limits;
  document.getElementById("limit-cb").textContent = l.Chargeback;
  document.getElementById("limit-rep").textContent = l.Representment;
  document.getElementById("limit-pre").textContent = l.PreArbitration;
  document.getElementById("limit-arb").textContent = l.Arbitration;
  document.getElementById("limit-fee").textContent = money(l.Fee);
}

let saleOptions = "";
function renderForm() {
  const sales = data.sales || [];
  const opts = sales.length
    ? sales
        .map((s) => `<option value="${escapeHTML(s.network_txn_id)}" data-amount="${s.amount}">${escapeHTML(s.arn.slice(-6))} · ${escapeHTML(s.merchant_name)} · ${escapeHTML(s.pan)} · ${money(s.amount, s.currency)} · ${escapeHTML(s.issuer_id)}</option>`)
        .join("")
    : `<option value="">No cleared sales yet: make a sale, then run the end of day</option>`;
  const sel = document.getElementById("sale");
  if (opts !== saleOptions) {
    const keep = sel.value;
    sel.innerHTML = opts;
    saleOptions = opts;
    if (keep && [...sel.options].some((o) => o.value === keep)) sel.value = keep;
    else syncAmount();
  }
  const reason = document.getElementById("reason");
  if (!reason.options.length) {
    reason.innerHTML = desk().reasons.map((r) => `<option value="${r.code}">${r.code} ${escapeHTML(r.name)} (${escapeHTML(r.category)})</option>`).join("");
    checkEligibility();
  }
  document.getElementById("reasons").innerHTML = desk().reasons
    .map((r) => `<tr><td class="mono">${r.code}</td><td><b>${escapeHTML(r.name)}</b><small>${escapeHTML(r.description)}</small></td><td>${r.responses.map(evidenceName).map(escapeHTML).join("<br>")}</td></tr>`)
    .join("");
}

function syncAmount() {
  const opt = document.getElementById("sale").selectedOptions[0];
  if (opt && opt.dataset.amount) document.getElementById("amount").value = (Number(opt.dataset.amount) / 100).toFixed(2);
  checkEligibility();
}

const toCents = (s) => Math.round(parseFloat(String(s).replace(/[$,]/g, "")) * 100) || 0;

let eligibilitySeq = 0;
async function checkEligibility() {
  const txn = document.getElementById("sale").value;
  const reason = document.getElementById("reason").value;
  const el = document.getElementById("eligibility");
  if (!txn || !reason) {
    el.innerHTML = "";
    return;
  }
  const seq = ++eligibilitySeq;
  const res = await (await fetch(`/api/disputes/eligible?txn=${encodeURIComponent(txn)}&reason=${encodeURIComponent(reason)}&amount=${toCents(document.getElementById("amount").value)}`)).json();
  if (seq !== eligibilitySeq) return;
  el.innerHTML = res.refusal
    ? `<div class="refusal" data-term="reason-code">✕ The network would refuse: ${escapeHTML(res.refusal)}</div>`
    : `<div class="ok" data-term="reason-code">✓ Eligible under ${escapeHTML(reason)}</div>`;
}

document.getElementById("sale").addEventListener("change", syncAmount);
document.getElementById("reason").addEventListener("change", checkEligibility);
document.getElementById("amount").addEventListener("input", checkEligibility);

document.getElementById("file").addEventListener("submit", async (e) => {
  e.preventDefault();
  const f = e.target;
  const err = document.getElementById("file-error");
  err.textContent = "";
  const res = await fetch("/api/disputes", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ network_txn_id: f.txn.value, reason: f.reason.value, amount: toCents(f.amount.value), note: f.note.value }),
  });
  const out = await res.json();
  if (!res.ok) {
    err.textContent = out.error;
    return;
  }
  selectedID = out.id;
  f.note.value = "";
  refresh();
});

function renderList() {
  const ds = desk().disputes;
  if (!ds.length) return;
  document.getElementById("disputes").innerHTML = ds
    .map((d) => `<tr data-id="${d.id}" aria-selected="${d.id === selectedID}">
      <td class="mono">${d.id}</td>
      <td>${escapeHTML(d.merchant_name)}<small class="mono">${escapeHTML(d.pan)}</small></td>
      <td class="mono">${escapeHTML(d.reason)}</td>
      <td class="num">${money(d.amount, d.currency)}${d.amount !== d.sale_amount ? `<small>of ${money(d.sale_amount, d.currency)}</small>` : ""}</td>
      <td>${stageBadge(d)}</td>
      <td>${d.deadline ? escapeHTML(d.deadline) : "—"}</td>
    </tr>`)
    .join("");
}

document.getElementById("disputes").addEventListener("click", (e) => {
  const tr = e.target.closest("tr[data-id]");
  if (!tr) return;
  selectedID = tr.dataset.id;
  history.replaceState(null, "", `#dispute=${selectedID}`);
  lastCase = "";
  render();
});

function renderCase() {
  const el = document.getElementById("case");
  const ds = desk().disputes;
  const d = ds.find((x) => x.id === selectedID) || ds[0];
  if (!d) return;
  selectedID = d.id;
  const key = JSON.stringify(d);
  if (key === lastCase) return; // Keep any half-filled action form
  lastCase = key;
  const r = reasonOf(d.reason);
  el.className = "";
  const events = d.events
    .map((ev) => `<li>
      <div class="t-head"><span class="badge ${ev.actor}" data-term="${ev.actor === "network" ? "network" : ev.actor}">${ev.actor}</span><b>${escapeHTML(ev.stage)}</b><span class="muted">${escapeHTML(ev.day)}</span></div>
      <div>${escapeHTML(ev.note)}</div>
      ${ev.money ? `<div class="t-money" data-term="net-settlement">Moves ${escapeHTML(ev.money)}</div>` : ""}
    </li>`)
    .join("");
  el.innerHTML = `
    <div class="case-head">
      <span class="amount">${money(d.amount, d.currency)}</span>
      ${stageBadge(d)}
      <span class="muted"><span class="mono">${d.id}</span> · ${escapeHTML(d.merchant_name)} · <span class="mono">${escapeHTML(d.pan)}</span> · ARN <span class="mono">${escapeHTML(d.arn)}</span></span>
    </div>
    <p><span data-term="reason-code"><b>${r.code} ${escapeHTML(r.name)}</b></span> · <span class="muted">${escapeHTML(r.description)}</span></p>
    <ol class="timeline">${events}</ol>
    ${actionPanel(d, r)}`;
}

// actionPanel offers whoever's turn it is their moves.
function actionPanel(d, r) {
  if (d.state === "chargeback") {
    const opts = r.responses.map((id) => `<option value="${id}">${escapeHTML(evidenceName(id))}</option>`).join("");
    return `<div class="turn"><h3><span class="badge acquirer" data-term="acquirer">acquirer</span> <span data-term="representment">Answer the chargeback by ${escapeHTML(d.deadline)}</span></h3>
      <label class="field"><span data-term="representment">Evidence</span><select data-input="evidence">${opts}</select></label>
      <div class="actions">
        <button type="button" class="primary" data-act="represent" data-term="representment">Represent with evidence</button>
        <button type="button" data-act="accept" data-actor="acquirer" data-term="chargeback">Accept the chargeback</button>
        <span class="error" data-error></span>
      </div></div>`;
  }
  if (d.state === "represented") {
    const opts = desk().rebuttals.map((x) => `<option value="${x.id}">${escapeHTML(x.name)}</option>`).join("");
    return `<div class="turn"><h3><span class="badge issuer" data-term="issuer">issuer</span> <span data-term="pre-arbitration">Answer "${escapeHTML(evidenceName(d.evidence))}" by ${escapeHTML(d.deadline)}</span></h3>
      <label class="field"><span data-term="pre-arbitration">Rebuttal</span><select data-input="rebuttal"><option value="">No new information</option>${opts}</select></label>
      <div class="actions">
        <button type="button" class="primary" data-act="prearbitrate" data-term="pre-arbitration">Go to pre-arbitration</button>
        <button type="button" data-act="accept" data-actor="issuer" data-term="representment">Accept and rebill the cardholder</button>
        <span class="error" data-error></span>
      </div></div>`;
  }
  if (d.state === "pre_arbitration") {
    return `<div class="turn"><h3><span class="badge acquirer" data-term="acquirer">acquirer</span> <span data-term="pre-arbitration">Issuer's rebuttal: "${escapeHTML(rebuttalName(d.rebuttal))}". Answer by ${escapeHTML(d.deadline)}</span></h3>
      <div class="actions">
        <button type="button" class="primary" data-act="arbitrate" data-term="pre-arbitration">Ask the network to arbitrate</button>
        <button type="button" data-act="accept" data-actor="acquirer" data-term="chargeback">Accept: give the money back</button>
        <span class="error" data-error></span>
      </div>
      <p class="hint">The network decides by whether the rebuttal answers "${escapeHTML(evidenceName(d.evidence))}". The loser pays ${money(desk().limits.Fee)}.</p></div>`;
  }
  return "";
}

document.getElementById("case").addEventListener("click", async (e) => {
  const b = e.target.closest("button[data-act]");
  if (!b) return;
  const panel = b.closest(".turn");
  const body = {
    actor: b.dataset.actor || "",
    evidence: panel.querySelector('[data-input="evidence"]')?.value || "",
    rebuttal: panel.querySelector('[data-input="rebuttal"]')?.value || "",
  };
  b.disabled = true;
  const res = await fetch(`/api/disputes/${selectedID}/${b.dataset.act}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    panel.querySelector("[data-error]").textContent = (await res.json()).error;
    b.disabled = false;
    return;
  }
  lastCase = "";
  refresh();
});

document.getElementById("advance").addEventListener("click", async () => {
  await fetch("/api/disputes/advance", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ days: 30 }) });
  lastCase = "";
  refresh();
});

function render() {
  renderKPIs();
  renderFlow();
  renderForm();
  renderList();
  renderCase();
}

let lastBody = "";
let timer = 0;
async function refresh() {
  clearTimeout(timer);
  timer = setTimeout(refresh, POLL_MS);
  let body;
  try {
    body = await (await fetch("/api/disputes")).text();
  } catch {
    lastBody = "";
    return;
  }
  if (body === lastBody) return;
  lastBody = body;
  data = JSON.parse(body);
  render();
}

// Point at each side's own desk, wherever its page is served.
fetch("/ui/pages")
  .then((r) => r.json())
  .then((pages) => {
    for (const p of pages) {
      const a = document.getElementById(`${p.id}-desk`);
      if (a) a.href = p.url;
    }
  })
  .catch(() => {});

refresh();
