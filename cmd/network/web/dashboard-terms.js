// dashboard-terms.js: attaches glossary terms to the parts of the network
// dashboard that app.js renders at runtime (KPIs, topology, feed, trace,
// issuers, BIN table, token vault). Static labels carry data-term in index.html.
// cmd/network/learn_test.go checks that every label app.js can show has a term.
(function () {
  "use strict";
  if (!window.Learn) return;

  const tag = (el, id) => { if (el && id && !el.dataset.term) el.dataset.term = id; };
  // Labels like "● Healthy" or "✕ Breaker open" lead with an icon.
  const text = (el) => (el?.textContent || "").replace(/^[^\p{L}\p{N}]+/u, "").trim();
  const byText = (el) => tag(el, Learn.lookup(text(el)));
  const cols = (tr, ids) => ids.forEach((id, i) => tr.cells[i] && tag(tr.cells[i].querySelector(".status") || tr.cells[i], id));

  function tagAll() {
    document.querySelectorAll("#kpis .k-label").forEach(byText);
    document.querySelectorAll("#codes .b-label").forEach((el) => tag(el, "de39"));

    // Topology: node titles, issuer health, and the switch's stage boxes.
    const topo = document.getElementById("topo");
    if (topo) {
      topo.querySelectorAll("text").forEach((t) => {
        const s = text(t);
        if (!s) return;
        if (/^Acquirer\b/.test(s)) return tag(t, "acquirer");
        if (/POS$/.test(s)) return tag(t, "pos-terminal");
        if (/ sent$/.test(s)) return tag(t, "authorization");
        if (/in flight$|^idle$/.test(s)) return tag(t, "auth-switch");
        if (/ auths · BINs /.test(s)) return tag(t, "issuer");
        t.dataset.term = Learn.lookup(s) || t.dataset.term || "";
        if (!t.dataset.term) delete t.dataset.term;
      });
    }

    // Live feed: Time, Card, Mode, Issuer, Amount, Response, Latency.
    document.querySelectorAll("#feed tr[data-id]").forEach((tr) => cols(tr, ["de12", "pan-masking", "de22", "issuer", "de4", "de39", "latency"]));

    // Trace: step names, the header, and the messages tables.
    const trace = document.getElementById("trace");
    if (trace) {
      trace.querySelectorAll(".w-name").forEach(byText);
      tag(trace.querySelector(".trace-head .status"), "txn-status");
      tag(trace.querySelector(".trace-head .amount"), "de4");
      tag(trace.querySelector(".trace-id"), "network-txn-id");
      tag(trace.querySelector(".legs summary"), "iso8583");
      trace.querySelectorAll(".legs th").forEach(byText);
      trace.querySelectorAll(".legs tbody tr").forEach((tr) => byText(tr.cells[0]));
    }

    // Issuers: name, health, counts, and the Normal/Slow/Down simulation buttons.
    const issuers = document.getElementById("issuers");
    if (issuers) {
      issuers.querySelectorAll("th").forEach((th) => tag(th, th.textContent.trim() === "Approved" ? "approval-rate" : Learn.lookup(th.textContent)));
      issuers.querySelectorAll("tbody tr").forEach((tr) => cols(tr, ["issuer", "issuer-health", "authorization", "approval-rate", "latency"]));
      issuers.querySelectorAll("button[data-mode]").forEach((b) => tag(b, "issuer-health"));
    }

    // Routing: BIN table rows and token vault rows.
    document.querySelectorAll("#bins tr").forEach((tr) => {
      cols(tr, ["bin"]);
      tag(tr.cells[1], Learn.lookup(text(tr.cells[1])) || "issuer");
      tag(tr.cells[2], Learn.lookup(text(tr.cells[2])) || "card-products");
      tag(tr.cells[3], "bin");
    });
    document.querySelectorAll("#tokens tr").forEach((tr) => {
      cols(tr, ["device-token"]);
      tag(tr.cells[1], Learn.lookup(text(tr.cells[1])) || "mobile-wallet");
      tag(tr.cells[2], "pan-masking");
      tag(tr.cells[3], "device-token");
      tag(tr.cells[4]?.querySelector(".status"), "token-status");
      tag(tr.querySelector("button[data-token]"), "token-status");
    });
  }

  Learn.ready.then(() => {
    tagAll();
    let queued = false;
    new MutationObserver(() => {
      if (queued) return;
      queued = true;
      requestAnimationFrame(() => { queued = false; tagAll(); });
    }).observe(document.querySelector("main") || document.body, { childList: true, subtree: true, characterData: true });
  });
})();
