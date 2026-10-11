// Acquirer back office: polls /api/overview and shows each authorization's
// path from the terminal through the acquirer to the network and back.

const LANES = [
  { id: "pos", label: "POS", role: "Merchant terminal", term: "pos-terminal" },
  { id: "acquirer", label: "Acquirer", role: "This bank", term: "acquirer" },
  { id: "network", label: "Network", role: "Routes to the issuer", term: "network" },
];
// Glossary terms for the labels Help mode explains.
const MTI_TERMS = { "0100": "msg-0100", "0110": "msg-0110", "0420": "reversal", "0430": "reversal" };
const FIELD_TERMS = { DE11: "de11", DE18: "de18", DE32: "de32-acquirer-id", DE37: "de37" };
const REVERSAL_TEXT = { pending: "0420 sent, waiting for the 0430", acknowledged: "acknowledged by the network", stopped: "not acknowledged; retries stopped at shutdown", rejected: "refused by the network; needs manual cleanup" };
const LANE_INDEX = Object.fromEntries(LANES.map((l, i) => [l.id, i]));
const POLL_MS = 2000;

let data = null;
let networkURL = "";
// The selected authorization, or null to follow the newest one.
let selectedKey = null;
// The authorization the URL asks for, as linked from the POS: the terminal's
// own keys (#tid=&stan=&de7=), which every record has, or a network
// transaction ID (#txn=), which only records the network answered have.
let wanted = parseHash();

function parseHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  const w = { tid: p.get("tid"), stan: p.get("stan"), de7: p.get("de7"), txn: p.get("txn") };
  return (w.tid && w.stan) || w.txn ? w : null;
}

function matches(rec, w) {
  if (w.tid && w.stan) {
    return rec.terminal_id === w.tid && rec.terminal_stan === w.stan && (!w.de7 || rec.request.de7_transmission_datetime === w.de7);
  }
  return rec.response.network_txn_id === w.txn;
}

const keyOf = (rec) => `${rec.received}|${rec.terminal_id}|${rec.terminal_stan}`;

function money(minor, currencyCode) {
  const iso = (data && data.currencies[currencyCode]) || "USD";
  return new Intl.NumberFormat(undefined, { style: "currency", currency: iso }).format(minor / 100);
}

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

const fmtMS = (ms) => (ms < 10 ? ms.toFixed(1) : Math.round(ms)) + " ms";

// statusBadge shows the outcome with words and an icon, not color alone.
function statusBadge(rec) {
  const code = rec.response.de39_response_code;
  const text = data.response_codes[code]?.text || "Unknown";
  const [cls, icon] = { APPROVED: ["good", "✓"], PARTIAL: ["", "◐"], DECLINED: ["bad", "✕"] }[rec.status] || ["", ""];
  let html = `<span class="status ${cls}" data-term="de39"><span aria-hidden="true">${icon}</span> ${escapeHTML(code)} ${escapeHTML(text)}</span>`;
  // An approved sale the terminal voided or reversed won't be paid.
  if (rec.reversal && rec.status !== "DECLINED") {
    const void_ = rec.reversal_reason === "17";
    html += ` <span class="status" data-term="${void_ ? "void" : "timeout-reversal"}"><span aria-hidden="true">↩</span> ${void_ ? "Voided" : "Reversed"}</span>`;
  }
  return html;
}

function renderKPIs() {
  const s = data.stats;
  const kpis = [
    ["Authorizations", "authorization", s.count],
    ["Approval rate", "approval-rate", s.count ? Math.round(s.approval_rate * 100) + "%" : "—"],
    ["Avg response time", "latency", s.count ? fmtMS(s.avg_ms) : "—"],
    ["Avg network round trip", "latency", s.network_ms ? fmtMS(s.network_ms) : "—"],
  ];
  document.getElementById("kpis").innerHTML = kpis
    .map(([label, term, value]) => `<div class="kpi" data-term="${term}"><small><span data-term="${term}">${label}</span></small><b>${value}</b></div>`)
    .join("");
}

function selected() {
  const txns = data.transactions;
  if (!txns.length) return null;
  if (wanted) {
    const hit = txns.find((r) => matches(r, wanted));
    if (hit) {
      selectedKey = keyOf(hit);
      wanted = null;
    }
  }
  return (selectedKey && txns.find((r) => keyOf(r) === selectedKey)) || txns[0];
}

// --- Sent to the network ------------------------------------------------------
// The messages the acquirer put on the wire to the network, field by field, as
// the record keeps them: card number masked, PIN block and CVV2 hidden.

