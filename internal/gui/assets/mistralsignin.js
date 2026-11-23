// A Mistral Vibe plan's key (Pro, Student, Team), got the way Mistral's
// own Vibe CLI gets it: the user signs in to Mistral in their browser and
// picks the workspace, and magpie trades the finished sign-in for the key
// (mistral_signin.go), which goes in the key box and is saved. app.js
// imports this when a Mistral provider's editor opens, so it weighs
// nothing on the core. The sign-in outlives a redraw of the editor: the
// newest editor's box gets the key.
let mistralFlow = null; // {id, fill, idle}

window.mistralSignIn = (side, key, save) => {
  // the key typed in as the user would, then saved as Save does
  const fill = (k) => {
    if (!key.isConnected) return false;
    key.value = k;
    key.oninput();
    save();
    return true;
  };
  if (mistralFlow) mistralFlow.fill = fill;
  const go = el("button", "link vibe-in", t("Sign in with Mistral ↗"));
  const stop = el("button", "text", t("Cancel"));
  const waiting = () => {
    go.disabled = true;
    go.textContent = t("Approve the sign-in in your browser…");
    stop.hidden = false;
  };
  const idle = () => {
    go.disabled = false;
    go.textContent = t("Sign in with Mistral ↗");
    stop.hidden = true;
  };
  stop.onclick = () => {
    if (mistralFlow) api("mistral/signin/" + encodeURIComponent(mistralFlow.id) + "/cancel", {}).catch(() => {});
    mistralFlow = null;
    idle();
  };
  go.onclick = async () => {
    go.disabled = true;
    try {
      const r = await api("mistral/signin", {});
      mistralFlow = { id: r.id, fill };
      waiting();
      api("open", { url: r.url }).catch(() => {});
      mistralWatch(mistralFlow, idle);
    } catch (e) {
      idle();
      statusErr(e);
    }
  };
  if (mistralFlow) { waiting(); mistralFlow.idle = idle; } else idle();
  side.append(go, stop);
};

// mistralWatch waits on the sign-in, which magpie finishes once the user
// has signed in in their browser
async function mistralWatch(f, idle) {
  f.idle = idle;
  while (mistralFlow === f) {
    await new Promise((r) => setTimeout(r, 1500));
    if (mistralFlow !== f) return;
    let s;
    try { s = await api("mistral/signin/" + encodeURIComponent(f.id)); } catch (_) { continue; }
    if (s?.state === "pending") continue;
    mistralFlow = null;
    f.idle();
    if (s?.state === "done" && s.key) {
      if (!f.fill(s.key)) statusErr(new Error(t("Signed in, but the provider's settings were closed: sign in again")));
    } else if (s?.error) {
      statusErr(new Error(s.error));
    }
    return;
  }
}
