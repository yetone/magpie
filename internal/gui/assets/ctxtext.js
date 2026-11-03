// The text of a part of the context window card (context.js): what the
// request sent there, opened from the card. context.js imports this when a
// part is first opened, so it weighs nothing on the core. A request whose
// body has left memory is read from the request archive, when it kept one,
// and the reply beside it (wangbinyq on Discord).
const TEXT_STEP = 20000, ITEM_STEP = 100;
const ROLE_NAMES = {
  system: "System prompt", user: "You", assistant: "Assistant", thinking: "Thinking",
  call: "Tool call", result: "Tool result", tool: "Tool definition", image: "Image", sealed: "Sealed",
};
// read: /api/context/<what>; a 404 is {archive: why}, why it is gone
async function read(what, q) {
  // fetched, not api(): the text isn't looked through for accounts
  const res = await fetch("/api/context/" + what + "?" + new URLSearchParams(q));
  if (res.status === 404) return { gone: (await res.json().catch(() => ({}))).archive || "off" };
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res.json();
}
const GONE = {
  off: "This request's text is no longer kept: magpie keeps the text of the last {n} requests, in memory only, while it runs.",
  missing: "Not in the request archive: it is saved just after the call, the oldest are removed past the archive's size, or the save failed (magpie's log says why)",
  cut: "The request archive kept only the start of this request, so it can't be read",
  unread: "The request archive's copy couldn't be read or masked, so none of it is shown",
};
const gone = (why, n) => el("p", "ctx-view-gone", t(GONE[why] || GONE.off, { n }));
// a stretch of text, and a button for the next
function longText(text) {
  const box = el("div", "ctx-text-box");
  const pre = el("pre", "ctx-text");
  let at = 0;
  const more = el("button", "text ctx-text-more");
  more.type = "button";
  const step = () => {
    pre.append(document.createTextNode(text.slice(at, at + TEXT_STEP)));
    at = Math.min(text.length, at + TEXT_STEP);
    more.hidden = at >= text.length;
    more.textContent = t("Show more · {n} characters left", { n: (text.length - at).toLocaleString(intlLang()) });
  };
  more.onclick = (e) => { e.stopPropagation(); step(); };
  step();
  box.append(pre, more);
  return box;
}
// picture: an image the request carried, as it sent it (a data: URL
// the gateway checked is a PNG, JPEG, GIF or WebP). It fades in once
// decoded; clicked, it opens to its own size and back
function picture(src) {
  const fig = el("button", "ctx-pic");
  fig.type = "button";
  fig.setAttribute("aria-expanded", "false");
  const img = el("img");
  img.alt = t("Image");
  img.decoding = "async";
  img.onload = () => fig.classList.add("in");
  img.onerror = () => fig.replaceWith(el("div", "ctx-piece-note", t("An image: magpie doesn't show it here")));
  img.src = src;
  fig.append(img);
  fig.onclick = (e) => {
    e.stopPropagation();
    const first = fig.getBoundingClientRect();
    const open = fig.classList.toggle("open");
    fig.setAttribute("aria-expanded", String(open));
    if (calm() || !fig.animate) return;
    const last = fig.getBoundingClientRect();
    if (!first.width || !last.width) return;
    fig.animate([{ transform: `scale(${first.width / last.width}, ${first.height / last.height})` }, { transform: "none" }],
      { duration: 460, easing: IOS_EASE });
  };
  return fig;
}
function pieceView(pc) {
  const box = el("div", "ctx-piece");
  const head = el("div", "ctx-piece-head");
  if (pc.role) head.append(el("span", "ctx-role r-" + pc.role, t(ROLE_NAMES[pc.role] || pc.role)));
  if (pc.name) head.append(el("code", "ctx-piece-name", pc.name));
  head.append(el("span", "grow"));
  if (pc.text) head.append(copyBtn(pc.text, pc.name || t(ROLE_NAMES[pc.role] || "Text")));
  box.append(head);
  if (pc.role === "image" && pc.src) box.append(picture(pc.src));
  else if (pc.role === "image") box.append(el("div", "ctx-piece-note", t("An image: magpie doesn't show it here")));
  else if (pc.role === "sealed") box.append(el("div", "ctx-piece-note", t("Sealed by the vendor, {n} bytes: only the vendor can read it", { n: (pc.size || 0).toLocaleString(intlLang()) })));
  else {
    if (pc.size) box.append(el("div", "ctx-piece-note", t("Sealed by the vendor, {n} bytes: only the vendor can read it", { n: pc.size.toLocaleString(intlLang()) })));
    if (pc.text) box.append(longText(pc.text));
    else if (!pc.size) box.append(el("div", "ctx-piece-note", t("Empty")));
  }
  return box;
}
export async function open(kit, route, kind, pick) {
  const { fmtK, partName, itemName, itemLabel, tagOf } = kit;
  const id = route?.id;
  if (!id) return;
  // the day the history keeps it under, for one the gateway no longer holds
  const day = String(route.time || "").slice(0, 10);
  const readText = (kind, item) => read("text", { id, kind, day, ...(item === undefined ? {} : { item }) });
  const ed = el("div", "editor ctx-view wide");
  const head = el("div", "ehead");
  const title = el("b", "", partName(kind));
  const total = el("span", "ctx-view-total");
  head.append(el("i", "dot k-" + kind), title, el("span", "grow"), total);
  const body = el("div", "ctx-view-body");
  body.append(el("div", "ctx-view-wait", t("Reading…")));
  const bar = el("div", "bar");
  const close = el("button", "text primary", t("Close"));
  close.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); };
  // the reply beside it: what the model answered, from memory or the archive
  const reply = el("button", "text", t("Reply"));
  reply.type = "button";
  let view = null;
  reply.onclick = async (e) => {
    e.stopPropagation();
    if (reply.classList.toggle("on")) {
      reply.textContent = partName(kind);
      title.textContent = t("Reply");
      total.textContent = "";
      body.replaceChildren(el("div", "ctx-view-wait", t("Reading…")));
      let r;
      try { r = await read("reply", { id, day }); } catch (err) { r = { err }; }
      if (confirmAsk !== ed || !reply.classList.contains("on")) return;
      if (r.err) return body.replaceChildren(el("p", "ctx-view-gone", r.err.message));
      if (r.gone) return body.replaceChildren(gone(r.gone, 10));
      const note = el("p", "ctx-view-note", t(r.from === "archive" ? "The reply as the agent got it, read from the request archive, masked where masking is on." : "The reply as the agent got it, masked where masking is on."));
      body.replaceChildren(note, ...r.pieces.map(pieceView));
      if (r.cut) body.append(el("p", "ctx-view-note", t("Only the start of the reply was kept")));
    } else {
      reply.textContent = t("Reply");
      title.textContent = partName(kind);
      if (view) view(); else body.replaceChildren(el("div", "ctx-view-wait", t("Reading…")));
    }
  };
  bar.append(reply, el("span", "grow"), close);
  ed.append(head, body, bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  let list;
  const failed = (err) => { view = () => body.replaceChildren(el("p", "ctx-view-gone", err.message)); };
  try { list = await readText(kind); } catch (err) { failed(err); }
  if (confirmAsk !== ed) return;
  if (!list) return reply.classList.contains("on") || view();
  if (list.gone) {
    view = () => body.replaceChildren(gone(list.gone, 10));
    return reply.classList.contains("on") || view();
  }
  const items = list.items || [];
  const sum = () => fmtK(items.reduce((s, it) => s + it.tokens, 0)) + " " + t("tokens");
  const note = el("p", "ctx-view-note", list.from === "archive"
    ? t("As magpie sent it, read from the request archive, masked where masking is on.")
    : t("As magpie sent it, masked where masking is on. Kept in memory for the last {n} requests only.", { n: list.kept }));
  // an item: its text alone; "N more" or the earlier turns: the part's list
  const at = pick && pick.tag !== "more" && pick.tag !== "turns"
    ? items.findIndex((it) => it.name === pick.name && (it.tag || "") === (pick.tag || "")) : -1;
  const entry = (it, i, open) => {
    const box = el("div", "ctx-vi");
    const h = el("button", "ctx-vi-head");
    h.type = "button";
    h.setAttribute("aria-expanded", "false");
    const tg = tagOf(it);
    h.append(el("span", "tw"), itemLabel(it, kind));
    h.querySelector(".tw").append(svg(CHEV, 10, 1.7));
    if (it.n > 1) h.append(el("span", "ctx-x", "×" + it.n));
    if (tg) h.append(el("code", "ctx-tag", tg));
    h.append(el("span", "ctx-v", fmtK(it.tokens)));
    const text = el("div", "ctx-vi-text");
    text.hidden = true;
    let read = null;
    const show = async () => {
      const on = text.hidden;
      text.hidden = !on;
      h.setAttribute("aria-expanded", String(on));
      box.classList.toggle("open", on);
      if (!on || read) return;
      text.replaceChildren(el("div", "ctx-view-wait", t("Reading…")));
      read = readText(kind, i).then((one) => {
        const pieces = one?.items?.[i]?.pieces;
        if (!pieces) { read = null; text.replaceChildren(gone(one?.gone, list.kept)); return; }
        text.replaceChildren(...(pieces.length ? pieces.map(pieceView) : [el("div", "ctx-piece-note", t("Empty"))]));
      }).catch((err) => { read = null; text.replaceChildren(el("p", "ctx-view-gone", err.message)); });
    };
    h.onclick = (e) => { e.stopPropagation(); show(); };
    box.append(h, text);
    if (open) show();
    return box;
  };
  const showAll = () => {
    view = showAll;
    title.textContent = partName(kind);
    total.textContent = sum();
    const rows = el("div", "ctx-vi-list");
    let shown = 0;
    const more = el("button", "text ctx-text-more");
    more.type = "button";
    const step = () => {
      const next = items.slice(shown, shown + ITEM_STEP).map((it, j) => entry(it, shown + j, items.length === 1));
      rows.append(...next);
      shown += next.length;
      more.hidden = shown >= items.length;
      more.textContent = t("Show {n} more", { n: items.length - shown });
    };
    more.onclick = (e) => { e.stopPropagation(); step(); };
    step();
    body.replaceChildren(note, items.length ? rows : el("div", "ctx-empty", t("Nothing in it")), more);
  };
  if (at < 0) view = showAll;
  else {
    // the item alone, the part's other items a click away
    const all = el("button", "text ctx-view-all", t("All of {part} · {n}", { part: partName(kind), n: items.length }));
    all.type = "button";
    all.onclick = (e) => {
      e.stopPropagation();
      all.remove();
      if (reply.classList.contains("on")) reply.click();
      showAll();
    };
    if (items.length > 1) bar.prepend(all);
    view = () => {
      title.textContent = partName(kind) + " · " + itemName(items[at]);
      total.textContent = fmtK(items[at].tokens) + " " + t("tokens");
      body.replaceChildren(note, entry(items[at], at, true));
    };
  }
  if (!reply.classList.contains("on")) view();
}
