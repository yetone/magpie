// A provider's Images per request (#6849, Yuu_jiang_ on X: 100milionseek
// and Coarena take 8 images a request and refuse the ninth): one for all
// its models and model=n for one, as the context window is set. The
// gateway leaves the oldest images out down to it before sending
// (image_cap.go). The provider editor's Advanced draws it under Max
// output; app.js imports this as the editor opens (outputField), so it
// weighs nothing on the core.
window.maxImagesField = function (p) {
  // the one for all models first, then model=n by id; the draft takes it
  // only when typed, so an editor opened and closed has nothing to discard
  const was = draft?.maxImages ?? Object.entries(p?.maxImages || {}).sort(([a], [b]) => (a !== "*") - (b !== "*") || (a > b) - (a < b)).map(([k, n]) => (k === "*" ? n : k + "=" + n)).join(", ");
  const im = input(was, t("e.g. 8 · or gemini-3-flash=10, comma separated"));
  im.classList.add("max-images");
  im.oninput = () => { if (draft) draft.maxImages = im.value; };
  return field(t("Images per request"), im, t("The most images one request carries: the oldest are left out first, the latest message's kept; empty leaves it to the vendor"));
};

// maxImagesOf is the draft's Images per request as saved, by model id
// ("*" for all, {} for none), or false once its error is shown; with no
// field drawn it is undefined, and the save keeps what was set
window.maxImagesOf = function (ed) {
  const im = ed.querySelector("input.max-images");
  if (!im) return undefined;
  const map = {};
  for (const x of im.value.split(/[,，\n]/).map((x) => x.trim()).filter(Boolean)) {
    const m = /^(?:(.+?)\s*=\s*)?(\d+)$/.exec(x);
    if (!m || +m[2] > 1000) {
      shown(im)?.focus(NO_SCROLL);
      editorError(t("Images per request: {v} is not a whole number up to 1000", { v: x }), "warn");
      return false;
    }
    if (+m[2]) map[m[1] || "*"] = +m[2];
  }
  return map;
};
