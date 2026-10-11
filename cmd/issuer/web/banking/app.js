// The cardholder's online banking: sign in as one of this bank's customers,
// pick a card, and see its limit, balance, and pending and posted
// transactions. Everything comes from the issuer that holds the card.

const POLL_MS = 3000;
const PRODUCTS = {
  credit: ["Credit card", "credit-card"],
  debit: ["Debit card", "debit-card"],
  prepaid: ["Gift card", "prepaid-card"],
  healthcare: ["HSA/FSA card", "hsa-fsa-card"],
  fleet: ["Fleet card", "fleet-card"],
};
// Credit and fleet cards borrow against a limit; the others spend a balance.
const isCredit = (product) => product === "credit" || product === "fleet";

let bank = null;
let customers = [];
let customer = "";
let accountID = "";
let lastCustomers = "";
let lastStatement = "";
let posURL = ""; // The POS page, for "Pay at the POS"
let claims = []; // What a cardholder can say went wrong with a charge
let disputing = ""; // The charge whose dispute form is open
let draft = {}; // That form's answers, kept across redraws
let disputeNotice = ""; // What the bank said after filing

const $ = (id) => document.getElementById(id);
const store = {
  get: (k) => { try { return localStorage.getItem(k) || ""; } catch { return ""; } },
  set: (k, v) => { try { localStorage.setItem(k, v); } catch {} },
};

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}
const CURRENCIES = { "840": "USD", "978": "EUR", "826": "GBP", "124": "CAD" };
const money = (minor, cur = "840") =>
  new Intl.NumberFormat(undefined, { style: "currency", currency: CURRENCIES[cur] || "USD" }).format(minor / 100);
const day = (t) => new Date(t).toLocaleDateString(undefined, { month: "short", day: "numeric" });
const time = (t) => new Date(t).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });

// --- Customers and cards ---------------------------------------------------------

function renderCustomers() {
  const sel = $("customer");
  sel.innerHTML = customers.map((c) => `<option>${escapeHTML(c.holder)}</option>`).join("");
  if (!customers.some((c) => c.holder === customer)) customer = customers[0]?.holder || "";
  sel.value = customer;
  // The nav links carry the signed-in customer, so the POS rings up for them.
  window.snNav?.set("customer", customer);
}

function renderCards() {
  const c = customers.find((x) => x.holder === customer);
  const accounts = c ? c.accounts : [];
  if (!accounts.some((a) => a.id === accountID)) accountID = accounts[0]?.id || "";
  $("cards").innerHTML = accounts
    .map((a) => {
      const [name, term] = PRODUCTS[a.product] || [a.product, ""];
      return `<button type="button" class="card-pick" role="radio" aria-checked="${a.id === accountID}" data-id="${escapeHTML(a.id)}" data-term="${term}">
        <span class="mini" aria-hidden="true"></span>
        <span><b>${escapeHTML(name)}</b><small>•••• ${escapeHTML(a.pan.slice(-4))}</small></span>
        ${a.blocked ? '<span class="locked-tag">Locked</span>' : ""}
      </button>`;
    })
    .join("") || `<p class="empty">No cards for this customer.</p>`;
}

$("customer").addEventListener("change", (e) => {
  customer = e.target.value;
  store.set(`banking-customer-${bank.id}`, customer);
  window.snNav?.set("customer", customer);
  accountID = "";
  disputing = disputeNotice = "";
  renderCards();
  loadStatement(true);
});

$("cards").addEventListener("click", (e) => {
  const btn = e.target.closest(".card-pick");
  if (!btn) return;
  accountID = btn.dataset.id;
  store.set(`banking-account-${bank.id}`, accountID);
  disputing = disputeNotice = "";
  renderCards();
  loadStatement(true);
});

// payAtPOS links to the POS ringing up for this customer, with this card
// picked in the wallet.
function payAtPOS(a) {
  const u = new URL(window.snNav ? window.snNav.link(posURL) : posURL);
  u.searchParams.set("customer", customer);
  u.searchParams.set("card", a.pan.slice(-4));
  return u.toString();
}

// --- One account -----------------------------------------------------------------

