// Settings' Check in now for one account (xiaowu_95660: 手动签到一次):
// its row, the press and what it came to. app.js imports this when
// Settings draws a vendor's check-in (checkinNowRow), so it weighs nothing
// on the core.

// what each vendor's last press said, kept over Settings' redraws
const checkinNowSaid = {};

// ciNowFill draws Check in now into row, for vendor by's accounts: one
// account checked in once now, picked from the app menu when there are
// several, its switch and the account's left as they are; what came of it
// is said in the row.
window.ciNowFill = function (row, s, by) {
  const accts = (s.checkinAccounts || []).filter((a) => (a.by || "") === by);
  row.dataset.by = by;
  const sub = el("div", "sub");
  const who = el("div", "who");
  who.append(el("div", "name", t("Check in now")), sub);
  const box = el("div");
  row.append(who, box);
  const draw = () => {
    const said = checkinNowSaid[by];
    sub.textContent = said ? said.text : t("Checks one account in once now; the daily check-in's switches stay as they are");
    sub.classList.toggle("err", !!said?.bad);
    sub.classList.toggle("say", !!said); // what came of it: a state, kept in sight
    const b = el("button", "text ci-now-go", t(accts.length > 1 ? "Check in…" : "Check in"));
    b.type = "button";
    b.disabled = !!said?.busy;
    b.classList.toggle("busy", !!said?.busy);
    if (accts.length > 1) b.setAttribute("aria-haspopup", "menu");
    b.onclick = (e) => {
      e.stopPropagation();
      if (accts.length === 1) return send(accts[0]);
      if (b.classList.contains("open")) return closeProtoMenu();
      openProtoMenu(b, accts.map((a) => ({ v: a.key, name: a.user || a.key, note: "", literalName: true })), null,
        (k) => send(accts.find((a) => a.key === k)), "Check in which account", "sess-menu", "right");
    };
    box.replaceChildren(b);
  };
  const send = (a) => ciNowSend(a, by, (o) => { checkinNowSaid[by] = o; if (row.isConnected) draw(); });
  draw();
  row.hidden = false;
};

// ciNowSend checks account a of vendor by in once now and says, through
// say, how it went; no switch is changed.
window.ciNowSend = async function (a, by, say) {
  if (!a) return;
  const name = a.user || a.key;
  say({ busy: true, text: t("Checking in {who}…", { who: name }) });
  try {
    const r = await api("usage/checkin-account", { by, key: a.key });
    const said = checkinNowLine(r, name);
    say(said);
    status(said.text, said.bad ? "err" : "ok");
  } catch (err) {
    const text = t("Couldn't check in {who}: {why}", { who: name, why: t(err.message) });
    say({ bad: true, text });
    status(text, "err");
  }
  // the Usage cards' lines read again; Settings isn't drawn again under
  // the button just pressed, which a redraw moved at 420px
  loadQuotas();
};

// checkinNowLine is what one account's press came to: checked in now,
// already in today, not eligible or no event, its own app's, or failed,
// with the vendor's reason.
function checkinNowLine(r, who) {
  const why = r?.msg ? " · " + r.msg : "";
  switch (r?.outcome) {
    case "claimed":
    case "done":
      if (r.again) return { text: t("{user}: {name} says already checked in; magpie asks again later", { user: who, name: checkinVendorName(r) }) };
      if (r.outcome === "claimed" && r.asked) return { text: t("Checked in {who}", { who }) + (r.credit ? " +" + r.credit : "") + (r.streak ? " · " + t("{n}-day streak", { n: r.streak }) : "") };
      return { text: t("{who} was already checked in today", { who }) };
    case "ineligible":
      return { text: t("{user} is not eligible", { user: who }) + why };
    case "inactive":
      return { text: t("{user}: no check-in event now", { user: who }) + why };
    case "captcha":
      return { text: t("{user} is asked for a captcha; check in in the app", { user: who }) };
    case "own-app":
      return { text: r.by === "trae"
        ? t("{user}: Trae CN refused this check-in (9074: too many taking part); check in in the Trae CN app", { user: who })
        : t("{user}: {name} only gives check-in credits to its own app; check in in the {name} app", { user: who, name: checkinVendorName(r) }) };
    default:
      return { bad: true, text: t("Couldn't check in {who}: {why}", { who, why: r?.msg || t("Check-in failed") }) };
  }
}
