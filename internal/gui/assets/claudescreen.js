// The Claude Code provider editor's look at each hidden Claude Code's
// screen while it runs (#8075, FremontDango on Discord: a run that seemed
// stuck said nothing of where until it ended). /api/claude/screens is read
// every second while it is open, and not once it is closed or the editor
// goes; the gateway masks the screen as it masks the logs, and nothing is
// typed into it. claudemode.js imports this on Show screen's first press,
// so it weighs nothing on the core.

const CC_STATE = { answering: "Answering", idle: "Kept for its next turn", tools: "Waiting on your agent's tools" };

// ccWhyNone: why a run shows no screen, by the gateway's English reason
function ccWhyNone(why) {
  if (/draws no screen/.test(why)) return t("A claude -p run draws no screen");
  if (/hasn't drawn/.test(why)) return t("Hasn't drawn anything yet");
  return t("The screen couldn't be read");
}

// ccScreensDraw fills box from a read of /api/claude/screens, keeping each
// run's block (and where it is scrolled to) by its pid
function ccScreensDraw(box, r) {
  const runs = r?.runs || [];
  const had = new Map([...box.querySelectorAll(".cc-screen")].map((b) => [b.dataset.pid, b]));
  const out = runs.map((run) => {
    const pid = String(run.pid);
    let b = had.get(pid);
    if (!b) {
      b = el("div", "cc-screen");
      b.dataset.pid = pid;
      b.append(el("div", "h"), el("pre"));
    }
    b.firstChild.textContent = [run.model, t(CC_STATE[run.state] || run.state), "pid " + pid].filter(Boolean).join(" · ");
    const pre = b.lastChild, rows = run.screen?.length ? run.screen.join("\n") : "";
    pre.classList.toggle("none", !rows);
    pre.title = rows ? "" : run.screenWhy || "";
    const text = rows || ccWhyNone(run.screenWhy || "");
    if (pre.textContent !== text) pre.textContent = text;
    return b;
  });
  if (!out.length) out.push(el("div", "cc-screen-none", t("No Claude Code is running now.")));
  box.replaceChildren(...out);
}

// claudeScreenToggle opens the screens under the row v, or closes them
window.claudeScreenToggle = function (v, btn) {
  const open = v.querySelector(".cc-screens");
  btn.setAttribute("aria-expanded", String(!open));
  btn.textContent = open ? t("Show screen") : t("Hide screen");
  if (open) return open.remove();
  const box = el("div", "cc-screens");
  box.id = "ccScreens";
  btn.setAttribute("aria-controls", box.id);
  v.append(box);
  const read = async () => {
    if (!box.isConnected) return;
    try {
      ccScreensDraw(box, await api("claude/screens"));
    } catch (e) {
      box.replaceChildren(el("div", "cc-screen-none bad", e.message));
    }
    if (box.isConnected) setTimeout(read, 1000);
  };
  read();
};
