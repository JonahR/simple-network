// The network's fraud department: its rules, the cases they open, and the
// blocks they place. Rules are data; the builder composes one as a sentence
// and backtests it against the switch's recent traffic before it is saved.

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
const MODE_LABEL = { on: "On", test: "Test", off: "Off" };
const VERBS = { block: "declined and blocked", decline: "declined", review: "flagged for review" };
const STATUS_LABEL = { open: "Open", confirmed: "Confirmed fraud", cleared: "Cleared" };

let data = null; // GET /api/fraud
let editing = null; // Rule ID being edited, or "" for a new rule
let caseFilter = "open";
let armed = null; // Rule ID whose Delete was clicked once
let refreshTimer = null;

async function refresh() {
  clearTimeout(refreshTimer);
  try {
    data = await (await fetch("/api/fraud")).json();
    render();
  } finally {
    refreshTimer = setTimeout(refresh, 2000);
  }
}

function render() {
  const s = data.snapshot;
  const hits30 = s.rules.reduce((n, r) => n + r.stats.per_minute.reduce((a, b) => a + b, 0), 0);
  const open = s.cases.filter((c) => c.status === "open").length;
  const rate = s.screened ? ((s.declined / s.screened) * 100).toFixed(1) + "% of screened" : "";
  $("kpis").innerHTML = [
    ["Screened", s.screened.toLocaleString(), "authorizations checked", "fraud-rule"],
    ["Declined by fraud", s.declined.toLocaleString(), rate, "fraud-rule"],
    ["Rule hits", hits30.toLocaleString(), "in the last 30 minutes", "fraud-rule"],
    ["Open cases", open, "waiting for an analyst", "fraud-case"],
    ["Active blocks", s.blocks.length, "cards, terminals or merchants", "fraud-block"],
  ]
    .map(([label, value, sub, term]) => `<div class="kpi" data-term="${term}"><div class="k-label">${label}</div><div class="k-value">${value}</div><div class="k-sub">${esc(sub)}</div></div>`)
    .join("");

  const on = s.rules.filter((r) => r.mode === "on").length;
  const test = s.rules.filter((r) => r.mode === "test").length;
  $("flow-sub").textContent = `${on} rule${on === 1 ? "" : "s"} on · ${test} in test`;

  $("rules").innerHTML = s.rules.length ? s.rules.map(ruleCard).join("") : `<p class="empty">No rules. Add one with New rule.</p>`;
  renderCases(s.cases);
  $("blocks").innerHTML = s.blocks.length ? s.blocks.map(blockRow).join("") : `<p class="empty">Nothing is blocked.</p>`;
}

function ruleCard(r) {
  const modes = ["on", "test", "off"]
    .map((m) => `<button type="button" data-mode="${m}" data-rule="${esc(r.id)}" aria-pressed="${r.mode === m}" class="m-${m}" data-term="rule-mode">${MODE_LABEL[m]}</button>`)
    .join("");
  const total30 = r.stats.per_minute.reduce((a, b) => a + b, 0);
  return `<article class="rule mode-${esc(r.mode)}">
    <div class="rule-head">
      <h3>${esc(r.name)}</h3>
      <div class="seg modes" role="group" aria-label="Mode">${modes}</div>
    </div>
    <p class="sentence">${sentenceHTML(r.sentence)}</p>
    ${r.why ? `<p class="why">${esc(r.why)}</p>` : ""}
    <div class="rule-foot">
      ${spark(r.stats.per_minute)}
      <span class="stat"><b>${r.stats.hits}</b> <span data-term="fraud-rule">${r.stats.hits === 1 ? "hit" : "hits"}</span> · <b>${r.stats.declined}</b> declined · ${total30} in the last 30 min</span>
      <span class="rule-actions"><button type="button" class="link" data-edit="${esc(r.id)}">Edit</button><button type="button" class="link danger" data-delete="${esc(r.id)}">${armed === r.id ? "Click again to delete" : "Delete"}</button></span>
    </div>
  </article>`;
}

// sentenceHTML emphasizes the action after the arrow.
function sentenceHTML(s) {
  const [when, then] = s.split(" → ");
  return then ? `${esc(when)} <span class="arrow">→</span> <b>${esc(then)}</b>` : esc(s);
}

