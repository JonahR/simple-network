// Issuer back office: polls /api/overview and shows how this bank decided
// each authorization, check by check, and what each hold did to the
// cardholder's available credit.

// Checks the bank runs, in order, with the glossary term that explains each.
const CHECKS = ["Account", "Card status", "Expiry", "CVV2", "PIN", "Product rules", "Funds"];
const CHECK_TERMS = {
  Reversal: "reversal",
  Account: "account-id",
  "Card status": "card-controls",
  Expiry: "de14",
  CVV2: "cvv2",
  PIN: "pin",
  "Product rules": "card-products",
  Funds: "available-credit",
};
const MODES = [
  { id: "normal", label: "Normal" },
  { id: "slow", label: "Slow" },
  { id: "down", label: "Down" },
];
const POLL_MS = 1500;
// The banks hold their accounts in US dollars.
const ACCOUNT_CURRENCY = "840";

let data = null;
// The selected authorization's network transaction ID, or null to follow the newest.
let selectedID = new URLSearchParams(location.hash.slice(1)).get("txn");

function money(minor, currencyCode) {
  const iso = (data && data.currencies[currencyCode]) || "USD";
  return new Intl.NumberFormat(undefined, { style: "currency", currency: iso }).format(minor / 100);
}

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

function timeOf(iso) {
  return new Date(iso).toLocaleTimeString();
}

// statusBadge shows the response with words and an icon, not color alone.
function statusBadge(d) {
  const partial = d.response_code === "10";
  const ok = data.response_codes[d.response_code]?.approved;
  const [cls, icon, term] = partial ? ["", "◐", "partial-approval"] : ok ? ["good", "✓", "de39"] : ["bad", "✕", "de39"];
  return `<span class="status ${cls}" data-term="${term}"><span aria-hidden="true">${icon}</span> ${escapeHTML(d.response_code)} ${escapeHTML(d.response_text)}</span>`;
}

function holdLabel(d) {
  if (d.hold === "active") return `<span data-term="auth-hold">Active</span>`;
  if (d.hold === "released") return `<span data-term="reversal">Released</span>`;
  return `<span class="muted">—</span>`;
}

function renderHeader() {
  document.getElementById("banks").innerHTML = data.banks
    .map((b) => `<a href="${escapeHTML(b.url)}" data-term="issuer"${b.id === data.bank.id ? ' aria-current="page"' : ""} title="${escapeHTML(b.name)}">${escapeHTML(b.id)}</a>`)
    .join("");
  const info = document.getElementById("bank-info");
  info.textContent = `${data.bank.name} · ${data.accounts.length} card accounts`;
  info.dataset.term = "issuer";
  document.title = `${data.bank.name} · simple-network Issuer`;
}

function renderKPIs() {
  const s = data.stats;
  const kpis = [
    ["Authorizations", "authorization", s.count],
    ["Approval rate", "approval-rate", s.count ? Math.round(s.approval_rate * 100) + "%" : "—"],
    ["Active holds", "auth-hold", s.held_count ? `${money(s.held_amount, ACCOUNT_CURRENCY)} · ${s.held_count}` : "—"],
    ["Reversals received", "reversal", s.reversals],
  ];
  document.getElementById("kpis").innerHTML = kpis
    .map(([label, term, value]) => `<div class="kpi"><small><span data-term="${term}">${label}</span></small><b>${value}</b></div>`)
    .join("");
}

function selected() {
  const ds = data.decisions;
  if (!ds.length) return null;
  return (selectedID && ds.find((d) => d.network_txn_id === selectedID)) || ds[0];
}

// bar draws the account's limit as segments: holds placed earlier, this
// authorization's hold, and what is still available.
function bar(limit, earlier, thisHold) {
  if (limit <= 0) return "";
  const pct = (v) => `${Math.max((v / limit) * 100, v > 0 ? 0.8 : 0)}%`;
  return `<div class="bar" role="img" aria-label="${escapeHTML(`${money(earlier, ACCOUNT_CURRENCY)} held earlier, ${money(thisHold, ACCOUNT_CURRENCY)} this hold`)}">
    ${earlier > 0 ? `<span class="held" style="width:${pct(earlier)}"></span>` : ""}
    ${thisHold > 0 ? `<span class="this" style="width:${pct(thisHold)}"></span>` : ""}
    <span class="free"></span>
  </div>`;
}