function overview(st) {
  const a = st.account;
  const [name, term] = PRODUCTS[a.product] || [a.product, ""];
  // Pending is what is held against the card; a refund on its way doesn't
  // free anything up until it posts, so it is noted separately.
  const pending = `${money(a.held)}${st.pending_refs ? `<small class="refund-note">−${money(st.pending_refs)} refund on the way</small>` : ""}`;
  let numbers;
  if (isCredit(a.product)) {
    const pct = (v) => `${Math.max(0, Math.min(100, (v / a.limit) * 100))}%`;
    numbers = `
      <div class="big" data-term="available-credit"><small data-term="available-credit">Available credit</small><b>${money(a.available)}</b></div>
      <div data-term="credit-card"><small data-term="credit-card">Credit limit</small><b>${money(a.limit)}</b></div>
      <div data-term="statement"><small data-term="statement">Current balance</small><b>${money(st.balance)}</b></div>
      <div data-term="auth-hold"><small data-term="auth-hold">Pending</small><b>${pending}</b></div>
      <div class="util">
        <div class="util-bar" role="img" aria-label="${money(st.balance)} posted, ${money(a.held)} pending, of a ${money(a.limit)} limit">
          <span class="posted" style="width:${pct(st.balance)}"></span><span class="pending" style="width:${pct(a.held)}"></span>
        </div>
        <div class="util-legend"><span><i style="background:var(--brand)"></i>Posted ${money(st.balance)}</span>
          <span><i style="background:color-mix(in srgb, var(--brand) 45%, transparent)"></i>Pending ${money(a.held)}</span>
          <span>${Math.round(((st.balance + a.held) / a.limit) * 100)}% of your limit used</span></div>
      </div>`;
  } else {
    numbers = `
      <div class="big" data-term="available-credit"><small data-term="available-credit">Available balance</small><b>${money(a.available)}</b></div>
      <div data-term="statement"><small data-term="statement">Account balance</small><b>${money(st.balance)}</b></div>
      <div data-term="auth-hold"><small data-term="auth-hold">Pending</small><b>${pending}</b></div>
      <div data-term="${term}"><small data-term="${term}">Opening balance</small><b>${money(a.limit)}</b></div>`;
  }
  return `<section class="panel overview">
    <div>
      <div class="plastic${a.blocked ? " locked" : ""}" data-term="${term}">
        <div class="p-top"><span>${escapeHTML(st.bank)}</span><span>${escapeHTML(name)}</span></div>
        <div class="p-num">•••• •••• •••• ${escapeHTML(a.pan.slice(-4))}</div>
        <div class="p-bottom"><span>${escapeHTML(a.holder)}</span><span>Exp ${escapeHTML(a.expiry)}</span></div>
      </div>
      <div class="controls" data-term-scope="card-controls">
        <label class="switch" data-term="card-controls"><input type="checkbox" id="lock" ${a.blocked ? "checked" : ""}> Lock card</label>
        <span class="muted">${a.blocked ? "New purchases are declined until you unlock." : "Locking declines new purchases right away."}</span>
      </div>
      ${posURL ? `<a class="pay-pos" data-term="pos-terminal" href="${escapeHTML(payAtPOS(a))}">Pay at the POS with this card →</a>` : ""}
    </div>
    <div class="numbers">${numbers}</div>
  </section>`;
}

function amount(line) {
  return line.amount < 0
    ? `<span class="amt-credit">−${money(-line.amount, line.currency)}</span>`
    : money(line.amount, line.currency);
}

function description(line) {
  const refund = {
    refund: '<span class="tag credit" data-term="refund">Refund</span>',
    dispute: '<span class="tag credit" data-term="chargeback">Dispute credit</span>',
  }[line.kind] || "";
  return `${escapeHTML(line.merchant || "Card transaction")}${refund}${line.note ? `<small>${escapeHTML(line.note)}</small>` : ""}`;
}

function pendingTable(st) {
  if (!st.pending.length) return `<p class="empty">Nothing pending. Purchases show here as soon as the bank approves them.</p>`;
  return `<table><thead><tr><th>Date</th><th>Description</th><th class="num">Amount</th></tr></thead><tbody>
    ${st.pending.map((l) => `<tr>
      <td class="date">${day(l.time)}<small>${time(l.time)}</small></td>
      <td>${description(l)}<small>${l.kind === "refund" ? "Credited when the merchant's bank clears it" : `Authorization ${escapeHTML(l.auth_code || "")} · held against your ${isCredit(st.account.product) ? "available credit" : "balance"}`}</small></td>
      <td class="num">${amount(l)}</td></tr>`).join("")}
  </tbody></table>`;
}

