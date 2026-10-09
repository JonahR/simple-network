// Fills every <nav class="sn-nav" data-page="..."> with links to the
// participants' pages, in the order a payment flows through them.
(async () => {
  const navs = document.querySelectorAll("nav.sn-nav");
  if (!navs.length) return;
  let pages;
  try {
    pages = await (await fetch("/ui/pages")).json();
  } catch {
    return;
  }
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
  for (const nav of navs) {
    nav.setAttribute("aria-label", "Participants");
    nav.innerHTML = pages
      .map((p, i) => {
        const current = p.id === nav.dataset.page ? ' aria-current="page"' : "";
        const arrow = i ? '<span class="sn-nav-arrow" aria-hidden="true">→</span>' : "";
        return `${arrow}<a href="${esc(p.url)}"${current} title="${esc(p.role)}">${esc(p.label)}</a>`;
      })
      .join("");
  }
})();
