// receipt.js: the printed receipt for a transaction, built from what the
// terminal sent and got back. Loaded after app.js and uses its helpers
// (money, brand, entryModes, wallets, terminal, escapeHTML).

// Contactless sales at or under this amount need no cardholder verification
// (a US-style contactless CVM limit), so the receipt says so.
const CONTACTLESS_CVM_LIMIT = 10_000;

// Application IDs a chip reports, by brand. They identify the card's payment
// application and must appear on chip receipts.
const AIDS = { Visa: "A0000000031010", Mastercard: "A0000000041010", "American Express": "A00000002501", Discover: "A0000001523010" };

// cvmLine says how the cardholder was verified, as receipts must.
function cvmLine(r, approved) {
  const entry = r.de22_pos_entry_mode;
  if (entry === ENTRY.ecommerce || entry === ENTRY.cof) return ["Card not present", "card-present"];
  if (r.wallet_provider) return ["Verified on device", "cdcvm"];
  if (r.de52_pin_data) return ["Verified by PIN", "pin"];
  if (entry === ENTRY.contactless && r.de4_amount <= CONTACTLESS_CVM_LIMIT) return ["No signature required", "cvm"];
  if (!approved) return null;
  return ["SIGNATURE", "cvm"];
}

function receiptHTML(tx, copy) {
  const r = tx.request;
  const resp = tx.response;
  const cur = r.de49_currency;
  const last4 = r.de2_pan.slice(-4);
  const b = brand(r.de2_pan);
  const chip = [ENTRY.chip, ENTRY.contactless].includes(r.de22_pos_entry_mode) || r.wallet_provider;
  const approved = tx.status === "APPROVED" || tx.status === "PARTIAL" || tx.status === "VOIDED";
  const cashback = (r.de54_additional_amounts || []).find((a) => a.type === "40");
  const when = `${r.de13_local_date.slice(0, 2)}/${r.de13_local_date.slice(2)}/${new Date(tx.created).getFullYear()}  ${r.de12_local_time.match(/../g).join(":")}`;
  const line = (label, value, term) =>
    `<div class="r-line"><span${term ? ` data-term="${term}"` : ""}>${escapeHTML(label)}</span><span>${escapeHTML(value)}</span></div>`;
  const rule = `<div class="r-rule"></div>`;

  let title = "SALE";
  if (tx.status === "VOIDED") title = "VOID";
  else if (r.de3_processing_code.startsWith("20")) title = "REFUND";

  const body = [];
  body.push(`<div class="r-center"><b>${escapeHTML(terminal.merchant_name || "")}</b><br>${escapeHTML(terminal.city || "")}, ${escapeHTML(terminal.country || "")}</div>`);
  body.push(line("MID " + r.de42_merchant_id.trim(), "TID " + r.de41_terminal_id, "de42"));
  body.push(line(when, "", "de12"));
  body.push(rule);
  body.push(`<div class="r-center r-title" data-term="${title === "VOID" ? "void" : "receipt"}">${title}</div>`);
  body.push(line(`${b} ${(productNames[productFor(r.de2_pan)] || "Credit").toUpperCase()}`, `**** ${last4}`, "pan-masking"));
  body.push(line("Entry", r.wallet_provider ? `${wallets[r.wallet_provider] || r.wallet_provider} (contactless)` : entryModes[r.de22_pos_entry_mode] || r.de22_pos_entry_mode, "de22"));
  if (chip) {
    body.push(line("AID", AIDS[b] || "—", "aid"));
    if (r.de55_arqc) body.push(line("ARQC", r.de55_arqc.length > 16 ? r.de55_arqc.slice(0, 16) + "…" : r.de55_arqc, "de55"));
  }
  body.push(rule);
  if (cashback) {
    body.push(line("AMOUNT", money(r.de4_amount - cashback.amount, cur), "de4"));
    body.push(line("CASH BACK", money(cashback.amount, cur), "cashback"));
  }
  body.push(`<div class="r-line r-total"><span data-term="de4">TOTAL</span><span>${title === "VOID" ? "-" : ""}${money(r.de4_amount, cur)}</span></div>`);
  if (tx.status === "PARTIAL" && resp) {
    body.push(line("APPROVED", money(resp.de4_amount, cur), "partial-approval"));
    body.push(line("BALANCE DUE", money(r.de4_amount - resp.de4_amount, cur), "partial-approval"));
  }
  body.push(rule);
  if (resp) {
    const ok = resp.de39_response_code === "00" || resp.de39_response_code === "10";
    body.push(line(ok ? (tx.status === "VOIDED" ? "VOIDED" : "APPROVED") : "DECLINED", resp.de38_auth_code ? `AUTH ${resp.de38_auth_code}` : resp.response_text || "", ok ? "de38" : "de39"));
    body.push(line(`RESP ${resp.de39_response_code}`, resp.response_text || "", "de39"));
  } else {
    body.push(line("NO RESPONSE", "Sale reversed", "timeout-reversal"));
  }
  body.push(line(`RRN ${r.de37_rrn}`, `STAN ${r.de11_stan}`, "de37"));
  if (tx.reversal && tx.reversal.advice) body.push(line(`REVERSAL STAN ${tx.reversal.advice.de11_stan}`, tx.reversal.status, "reversal"));

  const cvm = cvmLine(r, approved && tx.status !== "VOIDED");
  if (cvm) {
    body.push(rule);
    if (cvm[0] === "SIGNATURE" && copy === "merchant") {
      body.push(`<div class="r-sign"><span data-term="cvm">X</span>________________________</div><div class="r-center">${escapeHTML((r.cardholder_name || "Cardholder").toUpperCase())}</div>`);
    } else {
      body.push(`<div class="r-center" data-term="${cvm[1]}">${escapeHTML(cvm[0] === "SIGNATURE" ? "Signature on merchant copy" : cvm[0])}</div>`);
    }
  }
  body.push(rule);
  if (approved && tx.status !== "VOIDED") body.push(`<div class="r-center">I agree to pay the above total amount<br>according to the card issuer agreement</div>`);
  body.push(`<div class="r-center r-copy" data-term="receipt">${copy === "merchant" ? "MERCHANT COPY" : "CUSTOMER COPY"}</div>`);
  return `<div class="receipt">${body.join("")}</div>`;
}

