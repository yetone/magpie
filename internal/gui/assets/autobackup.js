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
    : on ? t("Your providers, settings and profiles, without API keys or sign-ins, into a folder on this computer") : t("Off: no backups are made by themselves"),
  segs([[0, t("Off")], [1, t("1 hour")], [6, t("6 hours")], [24, t("1 day")], [168, t("1 week")]], on ? v.hours : 0,
    (h) => autoBackupDo("set", h ? { on: true, hours: h } : { on: false })));
  if (v.error) head.querySelector(".sub").classList.add("bad", "wraps");
  if (on) {
    row(t("Versions kept"), t("The oldest is removed when a new one is made; one is made only when something changed"),
      segs([3, 7, 14, 30].map((n) => [n, String(n)]), v.keep, (n) => autoBackupDo("set", { keep: n })));
  }
  if (on || v.backups?.length) {
    const tools = [btn(t("Change…"), (b) => autoBackupDo("choose", {}, b)), btn(t("Show"), () => api("autobackup/reveal", {}).catch(statusErr))];
    if (v.custom) tools.push(btn(t("Default"), (b) => autoBackupDo("set", { dir: "" }, b)));
    row(t("Backup folder"), v.dir || "", ...tools).querySelector(".sub").classList.add("wraps", "auto-backup-dir");
  }
  const list = v.backups || [];
  const sub = row(t("Versions"), list.length ? t("{n} backups · newest {when}", { n: list.length, when: syncWhen(list[0].made) }) : t("None yet"),
    btn(t("Back up now"), (b) => autoBackupDo("now", {}, b))).querySelector(".sub");
  if (autoBackupSaid) {
    sub.after(...autoBackupSaid.map((l) => el("div", "sub wraps auto-backup-said", l)));
    autoBackupSaid = null;
  }
  for (const b of list) {
    const when = autoBackupWhen(b.made);
    const what = [t(b.providers === 1 ? "1 provider" : "{n} providers", { n: b.providers })];
    if (b.reason === "restore") what.unshift(t("What was here before a restore"));
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