const PROC_TYPES = { "00": "Purchase", "09": "Purchase with cash back", "20": "Refund", "30": "Balance inquiry" };
const ACCOUNT_TYPES = { "00": "default account", "10": "savings", "20": "checking", "30": "credit" };
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

// mmdd formats an MMDD date, and hms an hhmmss time, as they are read.
const mmdd = (v) => `${MONTHS[Number(v.slice(0, 2)) - 1] || v.slice(0, 2)} ${Number(v.slice(2, 4))}`;
const hms = (v) => `${v.slice(0, 2)}:${v.slice(2, 4)}:${v.slice(4, 6)}`;
const amountWire = (minor) => String(minor).padStart(12, "0");

// The 0100's fields in ISO 8583 order: the data element, the record's key,
// and how to read the value.
const AUTH_FIELDS = [
  { de: "MTI", name: "Message type", key: "mti", term: "mti", read: () => "Authorization request" },
  { de: "DE2", name: "Card number", key: "de2_pan", term: "de2", read: (v, r) => (r.wallet_provider ? "Device token, masked here" : "Masked here; sent in full") },
  { de: "DE3", name: "Processing code", key: "de3_processing_code", term: "de3", read: (v) => [PROC_TYPES[v.slice(0, 2)], ACCOUNT_TYPES[v.slice(2, 4)] && `from ${ACCOUNT_TYPES[v.slice(2, 4)]}`].filter(Boolean).join(", ") },
  { de: "DE4", name: "Amount", key: "de4_amount", term: "de4", wire: amountWire, read: (v, r) => money(v, r.de49_currency) },
  { de: "DE7", name: "Transmission time", key: "de7_transmission_datetime", term: "de7", read: (v) => `${mmdd(v)} ${hms(v.slice(4))} UTC` },
  { de: "DE11", name: "STAN", key: "de11_stan", term: "de11", read: () => "Acquirer's trace number for this leg" },
  { de: "DE12", name: "Local time", key: "de12_local_time", term: "de12", read: hms },
  { de: "DE13", name: "Local date", key: "de13_local_date", term: "de12", read: mmdd },
  { de: "DE14", name: "Expiry", key: "de14_expiry", term: "de14", read: (v) => `${v.slice(2)}/${v.slice(0, 2)}` },
  { de: "DE18", name: "Merchant category", key: "de18_mcc", term: "de18", read: (v) => data.mccs[v] || "" },
  { de: "DE22", name: "Entry mode", key: "de22_pos_entry_mode", term: "de22", read: (v) => data.entry_modes[v] || "" },
  { de: "DE32", name: "Acquirer ID", key: "de32_acquirer_id", term: "de32-acquirer-id", read: (v) => (v === data.bank.id ? data.bank.name : "") },
  { de: "DE35", name: "Track 2", key: "de35_track2", term: "de35", read: () => "Stripe data, masked here" },
  { de: "DE37", name: "Retrieval reference", key: "de37_rrn", term: "de37", read: () => "Year digit, day of year, hour, STAN" },
  { de: "DE41", name: "Terminal ID", key: "de41_terminal_id", term: "de41", read: () => "" },
  { de: "DE42", name: "Merchant ID", key: "de42_merchant_id", term: "de42", read: () => "" },
  { de: "DE43", name: "Merchant name and location", key: "de43_merchant_name_location", term: "de43", read: () => "" },
  { de: "DE48", name: "Fleet prompts", key: "de48_fleet", term: "de48-fleet", wire: (f) => [f.odometer, f.vehicle_id, f.driver_id].join(" / "), read: () => "Odometer / vehicle / driver" },
  { de: "DE48", name: "Card-on-file indicator", key: "de48_cof_indicator", term: "de48-cof", read: () => "" },
  { de: "DE49", name: "Currency", key: "de49_currency", term: "de49", read: (v) => data.currencies[v] || "" },
  { de: "DE52", name: "PIN block", key: "de52_pin_data", term: "de52", wire: () => "••••••••••••••••", read: () => "Encrypted on the PIN pad; passed through unchanged" },
  { de: "DE54", name: "Additional amounts", key: "de54_additional_amounts", term: "de54", wire: (list) => list.map((a) => `${a.type}:${amountWire(a.amount)}`).join(" "), read: (list, r) => list.map((a) => money(a.amount, r.de49_currency)).join(", ") },
  { de: "DE55", name: "Chip cryptogram", key: "de55_arqc", term: "de55", read: () => "ARQC" },
  { de: "DE123", name: "Billing address (AVS)", key: "de123_avs", term: "avs", wire: (a) => [a.zip, a.street].filter(Boolean).join(" · "), read: () => "Issuer checks it against its records" },
  { de: "", name: "E-commerce indicator", key: "eci", term: "eci", read: (v) => ({ "05": "3-D Secure authenticated", "06": "Authentication attempted", "07": "Not authenticated" })[v] || "" },
  { de: "", name: "CAVV", key: "cavv", term: "cavv", read: () => "Proof of 3-D Secure authentication" },
  { de: "", name: "Cardholder name", key: "cardholder_name", term: "cardholder-name", read: () => "" },
  { de: "", name: "Wallet provider", key: "wallet_provider", term: "wallet-provider", read: () => "" },
];

