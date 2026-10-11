const form = document.getElementById("sale");
const resultEl = document.getElementById("result");
const historyEl = document.getElementById("history");

// Terminal configuration, loaded from /api/terminal. `terminal` is the lane
// the cashier picked in the merchant picker; `terminals` lists every lane.
let terminal = {};
let terminals = [];
let mtis = {};
let mccs = {};
let currencies = {};
let entryModes = {};
let wallets = {};
let dashboardURL = "";
let acquirerURL = "";
let cofIndicators = {};
let binProducts = {};
let productNames = {};

const ENTRY = { manual: "011", swipe: "901", chip: "051", contactless: "071", ecommerce: "812", cof: "100", fallback: "801" };

// Human-readable labels for the ISO 8583 fields, in display order.
const FIELDS = [
  ["mti", "MTI", "Message type"],
  ["de2_pan", "DE2", "Primary account number"],
  ["de3_processing_code", "DE3", "Processing code"],
  ["de4_amount", "DE4", "Amount (minor units)"],
  ["de7_transmission_datetime", "DE7", "Transmission date/time"],
  ["de11_stan", "DE11", "System trace audit number"],
  ["de12_local_time", "DE12", "Local time"],
  ["de13_local_date", "DE13", "Local date"],
  ["de14_expiry", "DE14", "Expiration (YYMM)"],
  ["de18_mcc", "DE18", "Merchant category code"],
  ["de22_pos_entry_mode", "DE22", "POS entry mode"],
  ["de32_acquirer_id", "DE32", "Acquirer ID"],
  ["de35_track2", "DE35", "Track 2 data"],
  ["de37_rrn", "DE37", "Retrieval reference number"],
  ["de41_terminal_id", "DE41", "Terminal ID"],
  ["de42_merchant_id", "DE42", "Merchant ID"],
  ["de43_merchant_name_location", "DE43", "Merchant name/location"],
  ["de48_fleet", "DE48", "Fleet data"],
  ["de48_cof_indicator", "DE48", "Credential on file"],
  ["de49_currency", "DE49", "Currency code"],
  ["de52_pin_data", "DE52", "PIN block"],
  ["de54_additional_amounts", "DE54", "Additional amounts"],
  ["de55_arqc", "DE55", "Application cryptogram (ARQC)"],
  ["de123_avs", "DE123", "Billing address"],
  ["eci", "", "E-commerce indicator"],
  ["cavv", "", "Authentication value"],
  ["auth_type", "", "Authorization type"],
  ["original_txn_id", "", "Original transaction ID"],
  ["cardholder_name", "", "Cardholder name"],
  ["wallet_provider", "", "Wallet provider"],
];

const TXN_TYPES = { "00": "Purchase", "09": "Purchase with cash back", "20": "Refund", "30": "Balance inquiry" };
const ACCOUNT_TYPES = { "00": "default account", "20": "checking", "30": "credit" };
const AMOUNT_TYPES = { "40": "Cash back", "4S": "HSA/FSA eligible" };
const AUTH_TYPES = { estimated: "Estimated: a pre-auth for an estimate", incremental: "Incremental: adds to an earlier pre-auth" };
const ECIS = { "05": "Fully authenticated with 3-D Secure", "06": "Authentication attempted", "07": "Not authenticated" };
const AVS_RESULTS = { Y: "Street address and ZIP match", Z: "ZIP matches, street doesn't", A: "Street matches, ZIP doesn't", N: "Neither matches", U: "Address unavailable" };
const CVV2_RESULTS = { M: "Match", N: "No match", P: "Not processed", U: "Issuer has no CVV2 for this card" };
const THREEDS_STATUS = { Y: "Authenticated", C: "Challenge required", N: "Not authenticated", U: "Couldn't authenticate" };

// --- Cardholder wallet -------------------------------------------------------

const ALL_METHODS = ["tap", "insert", "swipe", "key"];

// The cardholder's cards. Numbers are Luhn-valid test numbers; BINs match the
// terminal's BIN table so the right prompts appear. The two credit cards are
// also provisioned to each mobile wallet with a distinct device token.
// Expiries are fixed and must match the issuer's records (internal/issuer/seed.go)
// and the token vault (internal/network/vault.go); cmd/pos/expiry_test.go checks.
const WALLET = [
  { label: "Everyday Credit", style: "blue", number: "4242424242424242", expiry: "12/33", cvv: "123", name: "Jane Doe",
    zip: "94105", street: "123 Market St",
    methods: ALL_METHODS,
    tokens: {
      apple_pay:   { number: "4895372051310681", expiry: "09/34" },
      google_pay:  { number: "4895373708421368", expiry: "11/34" },
      samsung_pay: { number: "4895377555006883", expiry: "06/34" },
    } },
  { label: "Rewards Plus", style: "dark", number: "5555555555554444", expiry: "08/32", cvv: "456", name: "John Smith",
    zip: "10001", street: "456 Oak Ave",
    methods: ALL_METHODS,
    tokens: {
      apple_pay:   { number: "5220930238452479", expiry: "03/35" },
      google_pay:  { number: "5220934871046911", expiry: "01/35" },
      samsung_pay: { number: "5220933784416278", expiry: "05/35" },
    } },
  { label: "Checking Debit", style: "green", number: "4000056655665556", expiry: "05/33", cvv: "789", name: "Jane Doe",
    hint: "PIN 1234", methods: ALL_METHODS },
  { label: "Gift Card", style: "purple", number: "4358805984634941", expiry: "07/32", cvv: "321", name: "",
    methods: ["swipe", "key"] },
  { label: "Fleet Card", style: "orange", number: "5568007143162129", expiry: "02/33", cvv: "654", name: "Acme Logistics",
    hint: "Driver 4821", methods: ["insert", "swipe", "key"] },
  { label: "Health Savings", style: "teal", number: "4716006861111015", expiry: "11/32", cvv: "852", name: "Jane Doe",
    methods: ALL_METHODS },
  // Buy now, pay later: the provider issues a new single-use virtual card for each purchase.
  { label: "Pay Later", style: "pink", virtual: true, bin: "485932", name: "Jane Doe", methods: ["key"] },
];

let selected = 0;

