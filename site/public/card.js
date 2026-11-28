// A contributor's card (cards.js serves the page, its data in #cc-data):
// a black metal card drawn on two canvases, front and back. The front has
// the bird, the person's picture where a chip would be, their number by
// when they were first credited, their name and since when; the back what
// they built, newest first. The fine lines and the rosette are made from
// the person's id, so no two cards are cut alike. It tilts under a mouse,
// turns over on a click, and saves as one picture of both sides.
(function () {
  const data = document.getElementById("cc-data");
  const card = document.querySelector(".cc-card");
  if (!data || !card) return;
  let got;
  try { got = JSON.parse(data.textContent); } catch (e) { return; }
  const c = got.card, w = got.w;

  // the card in its own units; a canvas is these times its scale
  const W = 1000, H = 631, R = 38, M = 64;
  const SANS = '-apple-system, BlinkMacSystemFont, "SF Pro Display", "Inter", "Segoe UI", "PingFang SC", "Hiragino Sans", "Microsoft YaHei", "Noto Sans CJK SC", system-ui, sans-serif';
  const MONO = 'ui-monospace, "SF Mono", "JetBrains Mono", Menlo, Consolas, monospace';
  const PLAT = "#dedee0", DIM = "#8e8e93", FAINT = "#5f5f64";
  const no = String(c.no).padStart(4, "0");

  function seeded(s) {
    let h = 1779033703 ^ s.length;
    for (let i = 0; i < s.length; i++) { h = Math.imul(h ^ s.charCodeAt(i), 3432918353); h = (h << 13) | (h >>> 19); }
    return function () {
      h = Math.imul(h ^ (h >>> 16), 2246822507); h = Math.imul(h ^ (h >>> 13), 3266489909);
      return ((h ^= h >>> 16) >>> 0) / 4294967296;
    };
  }

  function round(x, px, py, pw, ph, r) {
    x.beginPath();
    x.moveTo(px + r, py);
    x.arcTo(px + pw, py, px + pw, py + ph, r);
    x.arcTo(px + pw, py + ph, px, py + ph, r);
    x.arcTo(px, py + ph, px, py, r);
    x.arcTo(px, py, px + pw, py, r);
    x.closePath();
  }

  // black metal: a flat body, brushed across, darker towards the edge
  function body(x, rand) {
    round(x, 0, 0, W, H, R);
    x.clip();
    x.fillStyle = "#121214";
    x.fillRect(0, 0, W, H);
    for (let y = 0; y < H; y += 1.5) {
      const a = rand() * 0.035;
      x.fillStyle = rand() < 0.5 ? `rgba(255,255,255,${a})` : `rgba(0,0,0,${a * 3})`;
      x.fillRect(0, y, W, 1);
    }
    const v = x.createRadialGradient(W * 0.42, H * 0.3, H * 0.2, W * 0.5, H * 0.5, W * 0.72);
    v.addColorStop(0, "rgba(0,0,0,0)");
    v.addColorStop(1, "rgba(0,0,0,0.34)");
    x.fillStyle = v;
    x.fillRect(0, 0, W, H);
    // a hairline just inside the edge, as a machined bevel catches light
    round(x, 1.5, 1.5, W - 3, H - 3, R - 1.5);
    x.strokeStyle = "rgba(255,255,255,0.09)";
    x.lineWidth = 1.5;
    x.stroke();
  }

  // a guilloché rosette, its petals and turns the person's own
  function rosette(x, rand, cx, cy, r0) {
    const petals = 7 + Math.floor(rand() * 7), turn = rand() * Math.PI * 2, rings = 22;
    const depth = 0.08 + rand() * 0.06;
    x.lineWidth = 1;
    for (let k = 0; k < rings; k++) {
      const r = r0 * (0.32 + 0.68 * (k / (rings - 1))), ph = turn + k * (0.18 + rand() * 0.02);
      x.beginPath();
      for (let i = 0; i <= 360; i++) {
        const t = (i / 360) * Math.PI * 2;
        const rr = r * (1 + depth * Math.sin(petals * t + ph));
        const px = cx + rr * Math.cos(t), py = cy + rr * Math.sin(t);
        if (i) x.lineTo(px, py); else x.moveTo(px, py);
      }
      x.strokeStyle = `rgba(255,255,255,${0.028 + 0.02 * (k % 3 === 0)})`;
      x.stroke();
    }
  }

  // letters set apart, as a card's small capitals are; text with no
  // spaces between words (Chinese, Japanese) is set as written
  const caps = (s) => (/[\u3000-\u9fff\uff00-\uffef]/.test(s) ? s : s.toUpperCase());
  function spaced(x, s, px, py, gap, align) {
    if (/[　-鿿＀-￯]/.test(s)) gap = 0;
    const ws = Array.from(s, (ch) => x.measureText(ch).width);
    const all = ws.reduce((a, b) => a + b, 0) + gap * (ws.length - 1);
    let at = align === "right" ? px - all : align === "center" ? px - all / 2 : px;
    const was = x.textAlign;
    x.textAlign = "left";
    Array.from(s).forEach((ch, i) => { x.fillText(ch, at, py); at += ws[i] + gap; });
    x.textAlign = was;
    return all;
  }

  // pressed into the metal: a shadow under, a catch of light over
  function emboss(x, draw, ink) {
    x.save();
    x.fillStyle = "rgba(0,0,0,0.75)"; x.translate(0, 2.2); draw(); x.translate(0, -2.2);
    x.fillStyle = "rgba(255,255,255,0.22)"; x.translate(0, -1.1); draw(); x.translate(0, 1.1);
    x.fillStyle = ink; draw();
    x.restore();
  }

  function fit(x, s, max) {
    if (x.measureText(s).width <= max) return s;
    let a = Array.from(s);
    while (a.length > 1 && x.measureText(a.join("") + "…").width > max) a.pop();
    return a.join("").trimEnd() + "…";
  }

  const assets = { bird: null, face: null };

  function bird(x, px, py, size, ink) {
    if (!assets.bird) return;
    const k = 4, o = document.createElement("canvas");
    o.width = o.height = size * k;
    const ox = o.getContext("2d");
    ox.drawImage(assets.bird, 0, 0, o.width, o.height);
    ox.globalCompositeOperation = "source-in";
    ox.fillStyle = ink;
    ox.fillRect(0, 0, o.width, o.height);
    x.drawImage(o, px, py, size, size);
  }

  const since = (() => {
    if (!c.since) return "";
    if (!c.since.t) return c.since.v;
    const d = new Date(c.since.t * 1000);
    return `${c.since.v}  ${String(d.getUTCMonth() + 1).padStart(2, "0")}/${String(d.getUTCFullYear() % 100).padStart(2, "0")}`;
  })();
  const handle = c.github ? `@${c.github}` : c.x ? `@${c.x}` : "";

  function front(x) {
    const rand = seeded(c.id);
    x.save();
    body(x, rand);
    rosette(x, rand, W * 0.8, H * 0.44, H * 0.44);
    // bird and name of the house
    bird(x, M - 4, 50, 58, PLAT);
    x.fillStyle = PLAT;
    x.font = `600 36px ${SANS}`;
    x.textBaseline = "alphabetic";
    spaced(x, "magpie", M + 64, 94, 0.6);
    x.font = `500 15px ${SANS}`;
    x.fillStyle = DIM;
    spaced(x, caps(w.contributor), W - M, 90, 4.2, "right");
    // where a chip would be: the person
    const ax = M, ay = 178, as = 118, ar = 20;
    x.save();
    round(x, ax, ay, as, as, ar);
    x.clip();
    x.fillStyle = "#1d1d20";
    x.fillRect(ax, ay, as, as);
    if (assets.face) x.drawImage(assets.face, ax, ay, as, as);
    else {
      x.fillStyle = DIM;
      x.font = `600 52px ${SANS}`;
      x.textAlign = "center";
      x.fillText(Array.from(c.name)[0].toUpperCase(), ax + as / 2, ay + as / 2 + 19);
      x.textAlign = "left";
    }
    x.restore();
    round(x, ax + 0.75, ay + 0.75, as - 1.5, as - 1.5, ar);
    x.strokeStyle = "rgba(255,255,255,0.28)";
    x.lineWidth = 1.5;
    x.stroke();
    // the number, set as a card's is
    x.font = `500 50px ${MONO}`;
    emboss(x, () => { spaced(x, "No.", M, 392, 2); spaced(x, no, M + 128, 392, 10); }, "#cfcfd2");
    // the name
    let size = 46;
    x.font = `600 ${size}px ${SANS}`;
    while (size > 26 && x.measureText(c.name).width > W - 2 * M) { size -= 2; x.font = `600 ${size}px ${SANS}`; }
    const name = fit(x, c.name, W - 2 * M);
    emboss(x, () => x.fillText(name, M, 478), PLAT);
    // since, how many, who
    const label = (s, px, align) => { x.font = `500 13px ${SANS}`; x.fillStyle = FAINT; spaced(x, caps(s), px, 540, 2.6, align); };
    const value = (s, px, align) => { x.font = `500 22px ${MONO}`; x.fillStyle = "#c4c4c8"; x.textAlign = align || "left"; x.fillText(s, px, 574); x.textAlign = "left"; };
    let at = M;
    if (since) {
      label(w.since, at);
      value(since, at);
      x.font = `500 22px ${MONO}`;
      at += Math.max(x.measureText(since).width, 120) + 56;
    }
    label(w.releases, at);
    value(String(c.n), at);
    if (handle) {
      x.font = `500 22px ${MONO}`;
      value(fit(x, handle, W - M - at - 160), W - M, "right");
    }
    x.restore();
  }

  function back(x) {
    const rand = seeded(c.id + "/back");
    x.save();
    body(x, rand);
    // the stripe
    x.fillStyle = "#050506";
    x.fillRect(0, 60, W, 92);
    for (let y = 60; y < 152; y += 2) {
      x.fillStyle = `rgba(255,255,255,${rand() * 0.025})`;
      x.fillRect(0, y, W, 1);
    }
    rosette(x, rand, W * 0.86, H * 0.82, H * 0.3);
    x.font = `500 15px ${SANS}`;
    x.fillStyle = DIM;
    spaced(x, caps(w.built), M, 218, 2.8);
    const shown = c.lines.slice(0, 4);
    let y = 272;
    for (const l of shown) {
      x.font = `500 17px ${MONO}`;
      x.fillStyle = DIM;
      x.fillText(l.v, M, y);
      const vw = Math.max(x.measureText(l.v).width + 22, 128);
      x.font = `400 23px ${SANS}`;
      x.fillStyle = PLAT;
      x.fillText(fit(x, l.t, W - 2 * M - vw), M + vw, y);
      y += 58;
    }
    const more = c.lines.length - shown.length;
    if (more > 0) {
      x.font = `500 17px ${SANS}`;
      x.fillStyle = DIM;
      x.fillText(w.more.replace("{n}", String(more)), M, y - 6);
    }
    x.font = `500 19px ${MONO}`;
    x.fillStyle = "#a6a6ab";
    x.fillText(fit(x, "usemagpie.ai/c/" + c.id, W - M * 2 - 260), M, H - 52);
    x.textAlign = "right";
    x.fillText(`No. ${no}`, W - M, H - 52);
    x.textAlign = "left";
    x.font = `500 19px ${MONO}`;
    bird(x, W - M - x.measureText(`No. ${no}`).width - 44, H - 79, 32, "#a6a6ab");
    x.restore();
  }

  const canvases = [card.querySelector(".cc-front canvas"), card.querySelector(".cc-back canvas")];
  function draw() {
    const scale = (card.clientWidth / W) * (window.devicePixelRatio || 1);
    if (!scale) return;
    canvases.forEach((cv, i) => {
      cv.width = Math.round(W * scale);
      cv.height = Math.round(H * scale);
      const x = cv.getContext("2d");
      x.setTransform(scale, 0, 0, scale, 0, 0);
      (i ? back : front)(x);
    });
  }

  function load(src, cors) {
    return new Promise((ok) => {
      const im = new Image();
      if (cors) { im.crossOrigin = "anonymous"; im.referrerPolicy = "no-referrer"; }
      im.onload = () => ok(im);
      im.onerror = () => ok(null);
      im.src = src;
    });
  }

  // the picture in grey, as engraved; left in colour if it can't be read
  function grey(im) {
    const s = 256, o = document.createElement("canvas");
    o.width = o.height = s;
    const ox = o.getContext("2d");
    const k = Math.min(im.naturalWidth, im.naturalHeight);
    ox.drawImage(im, (im.naturalWidth - k) / 2, (im.naturalHeight - k) / 2, k, k, 0, 0, s, s);
    try {
      const d = ox.getImageData(0, 0, s, s), p = d.data;
      for (let i = 0; i < p.length; i += 4) {
        const l = 0.2126 * p[i] + 0.7152 * p[i + 1] + 0.0722 * p[i + 2];
        const v = Math.max(0, Math.min(255, (l - 128) * 1.08 + 120));
        p[i] = p[i + 1] = p[i + 2] = v;
      }
      ox.putImageData(d, 0, 0);
    } catch (e) {}
    return o;
  }

  const stage = document.querySelector(".cc-stage");
  stage.hidden = false;
  draw();
  if (window.ResizeObserver) {
    let was = card.clientWidth;
    new ResizeObserver(() => { if (card.clientWidth !== was) { was = card.clientWidth; draw(); } }).observe(card);
  }
  const ready = Promise.all([
    load("/img/magpie-small.svg").then((im) => { assets.bird = im; }),
    c.pic ? load(c.pic, true).then((im) => { if (im) assets.face = grey(im); }) : null,
  ]).then(() => { draw(); card.dataset.ready = "1"; });

  // turn over
  card.setAttribute("aria-pressed", "false");
  card.addEventListener("click", () => {
    const on = !card.classList.contains("cc-flipped");
    card.classList.toggle("cc-flipped", on);
    card.setAttribute("aria-pressed", String(on));
  });

  // tilt under a mouse, the light following it
  const still = matchMedia("(prefers-reduced-motion: reduce)");
  const mouse = matchMedia("(hover: hover) and (pointer: fine)");
  stage.addEventListener("pointermove", (e) => {
    if (still.matches || !mouse.matches || e.pointerType !== "mouse") return;
    const r = card.getBoundingClientRect();
    const px = Math.max(-1, Math.min(1, ((e.clientX - r.left) / r.width) * 2 - 1));
    const py = Math.max(-1, Math.min(1, ((e.clientY - r.top) / r.height) * 2 - 1));
    card.style.setProperty("--rx", `${(-py * 7).toFixed(2)}deg`);
    card.style.setProperty("--ry", `${(px * 9).toFixed(2)}deg`);
    card.style.setProperty("--mx", `${((px + 1) * 50).toFixed(1)}%`);
    card.style.setProperty("--my", `${((py + 1) * 50).toFixed(1)}%`);
    card.classList.add("cc-lit");
  });
  stage.addEventListener("pointerleave", () => {
    card.style.removeProperty("--rx");
    card.style.removeProperty("--ry");
    card.classList.remove("cc-lit");
  });

  // both sides, one picture, at twice the card's units
  const save = document.querySelector(".cc-save");
  if (save && HTMLCanvasElement.prototype.toBlob) {
    save.hidden = false;
    save.addEventListener("click", async () => {
      await ready;
      const k = 2, gap = 48;
      const o = document.createElement("canvas");
      o.width = W * k;
      o.height = (H * 2 + gap) * k;
      const x = o.getContext("2d");
      x.setTransform(k, 0, 0, k, 0, 0);
      front(x);
      x.translate(0, H + gap);
      back(x);
      o.toBlob((b) => {
        if (!b) return;
        const a = document.createElement("a");
        a.href = URL.createObjectURL(b);
        a.download = `magpie-card-${c.id}.png`;
        document.body.appendChild(a);
        a.click();
        a.remove();
        setTimeout(() => URL.revokeObjectURL(a.href), 4000);
      }, "image/png");
    });
  }

  const copy = document.querySelector(".cc-copy");
  if (copy && navigator.clipboard && navigator.clipboard.writeText) {
    copy.hidden = false;
    const was = copy.textContent;
    let t = 0;
    copy.addEventListener("click", () => {
      navigator.clipboard.writeText(got.link).then(() => {
        copy.textContent = copy.dataset.done;
        clearTimeout(t);
        t = setTimeout(() => { copy.textContent = was; }, 1600);
      }, () => {});
    });
  }
})();