// The 0420's fields: which ones it copies from the 0100 it undoes, and which
// are its own.
const REVERSAL_FIELDS = [
  { de: "MTI", name: "Message type", key: "mti", term: "mti", own: true, read: () => "Reversal advice" },
  { de: "DE2", name: "Card number", key: "de2_pan", term: "de2", read: () => "Masked here; sent in full" },
  { de: "DE4", name: "Amount", key: "de4_amount", term: "de4", wire: amountWire, read: (v, r, rec) => money(v, rec.request.de49_currency) },
  { de: "DE7", name: "Transmission time", key: "de7_transmission_datetime", term: "de7", own: true, read: (v) => `${mmdd(v)} ${hms(v.slice(4))} UTC` },
  { de: "DE11", name: "STAN", key: "de11_stan", term: "de11", own: true, read: () => "The 0420's own trace number" },
  { de: "DE32", name: "Acquirer ID", key: "de32_acquirer_id", term: "de32-acquirer-id", read: (v) => (v === data.bank.id ? data.bank.name : "") },
  { de: "DE37", name: "Retrieval reference", key: "de37_rrn", term: "de37", read: () => "" },
  { de: "DE39", name: "Reversal reason", key: "de39_response_code", term: "de39", own: true, read: (v) => data.response_codes[v]?.text || "" },
  { de: "DE90", name: "Original data elements", key: "de90_original_data", term: "de90", own: true,
    wire: (o) => `${o.mti} ${o.stan} ${o.transmission_time} ${o.acquirer_id}`,
    read: (o) => `Original ${o.mti}: STAN ${o.stan}, sent ${mmdd(o.transmission_time)} ${hms(o.transmission_time.slice(4))} UTC` },
];

const present = (v) => v !== undefined && v !== null && v !== "" && !(Array.isArray(v) && !v.length);

// source tags a field with where its value came from.
function source(label, cls) {
  return `<span class="src ${cls}">${label}</span>`;
}

// wireCard draws one message: its bitmap of present fields, then each field's
// value on the wire and what it means.
function wireCard(id, mti, title, msg, fields, tag, rec) {
  const rows = fields.filter((f) => present(msg[f.key]));
  const chips = rows
    .filter((f) => f.de.startsWith("DE"))
    .map((f) => `<span class="bit ${tag(f).cls}">${f.de.slice(2)}</span>`)
    .join("");
  const body = rows
    .map((f) => {
      const v = msg[f.key];
      const t = tag(f);
      return `<tr class="${t.cls}">
        <td><span class="mono de" data-term="${f.term}">${escapeHTML(f.de)}</span> ${escapeHTML(f.name)}</td>
        <td class="mono value">${escapeHTML(f.wire ? f.wire(v) : v)}</td>
        <td class="meaning">${escapeHTML(f.read(v, msg, rec))}</td>
        <td>${source(t.label, t.cls)}</td>
      </tr>`;
    })
    .join("");
  return `<div class="wire-card" id="${id}">
    <div class="wire-head">
      <span class="mti-badge" data-term="${MTI_TERMS[mti] || "mti"}">${mti}</span>
      <b>${title}</b>
      <span class="muted">STAN ${escapeHTML(msg.de11_stan)}</span>
    </div>
    <div class="bitmap"><small data-term="bitmap">Bitmap: fields present</small>${chips}</div>
    <div class="table-wrap"><table class="wire">
      <thead><tr><th data-term="iso8583">Field</th><th>On the wire</th><th>Meaning</th><th>From</th></tr></thead>
      <tbody>${body}</tbody>
    </table></div>
    ${mti === "0100" ? rawWindows(rec) : ""}
  </div>`;
}

// --- Raw message windows ---------------------------------------------------------
// simple-network's services talk JSON over HTTP. A real network link carries
// the same 0100 as ISO 8583 bytes; the acquirer encodes the masked request to
// show them, with each byte mapped to the field it belongs to.

// Which raw windows are open, kept across redraws.
const rawOpen = { iso: false, json: false };
// Encoded 0100s by record key; only the selected one is ever drawn.
const isoCache = new Map();
const ISO_CACHE_MAX = 20;