// spark draws 30 one-minute bars of rule hits, oldest first.
function spark(values) {
  const max = Math.max(1, ...values);
  const w = 3, gap = 1, h = 22;
  const bars = values
    .map((v, i) => {
      const bh = v ? Math.max(2, (v / max) * h) : 1;
      return `<rect x="${i * (w + gap)}" y="${h - bh}" width="${w}" height="${bh}" class="${v ? "on" : ""}"><title>${30 - i} min ago: ${v}</title></rect>`;
    })
    .join("");
  return `<svg class="spark" viewBox="0 0 ${values.length * (w + gap)} ${h}" width="${values.length * (w + gap)}" height="${h}" role="img" aria-label="Hits per minute, last 30 minutes" data-term="fraud-rule">${bars}</svg>`;
}

function renderCases(cases) {
  const list = caseFilter === "open" ? cases.filter((c) => c.status === "open") : cases;
  if (!list.length) {
    $("cases").innerHTML = `<p class="empty">${caseFilter === "open" ? "No open cases." : "No cases yet."}</p>`;
    return;
  }
  $("cases").innerHTML = list.slice(0, 100).map(caseRow).join("");
}

function caseRow(c) {
  const last = c.txns[c.txns.length - 1];
  const testBadge = c.mode === "test" ? ` <span class="badge test" data-term="rule-mode">Test</span>` : "";
  const did = (c.mode === "test" ? "would have " : "") + VERBS[c.action];
  const buttons =
    c.status === "open"
      ? `<button type="button" class="small" data-case="${esc(c.id)}" data-status="confirmed">Confirm fraud</button><button type="button" class="small" data-case="${esc(c.id)}" data-status="cleared">Clear</button>`
      : `<span class="badge s-${esc(c.status)}" data-term="fraud-case">${STATUS_LABEL[c.status]}</span>`;
  return `<article class="case s-${esc(c.status)}">
    <div class="case-head"><b>${esc(c.label)}</b><span class="muted">${new Date(c.updated).toLocaleTimeString()}</span></div>
    <div class="case-rule">${esc(c.rule_name)}${testBadge}: ${esc(did)}</div>
    <div class="muted">${esc(c.reason)}</div>
    <div class="case-foot">
      <span class="muted">${c.hits} hit${c.hits === 1 ? "" : "s"} · ${c.declined} declined${last ? ` · <a href="/#txn=${esc(last)}" data-term="switch-trace">latest trace</a>` : ""}</span>
      <span class="case-actions">${buttons}</span>
    </div>
    ${c.note ? `<div class="muted note">${esc(c.note)}</div>` : ""}
  </article>`;
}

function blockRow(b) {
  const left = Math.max(0, Math.round((new Date(b.until) - Date.now()) / 1000));
  const mins = left >= 60 ? `${Math.floor(left / 60)}m ${left % 60}s` : `${left}s`;
  return `<div class="block">
    <div><b>${esc(b.label)}</b><div class="muted">${esc(b.rule_name)} · ${b.hits} declined while blocked</div></div>
    <div class="block-right"><span class="countdown">${mins} left</span><button type="button" class="small" data-lift="${esc(b.id)}" data-term="fraud-block">Lift</button></div>
  </div>`;
}

// --- Actions -----------------------------------------------------------------

async function call(method, url, body) {
  const res = await fetch(url, { method, headers: { "Content-Type": "application/json" }, body: body ? JSON.stringify(body) : undefined });
  if (!res.ok) throw new Error((await res.text()).trim());
  return res.status === 204 ? null : res.json();
}