const receiptDialog = document.createElement("dialog");
receiptDialog.className = "receipt-dialog";
receiptDialog.setAttribute("aria-label", "Receipt");
document.body.append(receiptDialog);
let receiptTx = null;

function showReceipt(tx, copy = "customer") {
  receiptTx = tx;
  receiptDialog.innerHTML = `
    <div class="receipt-tabs" role="group" aria-label="Copy">
      <button type="button" data-copy="customer" aria-pressed="${copy === "customer"}">Customer copy</button>
      <button type="button" data-copy="merchant" aria-pressed="${copy === "merchant"}">Merchant copy</button>
    </div>
    ${receiptHTML(tx, copy)}
    <div class="receipt-actions">
      <button type="button" class="secondary" data-receipt-print>Print</button>
      <button type="button" data-receipt-close>Close</button>
    </div>`;
  if (!receiptDialog.open) receiptDialog.showModal();
}

receiptDialog.addEventListener("click", (e) => {
  const copy = e.target.closest("button[data-copy]");
  if (copy) return showReceipt(receiptTx, copy.dataset.copy);
  if (e.target.closest("[data-receipt-print]")) return window.print();
  if (e.target.closest("[data-receipt-close]") || e.target === receiptDialog) receiptDialog.close();
});

// Receipt buttons on the result panel and in the history. They hold the
// transaction's STAN; the transaction is looked up from the last render.
document.addEventListener("click", (e) => {
  const btn = e.target.closest("button[data-receipt]");
  if (!btn) return;
  const tx = (window.lastTransactions || []).find((t) => t.request.de11_stan === btn.dataset.receipt);
  if (tx) showReceipt(tx);
});