function rawWindows(rec) {
  return `<details class="raw" data-raw="iso" ${rawOpen.iso ? "open" : ""}>
      <summary><span data-term="iso8583">Raw ISO 8583 bytes</span> (PAN masked, PIN block hidden)</summary>
      <div class="iso" id="iso-body">${isoBody(rec)}</div>
    </details>
    <details class="raw" data-raw="json" ${rawOpen.json ? "open" : ""}>
      <summary><span data-term="msg-0100">Raw JSON the simulation sends</span> (PAN masked, PIN block and CVV hidden)</summary>
      <pre>${escapeHTML(JSON.stringify(rec.request, null, 2))}</pre>
    </details>`;
}

function isoBody(rec) {
  const w = isoCache.get(keyOf(rec));
  if (!w) return `<p class="muted">Encoding…</p>`;
  if (w.error) return `<p class="muted">Couldn't encode this message: ${escapeHTML(w.error)}</p>`;
  return isoHTML(w);
}

// loadISO encodes the selected 0100 once, when its window is open.
async function loadISO(rec) {
  const key = keyOf(rec);
  if (!rawOpen.iso || isoCache.has(key)) return;
  let w;
  try {
    const res = await fetch("/api/iso8583", { method: "POST", body: JSON.stringify(rec.request) });
    w = res.ok ? await res.json() : { error: (await res.text()).trim() };
  } catch {
    return; // Try again on the next redraw
  }
  isoCache.set(key, w);
  if (isoCache.size > ISO_CACHE_MAX) isoCache.delete(isoCache.keys().next().value);
  const cur = data && selected();
  const el = document.getElementById("iso-body");
  if (el && cur && keyOf(cur) === key) el.innerHTML = isoBody(cur);
}

const hex2 = (n) => n.toString(16).padStart(2, "0").toUpperCase();
const printable = (n) => (n >= 0x20 && n < 0x7f ? String.fromCharCode(n) : "·");

