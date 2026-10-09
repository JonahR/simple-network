const form = document.getElementById("sale");
const resultEl = document.getElementById("result");
const historyEl = document.getElementById("history");
let currencies = {};
let entryModes = {};
let wallets = {};

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
  ["de37_rrn", "DE37", "Retrieval reference number"],
  ["de41_terminal_id", "DE41", "Terminal ID"],
  ["de42_merchant_id", "DE42", "Merchant ID"],
  ["de43_merchant_name_location", "DE43", "Merchant name/location"],
  ["de49_currency", "DE49", "Currency code"],
  ["cardholder_name", "", "Cardholder name"],
  ["wallet_provider", "", "Wallet provider"],
  ["token_cryptogram", "", "Token cryptogram"],
];

// The cardholder's wallet. These are well-known test numbers that pass Luhn.
// Each card is also provisioned to every mobile wallet. Each wallet gets its
// own device token (a Luhn-valid number distinct from the card number) and
// token expiry, so the same card has a different token in each wallet.
const WALLET = [
  { label: "Everyday Credit", style: "blue", number: "4242424242424242", expiry: "12/29", cvv: "123", name: "Jane Doe",
    tokens: {
      apple_pay:   { number: "4895372051310681", expiry: "09/30" },
      google_pay:  { number: "4895373708421368", expiry: "11/30" },
      samsung_pay: { number: "4895377555006883", expiry: "06/30" },
    } },
  { label: "Rewards Plus", style: "dark", number: "5555555555554444", expiry: "08/28", cvv: "456", name: "John Smith",
    tokens: {
      apple_pay:   { number: "5220930238452479", expiry: "03/31" },
      google_pay:  { number: "5220934871046911", expiry: "01/31" },
      samsung_pay: { number: "5220933784416278", expiry: "05/31" },
    } },
];

// Mobile wallets and how each one asks the cardholder to authenticate.
const MOBILE_WALLETS = [
  { id: "apple_pay", name: "Apple Pay", short: "Apple", auth: "Face ID…" },
  { id: "google_pay", name: "Google Pay", short: "Google", auth: "Unlock phone…" },
  { id: "samsung_pay", name: "Samsung Pay", short: "Samsung", auth: "Fingerprint…" },
];

function renderWallet() {
  const container = document.getElementById("cards");
  for (const c of WALLET) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = `card ${c.style}`;
    btn.setAttribute("aria-label", `Use ${c.label} ending in ${c.number.slice(-4)}`);
    btn.innerHTML = `
      <div class="card-top"><span>${escapeHTML(c.label)}</span><span>simple-network</span></div>
      <div class="chip"></div>
      <div class="card-number">•••• •••• •••• ${c.number.slice(-4)}</div>
      <div class="card-bottom">
        <span><small>Cardholder</small>${escapeHTML(c.name)}</span>
        <span><small>Expires</small>${c.expiry}</span>
      </div>`;
    btn.addEventListener("click", () => {
      presentCard(c);
      container.querySelectorAll(".card").forEach((el) => el.classList.remove("selected"));
      btn.classList.add("selected");
    });
    container.append(btn);
  }
}

function presentCard(c) {
  clearErrors();
  clearWalletToken();
  form.card_number.value = c.number.replace(/(\d{4})(?=\d)/g, "$1 ");
  form.expiry.value = c.expiry;
  form.cvv.value = c.cvv;
  form.cardholder_name.value = c.name;
  form.amount.focus();
}

function clearWalletSelection() {
  document.querySelectorAll("#cards .card").forEach((el) => el.classList.remove("selected"));
}