function postedTable(st) {
  if (!st.posted.length) return `<p class="empty">No posted transactions yet. Pending purchases post after the network's end of day.</p>`;
  return `<table><thead><tr><th>Date</th><th>Posted</th><th>Description</th><th class="num">Amount</th></tr></thead><tbody>
    ${st.posted.map((l) => `<tr${l.network_txn_id === disputing && l.disputable ? ' class="disputing"' : ""}>
      <td class="date">${day(l.time)}</td>
      <td class="date">${day(l.posted)}</td>
      <td>${description(l)}${l.disputable && disputing !== l.network_txn_id ? `<button type="button" class="dispute-btn" data-dispute="${escapeHTML(l.network_txn_id)}" data-term="chargeback">Dispute this charge</button>` : ""}</td>
      <td class="num">${amount(l)}</td></tr>
      ${l.disputable && disputing === l.network_txn_id ? `<tr class="dispute-row"><td colspan="4">${disputeForm(l)}</td></tr>` : ""}`).join("")}
  </tbody></table>`;
}

// --- Disputing a charge ----------------------------------------------------------

// disputeForm asks what went wrong, in the cardholder's words. The bank
// picks the network reason code and files the chargeback.
function disputeForm(l) {
  const c = claims.find((x) => x.id === draft.claim);
  return `<form class="dispute-form" data-dispute-form data-term-scope="chargeback">
    <h4><span data-term="chargeback">Dispute</span> ${escapeHTML(l.merchant || "this charge")} · ${amount(l)}</h4>
    <fieldset>
      <legend data-term="reason-code">What went wrong?</legend>
      ${claims.map((x) => `<label class="claim"><input type="radio" name="claim" value="${escapeHTML(x.id)}"${x.id === draft.claim ? " checked" : ""}>
        <span><b>${escapeHTML(x.label)}</b><small>${escapeHTML(x.hint)}</small></span></label>`).join("")}
    </fieldset>
    <label class="d-field"${c?.ask_amount ? "" : " hidden"} data-agreed><span data-term="de4">Amount you agreed to pay</span><input name="agreed" inputmode="decimal" placeholder="0.00" value="${escapeHTML(draft.agreed || "")}"></label>
    <label class="d-field"><span>Tell us more <small>(optional)</small></span><input name="story" maxlength="280" value="${escapeHTML(draft.story || "")}"></label>
    <p class="muted">While we look into it, we credit the disputed amount back to your card. The merchant can answer with evidence; if it shows the charge was right, we charge it again.</p>
    <div class="d-actions"><button type="submit" class="primary" data-term="chargeback">Submit dispute</button><button type="button" data-cancel-dispute>Cancel</button><span class="d-error" data-dispute-error></span></div>
  </form>`;
}

function redrawStatement() {
  lastStatement = "";
  loadStatement(true);
}

$("account").addEventListener("click", (e) => {
  const open = e.target.closest("[data-dispute]");
  if (open) {
    disputing = open.dataset.dispute;
    draft = {};
    disputeNotice = "";
    redrawStatement();
  } else if (e.target.closest("[data-cancel-dispute]")) {
    disputing = "";
    redrawStatement();
  }
});

$("account").addEventListener("input", (e) => {
  const f = e.target.closest("[data-dispute-form]");
  if (!f) return;
  draft = { claim: f.claim.value, agreed: f.agreed.value, story: f.story.value };
  f.querySelector("[data-agreed]").hidden = !claims.find((x) => x.id === draft.claim)?.ask_amount;
});