// isoHTML draws an encoded message three ways that point at each other:
// a hex dump, the bitmap bit by bit, and a map of each field's bytes.
function isoHTML(w) {
  const bytes = w.hex.match(/../g).map((h) => parseInt(h, 16));
  // owner[i] is the field index byte i belongs to; prefix marks length digits.
  const owner = new Array(bytes.length);
  const prefix = new Array(bytes.length).fill(false);
  w.fields.forEach((f, fi) => {
    for (let i = f.offset; i < f.offset + f.length; i++) owner[i] = fi;
    for (const p of f.parts || []) {
      if (p.label.startsWith("Length ")) for (let i = p.offset; i < p.offset + p.length; i++) prefix[i] = true;
    }
  });
  const hidden = (i) => w.fields[owner[i]].masked && bytes[i] === 0x2a;
  const byteCls = (i) => `b f${owner[i] % 6}${prefix[i] ? " pfx" : ""}${hidden(i) ? " hid" : ""}`;

  const rows = [];
  for (let at = 0; at < bytes.length; at += 16) {
    let hexCells = "";
    let ascii = "";
    for (let i = at; i < Math.min(at + 16, bytes.length); i++) {
      const tip = escapeHTML(w.fields[owner[i]].id);
      hexCells += `<span class="${byteCls(i)}" data-f="${owner[i]}" title="${tip}">${hidden(i) ? "••" : hex2(bytes[i])}</span>`;
      ascii += `<span class="${byteCls(i)}" data-f="${owner[i]}">${hidden(i) ? "•" : escapeHTML(printable(bytes[i]))}</span>`;
    }
    rows.push(`<div class="dump-row"><span class="off">${at.toString(16).padStart(4, "0")}</span><span class="hex">${hexCells}</span><span class="asc">${ascii}</span></div>`);
  }

  // Each bitmap byte, bit by bit: a set bit n means DE n follows.
  const fieldOfBit = Object.fromEntries(w.fields.filter((f) => f.bit).map((f) => [f.bit, w.fields.indexOf(f)]));
  const maps = w.fields
    .map((f, fi) => [f, fi])
    .filter(([f]) => f.id.startsWith("BITMAP"))
    .map(([f, fi]) => {
      const first = f.id === "BITMAP" ? 1 : 65;
      const groups = [];
      for (let k = 0; k < 8; k++) {
        const v = bytes[f.offset + k];
        let cells = "";
        for (let j = 0; j < 8; j++) {
          const bit = first + k * 8 + j;
          const on = (v >> (7 - j)) & 1;
          const target = fieldOfBit[bit];
          cells += on
            ? `<span class="cell on f${target % 6}" data-f="${target}" title="Bit ${bit} set: ${escapeHTML(w.fields[target].id)} follows">1<small>${bit}</small></span>`
            : `<span class="cell" title="Bit ${bit} clear: no DE${bit}">0<small>${bit}</small></span>`;
        }
        groups.push(`<div class="bm-byte"><span class="bm-hex f${fi % 6}" data-f="${fi}">${hex2(v)}</span><div class="bm-bits">${cells}</div></div>`);
      }
      return `<div class="bm"><div class="bm-title"><b>${escapeHTML(f.name)}</b> <span class="muted">bytes ${f.offset}–${f.offset + 7}, bits ${first}–${first + 63}</span></div><div class="bm-grid">${groups.join("")}</div></div>`;
    })
    .join("");

  // A field's value is its bytes after any length prefix: hex for binary
  // fields, text for the rest.
  const binary = (f) => f.id.startsWith("BITMAP") || f.id === "DE52" || f.id === "DE55";
  const show = (f, from, to) =>
    bytes
      .slice(from, to)
      .map((n, k) => (hidden(from + k) ? (binary(f) ? "••" : "•") : binary(f) ? hex2(n) : printable(n)))
      .join(binary(f) ? " " : "");
  const rowsMap = w.fields
    .map((f, fi) => {
      const parts = f.parts || [];
      const pfx = parts.find((p) => p.label.startsWith("Length ") && p.offset === f.offset);
      const start = pfx ? f.offset + pfx.length : f.offset;
      const sub = parts
        .filter((p) => p !== pfx)
        .map((p) => `<div class="part"><span class="muted">${escapeHTML(p.label)}</span> <span class="mono">${escapeHTML(show(f, p.offset, p.offset + p.length))}</span></div>`)
        .join("");
      return `<tr data-f="${fi}">
        <td><span class="swatch f${fi % 6}"></span><span class="mono">${escapeHTML(f.id)}</span> ${escapeHTML(f.name)}</td>
        <td class="mono">${escapeHTML(f.format)}</td>
        <td class="num mono">${f.offset}–${f.offset + f.length - 1}</td>
        <td class="num">${f.length}</td>
        <td class="mono value">${pfx ? `<span class="b pfx sample" title="Length prefix: ${f.length - pfx.length} bytes follow">${escapeHTML(pfx.label.slice(7))}</span> ` : ""}${escapeHTML(show(f, start, f.offset + f.length))}${sub}</td>
      </tr>`;
    })
    .join("");

  return `<p class="hint">simple-network sends this 0100 to the network as JSON over HTTP. On a real network link it travels as these ${bytes.length} bytes: the
      <span data-term="mti">MTI</span> and fields as ASCII text, the <span data-term="bitmap">bitmap</span> as binary, and each variable-length field led by its
      length (<span class="b pfx sample">LL</span> or <span class="b pfx sample">LLL</span> digits). Point at any byte, bit or row to see its field. DE48 subelement tags are simple-network's own; real networks each define theirs.</p>
    <div class="dump mono" role="img" aria-label="Hex dump of the ISO 8583 message">${rows.join("")}</div>
    ${maps}
    <div class="table-wrap"><table class="iso-map">
      <thead><tr><th data-term="iso8583">Field</th><th>Format</th><th class="num">Bytes</th><th class="num">Length</th><th>Value</th></tr></thead>
      <tbody>${rowsMap}</tbody>
    </table></div>`;
}

// Pointing at any byte, bit or row lights up everything for that field.
function lightField(target) {
  const root = document.querySelector("#path .iso");
  if (!root) return;
  const hit = target && root.contains(target) ? target.closest("[data-f]") : null;
  const f = hit ? hit.dataset.f : "";
  if (root.dataset.hot === f) return;
  root.querySelectorAll(".hot").forEach((n) => n.classList.remove("hot"));
  if (f) root.querySelectorAll(`[data-f="${f}"]`).forEach((n) => n.classList.add("hot"));
  root.dataset.hot = f;
}