document.addEventListener("click", async (e) => {
  const t = e.target.closest("button");
  if (!t) return;
  try {
    if (t.dataset.mode) await call("POST", `/api/fraud/rules/${encodeURIComponent(t.dataset.rule)}/mode`, { mode: t.dataset.mode });
    else if (t.dataset.delete) {
      if (armed !== t.dataset.delete) {
        armed = t.dataset.delete;
        return render();
      }
      armed = null;
      await call("DELETE", `/api/fraud/rules/${encodeURIComponent(t.dataset.delete)}`);
    } else if (t.dataset.edit) return openBuilder(data.snapshot.rules.find((x) => x.id === t.dataset.edit));
    else if (t.dataset.case) await call("POST", `/api/fraud/cases/${encodeURIComponent(t.dataset.case)}`, { status: t.dataset.status });
    else if (t.dataset.lift) await call("DELETE", `/api/fraud/blocks/${encodeURIComponent(t.dataset.lift)}`);
    else if (t.dataset.cases) {
      caseFilter = t.dataset.cases;
      document.querySelectorAll("[data-cases]").forEach((b) => b.setAttribute("aria-pressed", String(b === t)));
    } else if (t.dataset.template !== undefined) return fillForm(data.templates[+t.dataset.template]);
    else return;
    refresh();
  } catch (err) {
    alertLine(err.message);
  }
});

function alertLine(msg) {
  $("flash").textContent = msg;
  setTimeout(() => ($("flash").textContent = ""), 6000);
}

// --- Rule builder ---------------------------------------------------------------

const form = $("builder");
const f = (name) => form.elements[name];

function setupBuilder() {
  const o = data.options;
  const opts = (list) => list.map((x) => `<option value="${esc(x.id)}">${esc(x.label)}</option>`).join("");
  f("subject").innerHTML = opts(o.subjects);
  f("measure").innerHTML = opts(o.measures);
  f("action").innerHTML = opts(o.actions);
  f("mode").innerHTML = opts(o.modes);
  $("entry-chips").innerHTML =
    `<span class="chips-label" data-term="de22">Entry modes</span>` +
    o.entry.map((x) => `<label class="chip"><input type="checkbox" name="entry" value="${esc(x.id)}"> ${esc(x.label)}</label>`).join("");
  $("templates").innerHTML =
    `<span class="chips-label">Start from a template</span>` +
    data.templates.map((t, i) => `<button type="button" class="chip" data-template="${i}" title="${esc(t.sentence)}">${esc(t.name)}</button>`).join("");
  $("new-rule").addEventListener("click", () => openBuilder(null));
  $("cancel").addEventListener("click", closeBuilder);
  form.addEventListener("input", schedulePreview);
  form.addEventListener("change", schedulePreview);
  form.addEventListener("submit", save);
}

function openBuilder(rule) {
  editing = rule ? rule.id : "";
  $("builder-title").textContent = rule ? `Edit “${rule.name}”` : "New rule";
  $("templates").hidden = !!rule;
  $("save").textContent = rule ? "Save changes" : "Add rule";
  form.hidden = false;
  fillForm(
    rule || { name: "", kind: "velocity", subject: "terminal", measure: "declines", threshold: 5, window_sec: 60, action: "block", block_sec: 120, mode: "test" },
  );
  form.scrollIntoView({ behavior: "smooth", block: "start" });
  f("name").focus();
}

function closeBuilder() {
  form.hidden = true;
  editing = null;
  $("builder-error").textContent = "";
}

// unit picks the largest unit that divides seconds evenly.
function unit(sec) {
  for (const u of [86400, 3600, 60]) if (sec && sec % u === 0) return u;
  return 1;
}

function fillForm(r) {
  f("name").value = r.name || "";
  f("why").value = r.why || "";
  form.querySelector(`input[name=kind][value=${r.kind || "velocity"}]`).checked = true;
  f("subject").value = r.subject || "card";
  f("measure").value = r.measure || "declines";
  const spend = r.measure === "spend";
  f("threshold").value = spend ? r.threshold / 100 : r.threshold ?? 0;
  const wu = unit(r.window_sec || 60);
  f("window_unit").value = wu;
  f("window").value = (r.window_sec || 60) / wu;
  f("amount").value = r.kind === "amount" ? (r.threshold / 100).toFixed(2) : "1000.00";
  f("action").value = r.action || "decline";
  const bu = unit(r.block_sec || 120);
  f("block_unit").value = bu;
  f("block").value = (r.block_sec || 120) / bu;
  f("mode").value = r.mode || "test";
  form.querySelectorAll("input[name=entry]").forEach((c) => (c.checked = (r.entry_modes || []).includes(c.value)));
  f("mccs").value = (r.mccs || []).join(", ");
  form.querySelector(".filters").open = !!((r.entry_modes || []).length || (r.mccs || []).length);
  schedulePreview();
}