// --- Mobile wallets ----------------------------------------------------------

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
    btn.textContent = w.short;
    btn.setAttribute("aria-label", w.name);
    btn.addEventListener("click", () => {
      if (mwPay.disabled) return; // don't switch mid-tap
      mwWallet = w;
      renderMobileWallets();
    });
    tabs.append(btn);
  }

  const cards = document.getElementById("mw-cards");
  cards.innerHTML = "";
  WALLET.forEach((c, i) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "mw-card";
    btn.setAttribute("role", "radio");
    btn.setAttribute("aria-checked", String(i === mwCard));
    btn.innerHTML = `
      <span class="mw-swatch ${c.style}"></span>
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

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// A fresh 8-byte cryptogram per tap, as the phone's secure element would produce.
function newCryptogram() {
  const bytes = crypto.getRandomValues(new Uint8Array(8));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("").toUpperCase();
}

mwPay.addEventListener("click", async () => {
  const w = mwWallet;
  const token = WALLET[mwCard].tokens[w.id];
  mwPay.disabled = true;
  mwStatus.className = "mw-status";
  mwStatus.textContent = "Hold near reader…";
  await sleep(600);
  mwStatus.textContent = w.auth;
  await sleep(600);

  clearErrors();
  clearWalletSelection();
  form.card_number.value = token.number.replace(/(\d{4})(?=\d)/g, "$1 ");
  form.expiry.value = token.expiry;
  form.cvv.value = "";
  form.cardholder_name.value = ""; // Mobile wallets don't share the cardholder's name in store
  form.entry_mode.value = "071"; // Contactless
  form.wallet.value = w.id;
  form.cryptogram.value = newCryptogram();
  const badge = document.getElementById("token-badge");
  badge.textContent = `${w.name} · device token`;
  badge.hidden = false;

  mwStatus.className = "mw-status done";
  mwStatus.textContent = "Done ✓";
  mwPay.disabled = false;
  form.amount.focus();
});

function clearWalletToken() {
  form.wallet.value = "";
  form.cryptogram.value = "";
  document.getElementById("token-badge").hidden = true;
  mwStatus.className = "mw-status";
  mwStatus.textContent = "Ready";
}

// Editing the card details by hand means the token no longer applies.
for (const name of ["card_number", "expiry"]) {
  form[name].addEventListener("input", () => {
    if (form.wallet.value) clearWalletToken();
  });
}

async function init() {
  renderWallet();
  renderMobileWallets();
  const res = await fetch("/api/terminal");
  const data = await res.json();
  const t = data.terminal;
  currencies = data.currencies;
  entryModes = data.entry_modes;
  wallets = data.wallets;

  document.getElementById("merchant").textContent =
    `${t.merchant_name} · ${t.city}, ${t.country} · MID ${t.merchant_id} · TID ${t.terminal_id} · MCC ${t.mcc}`;

  fillSelect("currency", currencies, "840");
  fillSelect("entry_mode", entryModes, "010");
  loadHistory();
}

function fillSelect(id, options, selected) {
  const el = document.getElementById(id);
  for (const [code, name] of Object.entries(options).sort((a, b) => a[1].localeCompare(b[1]))) {
    el.add(new Option(name, code, false, code === selected));
  }
}

// Group card digits in fours as the cashier types.
form.card_number.addEventListener("input", (e) => {
  const digits = e.target.value.replace(/\D/g, "").slice(0, 19);
  e.target.value = digits.replace(/(\d{4})(?=\d)/g, "$1 ");
});
form.expiry.addEventListener("input", (e) => {
  const d = e.target.value.replace(/\D/g, "").slice(0, 4);
  e.target.value = d.length > 2 ? d.slice(0, 2) + "/" + d.slice(2) : d;
});
form.cvv.addEventListener("input", (e) => {
  e.target.value = e.target.value.replace(/\D/g, "");
});

document.getElementById("testcard").addEventListener("click", async () => {
  const card = await (await fetch("/api/test-card")).json();
  form.card_number.value = card.card_number.replace(/(\d{4})(?=\d)/g, "$1 ");
  form.expiry.value = card.expiry;
  form.cvv.value = card.cvv;
  if (!form.amount.value) form.amount.value = "12.50";
  clearWalletSelection();
  clearWalletToken();
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
    form.card_number.value = form.expiry.value = form.cvv.value = form.cardholder_name.value = "";
    form.amount.value = "";
    clearWalletSelection();
    clearWalletToken();
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

function money(minor, currencyCode) {
  const iso = currencies[currencyCode] || "USD";
  return new Intl.NumberFormat(undefined, { style: "currency", currency: iso }).format(minor / 100);
}

function renderResult(tx) {
  const r = tx.request;
  const rows = FIELDS.filter(([key]) => r[key] !== undefined && r[key] !== "")
    .map(([key, de, name]) => {
      let value = r[key];
      if (key === "de2_pan" && r.wallet_provider) name = "Device account number (token)";
      if (key === "wallet_provider") value = wallets[value] || value;
      if (key === "de4_amount") value = `${value}  (${money(value, r.de49_currency)})`;
      if (key === "de22_pos_entry_mode") value = `${value}  (${entryModes[value]})`;
      if (key === "de49_currency") value = `${value}  (${currencies[value]})`;
      return `<tr><td class="mono">${de}</td><td>${name}</td><td class="mono">${escapeHTML(String(value))}</td></tr>`;
    })
    .join("");

  resultEl.className = "";
  resultEl.innerHTML = `
    <div class="result-head">
      <span class="status">${escapeHTML(tx.status.replace("_", " "))}</span>
      <p>${escapeHTML(tx.message)}</p>
    </div>
    <div class="table-wrap"><table class="fields"><tbody>${rows}</tbody></table></div>
    <details><summary>Raw JSON (PAN masked, CVV removed)</summary><pre>${escapeHTML(JSON.stringify(r, null, 2))}</pre></details>`;
}

async function loadHistory() {
  const txs = await (await fetch("/api/transactions")).json();
  if (!txs.length) return;
  historyEl.innerHTML = txs
    .map((tx) => {
      const r = tx.request;
      return `<tr>
        <td>${new Date(tx.created).toLocaleTimeString()}</td>
        <td class="mono">${r.de11_stan}</td>
        <td class="mono">${r.de37_rrn}</td>
        <td class="mono">${escapeHTML(r.de2_pan)}</td>
        <td>${escapeHTML(entryModes[r.de22_pos_entry_mode] || r.de22_pos_entry_mode)}${r.wallet_provider ? " · " + escapeHTML(wallets[r.wallet_provider] || r.wallet_provider) : ""}</td>
        <td class="num">${money(r.de4_amount, r.de49_currency)}</td>
        <td><span class="status">${escapeHTML(tx.status.replace("_", " "))}</span></td>
      </tr>`;
    })
    .join("");
}

function escapeHTML(s) {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

init();
