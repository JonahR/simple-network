// hub.js: the /learn/ page. Inlines the visuals, lists the docs, and renders the glossary.
(function () {
  "use strict";

  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

  document.getElementById("doc-grid").innerHTML = Learn.DOCS.map(([file, title, sub]) =>
    `<a class="doc-card" href="doc.html?doc=${encodeURIComponent(file)}"><b>${esc(title)}</b><span>${esc(sub)}</span></a>`).join("");

  document.querySelectorAll("figure[data-svg]").forEach((fig) => {
    Learn.inlineSVG(fig, fig.dataset.svg).catch((err) => {
      fig.innerHTML = `<p class="err">Couldn't load ${esc(fig.dataset.svg)}: ${esc(err.message)}</p>`;
    });
  });

  Learn.ready.then(() => {
    const terms = Learn.terms();
    const ids = Object.keys(terms).sort((a, b) => terms[a].name.localeCompare(terms[b].name));
    document.getElementById("term-count").textContent = ids.length;
    const list = document.getElementById("glossary-list");
    list.innerHTML = ids.map((id) => {
      const t = terms[id];
      const search = [t.name, ...(t.aliases || []), t.def].join(" ").toLowerCase();
      return `<div class="entry" id="term-${esc(id)}" data-search="${esc(search)}">
        <dt><span data-term="${esc(id)}">${esc(t.name)}</span></dt>
        <dd>${esc(t.def)}</dd></div>`;
    }).join("");

    const filter = document.getElementById("term-filter");
    filter.addEventListener("input", () => {
      const q = filter.value.trim().toLowerCase();
      list.querySelectorAll(".entry").forEach((e) => { e.hidden = q && !e.dataset.search.includes(q); });
    });
  });
})();