// wireHTML shows what the acquirer sent the network for rec: the 0100 it
// forwarded, and the 0420 if it had to reverse it.
function wireHTML(rec) {
  const forwarded = rec.steps.some((s) => s.from === "acquirer" && s.to === "network" && s.mti === "0100");
  if (!forwarded) {
    return `<h3>Sent to the network</h3><p class="hint">Nothing. The acquirer declined this one itself, before asking the network.</p>`;
  }
  // A change applies to the 0100 when the network leg carries its new value;
  // the 0110's restored STAN doesn't.
  const changed = {};
  for (const c of rec.changes) {
    const f = AUTH_FIELDS.find((f) => f.de === c.field);
    if (f && String(rec.request[f.key]) === c.after) changed[c.field] = c.before ? "rewritten" : "added";
  }
  const authTag = (f) =>
    changed[f.de] === "added" ? { label: "Added by acquirer", cls: "acq" }
      : changed[f.de] === "rewritten" ? { label: "Rewritten by acquirer", cls: "acq" }
        : f.de === "MTI" ? { label: "Terminal", cls: "" }
          : { label: "Terminal, unchanged", cls: "" };
  let html = `<h3>Sent to the network</h3>
    <p class="hint wire-intro">Each data element the acquirer put on the wire. <span class="src acq">Highlighted</span> fields are the acquirer's own; the rest pass through from the terminal. The card number is masked and the PIN block and CVV2 are hidden here; the network receives them.</p>
    <div class="wire-grid">`;
  html += wireCard("wire-0100", "0100", "Authorization request", rec.request, AUTH_FIELDS, authTag, rec);
  const adv = rec.reversal_advice;
  if (adv) {
    const advTag = (f) => (f.own ? { label: "New in the 0420", cls: "acq" } : { label: "Copied from the 0100", cls: "" });
    html += wireCard("wire-0420", "0420", "Reversal advice", adv, REVERSAL_FIELDS, advTag, rec);
  }
  return html + "</div>";
}

// renderPath draws the selected authorization as a sequence diagram.
function renderPath(rec) {
  const el = document.getElementById("path");
  if (!rec) return;
  el.className = "";
  const r = rec.request;
  document.getElementById("path-title").textContent =
    `${rec.merchant_name || rec.merchant_id} · ${r.de2_pan} · ${money(r.de4_amount, r.de49_currency)}` +
    (selectedKey ? "" : " · following newest");

  const lanes = LANES.map(
    (l) => `<div class="lane${l.id === "acquirer" ? " here" : ""}"><span data-term="${l.term}">${l.label}</span><small>${l.role}</small></div>`
  ).join("");

  const approved = rec.status !== "DECLINED";
  const rows = rec.steps
    .map((s) => {
      // A step within one participant (the acquirer giving up on the network,
      // or waiting to resend a reversal) is a note on its lifeline, not an arrow.
      if (s.from === s.to) {
        const i = LANE_INDEX[s.from];
        return `<div class="seq-row">
          <div class="msg note" style="grid-column: ${2 * i + 1} / ${2 * i + 3}">
            <div class="msg-label"><span aria-hidden="true">⏱</span> ${escapeHTML(s.title)}</div>
            <div class="msg-ms">${s.ms ? fmtMS(s.ms) : "&nbsp;"}</div>
          </div>
          <div class="msg-detail">${escapeHTML(s.detail)}</div>
        </div>`;
      }
      const a = LANE_INDEX[s.from];
      const b = LANE_INDEX[s.to];
      const lo = Math.min(a, b);
      const hi = Math.max(a, b);
      const dir = b > a ? "right" : "left";
      const tone = s.mti === "0110" ? (approved ? "good" : "bad") : "";
      return `<div class="seq-row">
        <div class="msg ${dir} ${tone}" style="grid-column: ${2 * lo + 2} / ${2 * hi + 2}">
          <div class="msg-label"><span class="mti" data-term="${MTI_TERMS[s.mti] || "mti"}">${escapeHTML(s.mti)}</span>${escapeHTML(s.title)}</div>
          <div class="msg-line"></div>
          <div class="msg-ms">${s.ms ? fmtMS(s.ms) : "&nbsp;"}</div>
        </div>
        <div class="msg-detail">${escapeHTML(s.detail)}${toNetwork(s) ? ` <button type="button" class="link" data-wire="wire-${s.mti}">Show the fields ↓</button>` : ""}</div>
      </div>`;
    })
    .join("");

  const changes = rec.changes.length
    ? `<h3>What the acquirer changed</h3>
      <div class="table-wrap"><table class="changes"><thead><tr><th>Field</th><th>Terminal</th><th></th><th>Network leg</th><th>Why</th></tr></thead><tbody>
      ${rec.changes
        .map(
          (c) => `<tr><td><span class="mono" data-term="${FIELD_TERMS[c.field] || "iso8583"}">${escapeHTML(c.field)}</span> ${escapeHTML(c.name)}</td>
            <td class="mono">${escapeHTML(c.before || "—")}</td><td class="arrow">→</td>
            <td class="mono">${escapeHTML(c.after)}</td><td>${escapeHTML(c.why)}</td></tr>`
        )
        .join("")}</tbody></table></div>`
    : "";

  const txnID = rec.response.network_txn_id;
  const link = txnID && networkURL
    ? `<a href="${escapeHTML(networkURL)}/#txn=${encodeURIComponent(txnID)}">See the network and issuer hops →</a>`
    : "";
  el.innerHTML = `
    <div class="seq"><div class="seq-lanes">${lanes}</div>${rows}</div>
    <div class="path-foot">
      <span>Result ${statusBadge(rec)}</span>
      <span><span data-term="latency">Total at acquirer</span> <b>${fmtMS(rec.total_ms)}</b></span>
      ${rec.response.de38_auth_code ? `<span><span data-term="de38">Auth code</span> <b class="mono">${escapeHTML(rec.response.de38_auth_code)}</b></span>` : ""}
      ${txnID ? `<span><span data-term="network-txn-id">Network txn</span> <b class="mono">${escapeHTML(txnID)}</b></span>` : ""}
      ${rec.reversal ? `<span><span data-term="reversal">Reversal</span> <b>${escapeHTML(REVERSAL_TEXT[rec.reversal] || rec.reversal)}</b>${rec.reversal_reason ? ` · reason <b class="mono" data-term="reversal">${escapeHTML(rec.reversal_reason)}</b> ${escapeHTML(data.response_codes[rec.reversal_reason]?.text || "")}` : ""}</span>` : ""}
      ${link}
    </div>
    ${changes}
    ${wireHTML(rec)}`;
  loadISO(rec);
}

