// The release notes' dialogs: What's new (after an update, and Settings'
// row) and the ask before an update is put in. app.js imports this when
// one opens (notesJS), so it weighs nothing on the core.
// wnRange is Settings' pick of which notes (Linka): since the last update,
// or this version and 10, 20, 50 or 100 before it
function wnRange(last) {
  const name = (v) => v ? t("This version and {n} before", { n: v }) : t("Since the last update");
  const b = el("button", "sess-pick wn-range");
  b.type = "button";
  b.setAttribute("aria-haspopup", "menu");
  b.append(el("span", "", name(last)), svg(CHEV, 11, 1.6));
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(b, ["", "10", "20", "50", "100"].map((v) => ({ v, name: name(v), note: "", literalName: true })), last, (v) => v !== last && openWhatsNew(b, v), "Release notes shown", "sess-menu", "right");
  };
  return b;
}

// auto: shown by itself after an update, with "Don't show again today"
// (#525: releases come many a day); Settings' row opens it without one.
// more (from Settings): failed, the current version's notes couldn't be
// had, retry asks again; current and url, the version and its release
// page, for a dialog with no notes.
function showWhatsNew(releases, auto, more = {}) {
  const ed = el("div", "editor whatsnew");
  const head = el("div", "ehead");
  head.append(el("b", "", t("What's new in {v}", { v: "v" + (releases[0]?.version || more.current) })));
  if (!auto) head.append(el("span", "grow"), wnRange(more.last || ""));
  ed.append(head);
  if (releases.length > 1) {
    const sec = el("section", "wn-rel");
    const wait = releases.filter((r) => r.pending);
    if (wait.length) {
      const h = el("div", "wn-ver");
      h.append(el("b", "", "v" + wait.at(-1).version + (wait.length > 1 ? " – v" + wait[0].version : "")), el("span", "badge", t("Not installed yet")));
      sec.append(h);
    }
    sec.append(mergedNotes(releases, more.total));
    ed.append(sec);
  }
  else for (const r of releases) {
    const sec = el("section", "wn-rel");
    const h = el("div", "wn-ver");
    h.append(el("b", "", "v" + r.version));
    if (r.pending) h.append(el("span", "badge", t("Not installed yet")));
    sec.append(h, noteBlocks(r.notes));
    ed.append(sec);
  }
  if (more.failed || !releases.length) {
    const sec = el("section", "wn-rel wn-none" + (more.failed ? " wn-failed" : ""));
    const h = el("div", "wn-ver");
    if (more.current) h.append(el("b", "", "v" + more.current));
    const p = el("p", "wn-msg", more.failed ? t("Couldn't load the release notes") : t("No release notes provided."));
    const acts = el("div", "wn-acts");
    if (more.failed && more.retry) {
      const again = el("button", "text", t("Try again"));
      again.onclick = async (e) => {
        e.stopPropagation();
        again.disabled = true;
        again.classList.add("busy");
        await more.retry();
      };
      acts.append(again);
    }
    if (more.url) acts.append(noteLink(t("Open the release page"), more.url));
    sec.append(h, p, acts);
    head.after(sec); // above the notes kept from before
  }
  const bar = el("div", "bar");
  const ok = el("button", "text primary", t("Close"));
  ok.onclick = cancelAsk;
  if (auto) {
    // told at once, so Escape or a click outside keeps it as well as Close
    const [quiet, box] = tick(t("Don't show again today"), false);
    quiet.classList.add("wn-quiet");
    box.onchange = () => api("whatsnew/today", { on: box.checked }).catch(statusErr);
    bar.append(quiet);
  }
  bar.append(el("span", "grow"), ok);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  ok.focus(NO_SCROLL);
}

// noteBlocks draws a release's markdown as text: headings (tHeading),
// bullets, paragraphs, inline bold, code and links, never as HTML; #123
// links the issue.
function noteBlocks(md) {
  const box = el("div", "wn-notes");
  let list = null;
  for (const line of String(md || "").split(/\r?\n/)) {
    const h = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
    const li = /^\s*[-*+]\s+(.*)$/.exec(line);
    if (h) {
      list = null;
      box.append(inlineMD(el("div", "wn-h wn-h" + Math.min(h[1].length, 4)), tHeading(h[2])));
    } else if (li) {
      if (!list) box.append(list = el("ul"));
      list.append(inlineMD(el("li"), li[1]));
    } else if (!line.trim()) {
      list = null;
    } else if (list && /^\s{2,}\S/.test(line)) {
      inlineMD(list.lastElementChild, " " + line.trim());
    } else {
      list = null;
      box.append(inlineMD(el("p"), line.trim()));
    }
  }
  return box;
}

// NOTE_ORDER: the merged notes' sections, what's new first
const NOTE_ORDER = ["Features", "Improvements", "Performance Improvements", "Bug Fixes", "Security", "Provider Changes", "Changes", "Documentation"];