function renderDecision(d) {
  const el = document.getElementById("decision");
  if (!d) return;
  el.className = "";
  const entry = data.entry_modes[d.entry_mode] || d.entry_mode;
  const wallet = d.wallet ? ` · ${data.wallets[d.wallet] || d.wallet}` : "";
  document.getElementById("decision-title").textContent = selectedID && selectedID === d.network_txn_id ? "" : "following newest";

  const ran = d.checks.map((c) => c.name);
  const stopped = d.checks.find((c) => c.result === "fail");
  const notRun = stopped && stopped.name !== "Reversal" ? CHECKS.filter((n) => !ran.includes(n)) : [];
  const checks = d.checks
    .map((c) => {
      const icon = { pass: "✓", fail: "✕", skip: "–" }[c.result];
      const word = { pass: "passed", fail: "failed", skip: "skipped" }[c.result];
      return `<li class="${c.result}">
        <span class="icon" role="img" aria-label="${word}">${icon}</span>
        <span class="name"><span data-term="${CHECK_TERMS[c.name] || "issuer"}">${escapeHTML(c.name)}</span></span>
        <span class="detail">${escapeHTML(c.detail)}</span>
      </li>`;
    })
    .join("");

  // Available credit before and after. A decline leaves it unchanged.
  const earlier = d.limit - d.available_before;
  const thisHold = d.available_before - d.available_after;
  const impact = d.account_id && d.limit > 0
    ? `<div class="impact">
        <h3><span data-term="available-credit">Available credit</span> · ${escapeHTML(d.account_id)}</h3>
        <div class="bar-row"><span class="label">Before</span>${bar(d.limit, earlier, 0)}<span class="value">${money(d.available_before, d.currency)}</span></div>
        <div class="bar-row"><span class="label">After</span>${bar(d.limit, earlier, thisHold)}<span class="value">${money(d.available_after, d.currency)}</span></div>
        <div class="legend">
          <span><i class="held"></i><span data-term="auth-hold">Held earlier</span></span>
          <span><i class="this"></i><span data-term="auth-hold">This hold</span> ${thisHold > 0 ? money(thisHold, d.currency) : "none"}</span>
          <span><i class="free"></i><span data-term="available-credit">Available</span> of ${money(d.limit, d.currency)}</span>
        </div>
      </div>`
    : "";

  el.innerHTML = `
    <div class="decision-head">
      <span class="amount">${money(d.requested, d.currency)}</span>
      ${statusBadge(d)}
      ${d.approved && d.approved !== d.requested ? `<span class="muted"><span data-term="partial-approval">approved</span> ${money(d.approved, d.currency)}</span>` : ""}
    </div>
    <div class="decision-sub">${escapeHTML(d.merchant || "Unknown merchant")} · <span class="mono">${escapeHTML(d.pan)}</span> · ${escapeHTML(entry)}${escapeHTML(wallet)}${d.token ? ` · token <span class="mono">${escapeHTML(d.token)}</span>` : ""}</div>
    <ol class="checks">${checks}</ol>
    ${notRun.length ? `<div class="not-reached">Stopped at ${escapeHTML(stopped.name)}. Not checked: ${escapeHTML(notRun.join(", "))}.</div>` : ""}
    ${impact}
    <div class="decision-foot">
      ${d.auth_code ? `<span><span data-term="de38">Auth code</span> <b class="mono">${escapeHTML(d.auth_code)}</b></span>` : ""}
      <span><span data-term="auth-hold">Hold</span> <b>${d.hold === "none" ? "None" : d.hold === "active" ? "Active" : "Released by reversal"}</b></span>
      <span><span data-term="network-txn-id">Network txn</span> <b class="mono">${escapeHTML(d.network_txn_id)}</b></span>
    </div>`;
}

function renderDecisions(current) {
  const ds = data.decisions;
  if (!ds.length) return;
  document.getElementById("decisions").innerHTML = ds
    .map((d) => {
      const amount = d.approved && d.approved !== d.requested
        ? `${money(d.approved, d.currency)} <small>of ${money(d.requested, d.currency)}</small>`
        : money(d.requested, d.currency);
      return `<tr data-i="${escapeHTML(d.network_txn_id)}" aria-selected="${d === current}">
        <td>${timeOf(d.time)}</td>
        <td class="mono">${escapeHTML(d.pan)}</td>
        <td class="num">${amount}</td>
        <td>${statusBadge(d)}</td>
        <td>${holdLabel(d)}</td>
      </tr>`;
    })
    .join("");
}