function renderWallet() {
  const c = WALLET[selected];
  const last4 = c.number ? c.number.slice(-4) : "····";
  document.getElementById("card-face").innerHTML = `
    <div class="card ${c.style}">
      <div class="card-top"><span>${escapeHTML(c.label)}</span><span>simple-network</span></div>
      ${c.virtual ? `<div class="virtual-note">Virtual · new number each purchase</div>` : `<div class="chip"></div>`}
      <div class="card-number">•••• •••• •••• ${last4}</div>
      <div class="card-bottom">
        <span><small>Cardholder</small>${escapeHTML(c.name || "—")}</span>
        <span><small>Expires</small>${c.expiry || "—"}</span>
      </div>
    </div>`;

  const list = document.getElementById("card-list");
  list.innerHTML = "";
  WALLET.forEach((card, i) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "card-row";
    btn.setAttribute("role", "radio");
    btn.setAttribute("aria-checked", String(i === selected));
    const product = card.virtual ? "Virtual credit" : productNames[productFor(card.number)] || "Credit";
    btn.innerHTML = `
      <span class="swatch ${card.style}"></span>
      <span class="card-row-label">${escapeHTML(card.label)}</span>
      <span class="card-row-meta">${escapeHTML(card.hint || product)}</span>`;
    btn.addEventListener("click", () => {
      selected = i;
      renderWallet();
    });
    list.append(btn);
  });

  document.querySelectorAll(".present button").forEach((b) => {
    b.disabled = !c.methods.includes(b.dataset.method);
  });
}

document.querySelectorAll(".present button").forEach((b) =>
  b.addEventListener("click", () => presentCard(WALLET[selected], b.dataset.method))
);

// presentCard simulates the cardholder tapping, inserting, swiping, or reading
// out a card. The card supplies what the terminal can read from it.
// The card whose chip just failed to read; its next swipe is a fallback (DE22 801).
let chipFailedFor = null;

async function failChipRead(c) {
  resetCardRead();
  clearErrors();
  form.card_number.value = form.expiry.value = form.cvv.value = form.cardholder_name.value = "";
  chipFailedFor = c.number;
  setReadBadge("Chip read failed");
  try {
    const res = await fetch("/api/chip-failure", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ card_number: c.number }),
    });
    const data = await res.json();
    showErrors({ entry_mode: data.message || data.error });
  } catch {
    showErrors({ form: "Could not reach the terminal server." });
  }
}

function presentCard(c, method) {
  if (method === "insert" && document.getElementById("chip_broken").checked && !c.virtual) {
    failChipRead(c);
    return;
  }
  if (c.virtual) {
    c.number = generatePAN(c.bin, 16);
    c.expiry = monthsFromNow(12);
    c.cvv = String(100 + Math.floor(Math.random() * 900));
    renderWallet();
  }
  resetCardRead();
  clearErrors();
  form.entry_mode.value = { tap: ENTRY.contactless, insert: ENTRY.chip, swipe: ENTRY.swipe, key: ENTRY.manual }[method];
  form.card_number.value = groupDigits(c.number);
  form.expiry.value = c.expiry;
  form.cvv.value = method === "key" ? c.cvv : "";
  form.cardholder_name.value = method === "tap" ? "" : c.name; // Contactless reads don't include the name
  // Reading out a card for a keyed or online sale includes the billing address, for AVS.
  form.billing_zip.value = method === "key" ? c.zip || "" : "";
  form.billing_street.value = method === "key" ? c.street || "" : "";

  if (method === "tap" || method === "insert") form.cryptogram.value = newCryptogram();
  if (method === "swipe") form.track2.value = track2(c.number, c.expiry, c.methods.includes("insert"));
  const fallback = method === "swipe" && chipFailedFor === c.number;
  if (fallback) form.entry_mode.value = ENTRY.fallback;
  if (method !== "swipe" || !fallback) chipFailedFor = null;
  const badge = fallback ? "Fallback swipe" : { tap: "Contactless read", insert: "Chip read", swipe: "Stripe read" }[method];
  if (badge) setReadBadge(badge);

  updateForm();
  fillRandomAmount();
  focusNext();
}

// Track 2: PAN, "=", expiry as YYMM, service code, discretionary data. The
// service code is 201 for a chip card (the terminal declines the swipe and asks
// for the chip) and 101 for a stripe-only card.
function track2(pan, expiry, hasChip) {
  const [mm, yy] = expiry.split("/");
  const serviceCode = hasChip ? "201" : "101";
  const discretionary = String(Math.floor(Math.random() * 1e5)).padStart(5, "0");
  return `${pan}=${yy}${mm}${serviceCode}${discretionary}`;
}

// --- Mobile wallets ----------------------------------------------------------

const MOBILE_WALLETS = [
  { id: "apple_pay", name: "Apple Pay", short: "Apple", auth: "Face ID…" },
  { id: "google_pay", name: "Google Pay", short: "Google", auth: "Unlock phone…" },
  { id: "samsung_pay", name: "Samsung Pay", short: "Samsung", auth: "Fingerprint…" },
];
const MW_CARDS = WALLET.filter((c) => c.tokens);

let mwWallet = MOBILE_WALLETS[0];
let mwCard = 0;
const mwStatus = document.getElementById("mw-status");
const mwPay = document.getElementById("mw-pay");

function renderMobileWallets() {
  const tabs = document.getElementById("mw-tabs");
  tabs.innerHTML = "";
  for (const w of MOBILE_WALLETS) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "mw-tab";
    btn.setAttribute("role", "tab");
    btn.setAttribute("aria-selected", String(w === mwWallet));
    btn.setAttribute("aria-label", w.name);
    btn.textContent = w.short;
    btn.addEventListener("click", () => {
      if (mwPay.disabled) return; // don't switch mid-tap
      mwWallet = w;
      renderMobileWallets();
    });
    tabs.append(btn);
  }

  const cards = document.getElementById("mw-cards");
  cards.innerHTML = "";
  MW_CARDS.forEach((c, i) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "mw-card";
    btn.setAttribute("role", "radio");
    btn.setAttribute("aria-checked", String(i === mwCard));
    btn.innerHTML = `
      <span class="swatch ${c.style}"></span>
      <span class="mw-card-text">${escapeHTML(c.label)}<small>Device •••• ${c.tokens[mwWallet.id].number.slice(-4)}</small></span>`;
    btn.addEventListener("click", () => {
      mwCard = i;
      renderMobileWallets();
    });
    cards.append(btn);
  });

  document.getElementById("mw-phone").dataset.wallet = mwWallet.id;
  mwPay.textContent = `Pay with ${mwWallet.name}`;
}

