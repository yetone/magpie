// Settings › Version's rarer buttons, imported from renderUpdate (app.js)
// when one is pressed, so they weigh nothing on the core: Restart
// (yansheng1003 on X: "请在刷新旁边增加一个重启按钮"), magpie started again
// as it is (restart.go), and a failing mirror's Download from GitHub (#893).

// the row's parts, as renderUpdate drew them
const parts = (r) => {
  const val = r.querySelector(".val");
  const dim = () => { for (const b of val.querySelectorAll("button")) { b.disabled = true; b.classList.add("busy"); } };
  return { val, sub: r.querySelector(".who .sub"), dim };
};

// restartSelf asks for the restart, and says in the row what came of it.
// The app's window closes with the app, and the new one opens its own; a
// browser's page (magpie web, or a browser on the app's page) reloads once
// another run, of the same version, answers /api/update (its run).
window.restartSelf = async function (r, u) {
  const { val, sub, dim } = parts(r);
  const mine = seqOf(r) + 1;
  updateSeq.set(r, mine); // stops timers: their redraw would drop the line
  dim();
  try {
    await api("restart", installFrom());
  } catch (e) {
    if (!r.isConnected || seqOf(r) !== mine) return;
    sub.textContent = t("Couldn't restart magpie") + (e.message ? " · " + e.message : "");
    sub.classList.add("wraps");
    for (const b of val.querySelectorAll("button")) { b.disabled = false; b.classList.remove("busy"); }
    return;
  }
  sub.textContent = t("Restarting…");
  if (!web || !u.run) return;
  for (let i = 0; i < 180; i++) {
    await new Promise((res) => setTimeout(res, 1000));
    const n = await fetch("/api/update").then((x) => (x.ok ? x.json() : null)).catch(() => null);
    if (n && n.run && n.run !== u.run) return location.reload();
  }
};

// fromGitHub drops the mirror that failed and downloads from GitHub
window.fromGitHub = async function (r) {
  if (updateBusy) return;
  parts(r).dim();
  try {
    prefs = await writingPrefs(api("settings/update-mirror", { mirror: "" }));
    await api(updatePath("update/install"), installFrom());
    updateLast = null;
    renderSettings(); // the source row says GitHub, this one the download
  } catch (e) {
    statusTErr(e);
    renderUpdate(r);
  }
};
