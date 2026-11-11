// The notice under Codex's accounts when the open Codex app shows fewer
// projects than the accounts have (linsm66 on X): a switch while it ran
// loaded the new account's list from the app's memory, and a file written
// under it is lost. Quitting and opening it again brings them back, and
// magpie never quits it unasked. app.js imports this when the providers
// state says so (codexProjects), so the core doesn't carry it.

window.codexProjectsNote = function (codexNote, accountAction) {
  const box = codexNote("", t("The Codex app shows fewer projects than you have"),
    t("Quit the Codex app and open it again to get your projects back. magpie shares them between your accounts while it is closed."));
  const ok = el("button", "text", t("Got it"));
  ok.onclick = () => accountAction("codex/daemon/dismiss-projects", {});
  box.append(ok);
  return box;
};