mwPay.addEventListener("click", async () => {
  const w = mwWallet;
  const token = MW_CARDS[mwCard].tokens[w.id];
  mwPay.disabled = true;
  mwStatus.className = "mw-status";
  mwStatus.textContent = "Hold near reader…";
  await sleep(600);
  mwStatus.textContent = w.auth;
  await sleep(600);

  resetCardRead();
  clearErrors();
  form.entry_mode.value = ENTRY.contactless;
  form.card_number.value = groupDigits(token.number);
  form.expiry.value = token.expiry;
  form.cvv.value = "";
  form.cardholder_name.value = ""; // Mobile wallets don't share the cardholder's name in store
  form.wallet.value = w.id;
  form.cryptogram.value = newCryptogram();
  setReadBadge(`${w.name} · device token`);
  updateForm();
  fillRandomAmount();

  mwStatus.className = "mw-status done";
  mwStatus.textContent = "Done ✓";
  mwPay.disabled = false;
  focusNext();
});

// --- Sale form ---------------------------------------------------------------

function setReadBadge(text) {
  const badge = document.getElementById("read-badge");
  badge.textContent = text;
  badge.hidden = false;
}

// resetCardRead forgets whatever the last card or phone supplied.
function resetCardRead() {
  form.wallet.value = form.cryptogram.value = form.track2.value = "";
  document.getElementById("read-badge").hidden = true;
  mwStatus.className = "mw-status";
  mwStatus.textContent = "Ready";
}

// Editing card details by hand means the card read no longer applies, so the
// sale falls back to manual entry.
for (const name of ["card_number", "expiry"]) {
  form[name].addEventListener("input", () => {
    if (!form.wallet.value && !form.cryptogram.value && !form.track2.value) return;
    resetCardRead();
    form.entry_mode.value = ENTRY.manual;
    updateForm();
  });
}

function productFor(pan) {
  return pan && pan.length >= 6 ? binProducts[pan.slice(0, 6)] || "credit" : null;
}

// updateForm shows only the inputs this sale needs: the card-on-file picker
// or card fields, and the prompts the card's product and entry mode call for.
function updateForm() {
  const entry = form.entry_mode.value;
  const cof = entry === ENTRY.cof;
  show(document.getElementById("cof-fields"), cof);
  show(document.getElementById("card-fields"), !cof);
  if (cof) {
    resetCardRead();
    for (const name of ["card_number", "expiry", "cvv", "cardholder_name"]) form[name].value = "";
  } else {
    form.card_on_file.value = form.cof_indicator.value = "";
  }

  const product = cof ? null : productFor(digits(form.card_number.value));
  const productBadge = document.getElementById("product-badge");
  productBadge.hidden = !product;
  productBadge.textContent = productNames[product] || "";

  const wallet = form.wallet.value;
  const onPINPad = !wallet && [ENTRY.chip, ENTRY.swipe, ENTRY.contactless, ENTRY.fallback].includes(entry);
  const debitOnPad = product === "debit" && onPINPad;
  const prompts = {
    pin: debitOnPad,
    cashback: debitOnPad,
    fleet: product === "fleet",
    healthcare: product === "healthcare",
  };
  document.getElementById("pin-note").textContent =
    entry === ENTRY.contactless ? "(optional for contactless)" : "";
  for (const [name, visible] of Object.entries(prompts)) {
    show(form.querySelector(`[data-prompt="${name}"]`), visible);
  }
  show(document.getElementById("prompts"), Object.values(prompts).some(Boolean));

  // Card-not-present checks: AVS for keyed and online sales, 3-D Secure online.
  const cnp = !cof && !wallet && (entry === ENTRY.manual || entry === ENTRY.ecommerce);
  show(document.getElementById("cnp"), cnp);
  const threeDS = form.querySelector('[data-prompt="three_ds"]');
  threeDS.hidden = !cnp || entry !== ENTRY.ecommerce;
  if (threeDS.hidden) form.three_ds.checked = false;
}

// show toggles a section and clears any inputs it hides, so stale values are
// never sent.
function show(el, visible) {
  el.hidden = !visible;
  if (!visible) el.querySelectorAll("input:not([type=hidden]):not([type=checkbox])").forEach((i) => (i.value = ""));
}

// fillRandomAmount enters a random sale amount between $1.00 and $150.00 when
// a payment starts, unless the cashier already typed one.
function fillRandomAmount() {
  if (form.amount.value) return;
  const cents = 100 + Math.floor(Math.random() * 14_901);
  form.amount.value = (cents / 100).toFixed(2);
}

// focusNext moves to the first empty field the cashier still has to fill.
function focusNext() {
  const candidates = [form.amount, ...form.querySelectorAll("#prompts input")];
  const next = candidates.find((el) => !el.closest("[hidden]") && !el.value);
  (next || form.amount).focus();
}

form.entry_mode.addEventListener("change", () => {
  resetCardRead();
  updateForm();
});
form.card_number.addEventListener("input", updateForm);
form.card_on_file.addEventListener("change", () => {
  if (form.card_on_file.value) fillRandomAmount();
});

// Input formatting.
form.card_number.addEventListener("input", (e) => {
  e.target.value = groupDigits(digits(e.target.value).slice(0, 19));
});
form.expiry.addEventListener("input", (e) => {
  const d = digits(e.target.value).slice(0, 4);
  e.target.value = d.length > 2 ? d.slice(0, 2) + "/" + d.slice(2) : d;
});
for (const name of ["cvv", "pin", "odometer", "driver_id"]) {
  form[name].addEventListener("input", (e) => (e.target.value = digits(e.target.value)));
}

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  clearErrors();
  const btn = document.getElementById("charge");
  btn.disabled = true;
  try {
    const body = Object.fromEntries(new FormData(form));
    body.lose_response = form.lose_response.checked;
    body.balance_inquiry = e.submitter && e.submitter.id === "balance";
    body.three_ds = form.three_ds.checked;
    body.terminal_id = terminal.terminal_id;
    body.pre_auth = e.submitter && e.submitter.id === "preauth";
    body.refund = e.submitter && e.submitter.id === "refund";
    const res = await fetch("/api/sale", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = await res.json();
    if (!res.ok) {
      showErrors(data.errors || { form: "Request failed" });
      return;
    }
    renderResult(data);
    form.reset();
    setSplit(null);
    setRefund(null);
    resetCardRead();
    updateForm();
    loadHistory();
  } catch (err) {
    showErrors({ form: "Could not reach the terminal server." });
  } finally {
    btn.disabled = false;
  }
});

