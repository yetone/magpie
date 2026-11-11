// A picture a plugin's page drew (magpie.image.copy, magpie.image.save,
// plugin-runtime.md): its frame can't download a file or reach the
// clipboard, and the app's window can't copy a picture, so magpie does it
// (pageimage.go); in magpie web this server is another computer, and the
// browser's window does it here. pluginpages.js imports this the first
// time a page shares one (or asks for a file), so it weighs nothing on the core.

// A file of the page's own folder, for a page that cannot ask for it itself
// (a proxy that signs readers in by cookie turns its frame away,
// pageinline.go); this request carries the window's cookie. page is the
// page's own address, want the file's.
window.pageAsset = async function (page, want) {
  const dir = new URL(page.replace(/[^/]*$/, ""), location.href).href;
  const u = new URL(String(want || ""), location.href);
  if (!u.href.startsWith(dir) || u.hash) throw new Error(t("No such call: {what}", { what: "asset" }));
  const res = await fetch(u.href, { credentials: "same-origin" });
  if (!res.ok) throw new Error(String(res.status));
  return { type: res.headers.get("content-type") || "", data: await res.arrayBuffer() };
};

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
    // navigator.clipboard is missing on plain http (a LAN address) and in
    // some webviews: the picture is copied as a selected <img> instead, and
    // when neither works the page is told to save, not handed a TypeError
    if (navigator.clipboard?.write && window.ClipboardItem) {
      try { await navigator.clipboard.write([new ClipboardItem({ "image/png": png })]); return { ok: true }; } catch {}
    }
    if (await copyPicture(png)) return { ok: true };
    throw new Error(t("This browser can't copy a picture on this address (it needs https or localhost). Save it instead."));
  }
  const bytes = new Uint8Array(await png.arrayBuffer());
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
  return api("plugin-pages/image", { spec, action: save ? "save" : "copy", name, png: btoa(bin) });
};

// copyPicture selects an <img> of the PNG in a box out of sight and uses the
// page's copy command, which puts the picture on the clipboard where the
// async API is missing; it says whether the browser took the copy.
async function copyPicture(png) {
  const url = await new Promise((res) => { const r = new FileReader(); r.onload = () => res(String(r.result)); r.onerror = () => res(""); r.readAsDataURL(png); });
  if (!url) return false;
  const was = document.activeElement;
  const box = document.createElement("div");
  box.contentEditable = "true";
  box.style.cssText = "position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;overflow:hidden;pointer-events:none";
  const img = new Image();
  img.src = url;
  box.append(img);
  document.body.append(box);
  let ok = false;
  try {
    await img.decode().catch(() => {});
    box.focus({ preventScroll: true });
    const sel = getSelection();
    const r = document.createRange();
    r.selectNode(img);
    sel.removeAllRanges();
    sel.addRange(r);
    ok = document.execCommand("copy");
    sel.removeAllRanges();
  } catch { ok = false; }
  box.remove();
  was?.focus?.({ preventScroll: true });
  return ok;
}