$("account").addEventListener("submit", async (e) => {
  const f = e.target.closest("[data-dispute-form]");
  if (!f) return;
  e.preventDefault();
  const err = f.querySelector("[data-dispute-error]");
  if (!f.claim.value) {
    err.textContent = "Choose what went wrong.";
    return;
  }
  const btn = f.querySelector('button[type="submit"]');
  btn.disabled = true;
  err.textContent = "";
  let res, out;
  try {
    res = await fetch(`/api/banking/accounts/${encodeURIComponent(accountID)}/disputes`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        network_txn_id: disputing,
        claim: f.claim.value,
        agreed: Math.round(parseFloat(String(f.agreed.value).replace(/[$,]/g, "")) * 100) || 0,
        story: f.story.value,
      }),
    });
    out = await res.json();
  } catch {
    out = { error: "Couldn't reach the bank. Try again." };
  }
  if (!res?.ok) {
    err.textContent = out.error || `Something went wrong (${res?.status}).`;
    btn.disabled = false;
    return;
  }
  disputeNotice = `Dispute filed. We've credited ${money(out.amount)} back to your card while the merchant responds (by ${out.deadline}). Case ${out.dispute_id}, reason code ${out.reason}.`;
  disputing = "";
  draft = {};
  redrawStatement();
});

function renderStatement(st) {
  $("account").innerHTML = `${overview(st)}
    <section class="panel" data-term-scope="auth-hold">
      <div class="txn-head"><h3><span data-term="auth-hold">Pending transactions</span></h3><small>${st.pending.length} pending</small></div>
      ${pendingTable(st)}
    </section>
    <section class="panel" data-term-scope="clearing">
      <div class="txn-head"><h3><span data-term="clearing">Posted transactions</span></h3><small>${st.posted.length} posted</small></div>
      ${disputeNotice ? `<p class="d-notice" data-term="chargeback">${escapeHTML(disputeNotice)}</p>` : ""}
      ${postedTable(st)}
    </section>`;
  $("lock").addEventListener("change", async (e) => {
    e.target.disabled = true;
    await fetch(`/api/accounts/${encodeURIComponent(st.account.id)}/blocked`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ blocked: e.target.checked }),
    }).catch(() => {});
    lastCustomers = lastStatement = "";
    refresh();
  });
}

async function loadStatement(force) {
  if (!accountID) {
    $("account").innerHTML = `<p class="empty">This customer has no cards at ${escapeHTML(bank?.name || "this bank")}.</p>`;
    return;
  }
  const id = accountID;
  const body = await (await fetch(`/api/banking/accounts/${encodeURIComponent(id)}`)).text();
  if (id !== accountID || (!force && body === lastStatement)) return; // Switched cards meanwhile, or nothing changed
  lastStatement = body;
  renderStatement(JSON.parse(body));
}

// --- Polling ---------------------------------------------------------------------

// refresh loads the customers and the selected account once.
async function refresh() {
  try {
    const body = await (await fetch("/api/banking/customers")).text();
    if (body !== lastCustomers) {
      lastCustomers = body;
      const data = JSON.parse(body);
      if (!bank) {
        bank = data.bank;
        document.body.dataset.bank = bank.id;
        document.title = `${bank.name} · Online Banking`;
        $("bank-name").textContent = bank.name;
        $("bank-mark").textContent = bank.name[0];
        // A customer named in the link (from the POS, say) wins over the last
        // one signed in here. One who banks elsewhere goes to their bank's site.
        const wanted = window.snNav?.get("customer") || "";
        const home = data.directory?.[wanted];
        if (wanted && home && !data.customers.some((c) => c.holder === wanted) && new URL(home).origin !== location.origin) {
          location.replace(window.snNav ? window.snNav.link(home) : home);
          return;
        }
        customer = wanted || store.get(`banking-customer-${bank.id}`);
        accountID = new URLSearchParams(location.search).get("account") || store.get(`banking-account-${bank.id}`);
        $("banks").innerHTML = data.banks
          .map((b) => `<a href="${escapeHTML(b.url)}"${b.id === bank.id ? ' aria-current="page"' : ""}>${escapeHTML(b.name)}</a>`)
          .join("");
      }
      customers = data.customers;
      claims = data.claims || [];
      renderCustomers();
      renderCards();
    }
    await loadStatement(false);
  } catch {
    $("account").innerHTML = `<p class="empty">Online banking is unavailable right now. Retrying…</p>`;
    lastStatement = "";
  }
}

fetch("/ui/pages")
  .then((r) => r.json())
  .then((pages) => {
    posURL = pages.find((p) => p.id === "pos")?.url || "";
    lastStatement = ""; // Redraw with the button
  })
  .catch(() => {});

async function poll() {
  await refresh();
  setTimeout(poll, POLL_MS);
}

poll();
