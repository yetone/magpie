// A profile's Rename (#2721, erlinerd): the details' name becomes a field
// holding it, Enter renames through profileAction (its pending chip and
// rollback), Escape or a click away puts the name back. app.js imports
// this when Rename is pressed, so it weighs nothing on the core.
window.profRename = function (d, p) {
  const name = d.querySelector(".pd-name");
  if (!name) return;
  const input = el("input", "chip-input pd-rename-input");
  input.value = p.name;
  input.setAttribute("aria-label", t("Profile name"));
  // while it is open the field has the head, × aside: In use, Apply and
  // Rename left a narrow panel's field no room to type in
  const rest = [...d.querySelectorAll(".pd-cur, .pd-apply, .pd-rename")];
  const show = (on) => rest.forEach((b) => (b.hidden = !on));
  show(false);
  const back = () => { if (input.isConnected) { input.replaceWith(name); show(true); } };
  input.onkeydown = (e) => {
    e.stopPropagation(); // Escape is the field's, not the details'
    const to = input.value.trim();
    if (e.key === "Enter" && to && to !== p.name) { input.onblur = null; profileAction("rename", p.name, false, to); }
    else if (e.key === "Enter" || e.key === "Escape") back();
  };
  input.onblur = () => setTimeout(back, 100);
  name.replaceWith(input);
  input.focus(NO_SCROLL);
  input.select();
};
