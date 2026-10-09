// learn.js: definitions for every labeled thing in simple-network.
//
// Mark any element with data-term="<id>" (an id from glossary.json).
// Definitions stay out of the way until Help is on: toggle it with any
// [data-learn-toggle] button or the "h" key ("?" works too); Esc turns it off.
// While Help is on:
//   - hovering a marked element with a mouse previews its definition;
//   - clicking, Enter or Space on a plain label pins the definition;
//   - buttons and inputs keep working, so you can learn while you use the app;
//     on touch screens, which can't hover, tapping them explains them instead.
// Learn.show() opens a definition regardless, for pages that ask explicitly.
// Learn.autoTag(root) tags elements whose text matches a term's aliases; the
// doc viewer uses it on Mermaid diagrams.
(function () {
  "use strict";

  const BASE = "/learn/";
  const DOC = BASE + "doc.html?doc=";
  let terms = {};
  let aliasIndex = new Map();
  const ready = fetch(BASE + "glossary.json")
    .then((r) => r.json())
    .then((g) => {
      terms = g.terms;
      for (const [id, t] of Object.entries(terms)) {
        for (const a of t.aliases || []) aliasIndex.set(norm(a), id);
      }
      tagFocusable(document);
    })
    .catch((err) => console.error("learn.js: could not load glossary", err));

  // Links to the other apps (POS, dashboard) come from the serving service.
  fetch(BASE + "config.json")
    .then((r) => (r.ok ? r.json() : {}))
    .then((cfg) => {
      document.querySelectorAll("[data-link]").forEach((a) => {
        const url = cfg[a.dataset.link + "_url"];
        if (url) a.href = url;
      });
    })
    .catch(() => {});

  const INTERACTIVE ="button, a[href], input, select, textarea, summary, [role=tab], [role=radio], [role=button]:not([data-term])";
  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
  function norm(s) { return String(s).replace(/\s+/g, " ").replace(/[·:…]+$/g, "").trim().toLowerCase(); }

  // An element "acts" if clicking it already does something (a button, a link,
  // a form control). Those explain themselves on hover, not on click.
  function acts(el) { return el.matches(INTERACTIVE) || !!el.closest("button, a[href]"); }

  /* ---------- popover ---------- */
  const pop = document.createElement("div");
  pop.className = "learn-pop";
  pop.setAttribute("role", "dialog");
  pop.setAttribute("aria-modal", "false");
  pop.hidden = true;
  pop.innerHTML = `
    <div class="learn-pop-head"><h3 class="learn-pop-title" id="learn-pop-title"></h3>
      <button type="button" class="learn-pop-close" aria-label="Close">×</button></div>
    <div class="learn-pop-body"></div>`;
  pop.setAttribute("aria-labelledby", "learn-pop-title");
  const popTitle = pop.querySelector(".learn-pop-title");
  const popBody = pop.querySelector(".learn-pop-body");

  let anchor = null, pinned = false, showTimer = 0, hideTimer = 0, opener = null;

  function ktHref(ref) {
    // "07-...md#anchor" → doc viewer; other files (e.g. interactive pages) → served as-is.
    const [file, hash] = ref.split("#");
    if (file.endsWith(".md")) return DOC + encodeURIComponent(file) + (hash ? "#" + hash : "");
    return BASE + "kt/" + file + (hash ? "#" + hash : "");
  }

  function render(id) {
    const t = terms[id];
    if (!t) return false;
    popTitle.textContent = t.name;
    let h = `<p>${esc(t.def)}</p>`;
    if (t.kt && t.kt.length) {
      h += `<h4>Learn in the docs</h4><ul class="learn-links">` +
        t.kt.map(([label, ref]) => `<li><a href="${esc(ktHref(ref))}"><span class="learn-tag">KT</span>${esc(label)}</a></li>`).join("") + `</ul>`;
    }
    if (t.ext && t.ext.length) {
      h += `<h4>Official &amp; reference</h4><ul class="learn-links">` +
        t.ext.map(([label, url]) => `<li><a href="${esc(url)}" target="_blank" rel="noopener"><span class="learn-tag ext">↗</span>${esc(label)}</a></li>`).join("") + `</ul>`;
    }
    h += `<a class="learn-all" href="${BASE}#glossary">All terms →</a>`;
    popBody.innerHTML = h;
    popBody.scrollTop = 0;
    return true;
  }

  function place(el) {
    if (matchMedia("(max-width: 640px)").matches) { pop.style.left = pop.style.top = ""; return; }
    const r = el.getBoundingClientRect();
    const pw = pop.offsetWidth, ph = pop.offsetHeight;
    const vw = document.documentElement.clientWidth, vh = innerHeight;
    let left = Math.max(12, Math.min(r.left, vw - pw - 12));
    let top = r.bottom + 8;
    if (top + ph > vh - 8 && r.top - ph - 8 > 8) top = r.top - ph - 8;
    pop.style.left = left + scrollX + "px";
    pop.style.top = Math.max(8, top) + scrollY + "px";
  }

  function show(el, id, pin) {
    clearTimeout(showTimer); clearTimeout(hideTimer);
    if (!render(id)) return;
    if (anchor && anchor !== el) anchor.classList.remove("learn-active");
    anchor = el; pinned = pin;
    el.classList.add("learn-active");
    if (!pop.isConnected) document.body.append(pop);
    pop.hidden = false;
    pop.classList.toggle("pinned", pin);
    place(el);
    if (pin) pop.querySelector(".learn-pop-close").focus({ preventScroll: true });
  }

  function hide() {
    clearTimeout(showTimer); clearTimeout(hideTimer);
    pop.hidden = true; pinned = false;
    if (anchor) anchor.classList.remove("learn-active");
    anchor = null;
    if (opener && document.contains(opener)) opener.focus({ preventScroll: true });
    opener = null;
  }

  /* ---------- help mode ---------- */
  // Off on every page load: definitions appear only after asking for them.
  let help = false;
  const hint = document.createElement("div");
  hint.className = "learn-hint";
  hint.setAttribute("role", "status");
  hint.innerHTML = `Help is on. Hover anything to see what it means. <kbd>H</kbd> or <kbd>Esc</kbd> to turn it off.`;

  function setHelp(on) {
    help = on;
    document.documentElement.classList.toggle("learn-help", on);
    document.querySelectorAll("[data-learn-toggle]").forEach((b) => b.setAttribute("aria-pressed", String(on)));
    if (on) document.body.append(hint);
    else { hint.remove(); hide(); }
    tagFocusable(document);
  }

  /* ---------- events (delegated, so re-rendered content just works) ---------- */
  document.addEventListener("pointerover", (e) => {
    if (e.pointerType !== "mouse") return;
    if (pop.contains(e.target)) { clearTimeout(hideTimer); return; }
    if (!help) return;
    const el = e.target.closest("[data-term]");
    if (!el || pinned) return;
    clearTimeout(hideTimer); clearTimeout(showTimer);
    // Live pages redraw; skip a label that was replaced before the delay ran out.
    showTimer = setTimeout(() => ready.then(() => { if (el.isConnected) show(el, el.dataset.term, false); }), anchor ? 60 : 280);
  });
  document.addEventListener("pointerout", (e) => {
    if (e.pointerType !== "mouse" || pinned) return;
    const to = e.relatedTarget;
    if (to && (pop.contains(to) || (anchor && anchor.contains(to)))) return;
    clearTimeout(showTimer);
    hideTimer = setTimeout(() => { if (!pinned) hide(); }, 220);
  });

  // Capture phase so a tap on a touch screen can explain a button instead of pressing it.
  document.addEventListener("click", (e) => {
    if (e.target.closest(".learn-pop-close")) { hide(); return; }
    const toggle = e.target.closest("[data-learn-toggle]");
    if (toggle) { setHelp(!help); return; }
    if (pop.contains(e.target)) return;

    const el = help && e.target.closest("[data-term]");
    const touch = e.pointerType === "touch" || e.pointerType === "pen";
    if (el && (touch || !acts(el))) {
      e.preventDefault(); e.stopPropagation();
      opener = el;
      ready.then(() => show(el, el.dataset.term, true));
      return;
    }
    if (!pop.hidden && pinned) hide();
  }, true);

  document.addEventListener("keydown", (e) => {
    const tag = (document.activeElement && document.activeElement.tagName) || "";
    const typing = /INPUT|TEXTAREA|SELECT/.test(tag);
    if (e.key === "Escape" && !pop.hidden) { hide(); return; }
    if (e.key === "Escape" && help) { setHelp(false); return; }
    const plain = !typing && !e.ctrlKey && !e.metaKey && !e.altKey;
    if (plain && (e.key === "h" || e.key === "H" || e.key === "?")) { e.preventDefault(); setHelp(!help); return; }
    const el = help && e.target.closest && e.target.closest("[data-term]");
    if (el && !acts(el) && (e.key === "Enter" || e.key === " ")) {
      e.preventDefault(); opener = el;
      ready.then(() => show(el, el.dataset.term, true));
    }
  });
  addEventListener("resize", () => { if (anchor && !pop.hidden) place(anchor); });

  /* ---------- making labels reachable ---------- */
  // While Help is on, plain labels (spans, table cells, SVG text) get a tab
  // stop and a role so the keyboard can reach them. With Help off they go back
  // to being plain text. learnTab/learnRole record what this script added.
  function tagFocusable(root) {
    root.querySelectorAll("[data-term]").forEach((el) => {
      if (terms[el.dataset.term] && !el.hasAttribute("aria-label") && el.namespaceURI === "http://www.w3.org/2000/svg") {
        el.setAttribute("aria-label", terms[el.dataset.term].name);
      }
      if (acts(el)) return;
      if (help && !el.dataset.learnReady) {
        el.dataset.learnReady = "1";
        if (!el.hasAttribute("tabindex")) { el.setAttribute("tabindex", "0"); el.dataset.learnTab = "1"; }
        if (!el.hasAttribute("role")) { el.setAttribute("role", "button"); el.dataset.learnRole = "1"; }
        el.setAttribute("aria-haspopup", "dialog");
      } else if (!help && el.dataset.learnReady) {
        if (el.dataset.learnTab) el.removeAttribute("tabindex");
        if (el.dataset.learnRole) el.removeAttribute("role");
        el.removeAttribute("aria-haspopup");
        delete el.dataset.learnReady; delete el.dataset.learnTab; delete el.dataset.learnRole;
      }
    });
  }
  new MutationObserver(() => tagFocusable(document)).observe(document.documentElement, { childList: true, subtree: true });

  // autoTag gives data-term to elements whose own text matches a term alias.
  function autoTag(root) {
    return ready.then(() => {
      let n = 0;
      root.querySelectorAll("text, tspan, span, div, p, td, th, .nodeLabel, .messageText, .actor, .stateLabel").forEach((el) => {
        if (el.closest("[data-term]") || el.children.length > 1) return;
        const txt = norm(el.textContent);
        if (!txt || txt.length > 48) return;
        let id = aliasIndex.get(txt);
        if (!id) {
          const first = txt.split(/\s*[:(\[]\s*|<br>/)[0];
          id = aliasIndex.get(first);
        }
        if (id) { el.dataset.term = id; n++; }
      });
      tagFocusable(root);
      return n;
    });
  }

  // inlineSVG replaces `host`'s content with the SVG at `url`, so its labels can
  // be clicked. Each SVG's <style> and ids are scoped to it, because several
  // visuals on one page reuse the same class names (.t1, .box, ...).
  let svgCount = 0;
  async function inlineSVG(host, url) {
    const text = await (await fetch(url)).text();
    const doc = new DOMParser().parseFromString(text, "image/svg+xml");
    const svg = doc.documentElement;
    if (svg.nodeName !== "svg") throw new Error("not an SVG: " + url);
    const scope = "lsvg" + ++svgCount;
    svg.classList.add("learn-svg");
    svg.setAttribute("data-scope", scope);
    svg.removeAttribute("width");
    svg.removeAttribute("height");
    svg.querySelectorAll("[id]").forEach((el) => { el.id = scope + "-" + el.id; });
    for (const attr of ["aria-labelledby", "aria-describedby"]) {
      svg.querySelectorAll(`[${attr}]`).forEach((el) => el.setAttribute(attr, el.getAttribute(attr).split(/\s+/).map((v) => scope + "-" + v).join(" ")));
    }
    svg.querySelectorAll("[marker-end], [marker-start], [fill^='url(#'], [stroke^='url(#']").forEach((el) => {
      for (const a of ["marker-end", "marker-start", "fill", "stroke"]) {
        const v = el.getAttribute(a);
        if (v && v.startsWith("url(#")) el.setAttribute(a, v.replace("url(#", `url(#${scope}-`));
      }
    });
    const sel = `svg[data-scope="${scope}"]`;
    svg.querySelectorAll("style").forEach((st) => {
      st.textContent = st.textContent.replace(/([^{}@]+)\{([^{}]*)\}/g, (m, selectors, body) =>
        selectors.split(",").map((x) => x.trim()).filter(Boolean).map((x) => `${sel} ${x}`).join(", ") + " {" + body + "}");
    });
    host.replaceChildren(document.importNode(svg, true));
    tagFocusable(host);
    return host.firstElementChild;
  }

  // The Network KT docs, in reading order: [file, title, summary].
  const DOCS = [
    ["README.md", "Start here", "Reading path and the five ideas to remember"],
    ["01-what-is-a-card-network.md", "01 · What is a card network", "The switch, four-party vs three-party"],
    ["02-participants-and-roles.md", "02 · Participants and roles", "Issuers, acquirers, processors, PayFacs"],
    ["03-transaction-lifecycle.md", "03 · Transaction lifecycle", "Auth, clearing, settlement, funding"],
    ["04-authorization-deep-dive.md", "04 · Authorization deep dive", "ISO 8583, response codes, stand-in"],
    ["05-clearing-and-settlement.md", "05 · Clearing and settlement", "Matching, netting, a worked example"],
    ["06-economics-and-fees.md", "06 · Economics and fees", "MDR, interchange, who earns what"],
    ["07-cards-bins-and-tokens.md", "07 · Cards, BINs and tokens", "PAN anatomy, Luhn, EMV, tokenization"],
    ["08-exceptions-and-disputes.md", "08 · Exceptions and disputes", "Reversals, refunds, chargebacks"],
    ["09-risk-fraud-and-security.md", "09 · Risk, fraud and security", "PCI DSS, keys, 3-D Secure"],
    ["10-rules-governance-and-regulation.md", "10 · Rules and regulation", "Operating rules, Durbin, EU caps"],
    ["11-network-products-and-services.md", "11 · Products and services", "What a network sells"],
    ["12-design-decisions.md", "12 · Design decisions", "How simple-network should be built"],
    ["13-glossary.md", "13 · Glossary", "Every acronym in one table"],
  ];

  // lookup returns the term id for a label's text, or undefined (call after ready).
  const lookup = (text) => aliasIndex.get(norm(text)) || aliasIndex.get(norm(String(text).split(/\s*[(:]\s*/)[0]));

  window.Learn = { ready, show: (el, id) => ready.then(() => show(el, id, true)), hide, autoTag, lookup, inlineSVG, terms: () => terms, setHelp, ktHref, DOCS };
})();