function clearErrors() {
  form.querySelectorAll(".err").forEach((el) => (el.textContent = ""));
  form.querySelectorAll(".invalid").forEach((el) => el.classList.remove("invalid"));
}

function showErrors(errors) {
  for (const [field, msg] of Object.entries(errors)) {
    const el = form.querySelector(`.err[data-for="${field}"]`);
    if (el) el.textContent = msg;
    form.elements[field]?.classList?.add("invalid");
  }
}

// --- Results -----------------------------------------------------------------

function money(minor, currencyCode) {
  const iso = currencies[currencyCode] || "USD";
  return new Intl.NumberFormat(undefined, { style: "currency", currency: iso }).format(minor / 100);
}

// brand names the card scheme from the leading digits of a PAN.
function brand(pan) {
  if (/^4/.test(pan)) return "Visa";
  if (/^(5[1-5]|2[2-7])/.test(pan)) return "Mastercard";
  if (/^3[47]/.test(pan)) return "American Express";
  if (/^(6011|65)/.test(pan)) return "Discover";
  return "Unknown scheme";
}

// monthDay formats an ISO 8583 MMDD date, e.g. "1009" -> "Oct 9".
function monthDay(mmdd) {
  const d = new Date(Date.UTC(2000, Number(mmdd.slice(0, 2)) - 1, Number(mmdd.slice(2, 4))));
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
}

// clock formats an ISO 8583 hhmmss time, e.g. "140322" -> "14:03:22".
const clock = (hhmmss) => hhmmss.match(/../g).join(":");

// describe renders a field's value, followed by a human-readable explanation
// in parentheses for coded values.
function describe(key, r) {
  const v = r[key];
  const explain = (text) => (text ? `${v}  (${text})` : String(v));
  switch (key) {
    case "mti":
      return explain(mtis[v]);
    case "de2_pan":
      return explain(
        r.wallet_provider ? `${brand(v)} · device token` : `${brand(v)} · ${productNames[productFor(v)] || "Credit"}`
      );
    case "de3_processing_code":
      return explain(`${TXN_TYPES[v.slice(0, 2)] || "?"} · ${ACCOUNT_TYPES[v.slice(2, 4)] || "?"}`);
    case "de4_amount":
      return explain(money(v, r.de49_currency));
    case "de7_transmission_datetime":
      return explain(`${monthDay(v.slice(0, 4))}, ${clock(v.slice(4))} UTC`);
    case "de12_local_time":
      return explain(clock(v));
    case "de13_local_date":
      return explain(monthDay(v));
    case "de14_expiry":
      return explain(`Expires ${v.slice(2, 4)}/20${v.slice(0, 2)}`);
    case "de18_mcc":
      return explain(mccs[v]);
    case "de22_pos_entry_mode":
      return explain(entryModes[v]);
    case "de48_fleet":
      return `Odometer ${v.odometer} · Vehicle ${v.vehicle_id} · Driver ${v.driver_id}`;
    case "de48_cof_indicator":
      return explain(cofIndicators[v]);
    case "de49_currency":
      return explain(currencies[v]);
    case "de54_additional_amounts":
      return v
        .map((a) => `${a.type}  (${AMOUNT_TYPES[a.type] || "?"}): ${money(a.amount, r.de49_currency)}`)
        .join("\n");
    case "wallet_provider":
      return explain(wallets[v]);
    case "de123_avs":
      return [v.zip && `ZIP ${v.zip}`, v.street].filter(Boolean).join(" · ");
    case "eci":
      return explain(ECIS[v]);
    case "auth_type":
      return explain(AUTH_TYPES[v]);
    default:
      return String(v);
  }
}

const STATUS = {
  APPROVED: ["good", "✓", "Approved"],
  PARTIAL: ["warning", "◐", "Partial approval"],
  DECLINED: ["critical", "✕", "Declined"],
  NO_RESPONSE: ["critical", "!", "No response"],
  VOIDED: ["", "↩", "Voided"],
  REFUND: ["good", "↩", "Refund approved"],
  CASH: ["good", "$", "Cash"],
  BALANCE: ["good", "$", "Balance"],
  CHALLENGE: ["warning", "?", "Verify shopper"],
  NOT_AUTHENTICATED: ["critical", "✕", "Not authenticated"],
};
const REVERSAL_STATUS = {
  PENDING: ["warning", "…", "Reversal pending"],
  ACCEPTED: ["good", "↩", "Reversed"],
  FAILED: ["critical", "!", "Reversal failed"],
};

// statusBadge shows the outcome with an icon and words, not color alone.
function statusBadge(tx) {
  const [cls, icon, label] = STATUS[tx.status] || ["", "", tx.status];
  const code = tx.response && tx.status === "DECLINED" ? ` ${tx.response.de39_response_code}` : "";
  let html = `<span class="status ${cls}"><span aria-hidden="true">${icon}</span> ${escapeHTML(label + code)}</span>`;
  if (tx.status === "PARTIAL" && !tx.split_of && !tx.reversal) {
    html += tx.balance_due
      ? ` <span class="status warning" data-term="split-tender">${money(tx.balance_due, tx.request.de49_currency)} due</span>`
      : ` <span class="status good" data-term="split-tender">✓ Paid in full</span>`;
  }
  if (tx.captured) {
    const tip = tx.request.auth_type ? 0 : tx.captured - tx.response.de4_amount;
    html += tip > 0
      ? ` <span class="status good" data-term="tip-adjust">✓ Tip ${money(tip, tx.request.de49_currency)}</span>`
      : ` <span class="status good" data-term="capture">✓ Captured</span>`;
  } else if (tx.request.auth_type === "estimated" && canVoid(tx)) {
    html += ` <span class="status warning" data-term="pre-auth">Pre-auth open</span>`;
  }
  if (tx.refunded) {
    html += ` <span class="status" data-term="refund">↩ ${money(tx.refunded, tx.request.de49_currency)} refunded</span>`;
  }
  // A void shows as the sale's status; a timeout reversal needs its own badge.
  if (tx.reversal && tx.status !== "VOIDED") {
    const [rcls, ricon, rlabel] = REVERSAL_STATUS[tx.reversal.status] || ["", "", tx.reversal.status];
    html += ` <span class="status reversal ${rcls}"><span aria-hidden="true">${ricon}</span> ${escapeHTML(rlabel)}</span>`;
  }
  return html;
}

