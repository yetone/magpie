// A relay added by hand with no Balance URL has its balance found by
// magpie where relays tell a key it (provider/balance_probe.go). Under the
// Balance URL the editor says where, with Use to copy it into the fields,
// so an amount read at the wrong query can be put right. app.js imports
// this when the editor draws a relay's balance with no Balance URL.
let balFoundAt = null;
window.balFound = function (box, p, draft, bal, pathOf) {
  const line = el("div", "bal-found");
  line.hidden = true;
  balFoundAt = { line, draft, bal, pathOf };
  box.append(line);
  bal.addEventListener("input", () => { line.hidden = !line.dataset.url || !!bal.value.trim(); });
  window.balFoundShow({ found: p.balanceFound, foundField: p.balanceFoundField });
};
// what the editor's Check balance found (app.js), drawn again
window.balFoundShow = function (r) {
  const s = balFoundAt;
  if (!s || !s.line.isConnected) return;
  const { line, draft, bal, pathOf } = s;
  line.replaceChildren();
  line.dataset.url = r.found || "";
  line.hidden = !r.found || !!(draft.balanceURL || "").trim();
  if (!r.found) return;
  const txt = el("span", "", t("Found by magpie: {url}", { url: r.found + (r.foundField ? " " + r.foundField : "") }));
  txt.title = t("magpie asked this relay where relays tell a key its balance, and reads it here. If the amount is wrong, put in your own Balance URL and field; magpie looks again each day");
  const use = el("button", "text action", t("Use"));
  use.type = "button";
  use.title = t("Put this query in the Balance URL and Balance field, to change it");
  use.onclick = () => {
    draft.balanceURL = bal.value = r.found;
    draft.balancePath = r.foundField || "";
    const f = pathOf();
    if (f) { f.value = draft.balancePath; f.placeholder = balanceFieldOf(r.found); }
    line.hidden = true;
    bal.focus();
  };
  line.append(txt, use);
};
