// The hover hint beside Switch {agent}'s account automatically (the owner,
// 11-11): how magpie switches the agent's account, and why that is the
// agent's own use of the account. It says what is sent and by whom, and
// promises nothing about bans: what an account may be used for is the
// vendor's call. Its lines rest out of sight in the accounts list, and
// settingshints.js shows them in its card. Only where the switch moves the
// sign-in: under the gateway it moves Claude Code's requests instead.
// app.js imports this when it draws the switch, so the core doesn't
// carry it.

// autoSwitchTip puts the hint's lines in box and its mark after the
// switch's name in head, for p's agent (Codex or Claude Code)
window.autoSwitchTip = function (box, head, p) {
  const m = p.signInMoves;
  if (!m || !(m.mode === "moves" || m.mode === "kept" && !m.routed) || box.querySelector(":scope > .hint")) return;
  const codex = p.account?.agent === "codex";
  const lines = codex
    ? [t("When the account runs low, magpie signs Codex in to your next account, as codex login does. Sessions already open keep their account until they are restarted."),
      t("Requests still come from Codex itself. Use only accounts that are yours: sharing one is against OpenAI's terms.")]
    : [t("When the account runs low, magpie signs Claude Code in to your next account, as /login does. Sessions already open keep their account until they are restarted."),
      t("Requests still go from Claude Code straight to Anthropic, unchanged. Use only accounts that are yours: sharing one is against Anthropic's terms.")];
  box.classList.add("hint-row");
  box.append(...lines.map((s) => el("p", "hint", s)));
  const mark = el("span", "as-tip");
  mark.setAttribute("aria-hidden", "true"); // a screen reader reads the lines
  mark.append(svg(INFO_I, 13, 1.6));
  // a press on it is not one on the switch's label
  mark.onclick = (e) => e.preventDefault();
  head.querySelector(".as-name")?.after(mark);
};
