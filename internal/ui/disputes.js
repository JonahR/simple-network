// A member's dispute desk: the issuer's or acquirer's view of its own
// chargebacks, acting through the network's dispute system. The page's
// server forwards /api/disputes to the network as this member (see
// internal/dispute/desk.go), so the list holds only this member's cases and
// the network refuses moves that aren't its side's.
//
// A page opts in with:
//
//	<link rel="stylesheet" href="/ui/disputes.css">
//	<div class="sn-disputes" data-side="issuer"></div>   (or "acquirer")
//	<script src="/ui/disputes.js"></script>
(() => {
  const POLL_MS = 3000;
  const STAGES = {
    chargeback: ["First chargeback", "chargeback", "acquirer"],
    represented: ["Representment", "representment", "issuer"],
    pre_arbitration: ["Pre-arbitration", "pre-arbitration", "acquirer"],
  };
  const OUTCOMES = {
    merchant_liable: ["lost", "Cardholder won", "won"],
    cardholder_rebilled: ["won", "Merchant won", "lost"],
  };

  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const toCents = (s) => Math.round(parseFloat(String(s).replace(/[$,]/g, "")) * 100) || 0;

  function mount(root) {
    const side = root.dataset.side;
    const other = side === "issuer" ? "acquirer" : "issuer";
    let data = null;
    let selectedID = "";
    let lastBody = "";
    let lastCase = "";
    let saleOptions = "";
    let networkDesk = "";

    root.innerHTML = `
      <div class="sd-status" data-status></div>
      <div class="sd-cols">
        <div class="sd-side">
          <div class="table-wrap">
            <table class="list sd-list">
              <thead><tr><th data-term="chargeback">Case</th><th data-term="merchant">Sale</th><th data-term="reason-code">Reason</th><th class="num" data-term="de4">Amount</th><th data-term="chargeback">Stage</th></tr></thead>
              <tbody data-list><tr><td colspan="5" class="empty">Loading…</td></tr></tbody>
            </table>
          </div>
          ${side === "issuer" ? fileForm() : ""}
        </div>
        <div class="sd-case empty" data-case>Select a dispute.</div>
      </div>`;
    const $ = (sel) => root.querySelector(sel);

    function fileForm() {
      return `<details class="sd-file" data-file-details>
        <summary><span data-term="chargeback">File a chargeback</span> <span class="muted">for a cardholder who disputes a cleared sale</span></summary>
        <form class="sd-form" data-file>
          <label class="sd-field"><span data-term="presentment">Cleared sale</span><select name="txn"></select></label>
          <label class="sd-field"><span data-term="reason-code">Reason code</span><select name="reason"></select></label>
          <label class="sd-field"><span data-term="de4">Disputed amount</span><input name="amount" inputmode="decimal"></label>
          <label class="sd-field"><span data-term="chargeback">Cardholder's story</span><input name="note" placeholder="Optional"></label>
          <div data-eligibility></div>
          <div class="sd-actions"><button type="submit" class="primary" data-term="chargeback">File chargeback</button><span class="sd-error" data-file-error></span></div>
        </form>
      </details>`;
    }

    const desk = () => data.desk;
    const money = (minor, cur = "840") =>
      new Intl.NumberFormat(undefined, { style: "currency", currency: (data && data.currencies[cur]) || "USD" }).format(minor / 100);
    const reasonOf = (code) => desk().reasons.find((r) => r.code === code) || { code, name: code, description: "", responses: [] };
    const evidenceName = (id) => desk().evidence.find((e) => e.id === id)?.name || id;
    const rebuttalName = (id) => desk().rebuttals.find((r) => r.id === id)?.name || "No new information";
    const memberName = (id) => data.names?.[id] || id;
    const turnOf = (d) => STAGES[d.state]?.[2] || "";

    function stageBadge(d) {
      if (d.state === "closed") {
        const [cls, text] = OUTCOMES[d.outcome] || ["", d.outcome];
        return `<span class="sd-badge ${cls}" data-term="chargeback">${esc(text)}</span>`;
      }
      const [label, term, turn] = STAGES[d.state] || [d.state, "chargeback", ""];
      const yours = turn === side;
      return `<span class="sd-badge ${yours ? "yours" : turn}" data-term="${term}">${esc(label)} · ${yours ? "your turn" : `${turn}'s turn`}</span>`;
    }

    function renderStatus() {
      const ds = desk().disputes;
      const mine = ds.filter((d) => turnOf(d) === side);
      const open = ds.filter((d) => d.state !== "closed");
      const parts = [
        `<span><span data-term="dispute-window">Network date</span> <b>${esc(desk().today)}</b></span>`,
        `<span class="${mine.length ? "sd-due" : ""}" data-term="dispute-window">${mine.length ? `${mine.length} need your answer` : "Nothing waiting on you"}</span>`,
        `<span class="muted"><span data-term="chargeback">${open.length} open</span> · ${money(open.reduce((n, d) => n + d.amount, 0))} in dispute</span>`,
      ];
      if (networkDesk) parts.push(`<a href="${esc(networkDesk)}" data-term="network">Network's dispute desk</a>`);
      $("[data-status]").innerHTML = parts.join("");
    }

    function renderList() {
      const ds = desk().disputes;
      if (!ds.length) {
        $("[data-list]").innerHTML = `<tr><td colspan="5" class="empty">${
          side === "issuer" ? "No disputes. File one below for a cleared sale on this bank's cards." : "No chargebacks against this acquirer's merchants. Issuers file them from their own pages."
        }</td></tr>`;
        return;
      }
      $("[data-list]").innerHTML = ds
        .map((d) => `<tr data-id="${esc(d.id)}" aria-selected="${d.id === selectedID}">
          <td class="mono">${esc(d.id)}</td>
          <td>${esc(d.merchant_name)}<small class="mono">${esc(d.pan)}</small></td>
          <td class="mono">${esc(d.reason)}</td>
          <td class="num">${money(d.amount, d.currency)}</td>
          <td>${stageBadge(d)}${d.deadline ? `<small data-term="dispute-window">answer by ${esc(d.deadline)}</small>` : ""}</td>
        </tr>`)
        .join("");
    }

    function renderCase() {
      const el = $("[data-case]");
      const ds = desk().disputes;
      const d = ds.find((x) => x.id === selectedID) || ds.find((x) => turnOf(x) === side) || ds[0];
      if (!d) {
        el.className = "sd-case empty";
        el.textContent = "Select a dispute.";
        return;
      }
      selectedID = d.id;
      const key = JSON.stringify(d);
      if (key === lastCase) return; // Keep a half-chosen answer
      lastCase = key;
      const r = reasonOf(d.reason);
      const counterparty = side === "issuer" ? `against ${memberName(d.acquirer_id)}` : `filed by ${memberName(d.issuer_id)}`;
      el.className = "sd-case";
      el.innerHTML = `
        <div class="sd-head">
          <span class="sd-amount">${money(d.amount, d.currency)}</span>
          ${stageBadge(d)}
          <span class="muted"><span class="mono">${esc(d.id)}</span> · ${esc(d.merchant_name)} · <span class="mono">${esc(d.pan)}</span> · ${esc(counterparty)}</span>
        </div>
        <p><span data-term="reason-code"><b>${esc(r.code)} ${esc(r.name)}</b></span> · <span class="muted">${esc(r.description)}</span></p>
        <ol class="sd-timeline">${d.events
          .map((ev) => `<li>
            <div class="sd-t-head"><span class="sd-badge ${ev.actor === side ? "yours" : ev.actor}" data-term="${ev.actor}">${ev.actor === side ? "you" : esc(ev.actor)}</span><b>${esc(ev.stage)}</b><span class="muted">${esc(ev.day)}</span></div>
            <div>${esc(ev.note)}</div>
            ${ev.money ? `<div class="sd-t-money" data-term="net-settlement">Moves ${esc(ev.money)}</div>` : ""}
          </li>`)
          .join("")}</ol>
        ${turnPanel(d, r)}`;
    }

    // turnPanel offers this member its moves, or says who it's waiting on.
    function turnPanel(d, r) {
      const turn = turnOf(d);
      if (!turn) return "";
      if (turn !== side) {
        return `<div class="sd-wait" data-term="dispute-window">Waiting for the ${other} to answer by <b>${esc(d.deadline)}</b>. If it doesn't, ${side === "issuer" ? "the chargeback stands" : "the representment stands"}.</div>`;
      }
      const error = `<span class="sd-error" data-error></span>`;
      if (d.state === "chargeback") {
        const opts = r.responses.map((id) => `<option value="${esc(id)}">${esc(evidenceName(id))}</option>`).join("");
        return `<div class="sd-turn"><h3 data-term="representment">Answer the chargeback by ${esc(d.deadline)}</h3>
          <label class="sd-field"><span data-term="representment">Merchant's evidence</span><select data-input="evidence">${opts}</select></label>
          <div class="sd-actions">
            <button type="button" class="primary" data-act="represent" data-term="representment">Represent with evidence</button>
            <button type="button" data-act="accept" data-term="chargeback">Accept the chargeback</button>${error}
          </div>
          <p class="hint">Accepting leaves the money with the issuer and debits the merchant at the next funding.</p></div>`;
      }
      if (d.state === "represented") {
        const opts = desk().rebuttals.map((x) => `<option value="${esc(x.id)}">${esc(x.name)}</option>`).join("");
        return `<div class="sd-turn"><h3 data-term="pre-arbitration">The merchant answered "${esc(evidenceName(d.evidence))}". Respond by ${esc(d.deadline)}</h3>
          <label class="sd-field"><span data-term="pre-arbitration">Rebuttal</span><select data-input="rebuttal"><option value="">No new information</option>${opts}</select></label>
          <div class="sd-actions">
            <button type="button" class="primary" data-act="prearbitrate" data-term="pre-arbitration">Go to pre-arbitration</button>
            <button type="button" data-act="accept" data-term="representment">Accept and rebill the cardholder</button>${error}
          </div></div>`;
      }
      return `<div class="sd-turn"><h3 data-term="pre-arbitration">The issuer rebuts with "${esc(rebuttalName(d.rebuttal))}". Respond by ${esc(d.deadline)}</h3>
        <div class="sd-actions">
          <button type="button" class="primary" data-act="arbitrate" data-term="pre-arbitration">Ask the network to arbitrate</button>
          <button type="button" data-act="accept" data-term="chargeback">Accept: give the money back</button>${error}
        </div>
        <p class="hint">The network decides by whether the rebuttal answers "${esc(evidenceName(d.evidence))}". The loser pays ${money(desk().limits.Fee)}.</p></div>`;
    }

    function renderForm() {
      const f = $("[data-file]");
      if (!f) return;
      const opts = data.sales.length
        ? data.sales.map((s) => `<option value="${esc(s.network_txn_id)}" data-amount="${s.amount}">${esc(s.txn_date)} · ${esc(s.merchant_name)} · ${esc(s.pan)} · ${money(s.amount, s.currency)}</option>`).join("")
        : `<option value="">No cleared sales on this bank's cards yet: make a sale, then run the end of day</option>`;
      if (opts !== saleOptions) {
        const keep = f.txn.value;
        f.txn.innerHTML = opts;
        saleOptions = opts;
        if (keep && [...f.txn.options].some((o) => o.value === keep)) f.txn.value = keep;
        else syncAmount();
      }
      if (!f.reason.options.length) {
        f.reason.innerHTML = desk().reasons.map((r) => `<option value="${esc(r.code)}">${esc(r.code)} ${esc(r.name)} (${esc(r.category)})</option>`).join("");
        checkEligibility();
      }
    }

    function syncAmount() {
      const f = $("[data-file]");
      const opt = f.txn.selectedOptions[0];
      if (opt && opt.dataset.amount) f.amount.value = (Number(opt.dataset.amount) / 100).toFixed(2);
      checkEligibility();
    }

    let eligibilitySeq = 0;
    async function checkEligibility() {
      const f = $("[data-file]");
      const el = $("[data-eligibility]");
      if (!f.txn.value || !f.reason.value) {
        el.innerHTML = "";
        return;
      }
      const seq = ++eligibilitySeq;
      const q = new URLSearchParams({ txn: f.txn.value, reason: f.reason.value, amount: toCents(f.amount.value) });
      let res;
      try {
        res = await (await fetch(`/api/disputes/eligible?${q}`)).json();
      } catch {
        return;
      }
      if (seq !== eligibilitySeq) return;
      el.innerHTML = res.refusal
        ? `<div class="sd-refusal" data-term="reason-code">✕ The network would refuse: ${esc(res.refusal)}</div>`
        : `<div class="sd-ok" data-term="reason-code">✓ Eligible under ${esc(f.reason.value)}</div>`;
    }

    if (side === "issuer") {
      const f = $("[data-file]");
      f.txn.addEventListener("change", syncAmount);
      f.reason.addEventListener("change", checkEligibility);
      f.amount.addEventListener("input", checkEligibility);
      f.addEventListener("submit", async (e) => {
        e.preventDefault();
        const err = $("[data-file-error]");
        err.textContent = "";
        const res = await fetch("/api/disputes", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ network_txn_id: f.txn.value, reason: f.reason.value, amount: toCents(f.amount.value), note: f.note.value }),
        });
        const out = await res.json().catch(() => ({ error: `HTTP ${res.status}` }));
        if (!res.ok) {
          err.textContent = out.error;
          return;
        }
        selectedID = out.id;
        f.note.value = "";
        refresh();
      });
    }

    $("[data-list]").addEventListener("click", (e) => {
      const tr = e.target.closest("tr[data-id]");
      if (!tr) return;
      selectedID = tr.dataset.id;
      lastCase = "";
      render();
    });

    $("[data-case]").addEventListener("click", async (e) => {
      const b = e.target.closest("button[data-act]");
      if (!b) return;
      const panel = b.closest(".sd-turn");
      b.disabled = true;
      const res = await fetch(`/api/disputes/${encodeURIComponent(selectedID)}/${b.dataset.act}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          evidence: panel.querySelector('[data-input="evidence"]')?.value || "",
          rebuttal: panel.querySelector('[data-input="rebuttal"]')?.value || "",
        }),
      });
      if (!res.ok) {
        panel.querySelector("[data-error]").textContent = (await res.json().catch(() => ({ error: `HTTP ${res.status}` }))).error;
        b.disabled = false;
        return;
      }
      lastCase = "";
      refresh();
    });

    function render() {
      renderStatus();
      renderList();
      renderCase();
      renderForm();
    }

    let timer = 0;
    async function refresh() {
      clearTimeout(timer);
      timer = setTimeout(refresh, POLL_MS);
      if (document.hidden && data) return;
      let res, body;
      try {
        res = await fetch("/api/disputes");
        body = await res.text();
      } catch {
        return;
      }
      if (!res.ok) {
        $("[data-status]").innerHTML = `<span class="sd-error">The network's dispute system isn't answering (${esc(body.trim() || res.status)}).</span>`;
        lastBody = "";
        return;
      }
      if (body === lastBody) return;
      lastBody = body;
      data = JSON.parse(body);
      render();
    }

    fetch("/ui/pages")
      .then((r) => r.json())
      .then((pages) => {
        const net = pages.find((p) => p.id === "network");
        if (net) networkDesk = net.url.replace(/\/$/, "") + "/disputes/";
        if (data) renderStatus();
      })
      .catch(() => {});
    refresh();
  }

  document.querySelectorAll(".sn-disputes[data-side]").forEach(mount);
})();
