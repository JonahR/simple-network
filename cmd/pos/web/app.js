const form = document.getElementById("sale");
const resultEl = document.getElementById("result");
const historyEl = document.getElementById("history");

// Terminal configuration, loaded from /api/terminal.
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

const ENTRY = { manual: "011", swipe: "901", chip: "051", contactless: "071", ecommerce: "812", cof: "100" };

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
  ["cardholder_name", "", "Cardholder name"],
  ["wallet_provider", "", "Wallet provider"],
];

const TXN_TYPES = { "00": "Purchase", "09": "Purchase with cash back", "20": "Refund" };
const ACCOUNT_TYPES = { "00": "default account", "20": "checking", "30": "credit" };
const AMOUNT_TYPES = { "40": "Cash back", "4S": "HSA/FSA eligible" };

// --- Cardholder wallet -------------------------------------------------------

const ALL_METHODS = ["tap", "insert", "swipe", "key"];

// The cardholder's cards. Numbers are Luhn-valid test numbers; BINs match the
// terminal's BIN table so the right prompts appear. The two credit cards are
// also provisioned to each mobile wallet with a distinct device token.
// Expiries are relative to today so the test cards never expire.
const WALLET = [
  { label: "Everyday Credit", style: "blue", number: "4242424242424242", expiry: "12/29", cvv: "123", name: "Jane Doe",
    methods: ALL_METHODS,
    tokens: {
      apple_pay:   { number: "4895372051310681", expiry: "09/30" },
      google_pay:  { number: "4895373708421368", expiry: "11/30" },
      samsung_pay: { number: "4895377555006883", expiry: "06/30" },
    } },
  { label: "Rewards Plus", style: "dark", number: "5555555555554444", expiry: "08/28", cvv: "456", name: "John Smith",
    methods: ALL_METHODS,
    tokens: {
      apple_pay:   { number: "5220930238452479", expiry: "03/31" },
      google_pay:  { number: "5220934871046911", expiry: "01/31" },
      samsung_pay: { number: "5220933784416278", expiry: "05/31" },
    } },
  { label: "Checking Debit", style: "green", number: "4000056655665556", expiry: "05/29", cvv: "789", name: "Jane Doe",
    hint: "PIN 1234", methods: ALL_METHODS },
  { label: "Gift Card", style: "purple", number: "4358805984634941", expiry: "07/28", cvv: "321", name: "",
    methods: ["swipe", "key"] },
  { label: "Fleet Card", style: "orange", number: "5568007143162129", expiry: "02/29", cvv: "654", name: "Acme Logistics",
    hint: "Driver 4821", methods: ["insert", "swipe", "key"] },
  { label: "Health Savings", style: "teal", number: "4716006861111015", expiry: "11/28", cvv: "852", name: "Jane Doe",
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
function presentCard(c, method) {
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

  if (method === "tap" || method === "insert") form.cryptogram.value = newCryptogram();
  if (method === "swipe") form.track2.value = track2(c.number, c.expiry, c.methods.includes("insert"));
  const badge = { tap: "Contactless read", insert: "Chip read", swipe: "Stripe read" }[method];
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
  const onPINPad = !wallet && [ENTRY.chip, ENTRY.swipe, ENTRY.contactless].includes(entry);
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
}

// show toggles a section and clears any inputs it hides, so stale values are
// never sent.
function show(el, visible) {
  el.hidden = !visible;
  if (!visible) el.querySelectorAll("input:not([type=hidden])").forEach((i) => (i.value = ""));
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

document.getElementById("testcard").addEventListener("click", async () => {
  const card = await (await fetch("/api/test-card")).json();
  resetCardRead();
  clearErrors();
  form.entry_mode.value = ENTRY.manual;
  form.card_number.value = groupDigits(card.card_number);
  form.expiry.value = card.expiry;
  form.cvv.value = card.cvv;
  if (!form.amount.value) form.amount.value = "12.50";
  updateForm();
});

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  clearErrors();
  const btn = document.getElementById("charge");
  btn.disabled = true;
  try {
    const body = Object.fromEntries(new FormData(form));
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
    default:
      return String(v);
  }
}

const STATUS = {
  APPROVED: ["good", "✓", "Approved"],
  PARTIAL: ["warning", "◐", "Partial approval"],
  DECLINED: ["critical", "✕", "Declined"],
  NO_RESPONSE: ["critical", "!", "No response"],
};

// statusBadge shows the outcome with an icon and words, not color alone.
function statusBadge(tx) {
  const [cls, icon, label] = STATUS[tx.status] || ["", "", tx.status];
  const code = tx.response && tx.status === "DECLINED" ? ` ${tx.response.de39_response_code}` : "";
  return `<span class="status ${cls}"><span aria-hidden="true">${icon}</span> ${escapeHTML(label + code)}</span>`;
}

// responseRows shows the 0110 answer the acquirer passed back from the network.
function responseRows(tx) {
  const resp = tx.response;
  if (!resp) return "";
  const r = tx.request;
  const txn = encodeURIComponent(resp.network_txn_id || "");
  const links = [
    acquirerURL && `<a href="${acquirerURL}/#txn=${txn}" target="_blank" rel="noopener">View at acquirer →</a>`,
    dashboardURL && resp.network_txn_id && `<a href="${dashboardURL}/#txn=${txn}" target="_blank" rel="noopener">View in network →</a>`,
  ].filter(Boolean);
  const link = links.join(" · ");
  const rows = [
    ["MTI", "Message type", resp.mti],
    ["DE39", "Response code", `${resp.de39_response_code}  (${resp.response_text})`],
    ["DE38", "Auth code", resp.de38_auth_code],
    ["DE4", "Approved amount", resp.de4_amount ? `${resp.de4_amount}  (${money(resp.de4_amount, r.de49_currency)})` : ""],
    ["", "Network transaction ID", resp.network_txn_id],
  ].filter(([, , v]) => v);
  return `<h3>Response from network</h3>
    <div class="table-wrap"><table class="fields"><tbody>${rows
      .map(([de, name, v]) => `<tr><td class="mono">${de}</td><td>${name}</td><td class="mono">${escapeHTML(v)}</td></tr>`)
      .join("")}</tbody></table></div>
    <p class="trace-link">${link}</p>`;
}

function renderResult(tx) {
  const r = tx.request;
  const rows = FIELDS.filter(([key]) => r[key] !== undefined && r[key] !== "")
    .map(([key, de, name]) => {
      if (key === "de2_pan" && r.wallet_provider) name = "Device account number (token)";
      return `<tr><td class="mono">${de}</td><td>${name}</td><td class="mono">${escapeHTML(describe(key, r))}</td></tr>`;
    })
    .join("");

  resultEl.className = "";
  resultEl.innerHTML = `
    <div class="result-head">
      ${statusBadge(tx)}
      <p>${escapeHTML(tx.message)}</p>
    </div>
    ${responseRows(tx)}
    <h3>Request sent (0100)</h3>
    <div class="table-wrap"><table class="fields"><tbody>${rows}</tbody></table></div>
    <details><summary>Raw JSON (PAN masked, PIN block and CVV hidden)</summary><pre>${escapeHTML(JSON.stringify(r, null, 2))}</pre></details>`;
}

async function loadHistory() {
  const txs = await (await fetch("/api/transactions")).json();
  if (!txs.length) return;
  historyEl.innerHTML = txs
    .map((tx) => {
      const r = tx.request;
      const mode = [entryModes[r.de22_pos_entry_mode] || r.de22_pos_entry_mode];
      if (r.wallet_provider) mode.push(wallets[r.wallet_provider] || r.wallet_provider);
      if (r.de52_pin_data) mode.push("PIN");
      return `<tr>
        <td>${new Date(tx.created).toLocaleTimeString()}</td>
        <td class="mono">${r.de11_stan}</td>
        <td class="mono">${r.de37_rrn}</td>
        <td class="mono">${escapeHTML(r.de2_pan)}</td>
        <td>${escapeHTML(mode.join(" · "))}</td>
        <td class="num">${money(r.de4_amount, r.de49_currency)}</td>
        <td>${statusBadge(tx)}</td>
      </tr>`;
    })
    .join("");
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

async function init() {
  const data = await (await fetch("/api/terminal")).json();
  const t = data.terminal;
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

  document.getElementById("merchant").textContent =
    `${t.merchant_name} · ${t.city}, ${t.country} · MID ${t.merchant_id} · TID ${t.terminal_id} · MCC ${t.mcc}`;

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
  updateForm();
  loadHistory();
}

init();
