// An agent shown only the models picked for it takes in the models ticked
// in a provider's editor too (eiyuuzaki2333 on X): the switch under its
// model list's Hide all. app.js imports this beside the list's own read
// (openAgentModels), so it weighs nothing on the core. me and disarm are
// asked for at the click: the list's state is made after its foot.
window.amJoin = function (a, joins, me, disarm) {
  const box = el("label", "am-join");
  const say = el("span", "", t("Add models I pick in a provider"));
  const sw = libSwitch("use-credits", joins, say.textContent);
  box.title = t("On: a model ticked in a provider's editor, as it is added or later, is ticked here too. Other new models still wait for you to tick them");
  box.append(sw, say);
  const show = (on) => { sw.classList.toggle("on", on); sw.setAttribute("aria-checked", String(on)); };
  sw.onclick = (e) => {
    e.preventDefault();
    disarm();
    joins = !joins;
    show(joins);
    const want = joins, m = me();
    m.saving = m.saving.then(() => api("agent-models/" + encodeURIComponent(a.id), { joins: want }))
      .catch((err) => {
        statusErr(err);
        if (joins !== want) return;
        joins = !want;
        show(joins);
      });
  };
  return box;
};