const canVoid = (tx) => (tx.status === "APPROVED" || tx.status === "PARTIAL") && !tx.reversal;

// --- Pre-auth, capture and tips ------------------------------------------------

// adjustable: an approved card sale whose final amount isn't set yet.
const adjustable = (tx) =>
  canVoid(tx) && tx.tender === "card" && !tx.increment_of && !tx.captured && !tx.request.de3_processing_code.startsWith("30");

// adjustSection lets the cashier add to a pre-auth and capture its final
// amount, or add a tip to an ordinary sale.
function adjustSection(tx) {
  const r = tx.request;
  const cur = r.de49_currency;
  const stan = escapeHTML(r.de11_stan);
  if (tx.increment_of) {
    return `<p class="split-note"><span data-term="incremental-auth">Adds to pre-auth</span> STAN ${escapeHTML(tx.increment_of)}.</p>`;
  }
  if (tx.captured) {
    const authorized = tx.authorized || tx.response.de4_amount;
    return `<p class="split-note"><span data-term="completion-advice">Final amount</span> <b>${money(tx.captured, cur)}</b>
      captured with an 0220 (authorized ${money(authorized, cur)}). Clearing presents the final amount; the issuer releases the rest of the hold.</p>`;
  }
  if (!adjustable(tx)) return "";
  if (r.auth_type === "estimated") {
    return `<div class="adjust">
      <span><span data-term="pre-auth">Held so far</span> <b>${money(tx.authorized || tx.response.de4_amount, cur)}</b></span>
      <span class="adjust-row">
        <input data-adjust-input="increment" inputmode="decimal" placeholder="0.00" aria-label="Amount to add" data-term="incremental-auth">
        <button type="button" class="secondary" data-increment="${stan}" data-term="incremental-auth">Add to tab</button>
      </span>
      <span class="adjust-row">
        <input data-adjust-input="capture" inputmode="decimal" placeholder="${((tx.authorized || 0) / 100).toFixed(2)}" aria-label="Final amount" data-term="completion-advice">
        <button type="button" data-capture="${stan}" data-term="completion-advice">Capture final amount</button>
      </span>
      <small class="err" data-adjust-err></small>
    </div>`;
  }
  return `<div class="adjust">
    <span class="adjust-row">
      <input data-adjust-input="tip" inputmode="decimal" placeholder="0.00" aria-label="Tip" data-term="tip-adjust">
      <button type="button" class="secondary" data-tip="${stan}" data-term="tip-adjust">Add tip</button>
    </span>
    <small class="err" data-adjust-err></small>
  </div>`;
}

document.addEventListener("click", async (e) => {
  const btn = e.target.closest("button[data-increment], button[data-capture], button[data-tip]");
  if (!btn) return;
  const box = btn.closest(".adjust");
  const err = box.querySelector("[data-adjust-err]");
  const value = btn.parentElement.querySelector("input").value.trim();
  let path, body;
  if (btn.dataset.increment) [path, body] = ["/api/increment", { stan: btn.dataset.increment, amount: value }];
  else if (btn.dataset.capture) [path, body] = ["/api/capture", { stan: btn.dataset.capture, amount: value || btn.parentElement.querySelector("input").placeholder }];
  else [path, body] = ["/api/capture", { stan: btn.dataset.tip, tip: value }];
  btn.disabled = true;
  err.textContent = "";
  try {
    const res = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    const data = await res.json();
    if (!res.ok) {
      err.textContent = data.error || "Request failed.";
      return;
    }
    renderResult(data);
    loadHistory();
  } catch {
    err.textContent = "Could not reach the terminal server.";
  } finally {
    btn.disabled = false;
  }
});

// --- Split tender --------------------------------------------------------------

// balanceSection offers ways to collect what a partial approval left unpaid.
function balanceSection(tx) {
  if (tx.refund_of) return `<p class="split-note"><span data-term="refund">Refund</span> of sale STAN ${escapeHTML(tx.refund_of)}.</p>`;
  if (tx.split_of) return `<p class="split-note"><span data-term="split-tender">Split tender</span> toward sale STAN ${escapeHTML(tx.split_of)}.</p>`;
  if (tx.status !== "PARTIAL" || tx.reversal) return "";
  const cur = tx.request.de49_currency;
  if (!tx.balance_due) return `<p class="split-note"><span class="status good" data-term="split-tender">✓ Paid in full</span> with split tender.</p>`;
  return `<div class="balance-due">
    <span><span data-term="split-tender">Balance due</span> <b>${money(tx.balance_due, cur)}</b></span>
    <button type="button" data-split-card="${escapeHTML(tx.request.de11_stan)}" data-balance="${tx.balance_due}" data-term="split-tender">Pay rest by card</button>
    <button type="button" class="secondary" data-split-cash="${escapeHTML(tx.request.de11_stan)}" data-balance="${tx.balance_due}" data-term="split-tender">Pay rest in cash</button>
  </div>`;
}

function setSplit(stan, balance) {
  form.split_of.value = stan || "";
  const banner = document.getElementById("split-banner");
  banner.hidden = !stan;
  if (stan) {
    form.amount.value = (balance / 100).toFixed(2);
    banner.innerHTML = `<span data-term="split-tender">Collecting the balance</span> of sale STAN ${escapeHTML(stan)}: present another card. <button type="button" class="link" data-split-cancel>Cancel</button>`;
  }
}

