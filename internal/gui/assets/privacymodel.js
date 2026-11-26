// Settings › Privacy's local masking model (internal/pii): its state read on
// drawing, each second while it downloads or starts; then pmScore. Imported
// by privacyModelRow when Settings › Privacy draws, so it weighs nothing on
// the core.
// pmfolder.js loads with this module, not per draw: two draws' own
// imports could settle out of order (WebKit), and the first draw's, with
// no model read yet, took away the fold the second had drawn.
import "./pmfolder.js";
let pmState = null, pmTimer = 0;
window.pmStop = () => clearTimeout(pmTimer);
window.pmFill = function (r, name) {
  const sr = el("div", "row pref");
  sr.id = "pmScoreRow";
  let tries = 0;
  const draw = (m) => {
    if (!r.isConnected) return;
    const who = el("div", "who"), val = el("div", "val");
    who.append(name(), el("div", m && (m.on || m.installed || !m.supported || pmBusy(m) || m.error || m.models?.some((x) => x.error)) ? "sub say" : "sub", pmSub(m)));
    if (m?.supported || m?.installed || m?.managedWeights === false) {
      if (m.models?.length > 1) val.append(pmPick(m, (id) => act("pick", id)));
      if (pmBusy(m)) {
        const c = el("button", "text", t("Cancel"));
        c.onclick = () => act("cancel");
        val.append(c);
      } else if (m.installed && !m.on && m.managedWeights !== false) {
        const x = el("button", "text", t("Delete model"));
        x.onclick = async () => {
          if (await confirmAction(t("Delete the model?"), t("Turning it on again downloads {size} again.", { size: fmtBytes(m.size) }), t("Delete"))) act("remove");
        };
        val.append(x);
      }
      val.append(segs([["off", t("Off")], ["on", t("On")]], m.on || m.downloading ? "on" : "off", (v) => act(v)));
    }
    r.replaceChildren(who, val);
    if (m && (m.supported || m.installed) && m.defaultScore) {
      const sw = el("div", "who");
      sw.append(el("div", "name", t("Confidence threshold")), el("div", "sub", t("What the model is less sure of goes as written. Higher masks less, with fewer false alarms")));
      const sv = el("div", "val");
      sv.append(pmScore(m, (score) => act("score", m.model, score)));
      sr.replaceChildren(sw, sv);
      if (!sr.isConnected) r.after(sr);
    } else sr.remove();
    window.pmFolder(r, sr, m, act);
    clearTimeout(pmTimer);
    // a download's progress, and where pf-serve runs once it has started
    const starting = m?.on && m.installed && !m.running && !m.failed && tries++ < 30;
    if (pmBusy(m) || starting || m?.moving) pmTimer = setTimeout(load, 1000);
  };
  const load = () => api("privacy-model").then((m) => { pmState = m; draw(m); }).catch(() => {});
  const act = (doing, model, score, dir) => { tries = 0; api("privacy-model", { do: doing, model, score, dir }).then((m) => { pmState = m; draw(m); }).catch((e) => { statusTErr(e); load(); }); };
  draw(pmState);
  load();
};
// the local models to pick from (pii.Models), by the app's menu: what
// each is, its size and whether it is downloaded
const PM_MODELS = window.PM_MODELS = { "astrlink-guard": ["AstrLink Guard", "Chinese and English, few false alarms in code"], "privacy-filter": ["Privacy Filter", "OpenAI's, masks more ordinary values in code"] };
// pmBusy is the model downloading (the one picked while another masks, too)
const pmBusy = (m) => (m?.models || []).find((x) => x.downloading) || (m?.downloading ? m : null);
function pmPick(m, pick) {
  const b = el("button", "sess-pick pm-model");
  b.type = "button";
  menuOpener(b);
  b.title = t("The local model");
  b.append(el("span", "", m.name || (PM_MODELS[m.model] || [m.model])[0]), svg(CHEV, 11, 1.6));
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    const opts = (m.models || []).map(pmModelOption);
    openProtoMenu(b, opts, m.model, (v) => v !== m.model && pick(v), "The local model", "sess-menu", "right");
  };
  return b;
}
// pmScore picks the model's threshold (privacy-model's score) by the
// app's menu: its default, or one of PM_SCORES
const PM_SCORES = [0.5, 0.6, 0.7, 0.8, 0.85, 0.9, 0.95];
function pmScore(m, pick) {
  const cur = m.minScore === m.defaultScore ? "0" : String(m.minScore);
  const b = el("button", "sess-pick pm-score");
  b.type = "button";
  menuOpener(b);
  b.title = t("Confidence threshold");
  b.append(el("span", "", String(m.minScore)), svg(CHEV, 11, 1.6));
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    const note = (s) => s === PM_SCORES[0] ? "Masks the most" : s === PM_SCORES.at(-1) ? "Masks the least" : "";
    const opts = [{ v: "0", name: t("Default ({score})", { score: m.defaultScore }), literalName: true, note: note(m.defaultScore) },
      ...PM_SCORES.filter((s) => s !== m.defaultScore).map((s) => ({ v: String(s), name: String(s), literalName: true, note: note(s) }))];
    openProtoMenu(b, opts, cur, (v) => v !== cur && pick(Number(v)), "Confidence threshold", "sess-menu", "right");
  };
  return b;
}
function pmSub(m) {
  const about = t("Names, addresses and passwords no rule can find, found by a model that runs on this computer");
  if (!m) return about;
  // a plugin's model (pii/external.go): its files are the user's
  if (m.managedWeights === false && !m.installed) return t("Not available: its plugin is off, or its files are gone");
  if (!m.supported && !m.installed) return t("Not available on this system yet");
  const d = pmBusy(m);
  if (d) return t("Downloading the model: {done} of {total}", { done: fmtBytes(d.done), total: fmtBytes(d.total) });
  const bad = (m.models || []).find((x) => x.error) || (m.error ? m : null);
  if (bad) return t("The download stopped: {error}", { error: bad.error });
  if (m.on && m.failed) return m.chosen ? t("The model didn't answer, so requests are held back: {error}", { error: m.failed }) : t("The model didn't answer, so the rules mask requests alone: {error}", { error: m.failed });
  if (m.on && m.running) {
    if (m.gpu) return t("On, on the GPU ({device})", { device: m.device });
    // AstrLink Guard on an Intel NPU (Level Zero) or a Mac (CoreML)
    if (m.via === "Level Zero") return m.device ? t("On, on the NPU ({device})", { device: m.device }) : t("On, on the NPU");
    if (m.via) return { "Neural Engine": t("On, with CoreML on the Neural Engine"), GPU: t("On, with CoreML on the GPU"), CPU: t("On, with CoreML on the CPU") }[m.device] || t("On, with CoreML");
    // the Intel build on Apple silicon: Go, translated, several times slower
    if (m.rosetta) return t("On, slowly under Rosetta: the next update installs magpie for Apple silicon");
    // the x64 build on Windows on Arm, emulated
    if (m.emulated) return t("On, slowly under x64 emulation: the next update installs magpie for Arm");
    if (m.gpuError && m.npu) return t("On, on the CPU: the NPU failed ({error})", { error: m.gpuError });
    if (m.gpuError && m.model === "astrlink-guard") return t("On, on the CPU: CoreML failed ({error})", { error: m.gpuError });
    // Privacy Filter lost the GPU: to its driver resetting it (device
    // lost), or to a fault; the GPU is tried again at gpuAgain
    const time = m.gpuAgain ? hhmm(m.gpuAgain) : "";
    if (m.gpuReset) return time ? t("On, on the CPU until {time}: the graphics driver reset the GPU (device lost)", { time }) : t("On, on the CPU: the graphics driver reset the GPU (device lost)");
    if (m.gpuError && time) return t("On, on the CPU until {time}: the GPU failed ({error})", { time, error: m.gpuError });
    return m.gpuError ? t("On, on the CPU: the GPU failed ({error})", { error: m.gpuError }) : t("On, on the CPU");
  }
  if (m.on) return t("On: the model starts with the next request");
  if (m.installed) return t("Off. The model is downloaded ({size})", { size: fmtBytes(m.size) });
  return t("Names, addresses and passwords no rule can find, found by a model that runs on this computer. Turning it on downloads {size} once", { size: fmtBytes(m.size) });
}
