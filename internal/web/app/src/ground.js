// Search grounding (docs/design.md, Search grounding). A run grounded in
// Google Search comes with Google's Search Suggestions, HTML and CSS Google
// wrote, which its terms ask to show with the result as they are, and with
// the pages the result cites.
import { h, ic } from "./dom.js";
import { t } from "./i18n.js";
import { S } from "./state.js";

const web = (u) => { try { const x = new URL(u); return x.protocol === "https:" || x.protocol === "http:" ? x : null; } catch { return null; } };
// In Telegram a link goes through Telegram, which opens its own browser; on the site, a new tab.
function follow(e, href) {
  e.preventDefault();
  const u = web(href);
  if (!u) return;
  if (S.tg?.openLink) S.tg.openLink(u.href);
  else window.open(u.href, "_blank", "noopener,noreferrer");
}

const suggestionsOf = (j) => (j?.status === "done" ? j.grounding?.suggestions || [] : []).filter((x) => typeof x === "string" && x.trim());
const sourcesOf = (j) => (j?.status === "done" ? j.grounding?.sources || [] : []).map((s) => ({ s, u: web(s?.url) })).filter((x) => x.u);

// How much room the grounding of these jobs takes under a result, for the
// stage to keep when it sizes the result: a suggestion is one row of
// Google's chips, the sources one row of links.
export function groundRoom(js) {
  let px = 0;
  for (const j of js) {
    const n = suggestionsOf(j).length, src = sourcesOf(j).length;
    if (n || src) px += 12 + n * SUGGESTION + (n && src ? 6 : 0) + (src ? 24 : 0);
  }
  return px;
}
const SUGGESTION = 50;

// The suggestions and sources of a finished job, or null when it has none.
// label names the frame of a press the grounding belongs to.
export function grounding(j, label = "") {
  const sugg = suggestionsOf(j), src = sourcesOf(j);
  if (!sugg.length && !src.length) return null;
  return h("div", { class: "ground", "data-k": "ground-" + j.id }, label ? h("span", { class: "gnum" }, label) : null, sugg.map(frame),
    src.length ? h("p", { class: "gsrc", role: "group", "aria-label": t("ground.sources") }, ic("search"),
      src.map(({ s, u }) => h("a", { href: u.href, target: "_blank", rel: "noopener noreferrer", onclick: S.tg ? (e) => follow(e, u.href) : null },
        (s.title || "").trim() || u.hostname.replace(/^www\./, "")))) : null);
}

/* One suggestion, in a frame of framy's own origin that keeps the page's CSP,
   so no inline script or handler runs in it, and the page drops scripts,
   handlers, forms, frames and links that are not the web before the HTML goes
   in: Google's HTML reaches nothing of the page. The CSP takes no inline
   style either, so the page parses the HTML inert, takes its <style> and
   style attributes out and gives them to the frame through the CSSOM, which
   the CSP does not govern, and it shows as Google wrote it. A press on a chip
   is caught here and opens Google's results outside the frame. The frame is
   not sandboxed: WebKit runs no listener of the page on a document that may
   not run scripts, and the CSP the frame keeps lets no link of its own open a
   window, so in Safari and in Telegram on iOS and macOS a chip could not be
   pressed at all. */
function frame(html) {
  const f = h("iframe", { class: "gsug", title: t("ground.title"), referrerpolicy: "no-referrer", style: { height: SUGGESTION + "px" } });
  // Moving the frame in the page gives it a new document: it is filled again.
  const fill = () => {
    const doc = f.contentDocument, win = f.contentWindow;
    if (!doc || !win || f._doc === doc) return;
    f._doc = doc;
    // A template's content is inert: nothing in it loads or runs, and its <style> applies nowhere.
    const tpl = document.createElement("template");
    tpl.innerHTML = html;
    const src = tpl.content;
    const css = [...src.querySelectorAll("style")].map((s) => { s.remove(); return s.textContent; }).join("\n");
    const styled = [...src.querySelectorAll("[style]")].map((el, i) => { const v = el.getAttribute("style"); el.removeAttribute("style"); el.setAttribute("data-gs", i); return v; });
    // The CSP would stop most of these; out, they do not even ask it. A form would post to framy as the person.
    src.querySelectorAll("script, link, meta, base, iframe, object, embed, frame, form").forEach((el) => el.remove());
    for (const el of src.querySelectorAll("*")) for (const a of [...el.attributes]) if (/^on/i.test(a.name)) el.removeAttribute(a.name);
    for (const a of src.querySelectorAll("a[href], area[href]")) if (!web(a.getAttribute("href"))) a.removeAttribute("href");
    doc.body.replaceChildren(doc.importNode(src, true));
    for (const el of doc.querySelectorAll("[data-gs]")) { el.style.cssText = styled[+el.getAttribute("data-gs")] || ""; el.removeAttribute("data-gs"); }
    // A browser without constructable stylesheets (Safari before 16.4) shows the chips unstyled, still links.
    try {
      const own = new win.CSSStyleSheet(), google = new win.CSSStyleSheet();
      own.replaceSync(":root{color-scheme:dark}html,body{margin:0;background:transparent;overflow:hidden}");
      try { google.replaceSync(css); } catch {}
      doc.adoptedStyleSheets = [own, google];
    } catch {}
    doc.addEventListener("click", (e) => { const a = e.target.closest?.("a[href]"); if (a) follow(e, a.href); });
    const size = () => { const px = doc.documentElement.scrollHeight; if (px) f.style.height = px + "px"; };
    size();
    requestAnimationFrame(size);
  };
  f.addEventListener("load", fill);
  // An iframe without src has its empty document as soon as it is in the page.
  queueMicrotask(() => { if (f.isConnected) fill(); });
  return f;
}