document.addEventListener("click", async (e) => {
  const card = e.target.closest("button[data-split-card]");
  if (card) {
    setSplit(card.dataset.splitCard, Number(card.dataset.balance));
    form.amount.focus();
    return;
  }
  if (e.target.closest("button[data-split-cancel]")) return setSplit(null);
  const cash = e.target.closest("button[data-split-cash]");
  if (!cash) return;
  cash.disabled = true;
  try {
    const res = await fetch("/api/cash", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ split_of: cash.dataset.splitCash, amount: (Number(cash.dataset.balance) / 100).toFixed(2) }),
    });
    const data = await res.json();
    if (res.ok) renderResult(data);
    else alertResult(data.error || "Cash tender failed.");
  } catch {
    alertResult("Could not reach the terminal server.");
  }
  loadHistory();
});
// --- Refunds ---------------------------------------------------------------------

// refundable is what an approved card sale can still give back: what it
// charged (the captured amount for a pre-auth or tipped sale) less refunds.
function refundable(tx) {
  const code = tx.request.de3_processing_code || "";
  if (tx.tender !== "card" || !canVoid(tx) || code.startsWith("20") || code.startsWith("30") || tx.increment_of) return 0;
  if (tx.request.auth_type === "estimated" && !tx.captured) return 0; // Void an open pre-auth instead
  return (tx.captured || tx.response.de4_amount) - (tx.refunded || 0);
}

const refundButton = (tx, cls = "") => {
  const left = refundable(tx);
  return left > 0
    ? `<button type="button" class="secondary ${cls}" data-refund="${escapeHTML(tx.request.de11_stan)}" data-max="${left}" data-pan="${escapeHTML(tx.request.de2_pan)}" data-currency="${escapeHTML(tx.request.de49_currency)}" data-term="refund">Refund</button>`
    : "";
};

// setRefund links the next transaction to an earlier sale: the cashier
// presents the same card and presses Refund, for up to what is left.
function setRefund(stan, max, pan, currency) {
  form.refund_of.value = stan || "";
  const banner = document.getElementById("refund-banner");
  banner.hidden = !stan;
  if (!stan) return;
  form.amount.value = (max / 100).toFixed(2);
  if (currency) form.currency.value = currency;
  banner.innerHTML = `<span data-term="refund">Refunding</span> sale STAN ${escapeHTML(stan)}: up to ${money(max, currency)} back to card ${escapeHTML(pan)}. Present that card, then press <b>Refund</b>. <button type="button" class="link" data-refund-cancel>Cancel</button>`;
}

document.addEventListener("click", (e) => {
  const btn = e.target.closest("button[data-refund]");
  if (btn) {
    setSplit(null);
    setRefund(btn.dataset.refund, Number(btn.dataset.max), btn.dataset.pan, btn.dataset.currency);
    form.scrollIntoView({ behavior: "smooth", block: "start" });
    form.amount.focus({ preventScroll: true });
    return;
  }
  if (e.target.closest("button[data-refund-cancel]")) setRefund(null);
});

const voidButton = (tx, cls = "") =>
  canVoid(tx) ? `<button type="button" class="void ${cls}" data-void="${escapeHTML(tx.request.de11_stan)}" data-term="void">Void</button>` : "";

// reversalRows shows the 0400 the terminal sent to undo a sale, and the 0410.
function reversalRows(tx) {
  const rev = tx.reversal;
  if (!rev) return "";
  const adv = rev.advice;
  const o = adv.de90_original_data;
  const reason = rev.reason === "17" ? "17  (Customer cancellation: a void)" : `${rev.reason}  (Response received too late: no 0110 arrived)`;
  const ack = rev.ack;
  const rows = [
    ["MTI", "Message type", `${adv.mti}  (${mtis[adv.mti] || "Reversal advice"})`],
    ["DE11", "STAN", adv.de11_stan],
    ["DE90", "Original data elements", `${o.mti} · STAN ${o.stan} · ${o.transmission_time}`],
    ["DE4", "Amount", `${adv.de4_amount}  (${money(adv.de4_amount, tx.request.de49_currency)})`],
    ["DE39", "Reversal reason", reason],
    ["", "Reversal acknowledged", ack ? `0430 ${ack.de39_response_code}  (${ack.matched ? "the acquirer had the sale and will reverse it" : "the sale never reached the network"})` : "—"],
  ];
  const [cls, icon, label] = REVERSAL_STATUS[rev.status] || ["", "", rev.status];
  return `<h3>Reversal sent (0420)</h3>
    <p class="reversal-note"><span class="status ${cls}"><span aria-hidden="true">${icon}</span> ${escapeHTML(label)}</span> ${escapeHTML(rev.message)}</p>
    <div class="table-wrap"><table class="fields"><tbody>${rows
      .map(([de, name, v]) => `<tr><td class="mono">${de}</td><td>${name}</td><td class="mono">${escapeHTML(v)}</td></tr>`)
      .join("")}</tbody></table></div>`;
}

// responseRows shows the 0110 the terminal got from the acquirer: usually the
// network's answer passed back, but the acquirer's own when it declines
// (unknown merchant, or the network didn't answer).
function responseRows(tx) {
  const resp = tx.response;
  if (!resp) return "";
  const r = tx.request;
  const txn = encodeURIComponent(resp.network_txn_id || "");
  // The acquirer finds the record by the terminal's own keys, which it has
  // even when the network never answered.
  const atAcquirer = new URLSearchParams({ tid: r.de41_terminal_id, stan: r.de11_stan, de7: r.de7_transmission_datetime });
  const links = [
    acquirerURL && `<a href="${acquirerURL}/#${atAcquirer}" target="_blank" rel="noopener">View at acquirer →</a>`,
    dashboardURL && resp.network_txn_id && `<a href="${dashboardURL}/#txn=${txn}" target="_blank" rel="noopener">View in network →</a>`,
  ].filter(Boolean);
  const link = links.join(" · ");
  const rows = [
    ["MTI", "Message type", resp.mti],
    ["DE39", "Response code", `${resp.de39_response_code}  (${resp.response_text})`],
    ["DE38", "Auth code", resp.de38_auth_code],
    ["DE44", "AVS result", resp.de44_avs_result ? `${resp.de44_avs_result}  (${AVS_RESULTS[resp.de44_avs_result] || "?"})` : ""],
    ["DE44", "CVV2 result", resp.de44_cvv2_result ? `${resp.de44_cvv2_result}  (${CVV2_RESULTS[resp.de44_cvv2_result] || "?"})` : ""],
    ["DE54", "Available balance", (resp.de54_additional_amounts || []).filter((a) => a.type === "02").map((a) => `${a.amount}  (${money(a.amount, r.de49_currency)})`)[0] || ""],
    ["DE37", "Retrieval reference number", resp.de37_rrn !== r.de37_rrn ? resp.de37_rrn : ""],
    ["DE4", "Approved amount", resp.de4_amount ? `${resp.de4_amount}  (${money(resp.de4_amount, r.de49_currency)})` : ""],
    ["", "Network transaction ID", resp.network_txn_id],
  ].filter(([, , v]) => v);
  return `<h3 data-term="msg-0110">Response (via acquirer)</h3>
    <div class="table-wrap"><table class="fields"><tbody>${rows
      .map(([de, name, v]) => `<tr><td class="mono">${de}</td><td>${name}</td><td class="mono">${escapeHTML(v)}</td></tr>`)
      .join("")}</tbody></table></div>
    <p class="trace-link">${link}</p>`;
}

