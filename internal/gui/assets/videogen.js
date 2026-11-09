// Settings › Models › Images' Video generation: the model a video request
// that names none is made with, generate_video's — the one magpie picks or
// one named. A custom provider's model makes videos once marked so in its
// Model settings (#1399), which the row says while no model makes any,
// with a way to the Providers page.
// Image generation's Off turns it off too. app.js imports this when
// Settings draws Image generation, so it weighs nothing on the core.

// videoGenFill draws the row into r
window.videoGenFill = function (r, s, keep) {
  const models = s.videoGenModels || [];
  const v = s.videoGen || "";
  const named = namedIn(models);
  const icOf = (id) => models.find((x) => x.id === id)?.icon;
  const gone = v && v === s.videoGenMissing;
  r.id = "videoGenRow";
  r.hidden = false;
  const who = el("div", "who");
  who.append(el("div", "name", t("Video generation")), el("div", "sub wraps", t("The model Magpie Image makes videos with")));
  if (!models.length && !v) {
    // the way there: the Providers page, where a provider's models are
    const how = el("div", "sub wraps video-gen-how", t("To make videos with a provider's model, right-click it in the provider's Model settings and choose Makes › Video model"));
    const go = el("button", "text video-gen-go", t("Providers") + " ›");
    go.type = "button";
    go.onclick = () => show("providers");
    how.append(" ", go);
    who.append(how);
  }
  if (gone) who.append(el("div", "sub err video-gen-missing", s.videoGenAuto
    ? t("{model}, picked here, no longer makes videos: its provider was removed or turned off, or the model isn't marked a video model any more. {auto}, the automatic choice, makes videos in its place. Pick another model here.", { model: v, auto: named(s.videoGenAuto) })
    : t("{model}, picked here, no longer makes videos: its provider was removed or turned off, or the model isn't marked a video model any more. No other model makes videos. Pick another model here.", { model: v })));
  const b = el("button", "rt-cond on");
  if (gone) b.append(icon("generic"), el("span", "", v + " · " + t("missing")));
  else if (v) b.append(icon(icOf(v) || "generic"), el("span", "", named(v)));
  else {
    if (s.videoGenAuto) b.append(icon(icOf(s.videoGenAuto) || "generic"));
    b.append(el("span", "", s.videoGenAuto ? t("Automatic") + " · " + named(s.videoGenAuto) : t("Automatic") + " · " + t("no model that makes videos")));
  }
  b.onclick = (ev) => openPicker({ id: "", name: "", fields: [] }, { key: "videoGen", label: "model", value: v, options: [
    { value: "", label: t("Automatic"), note: s.videoGenAuto ? named(s.videoGenAuto) : t("no model that makes videos"), reset: true },
    ...models.map((x) => ({ value: x.id, label: x.name || x.id, note: x.providerName, icon: x.icon, group: x.providerName, ref: x.id }))],
  onPick: (id) => { if (id !== v) savePrefs({ ...keep, videoGen: id }); } }, b, ev);
  const val = el("div", "val");
  val.append(b);
  r.append(who, val);
};
