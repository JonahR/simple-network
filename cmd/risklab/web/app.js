// Risk lab: pick a drill, run it, and watch each probe's verdict arrive.
// Every row links to the same transaction on the acquirer, network and
// issuer pages, where the drill's traffic shows up like any other sale.

const VERDICTS = [
  ["caught", "Caught", "A control declined it, as expected"],
  ["allowed", "Allowed", "Legitimate baseline, approved"],
  ["gap", "Gaps", "Got through where a real network would stop it"],
  ["unexpected", "Unexpected", "Didn't match what the drill expected"],
];
const VERDICT_LABEL = { caught: "Caught", allowed: "Allowed", gap: "Gap", unexpected: "Unexpected", ok: "Done", failed: "Failed" };
const PARTY_LABEL = { acquirer: "Acquirer", network: "Network", issuer: "Issuer" };
const PAGE_LABEL = { acquirer: "Acquirer", network: "Network", issuer: "Issuer", settlement: "Settlement" };

let meta = null; // /api/drills
let current = null; // The run on screen
let pollTimer = null;

const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
const $ = (id) => document.getElementById(id);

function money(minor) {
  return "$" + (minor / 100).toFixed(2);
}

function code(c) {
  if (!c) return "";
  const rc = meta.response_codes[c];
  const cls = rc?.approved ? "ok" : "bad";
  return `<span class="code ${cls}" data-term="de39" title="${esc(rc?.text || "")}">${esc(c)}</span> <span class="code-text">${esc(rc?.text || "")}</span>`;
}

async function init() {
  meta = await (await fetch("/api/drills")).json();
  $("who").innerHTML = `Drills run as the <span data-term="merchant">Risk Lab merchant</span> (${esc(meta.merchant_id)}), <span data-term="de41">terminal ${esc(meta.terminal_id)}</span>`;
  for (const cat of ["fraud", "failure"]) {
    $(`drills-${cat}`).innerHTML = meta.drills.filter((d) => d.category === cat).map(drillCard).join("");
  }
  document.querySelectorAll("[data-run]").forEach((b) => b.addEventListener("click", () => start(b.dataset.run)));

  const runs = await (await fetch("/api/runs")).json();
  renderHistory(runs);
  const wanted = new URLSearchParams(location.hash.slice(1)).get("run");
  const pick = runs.find((r) => r.id === wanted) || runs.find((r) => r.status === "running") || runs[0];
  if (pick) load(pick.id);
}

function drillCard(d) {
  const disrupts = d.disrupts ? `<p class="disrupts"><span data-term="issuer-health">Affects live traffic</span>: ${esc(d.disrupts)}</p>` : "";
  return `<article class="drill" id="drill-${esc(d.id)}">
    <div class="drill-head">
      <h3><span data-term="${esc(d.term)}">${esc(d.title)}</span></h3>
      <button type="button" class="run" data-run="${esc(d.id)}" data-term="risk-lab">Run</button>
    </div>
    <p>${esc(d.threat)}</p>
    <p class="defense"><b data-term="fraud-scoring">Defense</b>: ${esc(d.defense)}</p>
    ${disrupts}
  </article>`;
}

async function start(id) {
  const res = await fetch("/api/runs", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ drill: id }) });
  if (!res.ok) {
    $("run-meta").textContent = (await res.text()).trim();
    return;
  }
  const run = await res.json();
  show(run);
  poll(run.id);
  // In one column the results sit below every drill; bring them into view.
  if (matchMedia("(max-width: 1000px)").matches) document.querySelector(".run-panel").scrollIntoView({ behavior: "smooth" });
}

async function load(id) {
  const res = await fetch(`/api/runs/${encodeURIComponent(id)}`);
  if (!res.ok) return;
  const run = await res.json();
  show(run);
  if (run.status === "running") poll(run.id);
}