// threeDSRows shows the issuer ACS's answer to a 3-D Secure authentication.
function threeDSRows(tx) {
  const t = tx.three_ds;
  if (!t) return "";
  const rows = [
    ["", "ACS", `${t.trans_status}  (${THREEDS_STATUS[t.trans_status] || "?"}${t.issuer_name ? " · " + t.issuer_name : ""})`],
    ["", "E-commerce indicator", t.eci ? `${t.eci}  (${ECIS[t.eci] || "?"})` : ""],
    ["", "Authentication value", t.cavv || ""],
  ].filter(([, , v]) => v);
  return `<h3>3-D Secure (3DS)</h3>
    ${t.reason ? `<p class="reversal-note">${escapeHTML(t.reason)}</p>` : ""}
    <div class="table-wrap"><table class="fields"><tbody>${rows
      .map(([de, name, v]) => `<tr><td class="mono">${de}</td><td>${name}</td><td class="mono">${escapeHTML(v)}</td></tr>`)
      .join("")}</tbody></table></div>`;
}

// renderChallenge asks the shopper for the one-time code the issuer sent.
// The terminal holds the sale until the code checks out.
function renderChallenge(tx) {
  resultEl.className = "";
  resultEl.innerHTML = `
    <div class="result-head">
      ${statusBadge(tx)}
      <p>${escapeHTML(tx.message)}</p>
    </div>
    <form id="challenge" class="challenge" data-id="${escapeHTML(tx.three_ds.threeds_id)}">
      <label>
        <span data-term="three-ds-challenge">One-time code</span>
        <input name="otp" inputmode="numeric" maxlength="6" autocomplete="one-time-code" required>
        <small class="err" data-for="otp"></small>
      </label>
      <button type="submit" data-term="three-ds-challenge">Verify</button>
    </form>
    ${threeDSRows(tx)}`;
  resultEl.querySelector("input[name=otp]").focus();
}

document.addEventListener("submit", async (e) => {
  const ch = e.target.closest("form#challenge");
  if (!ch) return;
  e.preventDefault();
  const btn = ch.querySelector("button");
  btn.disabled = true;
  try {
    const res = await fetch("/api/3ds/challenge", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ threeds_id: ch.dataset.id, otp: ch.otp.value }),
    });
    const data = await res.json();
    if (!res.ok) {
      ch.querySelector(".err").textContent = (data.errors && Object.values(data.errors)[0]) || "Verification failed.";
      return;
    }
    renderResult(data);
    loadHistory();
  } catch {
    ch.querySelector(".err").textContent = "Could not reach the terminal server.";
  } finally {
    btn.disabled = false;
  }
});

function renderResult(tx) {
  if (tx.status === "CHALLENGE") return renderChallenge(tx);
  const r = tx.request;
  const rows = FIELDS.filter(([key]) => r[key] !== undefined && r[key] !== "")
    .map(([key, de, name]) => {
      if (key === "de2_pan" && r.wallet_provider) name = "Device account number (token)";
      return `<tr><td class="mono">${de}</td><td>${name}</td><td class="mono">${escapeHTML(describe(key, r))}</td></tr>`;
    })
    .join("");

  shownSTAN = r.de11_stan;
  rememberTx(tx);
  resultEl.className = "";
  resultEl.innerHTML = `
    <div class="result-head">
      ${statusBadge(tx)}
      <p>${escapeHTML(tx.message)}</p>
      <span class="result-actions">${receiptButton(tx)}${voidButton(tx)}${refundButton(tx)}</span>
    </div>
    ${balanceSection(tx)}
    ${adjustSection(tx)}
    ${reversalRows(tx)}
    ${threeDSRows(tx)}
    ${responseRows(tx)}
    <h3>Request sent (0100)</h3>
    <div class="table-wrap"><table class="fields"><tbody>${rows}</tbody></table></div>
    <details><summary>Raw JSON (PAN masked, PIN block and CVV hidden)</summary><pre>${escapeHTML(JSON.stringify(r, null, 2))}</pre></details>`;
}

// The STAN of the sale in the result panel, so it can refresh as a reversal progresses.
let shownSTAN = null;
let reversalPoll = 0;

async function loadHistory() {
  const lane = terminal.terminal_id;
  const txs = await (await fetch(`/api/transactions?terminal=${encodeURIComponent(lane || "")}`)).json();
  if (lane !== terminal.terminal_id) return; // The cashier switched merchants meanwhile
  if (!txs.length) {
    historyEl.innerHTML = `<tr><td colspan="7" class="empty">No transactions yet at ${escapeHTML(terminal.merchant_name || "this merchant")}.</td></tr>`;
    return;
  }
  window.lastTransactions = txs;
  // Keep the result panel and history current while a reversal is retrying.
  const shown = txs.find((tx) => tx.request.de11_stan === shownSTAN);
  if (shown && shown.reversal) renderResult(shown);
  clearTimeout(reversalPoll);
  if (txs.some((tx) => tx.reversal && tx.reversal.status === "PENDING")) reversalPoll = setTimeout(loadHistory, 2000);
  historyEl.innerHTML = txs
    .map((tx) => {
      const r = tx.request;
      const mode = tx.tender === "cash" ? ["Cash"] : [entryModes[r.de22_pos_entry_mode] || r.de22_pos_entry_mode];
      if (tx.split_of) mode.push(`split of ${tx.split_of}`);
      if (tx.refund_of) mode.push(`refund of ${tx.refund_of}`);
      else if (r.de3_processing_code?.startsWith("20")) mode.push("refund");
      if (r.wallet_provider) mode.push(wallets[r.wallet_provider] || r.wallet_provider);
      if (r.de52_pin_data) mode.push("PIN");
      return `<tr>
        <td>${new Date(tx.created).toLocaleTimeString()}</td>
        <td class="mono">${r.de11_stan}</td>
        <td class="mono">${r.de37_rrn}</td>
        <td class="mono">${escapeHTML(r.de2_pan)}</td>
        <td>${escapeHTML(mode.join(" · "))}</td>
        <td class="num">${money(r.de4_amount, r.de49_currency)}</td>
        <td>${statusBadge(tx)} ${receiptButton(tx, "small")}${voidButton(tx, "small")}${refundButton(tx, "small")}</td>
      </tr>`;
    })
    .join("");
}

