// Settings › About's Open-source licenses: THIRD_PARTY_NOTICES, which
// every build carries. app.js imports this when View is pressed
// (showNotices), so it weighs nothing on the core (#5720).
window.noticesSheet = async function () {
  let text;
  try { text = (await api("notices")).text; } catch (e) { statusErr(e); return; }
  const ed = el("div", "editor wide notices");
  const head = el("div", "ehead");
  head.append(el("b", "", t("Open-source licenses")));
  const bar = el("div", "bar");
  const ok = el("button", "text primary", t("Done"));
  ok.onclick = cancelAsk;
  bar.append(el("span", "grow"), ok);
  ed.append(head, el("pre", "notices-text", text), bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  ok.focus(NO_SCROLL);
}