function poll(id) {
  clearTimeout(pollTimer);
  pollTimer = setTimeout(async () => {
    const res = await fetch(`/api/runs/${encodeURIComponent(id)}`);
    if (!res.ok) return;
    const run = await res.json();
    if (current?.id === id) show(run);
    if (run.status === "running") poll(id);
    else renderHistory(await (await fetch("/api/runs")).json());
  }, 600);
}

function show(run) {
  current = run;
  if (location.hash !== `#run=${run.id}`) history.replaceState(null, "", `#run=${run.id}`);
  const d = meta.drills.find((x) => x.id === run.drill);
  const running = run.status === "running";
  document.querySelectorAll("[data-run]").forEach((b) => (b.disabled = running));
  document.querySelectorAll(".drill").forEach((el) => el.classList.toggle("selected", el.id === `drill-${run.drill}`));

  $("run-meta").textContent = `${run.id} · started ${new Date(run.started).toLocaleTimeString()}`;
  $("run-head").className = "run-head";
  $("run-head").innerHTML = `<h3><span data-term="${esc(d?.term || "risk-lab")}">${esc(run.title)}</span></h3>
    <span class="status ${running ? "running" : "done"}">${running ? "Running…" : "Finished"}</span>`;

  const kpis = $("kpis");
  kpis.hidden = false;
  kpis.innerHTML =
    `<div class="kpi"><div class="k-label" data-term="msg-0100">Messages sent</div><div class="k-value">${run.probes}</div></div>` +
    VERDICTS.map(([v, label, sub]) => `<div class="kpi v-${v}"><div class="k-label" data-term="drill-verdict">${label}</div><div class="k-value">${run.tally[v] || 0}</div><div class="k-sub">${sub}</div></div>`).join("");

  const watch = $("watch");
  watch.hidden = !d?.watch?.length;
  if (d?.watch?.length) {
    watch.innerHTML = `<h4 data-term="payment-flows">Where to look</h4><ul>${d.watch
      .map((h) => `<li><a href="${esc(pageURL(h.page))}" target="_blank" rel="noopener" data-term="${esc(h.page === "network" ? "network" : h.page)}">${esc(PAGE_LABEL[h.page] || h.page)}</a> ${esc(h.text)}</li>`)
      .join("")}</ul>`;
  }

  $("results-wrap").hidden = false;
  $("results").innerHTML = run.results.length ? run.results.map(row).join("") : `<tr><td colspan="7" class="empty">Sending the first message…</td></tr>`;
  const trimmed = $("trimmed");
  trimmed.hidden = !run.trimmed;
  trimmed.textContent = run.trimmed ? `${run.trimmed} more rows not shown; the scorecard counts them.` : "";
}

function pageURL(page, issuer) {
  const l = meta.links;
  if (page === "issuer") return l.issuers[issuer] || Object.values(l.issuers)[0];
  return l[page] || "#";
}

