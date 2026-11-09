// A picture a plugin's page drew (magpie.image.copy, magpie.image.save,
// plugin-runtime.md): its frame can't download a file or reach the
// clipboard, and the app's window can't copy a picture, so magpie does it
// (pageimage.go); in magpie web this server is another computer, and the
// browser's window does it here. pluginpages.js imports this the first
// time a page shares one, so it weighs nothing on the core.

window.pageShare = async function (spec, save, params) {
  const png = params?.png;
  if (!(png instanceof Blob) || png.type !== "image/png" || !png.size || png.size > 16 << 20) throw new Error(t("A picture is a PNG of 16 MB at most"));
  const name = String(params?.name || "").slice(0, 120);
  if (web) {
    if (save) {
      const a = document.createElement("a");
      a.href = URL.createObjectURL(png);
      a.download = (name.replace(/[<>:"/\\|?*\u0000-\u001f]/g, "-").replace(/\.png$/i, "").trim() || "magpie") + ".png";
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 60e3);
      return { path: "" };
    }
    await navigator.clipboard.write([new ClipboardItem({ "image/png": png })]);
    return { ok: true };
  }
  const bytes = new Uint8Array(await png.arrayBuffer());
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
  return api("plugin-pages/image", { spec, action: save ? "save" : "copy", name, png: btoa(bin) });
};
