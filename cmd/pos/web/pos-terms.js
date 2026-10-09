// pos-terms.js: attaches glossary terms to the parts of the POS that app.js
// renders at runtime (the 0100 field table, transaction history, wallet, header).
// Static labels carry data-term directly in index.html. Keeping this mapping
// out of app.js means POS changes and learning changes rarely collide.
// cmd/pos/glossary_test.go checks that every field app.js can show has a term.
(function () {
  "use strict";
  if (!window.Learn) return;

  // DE22 values → the entry-mode term that explains them.
  const ENTRY_TERMS = { "010": "manual-entry", "011": "manual-entry", "901": "magstripe", "051": "chip-emv", "071": "contactless", "812": "ecommerce", "100": "credential-on-file" };
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

  function tagWallets() {
    const face = document.getElementById("card-face");
    if (face) {
      face.querySelectorAll(".card-bottom small").forEach((s) => tag(s, Learn.lookup(s.textContent)));
      tag(face.querySelector(".card-top span"), Learn.lookup(face.querySelector(".card-top span")?.textContent || ""));
      tag(face.querySelector(".virtual-note"), "virtual-card");
      tag(face.querySelector(".chip"), "chip-emv");
      tag(face.querySelector(".card-number"), "de2");
    }
    document.querySelectorAll("#card-list .card-row").forEach((row) => {
      const label = row.querySelector(".card-row-label");
      if (label) tag(label, Learn.lookup(label.textContent));
    });
    document.querySelectorAll("#mw-tabs [role=tab]").forEach((b) => tag(b, Learn.lookup(b.getAttribute("aria-label") || b.textContent)));
    document.querySelectorAll("#mw-cards small").forEach((s) => tag(s, "device-token"));

    // Badges change text as cards are read, so re-point them every time.
    const retag = (el, id) => { if (el && id) el.dataset.term = id; };
    const read = document.getElementById("read-badge");
    if (read && !read.hidden) retag(read, Learn.lookup(read.textContent.split(" · ")[0]));
    const product = document.getElementById("product-badge");
    if (product && !product.hidden) retag(product, Learn.lookup(product.textContent));
  }

  function tagAll() {
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
