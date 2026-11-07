// Settings › Sync's Back up by itself (jwaitq on Discord: 定期自动备份，
// 支持回退选择版本): the setup backed up into a folder on this computer on a
// schedule, the last few kept, and any of them put back, what is here being
// backed up first so that can be undone. app.js imports this when Settings
// draws Sync, so it weighs nothing on the core.

// the server's view, kept over Settings' redraws and read again when it is
// half a minute old; the read under way, which a redraw while it is out
// draws with too; what the last restore came to, said once under the
// versions
let autoBackupView = null;
let autoBackupAt = 0;
let autoBackupLoad = null;
let autoBackupSaid = null;
// the passphrase asked for as the sign-ins are turned on, when none is set
let autoBackupPhrase = false;
// the versions listed under Versions (chen2652 on Discord: a long list,
// folded to its count and newest until clicked), kept over redraws
let autoBackupOpen = false, autoBackupRefocus = false;

// autoBackupFill puts the rows in place of at, the empty element renderSync
// leaves at the end of Sync's list: rows, so the list's lines run between
// them as between Sync's own.
window.autoBackupFill = function (at) {
  let drawn = [at];
  const draw = () => {
    if (!drawn[0].isConnected) return;
    const rows = autoBackupRows(autoBackupView);
    drawn[0].before(...rows);
    for (const r of drawn) r.remove();
    drawn = rows;
  };
  if (autoBackupView) draw();
  if (Date.now() - autoBackupAt >= 30000) {
    autoBackupAt = Date.now();
    const load = api("autobackup").then((v) => { autoBackupView = v; }, (e) => { autoBackupView = { error: e.message, backups: [] }; });
    autoBackupLoad = load;
    load.then(() => { if (autoBackupLoad === load) autoBackupLoad = null; });
  }
  autoBackupLoad?.then(draw);
};

// autoBackupDo posts one action and draws Sync again with what it says
async function autoBackupDo(action, body, b) {
  b?.classList.add("busy");
  try {
    autoBackupView = await api("autobackup/" + action, body || {});
    autoBackupAt = Date.now();
  } catch (e) {
    statusErr(e);
  }
  renderSync();
}

function autoBackupRows(v) {
  const rows = [];
  const row = (name, sub, ...tools) => {
    const r = el("div", "row pref auto-backup");
    const who = el("div", "who");
    const s = el("div", "sub", sub);
    who.append(el("div", "name", name), s);
    const val = el("div", "val");
    val.append(...tools);
    r.append(who, val);
    rows.push(r);
    return r;
  };
  const btn = (label, fn) => { const b = el("button", "text", label); b.type = "button"; b.onclick = (e) => fn(b, e); return b; };
  const on = !!v.on;
  const head = row(t("Back up by itself"), v.error ? t("Couldn't back up: {error}", { error: v.error })
    : on ? (v.signIns ? t("Your providers, settings and profiles, without API keys, and the sign-ins sealed with a passphrase, into a folder on this computer")
      : t("Your providers, settings and profiles, without API keys or sign-ins, into a folder on this computer")) : t("Off: no backups are made by themselves"),
  segs([[0, t("Off")], [1, t("1 hour")], [6, t("6 hours")], [24, t("1 day")], [168, t("1 week")]], on ? v.hours : 0,
    (h) => autoBackupDo("set", h ? { on: true, hours: h } : { on: false })));
  if (v.error) head.querySelector(".sub").classList.add("bad", "wraps");
  if (on) {
    row(t("Versions kept"), t("The oldest is removed when a new one is made; one is made only when something changed"),
      segs([3, 7, 14, 30].map((n) => [n, String(n)]), v.keep, (n) => autoBackupDo("set", { keep: n })));
    // the sign-ins (qianxunji, curtinp118 on Discord): off unless picked,
    // and sealed with a passphrase asked for the first time
    const asking = autoBackupPhrase && !v.signIns;
    row(t("Sign-ins in backups"), v.signIns ? t("Subscription accounts and plugin sign-ins, sealed with the passphrase set here")
      : t("Off: after a restore on another computer, sign in again"),
    segs([["off", t("Off")], ["on", t("On")]], v.signIns || asking ? "on" : "off", (x) => {
      if (x === "on" && !v.passphrase) { autoBackupPhrase = true; renderSync(); return; }
      autoBackupPhrase = false;
      autoBackupDo("set", { signIns: x === "on" });
    }));
    if (asking) rows.push(autoBackupPhraseForm());
  }
  if (on || v.backups?.length) {
    const tools = [btn(t("Change…"), (b) => autoBackupDo("choose", {}, b)), btn(t("Show"), () => api("autobackup/reveal", {}).catch(statusErr))];
    if (v.custom) tools.push(btn(t("Default"), (b) => autoBackupDo("set", { dir: "" }, b)));
    const dir = row(t("Backup folder"), v.dir || "", ...tools).querySelector(".sub");
    dir.classList.add("wraps", "auto-backup-dir");
    // in magpie's own folder, the default: a reinstalled system takes the
    // backups with it (two_2025. on Discord, magpie on C:)
    if (v.inside) dir.after(el("div", "sub wraps auto-backup-inside", t("In magpie's own folder: a reinstalled system or a lost disk takes these backups too. Pick a folder on another disk, or one a cloud drive syncs")));
  }
  const list = v.backups || [];
  const vrow = row(t("Versions"), list.length ? t("{n} backups · newest {when}", { n: list.length, when: syncWhen(list[0].made) }) : t("None yet"),
    btn(t("Back up now"), (b) => autoBackupDo("now", {}, b)));
  // how many and the newest is a state, not a hint: it stays in sight
  const sub = vrow.querySelector(".sub");
  sub.classList.add("say");
  if (list.length) {
    // the name and the line under it fold the versions away: a chevron
    // after the name, turned down while they show
    const name = vrow.querySelector(".name"), who = vrow.querySelector(".who");
    const fold = el("button", "auto-backup-fold", name.textContent);
    fold.type = "button";
    fold.setAttribute("aria-expanded", String(autoBackupOpen));
    fold.append(el("span", "chev", svg(CHEV_R, 10, 1.6)));
    name.replaceChildren(fold);
    who.classList.add("auto-backup-folds");
    // the keyboard's focus stays on it over the redraw
    if (autoBackupRefocus) { autoBackupRefocus = false; requestAnimationFrame(() => fold.isConnected && fold.focus(NO_SCROLL)); }
    who.onclick = () => {
      autoBackupOpen = !autoBackupOpen;
      autoBackupRefocus = document.activeElement === fold;
      renderSync();
    };
  }
  if (autoBackupSaid) {
    sub.after(...autoBackupSaid.map((l) => el("div", "sub wraps auto-backup-said", l)));
    autoBackupSaid = null;
  }
  for (const b of autoBackupOpen ? list : []) {
    const when = autoBackupWhen(b.made);
    const what = [t(b.providers === 1 ? "1 provider" : "{n} providers", { n: b.providers })];
    if (b.reason === "restore") what.unshift(t("What was here before a restore"));
    if (b.signIns) what.push(t("sign-ins"));
    row(when, what.join(" · "), btn(t("Restore…"), () => autoBackupAsk(b, when))).classList.add("auto-backup-version");
  }
  return rows;
}