function renderAccounts() {
  document.getElementById("accounts").innerHTML = data.accounts
    .map((a) => {
      const product = data.product_names[a.product] || a.product;
      const status = a.blocked
        ? `<span class="frozen" data-term="card-controls"><span aria-hidden="true">❄</span> Frozen</span><button type="button" class="small" data-account="${a.id}" data-blocked="false" data-term="card-controls">Unfreeze</button>`
        : `<span data-term="card-controls">Active</span><button type="button" class="small" data-account="${a.id}" data-blocked="true" data-term="card-controls">Freeze</button>`;
      return `<tr>
        <td class="mono">${escapeHTML(a.id)}</td>
        <td class="mono">${escapeHTML(a.pan)}<small>expires ${escapeHTML(a.expiry)}${a.has_pin ? " · PIN" : ""}</small></td>
        <td>${escapeHTML(a.holder)}</td>
        <td>${escapeHTML(product)}</td>
        <td class="num">${money(a.limit, ACCOUNT_CURRENCY)}</td>
        <td><div class="meter">${bar(a.limit, a.held, 0)}<span>${money(a.available, ACCOUNT_CURRENCY)}</span></div></td>
        <td class="num">${a.holds ? `${money(a.held, ACCOUNT_CURRENCY)}<small>${a.holds} hold${a.holds > 1 ? "s" : ""}</small>` : "—"}</td>
        <td>${status}</td>
      </tr>`;
    })
    .join("");
}

function renderReversals() {
  if (!data.reversals.length) return;
  document.getElementById("reversals").innerHTML = data.reversals
    .map((r) => `<tr>
      <td>${timeOf(r.time)}</td>
      <td class="mono">${escapeHTML(r.network_txn_id.slice(0, 18))}…</td>
      <td>${escapeHTML(r.reason || "—")}</td>
      <td>${r.matched ? `Released ${money(r.released, ACCOUNT_CURRENCY)} on ${escapeHTML(r.account_id)}` : "No hold yet: the late authorization will be declined"}</td>
    </tr>`)
    .join("");
}

function renderMode() {
  document.getElementById("mode").innerHTML = `<span class="seg" role="group" aria-label="Simulate ${escapeHTML(data.bank.name)}">${MODES.map(
    (m) => `<button type="button" data-mode="${m.id}" data-term="issuer-health" aria-pressed="${data.mode === m.id}">${m.label}</button>`
  ).join("")}</span>`;
}

function render() {
  const current = selected();
  renderHeader();
  renderKPIs();
  renderDecision(current);
  renderDecisions(current);
  renderAccounts();
  renderReversals();
  renderMode();
}

document.getElementById("decisions").addEventListener("click", (e) => {
  const tr = e.target.closest("tr[data-i]");
  if (!tr) return;
  selectedID = tr.dataset.i;
  history.replaceState(null, "", `#txn=${selectedID}`);
  render();
});

document.getElementById("accounts").addEventListener("click", async (e) => {
  const b = e.target.closest("button[data-account]");
  if (!b) return;
  await fetch(`/api/accounts/${b.dataset.account}/blocked`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ blocked: b.dataset.blocked === "true" }),
  });
  poll();
});

document.getElementById("mode").addEventListener("click", async (e) => {
  const b = e.target.closest("button[data-mode]");
  if (!b) return;
  await fetch("/admin/mode", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode: b.dataset.mode }) });
  poll();
});

// lastBody is the previous poll's response; the page redraws only when it
// changes, so a label being hovered in Help mode isn't replaced under the pointer.
let lastBody = "";
let timer = 0;

async function poll() {
  clearTimeout(timer);
  timer = setTimeout(poll, POLL_MS);
  let body;
  try {
    body = await (await fetch("/api/overview")).text();
  } catch {
    lastBody = "";
    document.getElementById("bank-info").textContent = "Bank not reachable. Retrying…";
    return;
  }
  if (body === lastBody) return;
  lastBody = body;
  data = JSON.parse(body);
  render();
}

window.addEventListener("hashchange", () => {
  selectedID = new URLSearchParams(location.hash.slice(1)).get("txn");
  if (data) render();
});

fetch("/ui/pages")
  .then((r) => r.json())
  .then((pages) => {
    const net = pages.find((p) => p.id === "network");
    if (net) document.getElementById("network-link").href = net.url;
  })
  .catch(() => {});

poll();
