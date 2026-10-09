// pos-terms.js: attaches glossary terms to the parts of the POS that app.js
// renders at runtime (the 0100 field table, transaction history, wallet, header).
// Static labels carry data-term directly in index.html. Keeping this mapping
// out of app.js means POS changes and learning changes rarely collide.
// cmd/pos/learn_test.go checks that every field app.js can show has a term.
(function () {
  "use strict";
  if (!window.Learn) return;

  // DE22 values → the entry-mode term that explains them.
  const ENTRY_TERMS = { "011": "manual-entry", "901": "magstripe", "051": "chip-emv", "071": "contactless", "812": "ecommerce", "100": "credential-on-file", "801": "fallback" };
  // History columns, in order.
  const HISTORY_TERMS = ["de12", "de11", "de37", "pan-masking", "de22", "de4", "txn-status"];

  const tag = (el, id) => { if (el && id && !el.dataset.term) el.dataset.term = id; };

  // The 0100 table: each row is [DE number, field name, value].
  function tagResult(root) {
    root.querySelectorAll("table.fields tr").forEach((tr) => {
      const [deCell, nameCell, valueCell] = tr.cells;
      if (!nameCell || tr.dataset.learnRow) return;
      tr.dataset.learnRow = "1";
      const id = Learn.lookup(nameCell.textContent) || Learn.lookup(deCell.textContent);
      if (!id) { console.warn("pos-terms: no term for field", nameCell.textContent); return; }
      tag(deCell, id);
      tag(nameCell, id);
      let valueId = id;
      if (id === "de22") valueId = ENTRY_TERMS[valueCell.textContent.trim().slice(0, 3)] || id;
      if (id === "wallet-provider") valueId = Learn.lookup(valueCell.textContent) || id;
      tag(valueCell, valueId);
    });
    root.querySelectorAll(".result-head .status").forEach((el) => tag(el, "txn-status"));
    root.querySelectorAll("h3").forEach((h) => tag(h, Learn.lookup(h.textContent)));
    root.querySelectorAll(".trace-link a").forEach((a) => tag(a, "switch-trace"));
  }

  function tagHistory(tbody) {
    tbody.querySelectorAll("tr").forEach((tr) => {
      if (tr.cells.length < HISTORY_TERMS.length) return;
      HISTORY_TERMS.forEach((id, i) => tag(i === 6 ? tr.cells[i].querySelector(".status") || tr.cells[i] : tr.cells[i], id));
    });
  }

  // "Simple Coffee Co · San Francisco, US · MID … · TID … · MCC 5814" → labeled parts.
  function tagMerchant(el) {
    if (!el || el.children.length || !el.textContent.includes("MID")) return;
    const parts = el.textContent.split(" · ");
    const termFor = (p, i) => (i === 0 ? "merchant" : /^MID/.test(p) ? "de42" : /^TID/.test(p) ? "de41" : /^MCC/.test(p) ? "de18" : "de43");
    el.replaceChildren(...parts.flatMap((p, i) => {
      const s = document.createElement("span");
      s.textContent = p;
      s.dataset.term = termFor(p, i);
      return i ? [document.createTextNode(" · "), s] : [s];
    }));
  }

  // The second line of a wallet row: a product name, or a hint like "PIN 1234".
  const metaTerm = (text) =>
    /^PIN\b/.test(text) ? "pin" : /^Driver\b/.test(text) ? "de48-fleet" : /^Virtual\b/.test(text) ? "virtual-card" : Learn.lookup(text);

  // Every part of the wallet explains something in Help mode: the card face
  // and each row as a whole stand for the card's product, and their parts
  // (chip, number, name, expiry, hint) for themselves.
  function tagWallets() {
    const face = document.getElementById("card-face");
    if (face) {
      const product = Learn.lookup(face.querySelector(".card-top span")?.textContent || "");
      tag(face.querySelector(".card"), product);
      tag(face.querySelector(".card-top span"), product);
      tag(face.querySelector(".card-top span + span"), "network");
      face.querySelectorAll(".card-bottom small").forEach((s) => tag(s, Learn.lookup(s.textContent)));
      const [name, expiry] = face.querySelectorAll(".card-bottom > span");
      tag(name, "cardholder-name");
      tag(expiry, "de14");
      tag(face.querySelector(".virtual-note"), "virtual-card");
      tag(face.querySelector(".chip"), "chip-emv");
      tag(face.querySelector(".card-number"), "de2");
    }
    document.querySelectorAll("#card-list .card-row").forEach((row) => {
      const label = row.querySelector(".card-row-label");
      const id = label && Learn.lookup(label.textContent);
      tag(row, id);
      tag(label, id);
      const meta = row.querySelector(".card-row-meta");
      if (meta) tag(meta, metaTerm(meta.textContent.trim()));
    });
    document.querySelectorAll("#mw-tabs [role=tab]").forEach((b) => tag(b, Learn.lookup(b.getAttribute("aria-label") || b.textContent)));
    document.querySelectorAll("#mw-cards small").forEach((s) => tag(s, "device-token"));
    // A phone card row stands for its card's product, like the wallet rows.
    document.querySelectorAll("#mw-cards .mw-card").forEach((row) => {
      const label = row.querySelector(".mw-card-text")?.firstChild?.textContent || "";
      tag(row, Learn.lookup(label.trim()));
    });
    tag(document.getElementById("mw-phone"), "mobile-wallet");
    tag(document.querySelector("#mw-phone .phone-label"), "mobile-wallet");
    tag(document.getElementById("mw-status"), "cdcvm");
    tag(document.getElementById("mw-pay"), "mobile-wallet");

    // Badges change text as cards are read, so re-point them every time.
    const retag = (el, id) => { if (el && id) el.dataset.term = id; };
    const read = document.getElementById("read-badge");
    if (read && !read.hidden) retag(read, Learn.lookup(read.textContent.split(" · ")[0]));
    const product = document.getElementById("product-badge");
    if (product && !product.hidden) retag(product, Learn.lookup(product.textContent));
  }

  // Sale form fields, by name. The labels above them already carry terms;
  // the fields need their own so clicking the box itself explains it.
  const FIELD_TERMS = {
    amount: "de4", currency: "de49", entry_mode: "de22", card_number: "de2", expiry: "de14", cvv: "cvv2",
    cardholder_name: "cardholder-name", card_on_file: "credential-on-file", cof_indicator: "de48-cof", pin: "pin",
    cashback: "cashback", odometer: "de48-fleet", vehicle_id: "de48-fleet", driver_id: "de48-fleet",
    healthcare_amount: "healthcare-amount",
  };
  function tagForm() {
    const form = document.getElementById("sale");
    if (!form) return;
    for (const [name, id] of Object.entries(FIELD_TERMS)) tag(form.elements[name], id);
    // The rest of each label (hints like "(optional)") explains the same thing as its heading.
    form.querySelectorAll("label").forEach((l) => tag(l, l.querySelector(":scope > span[data-term]")?.dataset.term));
  }

  function tagAll() {
    tagForm();
    const result = document.getElementById("result");
    if (result) tagResult(result);
    const history = document.getElementById("history");
    if (history) tagHistory(history);
    tagMerchant(document.getElementById("merchant"));
    tagWallets();
  }

  Learn.ready.then(() => {
    tagAll();
    let queued = false;
    new MutationObserver(() => {
      if (queued) return;
      queued = true;
      requestAnimationFrame(() => { queued = false; tagAll(); });
    }).observe(document.querySelector("main") || document.body, { childList: true, subtree: true, characterData: true });
    new MutationObserver(tagAll).observe(document.getElementById("merchant") || document.body, { childList: true, characterData: true, subtree: true });
  });
})();
