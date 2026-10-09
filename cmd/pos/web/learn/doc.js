// doc.js: renders a Network KT markdown doc (doc.html?doc=<file>#<heading>).
// Doc links stay in the viewer, SVG visuals are inlined so their labels are
// clickable, and Mermaid diagrams are drawn and tagged with glossary terms.
(function () {
  "use strict";

  const GITHUB = "https://github.com/JonahR/simple-network/blob/main/";
  const MERMAID = "https://cdnjs.cloudflare.com/ajax/libs/mermaid/10.9.1/mermaid.min.js";
  const body = document.getElementById("doc-body");
  const params = new URLSearchParams(location.search);
  const file = params.get("doc") || "README.md";
  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

  // Only files from the docs list can be opened.
  const known = Learn.DOCS.map((d) => d[0]);
  const index = known.indexOf(file);

  document.getElementById("doc-nav").innerHTML = Learn.DOCS.map(([f, title]) =>
    `<a href="doc.html?doc=${encodeURIComponent(f)}"${f === file ? ' aria-current="page"' : ""}>${esc(title)}</a>`).join("");
  const prev = Learn.DOCS[index - 1], next = Learn.DOCS[index + 1];
  document.getElementById("doc-pager").innerHTML =
    (prev ? `<a href="doc.html?doc=${encodeURIComponent(prev[0])}">← ${esc(prev[1])}</a>` : "<span></span>") +
    (next ? `<a href="doc.html?doc=${encodeURIComponent(next[0])}">${esc(next[1])} →</a>` : "<span></span>");

  // GitHub-style heading ids, so links like 07-...md#luhn-algorithm land in the same place.
  function slugger() {
    const seen = new Map();
    return (text) => {
      let s = text.trim().toLowerCase().replace(/<[^>]+>/g, "").replace(/[^\p{L}\p{N}\- _]/gu, "").replace(/ /g, "-");
      const n = seen.get(s) || 0;
      seen.set(s, n + 1);
      return n ? `${s}-${n}` : s;
    };
  }

  function rewriteLinks(root) {
    root.querySelectorAll("a[href]").forEach((a) => {
      const href = a.getAttribute("href");
      if (/^(https?:|mailto:|#)/.test(href)) {
        if (/^https?:/.test(href)) { a.target = "_blank"; a.rel = "noopener"; }
        return;
      }
      const [path, hash] = href.split("#");
      const clean = decodeURIComponent(path);
      if (clean.startsWith("../")) { a.href = GITHUB + clean.slice(3) + (hash ? "#" + hash : ""); a.target = "_blank"; a.rel = "noopener"; return; }
      if (known.includes(clean)) { a.href = `doc.html?doc=${encodeURIComponent(clean)}${hash ? "#" + hash : ""}`; return; }
      a.href = "kt/" + clean + (hash ? "#" + hash : "");
    });
  }

  async function inlineVisuals(root) {
    const imgs = [...root.querySelectorAll("img[src$='.svg']")].filter((img) => !/^https?:/.test(img.getAttribute("src")));
    await Promise.all(imgs.map(async (img) => {
      const fig = document.createElement("figure");
      fig.className = "doc-visual";
      img.replaceWith(fig);
      try { await Learn.inlineSVG(fig, decodeURIComponent(img.getAttribute("src"))); }
      catch (err) { fig.replaceChildren(img); }
    }));
  }

  function loadScript(src) {
    return new Promise((ok, fail) => {
      const s = document.createElement("script");
      s.src = src; s.onload = ok; s.onerror = () => fail(new Error("could not load " + src));
      document.head.append(s);
    });
  }

  async function drawDiagrams(root) {
    const blocks = [...root.querySelectorAll("pre > code.language-mermaid")];
    if (!blocks.length) return;
    const nodes = blocks.map((code) => {
      const div = document.createElement("div");
      div.className = "mermaid";
      div.textContent = code.textContent;
      code.parentElement.replaceWith(div);
      return div;
    });
    try {
      await loadScript(MERMAID);
      const dark = matchMedia("(prefers-color-scheme: dark)").matches;
      window.mermaid.initialize({ startOnLoad: false, theme: dark ? "dark" : "default", securityLevel: "strict" });
      await window.mermaid.run({ nodes });
      await Promise.all(nodes.map((n) => Learn.autoTag(n)));
    } catch (err) {
      console.warn("Mermaid diagrams not drawn:", err);
      nodes.forEach((n) => { n.classList.add("mermaid-raw"); n.innerHTML = `<pre>${esc(n.textContent)}</pre>`; });
    }
  }

  async function render() {
    if (index < 0) {
      body.innerHTML = `<h1>Doc not found</h1><p>${esc(file)} isn't one of the Network KT docs. <a href="/learn/#docs">See the list</a>.</p>`;
      return;
    }
    const res = await fetch("kt/" + encodeURIComponent(file));
    if (!res.ok) { body.innerHTML = `<h1>Couldn't load ${esc(file)}</h1><p>HTTP ${res.status}</p>`; return; }
    const md = await res.text();

    const slug = slugger();
    const renderer = new marked.Renderer();
    renderer.heading = (text, level, raw) => `<h${level} id="${slug(raw)}">${text}</h${level}>`;
    const html = marked.parse(md, { renderer, gfm: true });
    // Build in a <template> (which loads nothing), point relative images at kt/, then attach.
    const tpl = document.createElement("template");
    tpl.innerHTML = DOMPurify.sanitize(html, { ADD_ATTR: ["target"] });
    tpl.content.querySelectorAll("img[src]").forEach((img) => {
      const src = img.getAttribute("src");
      if (!/^(https?:|data:|\/)/.test(src)) img.setAttribute("src", "kt/" + src);
    });
    body.replaceChildren(tpl.content);
    document.title = (body.querySelector("h1")?.textContent || file) + " · Network KT";

    rewriteLinks(body);
    await inlineVisuals(body);
    await drawDiagrams(body);
    if (location.hash) document.getElementById(decodeURIComponent(location.hash.slice(1)))?.scrollIntoView();
  }

  render().catch((err) => {
    body.innerHTML = `<h1>Couldn't render ${esc(file)}</h1><p>${esc(err.message)}</p><p>The Markdown renderer loads from cdnjs, so this page needs an internet connection.</p>`;
  });
})();