// toNetwork is true for the messages the acquirer sent the network; the
// page shows each one's fields.
const toNetwork = (s) => s.from === "acquirer" && s.to === "network" && (s.mti === "0100" || s.mti === "0420");

document.getElementById("path").addEventListener(
  "toggle",
  (e) => {
    const kind = e.target.dataset?.raw;
    if (!kind) return;
    rawOpen[kind] = e.target.open;
    const cur = data && selected();
    if (kind === "iso" && cur) loadISO(cur);
  },
  true
);
document.getElementById("path").addEventListener("mouseover", (e) => lightField(e.target));
document.getElementById("path").addEventListener("mouseleave", () => lightField(null));

document.getElementById("path").addEventListener("click", (e) => {
  const b = e.target.closest("button[data-wire]");
  if (!b) return;
  const card = document.getElementById(b.dataset.wire);
  if (!card) return;
  card.scrollIntoView({ behavior: "smooth", block: "start" });
  card.classList.remove("flash");
  void card.offsetWidth; // Restart the highlight
  card.classList.add("flash");
});

function renderTransactions(current) {
  const body = document.getElementById("txns");
  if (!data.transactions.length) return;
  body.innerHTML = data.transactions
    .map((rec, i) => {
      const r = rec.request;
      const stan = r.de11_stan === rec.terminal_stan ? r.de11_stan : `${rec.terminal_stan} → ${r.de11_stan}`;
      return `<tr data-i="${i}" aria-selected="${rec === current}">
        <td>${new Date(rec.received).toLocaleTimeString()}</td>
        <td>${escapeHTML(rec.merchant_name || "Unknown merchant")}<small class="mono">${escapeHTML(rec.merchant_id)}</small></td>
        <td class="mono">${escapeHTML(rec.terminal_id)}</td>
        <td class="mono">${escapeHTML(stan)}</td>
        <td class="mono">${escapeHTML(r.de2_pan)}</td>
        <td class="num">${money(r.de4_amount, r.de49_currency)}</td>
        <td>${statusBadge(rec)}</td>
        <td class="num">${fmtMS(rec.total_ms)}</td>
      </tr>`;
    })
    .join("");
}

document.getElementById("txns").addEventListener("click", (e) => {
  const row = e.target.closest("tr[data-i]");
  if (!row) return;
  const rec = data.transactions[Number(row.dataset.i)];
  // Clicking the selected row again goes back to following the newest.
  selectedKey = selectedKey === keyOf(rec) ? null : keyOf(rec);
  render();
});