// autoBackupWhen names a version by when it was made, as its row does
const autoBackupWhen = (made) => new Date(made).toLocaleString(intlLang(), { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });

// autoBackupAsk asks before a version is put back, in the app's own
// dialog, saying what changes and that what is here is backed up first.
function autoBackupAsk(b, when) {
  const ed = el("div", "editor restore-ask auto-backup-ask");
  const head = el("div", "ehead");
  head.append(el("b", "", t("Restore the backup of {when}?", { when })));
  ed.append(head);
  ed.append(el("p", "lib-confirm", t("Your providers, settings, profiles and agents' models become this backup's. Providers added since are kept, and each keeps its API keys here.")));
  if (b.signIns) ed.append(el("p", "lib-confirm", t("Its sign-ins are put in where they were renewed later than the ones here; none here is removed.")));
  ed.append(el("p", "lib-confirm", t("What is here now is backed up first: restore that one to undo this.")));
  const err = el("p", "editor-error", "");
  ed.append(err);
  const bar = el("div", "bar");
  const go = el("button", "text primary danger-fill", t("Restore"));
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    go.classList.add("busy");
    try {
      const r = await api("autobackup/restore", { name: b.name });
      closeConfirmAsk();
      autoBackupView = r.view;
      autoBackupAt = Date.now();
      const x = r.result || {};
      const lines = [t("Restored the backup of {when}", { when }) + " · " + t("Providers: {added} added, {replaced} replaced; {profiles} profiles; {agents} agent settings changed", { added: x.Added, replaced: x.Replaced, profiles: x.Profiles, agents: x.Agents })];
      if (x.NeedKey?.length) lines.push(t("Needs a key: {names}", { names: x.NeedKey.join(", ") }));
      if (x.SignIns) { await import("./syncfiles.js"); lines.push(...signInsSaid(x.SignIns)); }
      if (x.SignInsLocked) lines.push(t("This backup's sign-ins are sealed with another passphrase, so they were left out."));
      const before = r.view?.backups?.find((k) => k.name === r.before);
      if (before) lines.push(t("What was here before is the version of {when}: restore it to undo.", { when: autoBackupWhen(before.made) }));
      autoBackupSaid = lines;
      renderSync();
      refreshAfterSync();
    } catch (x) {
      go.disabled = false;
      go.classList.remove("busy");
      err.textContent = x.message;
    }
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelAsk;
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  cancel.focus(NO_SCROLL);
}

// autoBackupPhraseForm asks for the passphrase the backups' sign-ins are
// sealed with, as they are turned on
function autoBackupPhraseForm() {
  const ed = el("div", "editor sync-form auto-backup-phrase");
  const p1 = input("", t("passphrase"), "password");
  const p2 = input("", t("again"), "password");
  ed.append(...field(t("Passphrase"), p1, t("The sign-ins in each backup are sealed with it. Anyone with a backup and the passphrase can use those accounts. Keep it: without it a backup's sign-ins can't be opened.")),
    ...field("", p2));
  const go = el("button", "text primary", t("Turn on"));
  const cancel = el("button", "text", t("Cancel"));
  const say = syncBar(ed, "", el("span", "grow"), cancel, go);
  cancel.onclick = () => { autoBackupPhrase = false; renderSync(); };
  go.onclick = async () => {
    if (!p1.value) return say(t("Pick a passphrase: the file is sealed with it"));
    if (p1.value !== p2.value) return say(t("The two passphrases differ"));
    autoBackupPhrase = false;
    await autoBackupDo("set", { signIns: true, passphrase: p1.value }, go);
  };
  return ed;
}