function row(r) {
  const verdict = `<span class="verdict v-${esc(r.verdict)}" data-term="drill-verdict">${esc(VERDICT_LABEL[r.verdict] || r.verdict)}</span>`;
  const control = r.control ? `<div class="sub"><span data-term="${esc(r.term || "risk-lab")}">${esc(r.control)}</span></div>` : "";
  const gap = r.gap ? `<div class="gap"><b data-term="drill-verdict">Gap</b>: ${esc(r.gap)}</div>` : "";
  const note = r.note ? `<div class="sub note">${esc(r.note)}</div>` : "";
  if (r.kind !== "probe") {
    const kindTerm = r.kind === "finding" ? "velocity-check" : r.term || "risk-lab";
    return `<tr class="k-${esc(r.kind)}"><td class="num muted">${r.seq}</td>
      <td colspan="4"><div><span data-term="${esc(kindTerm)}">${esc(r.label)}</span></div>${r.kind === "finding" ? "" : control}${note}${gap}</td>
      <td>${verdict}</td><td></td></tr>`;
  }
  const entry = meta.entry_modes[r.entry] || r.entry;
  const via = r.via === "network" ? ` · <span data-term="message-authentication">sent straight to the network</span>` : "";
  const what = `<div>${esc(r.label)}</div><div class="sub"><span class="mono" data-term="pan-masking">${esc(r.card)}</span> · <span data-term="de22">${esc(entry)}</span> · <span data-term="de4">${money(r.amount)}</span>${via}</div>${control}${note}${gap}`;
  const expect = (r.expect || []).map((c) => `<span class="code" data-term="de39">${esc(c)}</span>`).join(" ");
  let stopped = "";
  if (r.stopped_at) {
    stopped = `<span data-term="${esc(r.stopped_at)}">${esc(PARTY_LABEL[r.stopped_at] || r.stopped_at)}</span>`;
    if (r.stop_step) stopped += ` <span class="step mono" data-term="switch-trace">${esc(r.stop_step)}</span>`;
    if (r.stop_detail) stopped += `<div class="sub">${esc(r.stop_detail)}</div>`;
  } else if (r.code && meta.response_codes[r.code]?.approved) {
    stopped = `<span class="muted">Not stopped</span>`;
  }
  return `<tr class="k-probe"><td class="num muted">${r.seq}</td><td>${what}</td><td>${expect}</td><td>${code(r.code)}<div class="sub">${r.ms ? r.ms.toFixed(0) + " ms" : ""}</div></td><td>${stopped}</td><td>${verdict}</td><td class="follow">${links(r)}</td></tr>`;
}

// links point at the same transaction on the other pages, using the deep
// links each page already understands.
function links(r) {
  const l = meta.links;
  const out = [];
  if (r.terminal_id && r.stan) {
    const q = new URLSearchParams({ tid: r.terminal_id, stan: r.stan, de7: r.de7 });
    out.push(`<a href="${esc(l.acquirer)}/#${q}" target="_blank" rel="noopener" data-term="acquirer">Acquirer</a>`);
  }
  if (r.network_txn_id) {
    out.push(`<a href="${esc(l.network)}/#txn=${esc(r.network_txn_id)}" target="_blank" rel="noopener" data-term="network">Network</a>`);
    if (r.issuer_id && (r.stopped_at === "issuer" || !r.stopped_at)) {
      out.push(`<a href="${esc(pageURL("issuer", r.issuer_id))}/#txn=${esc(r.network_txn_id)}" target="_blank" rel="noopener" data-term="issuer">Issuer</a>`);
    }
  }
  return out.join("");
}

function renderHistory(runs) {
  const el = $("history");
  if (!runs.length) {
    el.className = "empty";
    el.textContent = "No runs yet.";
    return;
  }
  el.className = "history";
  el.innerHTML = runs
    .map((r) => {
      const t = r.tally || {};
      const marks = VERDICTS.filter(([v]) => t[v]).map(([v]) => `<span class="verdict v-${v}">${t[v]} ${VERDICT_LABEL[v].toLowerCase()}${v === "gap" && t[v] > 1 ? "s" : ""}</span>`).join(" ");
      return `<button type="button" class="hist${current?.id === r.id ? " selected" : ""}" data-load="${esc(r.id)}">
        <span class="h-title">${esc(r.title)}</span>
        <span class="muted">${new Date(r.started).toLocaleTimeString()} · ${r.status === "running" ? "running" : r.probes + (r.probes === 1 ? " message" : " messages")}</span>
        <span class="h-marks">${marks}</span></button>`;
    })
    .join("");
  el.querySelectorAll("[data-load]").forEach((b) => b.addEventListener("click", () => load(b.dataset.load)));
}

window.addEventListener("hashchange", () => {
  const id = new URLSearchParams(location.hash.slice(1)).get("run");
  if (id && id !== current?.id) load(id);
});

init().catch((err) => {
  $("run-head").textContent = "Could not load the drills: " + err.message;
});