function renderMerchants() {
  const lines = (totals, fn) => (totals.length ? totals.map(fn).join("<br>") : "—");
  document.getElementById("merchants").innerHTML = data.merchants
    .map((m) => {
      const t = m.totals;
      const mcc = data.mccs[m.mcc] ? `${m.mcc} (${data.mccs[m.mcc]})` : m.mcc;
      return `<tr>
        <td>${escapeHTML(m.name)}<small><span class="mono">${escapeHTML(m.merchant_id)}</span> · ${escapeHTML(m.city)}, ${escapeHTML(m.country)}</small></td>
        <td>${escapeHTML(mcc)}</td>
        <td class="mono">${m.terminals.map(escapeHTML).join("<br>")}</td>
        <td class="num">${(m.discount_bps / 100).toFixed(2)}%</td>
        <td class="num">${lines(t, (x) => `${x.approved} / ${x.declined}`)}</td>
        <td class="num">${lines(t, (x) => money(x.sales, x.currency))}</td>
        <td class="num">${lines(t, (x) => money(x.refunds, x.currency))}</td>
        <td class="num">${lines(t, (x) => money(x.fees, x.currency))}</td>
        <td class="num"><b>${lines(t, (x) => money(x.net, x.currency))}</b></td>
      </tr>`;
    })
    .join("");
}

// renderClearing shows the clearing files the acquirer sent and what it paid
// each merchant once the network settled.
function renderClearing() {
  const c = data.clearing;
  document.getElementById("clearing-status").textContent =
    `${c.unpresented} approved not yet presented · ${c.presented} waiting for settlement`;
  if (c.files.length) {
    document.getElementById("files").innerHTML = c.files
      .map((f) => {
        const a = f.ack;
        const answer = !a
          ? `<span class="status bad" data-term="clearing-file">✕ Not acknowledged</span> <small>${escapeHTML(f.error)}; resent as is next time</small>`
          : a.status === "rejected"
            ? `<span class="status bad" data-term="clearing-file">✕ Rejected</span> <small>${escapeHTML(a.reason)}</small>`
            : `<span class="status good" data-term="clearing-file">✓ ${a.status === "duplicate" ? "Duplicate, already processed" : "Accepted"}</span> <small>${a.accepted} cleared${a.rejected.length ? `, ${a.rejected.length} rejected: ${escapeHTML(a.rejected.map((r) => r.reason).join("; "))}` : ""} · cycle ${escapeHTML(a.cycle_id)}</small>`;
        return `<tr><td class="num">${f.header.sequence}</td><td class="mono">${escapeHTML(f.header.file_id)}</td><td class="num">${f.trailer.record_count}</td><td class="num">${money(f.trailer.hash_total, "840")}</td><td>${answer}</td></tr>`;
      })
      .join("");
  }
  if (c.fundings.length) {
    document.getElementById("fundings").innerHTML = c.fundings
      .map((f) => `<tr>
        <td class="mono">${escapeHTML(f.cycle_id)}</td>
        <td>${escapeHTML(f.value_date)}</td>
        <td>${escapeHTML(f.merchant_name || f.merchant_id)}</td>
        <td class="num">${f.count}</td>
        <td class="num">${money(f.sales, f.currency)}</td>
        <td class="num">${f.refunds ? "−" + money(f.refunds, f.currency) : "—"}</td>
        <td class="num">${money(f.discount_fee, f.currency)}</td>
        <td class="num"><b>${money(f.paid, f.currency)}</b></td>
        <td class="num">${money(f.interchange, f.currency)}</td>
        <td class="num">${money(f.network_fees, f.currency)}</td>
        <td class="num">${money(f.margin, f.currency)}</td>
      </tr>`)
      .join("");
  }
}

function render() {
  const current = selected();
  renderKPIs();
  renderPath(current);
  renderTransactions(current);
  renderMerchants();
  renderClearing();
}

document.getElementById("send-file").addEventListener("click", async (e) => {
  e.target.disabled = true;
  try {
    await fetch("/clearing/submit", { method: "POST" });
  } finally {
    e.target.disabled = false;
  }
});

// lastBody is the previous poll's response; the page redraws only when it
// changes, so a label being hovered in Help mode isn't replaced under the pointer.
let lastBody = "";

async function poll() {
  try {
    const body = await (await fetch("/api/overview")).text();
    if (body !== lastBody) {
      lastBody = body;
      data = JSON.parse(body);
      document.getElementById("bank").textContent = `${data.bank.name} · Acquirer ID (DE32) ${data.bank.id}`;
      document.getElementById("bank").dataset.term = "de32-acquirer-id";
      render();
    }
  } catch {
    lastBody = "";
    document.getElementById("bank").textContent = "Acquirer not reachable. Retrying…";
  }
  setTimeout(poll, POLL_MS);
}

window.addEventListener("hashchange", () => {
  wanted = parseHash();
  if (data) render();
});

fetch("/ui/pages")
  .then((r) => r.json())
  .then((pages) => {
    networkURL = pages.find((p) => p.id === "network")?.url || "";
    const settlement = pages.find((p) => p.id === "settlement");
    if (settlement) document.getElementById("settlement-link").href = settlement.url;
  })
  .catch(() => {});

poll();