function readForm() {
  const kind = form.querySelector("input[name=kind]:checked").value;
  const measure = f("measure").value;
  const r = {
    name: f("name").value,
    why: f("why").value,
    mode: f("mode").value,
    kind,
    subject: f("subject").value,
    action: f("action").value,
    entry_modes: [...form.querySelectorAll("input[name=entry]:checked")].map((c) => c.value),
    mccs: f("mccs").value.split(/[\s,]+/).filter(Boolean),
  };
  if (kind === "velocity") {
    r.measure = measure;
    r.threshold = Math.round(+f("threshold").value * (measure === "spend" ? 100 : 1));
    r.window_sec = Math.round(+f("window").value * +f("window_unit").value);
  } else {
    r.threshold = Math.round(+f("amount").value * 100);
  }
  if (r.action === "block") r.block_sec = Math.round(+f("block").value * +f("block_unit").value);
  return r;
}

let previewTimer = null;
function schedulePreview() {
  const kind = form.querySelector("input[name=kind]:checked").value;
  $("when-velocity").hidden = kind !== "velocity";
  $("when-amount").hidden = kind !== "amount";
  $("block-for").hidden = f("action").value !== "block";
  const spend = f("measure").value === "spend";
  form.querySelector(".dollar").hidden = !spend;
  f("threshold").step = spend ? "0.01" : "1";
  clearTimeout(previewTimer);
  previewTimer = setTimeout(preview, 250);
}

async function preview() {
  const r = readForm();
  if (!r.name) r.name = "Preview";
  const out = await call("POST", "/api/fraud/preview", r).catch((err) => ({ error: err.message }));
  $("builder-error").textContent = "";
  if (out.error) {
    $("preview-sentence").innerHTML = `<span class="error">${esc(out.error)}</span>`;
    $("backtest").innerHTML = "";
    return;
  }
  $("preview-sentence").innerHTML = sentenceHTML(out.sentence);
  $("backtest").innerHTML = backtestHTML(out.backtest);
}

function backtestHTML(bt) {
  if (!bt) return "";
  if (!bt.screened) return `<span data-term="backtest">Backtest</span>: no traffic yet to replay. Run some sales or a Risk lab drill.`;
  const span = `${new Date(bt.from).toLocaleTimeString()}–${new Date(bt.to).toLocaleTimeString()}`;
  let h = `<span data-term="backtest">Backtest</span> on the last <b>${bt.screened}</b> authorizations (${span}): `;
  if (!bt.hits) return h + "it would not have fired.";
  const blocked = bt.while_blocked ? ` (${bt.declined - bt.while_blocked} when it fired, ${bt.while_blocked} more while its block lasted)` : "";
  h += `fires <b>${bt.hits}</b> time${bt.hits === 1 ? "" : "s"}, declines <b>${bt.declined}</b>${blocked}, opens <b>${bt.cases}</b> case${bt.cases === 1 ? "" : "s"}.`;
  h += `<ul class="top">${bt.top.map((x) => `<li><a href="/#txn=${esc(x.txn)}">${esc(x.label)}</a> · ${x.hits} hit${x.hits === 1 ? "" : "s"}</li>`).join("")}</ul>`;
  return h;
}

async function save(e) {
  e.preventDefault();
  const r = readForm();
  try {
    if (editing) await call("PUT", `/api/fraud/rules/${encodeURIComponent(editing)}`, r);
    else await call("POST", "/api/fraud/rules", r);
    closeBuilder();
    refresh();
  } catch (err) {
    $("builder-error").textContent = err.message;
  }
}

(async () => {
  data = await (await fetch("/api/fraud")).json();
  setupBuilder();
  render();
  if (location.hash === "#new-rule") openBuilder(null); // Linkable: /fraud/#new-rule
  refreshTimer = setTimeout(refresh, 2000);
})();
