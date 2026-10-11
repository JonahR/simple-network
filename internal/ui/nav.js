// Fills every <nav class="sn-nav" data-page="..."> with links to the
// participants' pages, in the order a payment flows through them.
//
// The links also carry the simulation's shared context: the customer at the
// counter (?customer=). Each page is its own origin, so it can't share browser
// storage; the context travels in the links instead. Pick John Smith on the
// Cardholder page, follow the nav to the POS, and the POS rings up for him.
// Pages read it with snNav.get("customer") and change it with snNav.set.
(() => {
  const CARRY = ["customer"];
  const ctx = {};
  const here = new URLSearchParams(location.search);
  for (const k of CARRY) if (here.get(k)) ctx[k] = here.get(k);

  // link returns url with the shared context in its query string.
  const link = (url) => {
    try {
      const u = new URL(url, location.href);
      for (const k of CARRY) {
        if (ctx[k]) u.searchParams.set(k, ctx[k]);
        else u.searchParams.delete(k);
      }
      return u.toString();
    } catch {
      return url;
    }
  };
  const relink = () => document.querySelectorAll("nav.sn-nav a[data-href]").forEach((a) => (a.href = link(a.dataset.href)));

  window.snNav = {
    get: (k) => ctx[k] || "",
    // set changes the context for every link, and this page's own address,
    // so a reload or a copied link keeps it.
    set(k, v) {
      if (!CARRY.includes(k)) return;
      if (v) ctx[k] = v;
      else delete ctx[k];
      relink();
      const u = new URL(location.href);
      if (v) u.searchParams.set(k, v);
      else u.searchParams.delete(k);
      if (u.toString() !== location.href) history.replaceState(history.state, "", u);
    },
    link,
  };

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
      // In Help mode, the bar as a whole (its arrows) explains the payment flow.
      nav.dataset.termScope = "payment-flows";
      nav.innerHTML = pages
        .map((p, i) => {
          const current = p.id === nav.dataset.page ? ' aria-current="page"' : "";
          // Pages outside the payment flow (the Risk lab) follow a divider, not an arrow.
          const arrow = !i ? "" : p.aside ? '<span class="sn-nav-sep" aria-hidden="true"></span>' : '<span class="sn-nav-arrow" aria-hidden="true">→</span>';
          // data-term lets the learning layer's Help mode explain each participant.
          return `${arrow}<a href="${esc(link(p.url))}" data-href="${esc(p.url)}"${current} data-term="${esc(p.term)}" aria-description="${esc(p.role)}">${esc(p.label)}</a>`;
        })
        .join("");
    }
    window.dispatchEvent(new Event("sn-nav-ready"));
  })();
})();