// mergedNotes draws many releases' notes as one (the owner: nine releases
// of a line each read as a scattered list): their bullets gathered under
// one heading per kind, New features first, each with the version it came
// in, newest first, and a line that says the range. A line outside a list
// is a bullet of its own; a release with no notes is left out.
// total: how many releases there are in all when the server kept only the
// newest (update.MostNotes), so 156 behind doesn't read "40 releases"
// (sujingshen on X).
function mergedNotes(releases, total = 0) {
  const secs = new Map();
  for (const r of releases) {
    const of = (k) => secs.get(k) || secs.set(k, []).get(k);
    let sec = null, li = null;
    const put = (text) => {
      sec ??= of(t("Changes"));
      li = sec.some((b) => b.text === text) ? null : { text, v: r.version };
      if (li) sec.push(li);
    };
    for (const line of String(r.notes || "").split(/\r?\n/)) {
      const h = /^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$/.exec(line);
      const b = /^\s*[-*+]\s+(.*)$/.exec(line);
      if (h) {
        sec = of(tHeading(h[1]));
        li = null;
      } else if (b) put(b[1]);
      else if (!line.trim()) li = null;
      else if (li && /^\s{2,}\S/.test(line)) li.text += " " + line.trim();
      else put(line.trim());
    }
  }
  const rank = (k) => { const i = NOTE_ORDER.findIndex((o) => t(o) === k); return i < 0 ? NOTE_ORDER.length : i; };
  const box = el("div", "wn-notes wn-merged");
  const vs = releases.map((r) => r.version);
  const span = total > vs.length
    ? t("{n} releases · the newest {m} shown, {from} – {to}", { n: total, m: vs.length, from: "v" + vs.at(-1), to: "v" + vs[0] })
    : t("{n} releases · {from} – {to}", { n: vs.length, from: "v" + vs.at(-1), to: "v" + vs[0] });
  box.append(el("div", "wn-span", span));
  for (const [k, items] of [...secs].filter(([, x]) => x.length).sort((a, b) => rank(a[0]) - rank(b[0]))) {
    const h = el("div", "wn-h wn-h2");
    h.append(k, el("span", "wn-n", String(items.length)));
    const list = el("ul");
    for (const it of items) {
      const e = inlineMD(el("li"), it.text);
      e.append(el("span", "wn-v", "v" + it.v));
      list.append(e);
    }
    box.append(h, list);
  }
  return box;
}

// inlineMD appends text to e with **bold**, `code`, [links](https://…),
// bare https:// links and #123 issue links; only http(s) addresses link.
function inlineMD(e, s) {
  const re = /\*\*(.+?)\*\*|`([^`]+)`|\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)|(https?:\/\/[^\s<>)]+)|(^|[\s(\[])#(\d+)\b/g;
  let at = 0, m;
  while ((m = re.exec(s))) {
    if (m.index > at) e.append(s.slice(at, m.index));
    if (m[1] !== undefined) e.append(inlineMD(el("b"), m[1]));
    else if (m[2] !== undefined) e.append(el("code", "", m[2]));
    else if (m[3] !== undefined) e.append(noteLink(m[3], m[4]));
    else if (m[5] !== undefined) e.append(noteLink(m[5], m[5]));
    else e.append(m[6], noteLink("#" + m[7], ISSUES + m[7]));
    at = re.lastIndex;
  }
  if (at < s.length) e.append(s.slice(at));
  return e;
}

// a link in the notes opens in the browser, as the app's other links do
function noteLink(text, url) {
  const a = el("a", "wn-link", text);
  a.href = url;
  a.target = "_blank";
  a.rel = "noopener";
  a.onclick = (e) => { e.preventDefault(); e.stopPropagation(); api("open", { url }).catch(() => {}); };
  return a;
}

// confirmUpdate shows what changed before an update downloaded is put in
// (Hu9956, #844: every update came unseen, its notes three clicks away in
// Settings › About): every release's notes after this one up to it, newest
// first, with Restart to update and Later. The update's own notes show at
// once; the releases between come in after. go restarts. The tray's panel,
// with no room for a dialog, restarts as before.
async function confirmUpdate(u, go) {
  const ed = el("div", "editor whatsnew update-ask");
  const head = el("div", "ehead");
  const title = el("b", "", t("Update to {v}", { v: "v" + u.latest }));
  head.append(title);
  if (u.current) head.append(el("span", "wn-since", t("What changed since {v}", { v: "v" + u.current })));
  const box = el("div", "wn-list");
  const draw = (releases, total) => {
    box.replaceChildren();
    if (releases.length > 1) {
      const sec = el("section", "wn-rel");
      sec.append(mergedNotes(releases, total));
      box.append(sec);
    }
    else for (const r of releases) {
      const sec = el("section", "wn-rel");
      const h = el("div", "wn-ver");
      h.append(el("b", "", "v" + r.version));
      sec.append(h, noteBlocks(r.notes));
      box.append(sec);
    }
    if (!releases.length) {
      const sec = el("section", "wn-rel wn-none");
      sec.append(el("p", "wn-msg", t("No release notes provided.")));
      if (u.url) sec.append(noteLink(t("Open the release page"), u.url));
      box.append(sec);
    }
  };
  draw(u.notes ? [{ version: u.latest, notes: u.notes }] : []);
  const bar = el("div", "bar");
  const later = el("button", "text", t("Later"));
  later.onclick = cancelAsk;
  const ok = el("button", "text primary", t("Restart to update"));
  ok.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); go(); };
  bar.append(el("span", "grow"), later, ok);
  ed.append(head, box, bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  ok.focus(NO_SCROLL);
  // the notes' answer asks the feed again first, and names a release out
  // since the one downloaded (#910), which the restart then waits for
  const w = await api(updatePath("update/notes")).catch(() => null);
  if (confirmAsk !== ed) return;
  if (w?.latest && w.latest !== u.latest) title.textContent = t("Update to {v}", { v: "v" + w.latest });
  if (w?.releases?.length) draw(w.releases, w.total);
}

window.wnShow = showWhatsNew;
window.wnConfirm = confirmUpdate;
