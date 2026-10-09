const form = document.getElementById("sale");
const resultEl = document.getElementById("result");
const historyEl = document.getElementById("history");
let currencies = {};
let entryModes = {};

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
];

// The cardholder's wallet. These are well-known test numbers that pass Luhn.
const WALLET = [
  { label: "Everyday Credit", style: "blue", number: "4242424242424242", expiry: "12/29", cvv: "123", name: "Jane Doe" },
  { label: "Rewards Plus", style: "dark", number: "5555555555554444", expiry: "08/28", cvv: "456", name: "John Smith" },
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
  form.card_number.value = c.number.replace(/(\d{4})(?=\d)/g, "$1 ");
  form.expiry.value = c.expiry;
  form.cvv.value = c.cvv;
  form.cardholder_name.value = c.name;
  form.amount.focus();
}

function clearWalletSelection() {
  document.querySelectorAll("#cards .card").forEach((el) => el.classList.remove("selected"));
}

async function init() {
  renderWallet();
  const res = await fetch("/api/terminal");
  const data = await res.json();
  const t = data.terminal;
  currencies = data.currencies;
  entryModes = data.entry_modes;

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
        <td>${escapeHTML(entryModes[r.de22_pos_entry_mode] || r.de22_pos_entry_mode)}</td>
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
