// Bedrock's AWS access key (#7687): a temporary key's session token, in the
// row under the Secret in the editor's Advanced, kept as the Secret is: the
// saved one never comes back here, and a blank box keeps it. app.js imports
// this as a Bedrock editor draws, so it weighs nothing on the core. It
// returns what the key's own Remove calls to empty the token's row too.
window.awsToken = (after, access, draft) => {
  const kept = () => access.tokenSet && !draft.clearAccessKey && !draft.clearSessionToken;
  const st = input(draft.sessionToken || "", kept() ? t("saved · paste a new one to replace it") : t("optional"), "password");
  st.classList.add("aws-token");
  st.oninput = () => { draft.sessionToken = st.value.trim(); };
  st.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") cancelEdit(); };
  const pair = el("div", "pair");
  pair.append(st);
  const empty = () => { st.value = ""; st.placeholder = t("optional"); pair.querySelector(".side")?.remove(); };
  if (kept()) {
    const side = el("div", "side");
    const drop = el("button", "text", t("Remove"));
    drop.onclick = () => { draft.clearSessionToken = true; draft.sessionToken = ""; empty(); };
    side.append(drop);
    pair.append(side);
  }
  after.after(...field(t("Session token"), pair, ""));
  return empty;
};