// Void an approved sale: the terminal sends an 0420 naming it in DE90.

// rememberTx keeps the newest copy of a transaction for receipts.
function rememberTx(tx) {
  const list = window.lastTransactions || [];
  const i = list.findIndex((t) => t.request.de11_stan === tx.request.de11_stan);
  if (i >= 0) list[i] = tx;
  else list.unshift(tx);
  window.lastTransactions = list;
}

const receiptButton = (tx, cls = "") => tx.tender === "cash" || tx.status === "NOT_AUTHENTICATED" ? "" :
  `<button type="button" class="receipt-btn secondary ${cls}" data-receipt="${escapeHTML(tx.request.de11_stan)}" data-term="receipt">Receipt</button>`;
document.addEventListener("click", async (e) => {
  const btn = e.target.closest("button[data-void]");
  if (!btn) return;
  btn.disabled = true;
  btn.textContent = "Voiding…";
  try {
    const res = await fetch("/api/void", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ stan: btn.dataset.void }),
    });
    const data = await res.json();
    if (res.ok) renderResult(data);
    else alertResult(data.error || "Void failed.");
  } catch {
    alertResult("Could not reach the terminal server.");
  }
  loadHistory();
});

function alertResult(msg) {
  const p = document.createElement("p");
  p.className = "err";
  p.textContent = msg;
  resultEl.prepend(p);
}

// --- Helpers -----------------------------------------------------------------

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const digits = (s) => s.replace(/\D/g, "");
const groupDigits = (s) => s.replace(/(\d{4})(?=\d)/g, "$1 ");

// A fresh 8-byte cryptogram, as a chip or phone secure element would produce.
function newCryptogram() {
  const bytes = crypto.getRandomValues(new Uint8Array(8));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("").toUpperCase();
}

// generatePAN returns a random Luhn-valid card number starting with bin.
function generatePAN(bin, length) {
  let body = bin;
  while (body.length < length - 1) body += Math.floor(Math.random() * 10);
  let sum = 0;
  for (let i = body.length - 1, double = true; i >= 0; i--, double = !double) {
    let d = Number(body[i]);
    if (double) d = d * 2 > 9 ? d * 2 - 9 : d * 2;
    sum += d;
  }
  return body + ((10 - (sum % 10)) % 10);
}

function monthsFromNow(n) {
  const d = new Date();
  d.setMonth(d.getMonth() + n);
  return `${String(d.getMonth() + 1).padStart(2, "0")}/${String(d.getFullYear()).slice(2)}`;
}

function fillSelect(id, options, selectedValue, placeholder) {
  const el = document.getElementById(id);
  if (placeholder) el.add(new Option(placeholder, ""));
  for (const [code, name] of Object.entries(options).sort((a, b) => a[1].localeCompare(b[1]))) {
    const isDefault = code === selectedValue;
    el.add(new Option(name, code, isDefault, isDefault)); // default too, so form.reset() restores it
  }
}

function escapeHTML(s) {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

// --- Merchant picker -------------------------------------------------------------

const RESULT_EMPTY = resultEl.innerHTML;
const merchantSelect = document.getElementById("merchant-select");

const merchantLine = (t) => `${t.merchant_name} · ${t.city}, ${t.country} · MID ${t.merchant_id} · TID ${t.terminal_id} · MCC ${t.mcc}`;

// selectTerminal rings up sales for another merchant: its IDs, category
// code and local time go into every 0100 from now on. The sale in progress
// is cleared, and the history shows only this merchant's sales.
function selectTerminal(id) {
  terminal = terminals.find((t) => t.terminal_id === id) || terminals[0];
  merchantSelect.value = terminal.terminal_id;
  document.getElementById("merchant").textContent = merchantLine(terminal);
  try { localStorage.setItem("pos-terminal", terminal.terminal_id); } catch {}

  form.reset();
  setSplit(null);
  setRefund(null);
  resetCardRead();
  clearErrors();
  updateForm();
  resultEl.className = "empty";
  resultEl.innerHTML = RESULT_EMPTY;
  loadHistory();
}

merchantSelect.addEventListener("change", () => selectTerminal(merchantSelect.value));

async function init() {
  const data = await (await fetch("/api/terminal")).json();
  terminals = data.terminals || [data.terminal];
  terminal = data.terminal;
  mtis = data.mtis;
  mccs = data.mccs;
  currencies = data.currencies;
  entryModes = data.entry_modes;
  wallets = data.wallets;
  dashboardURL = data.dashboard_url;
  acquirerURL = data.acquirer_url;
  cofIndicators = data.cof_indicators;
  binProducts = data.bin_products;
  productNames = data.product_names;

  for (const t of terminals) {
    merchantSelect.add(new Option(`${t.merchant_name} · ${t.city} · ${mccs[t.mcc] || "MCC " + t.mcc}`, t.terminal_id));
  }

  fillSelect("currency", currencies, "840");
  fillSelect("entry_mode", entryModes, ENTRY.manual);
  fillSelect("cof_indicator", cofIndicators, "", "Choose…");
  fillSelect(
    "card_on_file",
    Object.fromEntries(data.cards_on_file.map((c) => [c.id, `${c.customer} · ${c.masked.slice(-4)}`])),
    "",
    "Choose…"
  );

  renderWallet();
  renderMobileWallets();
  let saved = "";
  try { saved = localStorage.getItem("pos-terminal") || ""; } catch {}
  selectTerminal(saved);
}

init();
