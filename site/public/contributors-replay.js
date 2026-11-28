// The contributors page's replay: everyone credited, release by release, in
// the order the history went (credits.mjs replay(), served at
// /contributors/replay.json). Each person takes the next place out from the
// middle when a release first credits them; a later release that credits
// them again lights them up. The table below is the same list in words, and
// a click on a face goes to that person's row (the id renderWall gave it).
//
// Also here: a table picture that doesn't load turns into the name's letter.
(() => {
  // --- the table's pictures ---
  const letter = (img) => {
    const s = document.createElement("span");
    s.className = "cw-av";
    s.setAttribute("aria-hidden", "true");
    s.textContent = img.dataset.i || "";
    img.replaceWith(s);
  };
  document.addEventListener("error", (e) => {
    const t = e.target;
    if (t instanceof HTMLImageElement && t.classList.contains("cw-av")) letter(t);
  }, true);
  const swept = () => document.querySelectorAll("img.cw-av").forEach((i) => { if (i.complete && !i.naturalWidth) letter(i); });
  if (document.readyState === "complete") swept(); else addEventListener("load", swept);

  const root = document.getElementById("replay");
  if (!root || !window.fetch || !window.ResizeObserver) return;
  const lang = (document.documentElement.lang || "en").slice(0, 2);
  const WORDS = {
    en: {
      label: "How magpie was built: everyone credited, release by release",
      play: "Play", pause: "Pause", time: "Time", sound: "Sound",
      count: (p, r) => `<b>${p}</b> people · <b>${r}</b> releases`,
      who: (n, v) => `${n === 1 ? "1 release" : `${n} releases`} · first in v${v}`,
    },
    zh: {
      label: "magpie 是怎么建起来的：每个版本感谢了谁",
      play: "播放", pause: "暂停", time: "时间", sound: "声音",
      count: (p, r) => `<b>${p}</b> 人 · <b>${r}</b> 个版本`,
      who: (n, v) => `${n} 个版本 · 首次在 v${v}`,
    },
    ja: {
      label: "magpie ができるまで：リリースごとに名前が載った人",
      play: "再生", pause: "一時停止", time: "時間", sound: "サウンド",
      count: (p, r) => `<b>${p}</b> 人 · <b>${r}</b> リリース`,
      who: (n, v) => `${n} リリース · 初めは v${v}`,
    },
  };
  const W = WORDS[lang] || WORDS.en;
  const num = new Intl.NumberFormat(lang);
  const day = new Intl.DateTimeFormat(lang, { year: "numeric", month: "short", day: "numeric" });
  const still = matchMedia("(prefers-reduced-motion: reduce)");

  fetch("/contributors/replay.json")
    .then((r) => (r.ok ? r.json() : null))
    .then((d) => { if (d && Array.isArray(d.v) && d.v.length && Array.isArray(d.p) && d.p.length) start(d); })
    .catch(() => {});

  // strong ease-out (the site's curve for things appearing)
  const out = (x) => 1 - Math.pow(1 - x, 3.2);
  const TAU = Math.PI * 2;
  const POP = 220, PULSE = 420, FLY = 320; // ms
  // the clock is music's: 16 bars of sixteenths at 124 BPM, a step each
  // (contributors-music.js plays the same steps)
  const BPM = 124, STEPS = 256, BARS = STEPS / 16;
  const STEP_MS = 60000 / BPM / 4;
  const MAXPX = 44; // a place's size while there are few
  const HOLE = 1.6; // the middle, kept for the bird
  const SPRITE = 48; // device pixels a loaded face is kept at
  const KICK = .36; // a bar this intense has a beat (contributors-music.js AT.kick)

  function start(d) {
    const N = d.p.length, V = d.v.length;
    const END = d.t[V - 1] || 1;
    // events oldest first: minutes, person, release, and the step they land on
    const evT = [], evP = [], evV = [];
    for (const [vi, ...ps] of d.e) for (const p of ps) if (p >= 0 && p < N) { evT.push(d.t[vi]); evP.push(p); evV.push(vi); }
    const evS = Int16Array.from(evT, (t) => Math.min(STEPS - 1, Math.floor((t / END) * STEPS)));
    // what a face's card says, from the whole history
    const total = new Uint16Array(N), first = new Int32Array(N).fill(-1);
    for (const [vi, ...ps] of d.e) for (const p of ps) { if (p < 0 || p >= N) continue; total[p]++; if (first[p] < 0) first[p] = vi; }
    // each step's newcomers and returns, and each bar's intensity: how many
    // it credits, and how far the history has come
    const hits = Array.from({ length: STEPS }, () => [0, 0]);
    {
      const seen = new Uint8Array(N);
      evS.forEach((s, k) => { const p = evP[k]; if (seen[p]) hits[s][1]++; else { seen[p] = 1; hits[s][0]++; } });
    }
    const act = new Float64Array(BARS);
    hits.forEach(([n, m], s) => { act[s >> 4] += n + m / 2; });
    const actPeak = Math.max(1, ...act);
    const bars = Array.from(act, (a, b) => Math.min(1, .42 * Math.sqrt(a / actPeak) + .62 * (b / (BARS - 1))));
    bars[BARS - 1] = 1;
    const level = (p) => bars[Math.max(0, Math.min(BARS - 1, Math.floor(p / 16)))];
    // the bar under it: credits per day
    const days = Math.floor(END / 1440) + 1;
    const perDay = new Uint16Array(days);
    for (const t of evT) perDay[Math.min(days - 1, Math.floor(t / 1440))]++;
    const peak = Math.max(1, ...perDay);

    const ICON = (cls, body, stroke) => `<svg class="${cls}" viewBox="0 0 16 16" ${stroke ? 'fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"' : 'fill="currentColor"'} aria-hidden="true">${body}</svg>`;
    const SPEAKER = '<path fill="currentColor" stroke="none" d="M2.5 6.4c0-.3.2-.5.5-.5h2.1l3.4-2.6c.3-.2.8 0 .8.4v8.6c0 .4-.5.6-.8.4L5.1 10.1H3a.5.5 0 0 1-.5-.5Z"/>';
    root.innerHTML = `<div class="rp-stage" role="img"><canvas></canvas><div class="rp-bird"><img class="bird" src="/img/magpie-small.svg" alt="" width="40" height="40"></div><div class="rp-hud"><div><div class="rp-date"></div><div class="rp-ver mono"></div></div><div class="rp-count"></div></div><ol class="rp-feed" aria-hidden="true"></ol><div class="rp-tip" aria-hidden="true"></div></div>` +
      `<div class="rp-bar"><button class="icon-btn rp-play" type="button">${ICON("i-play", '<path d="M5 3.4v9.2c0 .4.4.6.7.4l7-4.6a.5.5 0 0 0 0-.8l-7-4.6c-.3-.2-.7 0-.7.4Z"/>')}${ICON("i-pause", '<rect x="4" y="3" width="2.6" height="10" rx=".8"/><rect x="9.4" y="3" width="2.6" height="10" rx=".8"/>')}</button>` +
      `<div class="rp-hist" role="slider" tabindex="0" aria-valuemin="0"><canvas></canvas></div>` +
      `<button class="icon-btn rp-sound" type="button" aria-pressed="false">${ICON("i-off", SPEAKER + '<path d="M11.4 6.3l3 3.4M14.4 6.3l-3 3.4"/>', true)}${ICON("i-on", SPEAKER + '<path d="M11.2 5.8a3.2 3.2 0 0 1 0 4.4M12.9 4.2a5.6 5.6 0 0 1 0 7.6"/>', true)}</button></div>`;
    const stage = root.querySelector(".rp-stage");
    stage.setAttribute("aria-label", W.label);
    const cv = stage.querySelector("canvas"), ctx = cv.getContext("2d");
    const bird = stage.querySelector(".rp-bird");
    const elDate = stage.querySelector(".rp-date"), elVer = stage.querySelector(".rp-ver"), elCount = stage.querySelector(".rp-count");
    const feed = stage.querySelector(".rp-feed"), tip = stage.querySelector(".rp-tip");
    const btn = root.querySelector(".rp-play"), hist = root.querySelector(".rp-hist"), snd = root.querySelector(".rp-sound");
    const hv = hist.querySelector("canvas"), hctx = hv.getContext("2d");
    hist.setAttribute("aria-label", W.time);
    hist.setAttribute("aria-valuemax", String(END));
    snd.setAttribute("aria-label", W.sound);
    snd.title = W.sound;
    root.hidden = false;
    // the music is its own file, fetched now so a click can start it at once
    if (window.AudioContext || window.webkitAudioContext) {
      const ms = document.createElement("script");
      ms.src = "/contributors-music.js";
      document.head.append(ms);
    } else snd.hidden = true;

    // colours, read again when the theme changes
    let C = {};
    const colours = () => {
      const s = getComputedStyle(document.documentElement);
      const v = (k) => s.getPropertyValue(k).trim();
      C = { ink: v("--ink"), ink2: v("--ink2"), ink3: v("--ink3"), line: v("--line"), code: v("--code"), card: v("--card") };
    };
    colours();

    // --- places: a hex lattice, nearest the middle first, stretched to the stage ---
    let w = 0, h = 0, dpr = 1, ax = 1, ay = 1, cells = [], cx = 0, cy = 0;
    const layout = () => {
      w = stage.clientWidth; h = stage.clientHeight; dpr = Math.min(2, devicePixelRatio || 1);
      cv.width = Math.round(w * dpr); cv.height = Math.round(h * dpr);
      const hw = hist.clientWidth, hh = hist.clientHeight;
      hv.width = Math.round(hw * dpr); hv.height = Math.round(hh * dpr);
      cx = w / 2; cy = h / 2 + 12;
      ax = Math.sqrt(Math.max(.5, w / Math.max(1, h - 24))); ay = 1 / ax;
      const R = Math.sqrt(N * 0.866 / Math.PI) + HOLE + 2;
      const list = [];
      const rows = Math.ceil((R * ay) / 0.866) + 1, cols = Math.ceil(R * ax) + 1;
      for (let r = -rows; r <= rows; r++) {
        const y = r * 0.866, off = (r & 1) * 0.5;
        for (let c = -cols; c <= cols; c++) {
          const x = c + off, e = Math.hypot(x / ax, y / ay);
          if (e >= HOLE) list.push({ x, y, e, a: Math.atan2(y, x) });
        }
      }
      list.sort((p, q) => p.e - q.e || p.a - q.a);
      cells = list.slice(0, N);
    };
    // pixels per lattice step that fit everyone placed up to e
    const fit = (e) => Math.min((w / 2 - 12) / ((e + .7) * ax), (h / 2 - 22) / ((e + .7) * ay));

    // --- faces ---
    // Pictures are asked for in the order the history needs them, a few at
    // a time, and only once the page has loaded and the replay is near the
    // screen: until then they would hold up the page's own load and its
    // table's pictures. Each is decoded off the main thread before it is
    // drawn.
    const faces = new Array(N); // a loaded face: { img, sprite, at }
    const queue = [];
    let qi = 0, inFlight = 0, open = false;
    const FLIGHTS = 16;
    const pump = () => {
      while (open && inFlight < FLIGHTS && qi < queue.length) {
        const i = queue[qi++], img = new Image();
        inFlight++;
        img.referrerPolicy = "no-referrer";
        img.decoding = "async";
        const done = () => { inFlight--; pump(); };
        img.onload = () => {
          (img.decode ? img.decode() : Promise.resolve()).catch(() => {}).then(() => {
            const sp = document.createElement("canvas");
            sp.width = sp.height = SPRITE;
            const g = sp.getContext("2d");
            g.beginPath(); g.arc(SPRITE / 2, SPRITE / 2, SPRITE / 2, 0, TAU); g.clip();
            g.drawImage(img, 0, 0, SPRITE, SPRITE);
            faces[i] = { img, sprite: sp, at: slot[i] >= 0 ? performance.now() : 0 };
            if (kept[i] === 1) fading.push(i);
            wake();
            done();
          });
        };
        img.onerror = done;
        img.src = d.p[i][1];
      }
      if (qi > 512 && qi === queue.length) { queue.length = 0; qi = 0; }
    };
    const load = (i) => {
      if (!d.p[i][1] || faces[i] !== undefined) return;
      faces[i] = null;
      queue.push(i);
      pump();
    };
    const initial = (name) => ((String(name).match(/[\p{L}\p{N}]/u) || ["?"])[0]).toUpperCase();

    // --- the history so far, by step ---
    let pos = 0, T = 0, ei = 0, vi = -1, placed = 0, ahead = 0;
    const slot = new Int32Array(N).fill(-1), order = [], born = new Float64Array(N), lit = new Float64Array(N);
    // what plays over the faces: rays to the people one release credits,
    // rings out from the bird on the beat, and a wave across
    // everyone every four bars once it is loud enough, and at the end
    let threads = [], rings = [], waves = [], cam = MAXPX, feedAt = 0, lastBeat = -1, lastPhrase = -1;
    const reset = () => {
      pos = 0; T = 0; ei = 0; ahead = 0; vi = -1; placed = 0; slot.fill(-1); order.length = 0; born.fill(-1e9); lit.fill(-1e9);
      threads = []; rings = []; waves = []; lastBeat = -1; lastPhrase = -1; feed.textContent = "";
    };
    reset();
    const advance = (p, now, quiet) => {
      pos = Math.min(STEPS, p);
      T = Math.min(END, (pos / STEPS) * END);
      const upto = pos >= STEPS ? STEPS : Math.floor(pos);
      if (ahead < ei) ahead = ei;
      while (ahead < evS.length && evS[ahead] <= upto + 20) load(evP[ahead++]);
      while (vi + 1 < V && d.t[vi + 1] <= T) vi++;
      let group = [], gv = -1;
      const tie = () => { if (group.length > 1 && threads.length < 40) threads.push({ ps: group.slice(0, 8), at: now, k: level(pos) }); group = []; };
      while (ei < evS.length && evS[ei] <= upto) {
        const i = evP[ei], v = evV[ei]; ei++;
        if (slot[i] < 0) {
          slot[i] = placed++; order.push(i); born[i] = quiet ? -1e9 : now; load(i);
          if (!quiet && now - feedAt > 140) { feedAt = now; arrive(d.p[i][0]); }
        } else if (!quiet) { if (now - lit[i] >= PULSE) litNow.push(i); lit[i] = now; }
        if (!quiet) { if (v !== gv) { tie(); gv = v; } group.push(i); }
      }
      if (!quiet) tie();
      if (quiet || still.matches) { lastBeat = Math.floor(pos / 4); lastPhrase = Math.floor(pos / 64); return; }
      // the beat: a ring a beat once the kick is in, a ring a bar before
      const beat = Math.floor(pos / 4);
      if (beat !== lastBeat && pos < STEPS) {
        lastBeat = beat;
        const k = level(pos);
        if (k >= KICK || (k >= .15 && beat % 4 === 0)) rings.push({ at: now, k });
      }
      const phrase = Math.floor(pos / 64);
      if (phrase !== lastPhrase) {
        lastPhrase = phrase;
        if (pos >= STEPS) waves.push({ at: now, k: 1, ms: 1400 });
        else if (phrase > 0 && level(pos) >= .5) waves.push({ at: now, k: level(pos) * .7, ms: 1000 });
      }
    };
    const seek = (p, now) => {
      p = Math.max(0, Math.min(STEPS, p));
      if (p < pos) reset();
      advance(p, now, true);
      cam = target();
      if (playing && music && sound) music.play(pos);
      wake();
    };
    const toStep = (t) => (t / END) * STEPS;
    const target = () => Math.min(MAXPX, fit(placed ? cells[Math.min(placed, cells.length) - 1].e : HOLE + 1));

    const arrive = (name) => {
      const li = document.createElement("li");
      // a long name is shortened, so the line stays clear of the faces
      const cut = [...String(name)];
      li.textContent = "+ " + (cut.length > 26 ? cut.slice(0, 25).join("") + "…" : name);
      feed.append(li);
      requestAnimationFrame(() => li.classList.add("shown"));
      const lis = feed.children;
      for (let k = 0; k < lis.length - 3; k++) if (!lis[k].classList.contains("going")) {
        const o = lis[k];
        o.classList.add("going");
        setTimeout(() => o.remove(), 160);
      }
    };

    // --- drawing ---
    // what the page shows in words is written only when it changes: the
    // page's own scripts and anything watching it see one change a day of
    // history, not one a frame
    let shown = { date: "", ver: "", count: "" };
    const hud = () => {
      const date = day.format(new Date((d.t0 + T * 60) * 1000));
      const ver = vi >= 0 ? "v" + d.v[vi] : "";
      const count = W.count(num.format(placed), num.format(vi + 1));
      if (date !== shown.date || ver !== shown.ver) {
        hist.setAttribute("aria-valuenow", String(Math.round(T)));
        hist.setAttribute("aria-valuetext", date + (ver ? " · " + ver : ""));
      }
      if (date !== shown.date) elDate.textContent = shown.date = date;
      if (ver !== shown.ver) elVer.textContent = shown.ver = ver;
      if (count !== shown.count) elCount.innerHTML = shown.count = count;
    };
    let histAt = -1;
    const drawHist = () => {
      const hw = hv.width / dpr, hh = hv.height / dpr;
      const x = Math.round((T / END) * hw * dpr) / dpr;
      if (x === histAt) return;
      histAt = x;
      hctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      hctx.clearRect(0, 0, hw, hh);
      const bw = hw / days, now = T / 1440;
      for (let k = 0; k < days; k++) {
        const bh = Math.max(1, (perDay[k] / peak) * (hh - 4));
        hctx.fillStyle = k < now ? C.ink3 : C.line;
        hctx.fillRect(k * bw + bw * .12, hh - bh, Math.max(1, bw * .76), bh);
      }
      hctx.fillStyle = C.ink;
      hctx.fillRect(Math.min(hw - 1.5, x), 0, 1.5, hh);
    };
    // a face as it is now: its picture, fading in over its place if it came
    // after the place was shown, or the empty place with its letter
    const fadeOf = (f, now) => (f && f.at ? (now - f.at) / 180 : 1);
    const face = (g, i, x, y, r, now) => {
      const f = faces[i];
      const fade = fadeOf(f, now);
      if (f && fade >= 1) {
        if (r * 2 * dpr > SPRITE) {
          g.save(); g.beginPath(); g.arc(x, y, r, 0, TAU); g.clip();
          g.drawImage(f.img, x - r, y - r, r * 2, r * 2);
          g.restore();
        } else g.drawImage(f.sprite, x - r, y - r, r * 2, r * 2);
        return;
      }
      if (f) {
        const a = g.globalAlpha;
        g.fillStyle = C.code; g.beginPath(); g.arc(x, y, r, 0, TAU); g.fill();
        g.globalAlpha = a * out(Math.max(0, fade));
        g.drawImage(f.sprite, x - r, y - r, r * 2, r * 2);
        g.globalAlpha = a;
        busy = true;
        return;
      }
      g.fillStyle = C.code;
      g.beginPath(); g.arc(x, y, r, 0, TAU); g.fill();
      if (r >= 3) { g.strokeStyle = C.line; g.lineWidth = 1; g.stroke(); }
      if (r >= 9) {
        g.fillStyle = C.ink3;
        g.font = `600 ${Math.round(r * .9)}px system-ui, sans-serif`;
        g.textAlign = "center"; g.textBaseline = "middle";
        g.fillText(initial(d.p[i][0]), x, y + r * .05);
      }
    };
    // where a newcomer is q of the way along its flight from the bird: a
    // shallow arc, bending one way or the other by who it is
    const flight = (i, c, s, q) => {
      const ex = c.x * s, ey = c.y * s, bend = (i & 1 ? .2 : -.2) * Math.sin(q * Math.PI);
      return [cx + ex * q - ey * bend, cy + ey * q + ex * bend];
    };

    // Everyone who has settled in their place is drawn once into a layer,
    // at the camera's scale then (s0), and the layer is drawn each frame
    // scaled by s / s0 around the middle: every place and size is in
    // proportion to the scale, so that is the same picture. Only what
    // moves is drawn again each frame: newcomers, the faces a ring or a wave
    // is passing, the lit ones and pictures fading in. The layer is drawn
    // again when the camera has moved more than SNAP from s0.
    const SNAP = .02, PAD = .08;
    const layer = document.createElement("canvas"), lg = layer.getContext("2d");
    let s0 = 0, lw = 0, lh = 0, upTo = 0;
    const kept = new Uint8Array(N); // 0 not in the layer, 1 as its empty place, 2 as its picture
    let fresh = [], fading = [], glowing = [], litNow = [];
    const settle = () => (still.matches ? POP : FLY);
    const keep = (n, now) => {
      const i = order[n], c = cells[n], r = s0 * .44;
      const x = cx + c.x * s0, y = cy + c.y * s0;
      const f = faces[i], whole = !!f && fadeOf(f, now) >= 1;
      if (x < -lw * PAD - r || y < -lh * PAD - r || x > lw * (1 + PAD) + r || y > lh * (1 + PAD) + r) { kept[i] = whole ? 2 : 1; return; }
      if (kept[i] === 1) {
        // the empty place goes, so its edge doesn't show around the picture
        lg.globalCompositeOperation = "destination-out";
        lg.beginPath(); lg.arc(x, y, r + .75, 0, TAU); lg.fill();
        lg.globalCompositeOperation = "source-over";
      }
      lg.globalAlpha = 1;
      face(lg, i, x, y, r, whole ? now : -1); // not whole yet: its empty place
      kept[i] = whole ? 2 : 1;
      if (f && !whole && !fading.includes(i)) fading.push(i);
    };
    const rebuild = (now) => {
      s0 = cam; lw = w; lh = h;
      const lpw = Math.round(w * (1 + 2 * PAD) * dpr), lph = Math.round(h * (1 + 2 * PAD) * dpr);
      if (layer.width !== lpw || layer.height !== lph) { layer.width = lpw; layer.height = lph; }
      lg.setTransform(dpr, 0, 0, dpr, w * PAD * dpr, h * PAD * dpr);
      lg.globalCompositeOperation = "source-over";
      lg.clearRect(-w * PAD, -h * PAD, w * (1 + 2 * PAD), h * (1 + 2 * PAD));
      kept.fill(0); fresh = []; fading = [];
      const S = settle(), M = Math.min(order.length, cells.length);
      for (upTo = 0; upTo < M; upTo++) if (now - born[order[upTo]] < S) fresh.push(upTo); else keep(upTo, now);
    };
    const unkept = () => { s0 = 0; };
    // the faces whose place is in [e0, e1): cells are nearest the middle first
    const from = (e, hi) => { let lo = 0; while (lo < hi) { const m = (lo + hi) >> 1; if (cells[m].e < e) lo = m + 1; else hi = m; } return lo; };

    let busy = false, seen = true, mark = new Uint32Array(N), stamp = 0;
    const draw = (now) => {
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w, h);
      const k = level(pos);
      // on the beat everything breathes, a little more the louder it is
      const beat = playing && !still.matches && k >= KICK ? Math.pow(Math.max(0, 1 - (pos % 4) / 1.6), 2) : 0;
      const s = cam * (1 + .01 * beat * k), r = cam * .44;
      busy = beat > 0;
      const M = Math.min(order.length, cells.length);
      const reach = placed ? cells[Math.min(placed, cells.length) - 1].e + 1 : HOLE + 1;

      rings = rings.filter((g) => now - g.at < 1100);
      threads = threads.filter((t) => now - t.at < 700);
      // how far out each ring is, in places from the middle
      const ringAt = rings.map((g) => { const q = (now - g.at) / 1100; return { q, e: HOLE * .8 + out(q) * reach * 1.15, k: g.k }; });
      waves = waves.filter((v) => now - v.at < v.ms);
      const waveAt = waves.map((v) => ({ e: out((now - v.at) / v.ms) * (reach + 2), k: v.k }));
      busy = busy || rings.length > 0 || threads.length > 0 || waves.length > 0;
      const glowOf = (e) => {
        let glow = 0;
        for (const v of waveAt) { const dd = v.e - e; if (dd > 0 && dd < 2) glow = Math.max(glow, Math.sin((Math.PI * dd) / 2) * v.k); }
        // a beat's ring lifts the faces it passes, a little
        for (const g of ringAt) { const dd = g.e - e; if (dd > 0 && dd < 1.4) glow = Math.max(glow, Math.sin((Math.PI * dd) / 1.4) * g.k * .4 * (1 - g.q)); }
        return glow;
      };

      // the layer: kept up to date, then drawn at this frame's scale
      if (!s0 || Math.abs(cam / s0 - 1) > SNAP || lw !== w || lh !== h || M < upTo) rebuild(now);
      const S = settle();
      if (fresh.length) {
        const still_ = [];
        for (const n of fresh) if (n < M && now - born[order[n]] >= S) keep(n, now); else if (n < M) still_.push(n);
        fresh = still_;
      }
      for (; upTo < M; upTo++) if (now - born[order[upTo]] < S) fresh.push(upTo); else keep(upTo, now);
      if (fading.length) {
        const left = [];
        for (const i of fading) {
          if (slot[i] < 0) continue;
          if (fadeOf(faces[i], now) >= 1) keep(slot[i], now); else left.push(i);
        }
        fading = left;
      }
      const z = s / s0;
      ctx.globalAlpha = 1;
      ctx.drawImage(layer, cx - (cx + w * PAD) * z, cy - (cy + h * PAD) * z, w * (1 + 2 * PAD) * z, h * (1 + 2 * PAD) * z);

      // over it, what moves: first the places a ring or a wave is passing
      stamp++;
      glowing = [];
      const band = (e0, e1) => { for (let n = from(e0, M); n < M && cells[n].e < e1; n++) if (mark[n] !== stamp) { mark[n] = stamp; glowing.push(n); } };
      for (const v of waveAt) band(v.e - 2, v.e);
      for (const g of ringAt) band(g.e - 1.4, g.e);
      const at = (n) => [cx + cells[n].x * s, cy + cells[n].y * s];
      for (const n of glowing) {
        const i = order[n];
        if (now - born[i] < S || kept[i] === 0) continue;
        const glow = glowOf(cells[n].e);
        if (glow <= .01) continue;
        const [x, y] = at(n), rr = r * (1 + .16 * glow);
        if (x < -rr || y < -rr || x > w + rr || y > h + rr) continue;
        ctx.globalAlpha = 1;
        face(ctx, i, x, y, rr, now);
        if (glow > .02) {
          ctx.globalAlpha = glow * .55; ctx.strokeStyle = C.ink; ctx.lineWidth = 1;
          ctx.beginPath(); ctx.arc(x, y, rr + 1.5, 0, TAU); ctx.stroke();
        }
      }
      // pictures that came in after their place was shown, fading in
      for (const i of fading) {
        const n = slot[i]; if (n < 0 || n >= M || now - born[i] < S) continue;
        const [x, y] = at(n), rr = r * (1 + .16 * glowOf(cells[n].e));
        ctx.globalAlpha = 1;
        face(ctx, i, x, y, rr, now);
      }
      // newcomers just landed (reduced motion: fading in where they are)
      const flying = [];
      for (const n of fresh) {
        const i = order[n], age = now - born[i];
        if (age < FLY && !still.matches) { flying.push(n); busy = true; continue; }
        const [x, y] = at(n), glow = glowOf(cells[n].e), rr = r * (1 + .16 * glow);
        busy = true;
        ctx.globalAlpha = age < POP ? out(Math.max(0, age) / POP) : 1;
        face(ctx, i, x, y, rr, now);
        if (glow > .02) {
          ctx.globalAlpha = glow * .55; ctx.strokeStyle = C.ink; ctx.lineWidth = 1;
          ctx.beginPath(); ctx.arc(x, y, rr + 1.5, 0, TAU); ctx.stroke();
        }
      }
      // the ones a release credits again: a ring that opens and goes
      litNow = litNow.filter((i) => now - lit[i] < PULSE);
      for (const i of litNow) {
        const n = slot[i]; if (n < 0 || n >= M || now - born[i] < S) continue;
        const q = (now - lit[i]) / PULSE, [x, y] = at(n), rr = r * (1 + .16 * glowOf(cells[n].e));
        ctx.globalAlpha = (1 - q) * .7;
        ctx.strokeStyle = C.ink;
        ctx.lineWidth = 1.25;
        ctx.beginPath(); ctx.arc(x, y, rr + 1.5 + (still.matches ? 0 : out(q) * s * .22), 0, TAU); ctx.stroke();
        busy = true;
      }
      // over the faces: the beat's rings, and the threads of one release
      ctx.strokeStyle = C.ink;
      for (const g of ringAt) {
        ctx.globalAlpha = Math.pow(1 - g.q, 2) * (.06 + .2 * g.k);
        ctx.lineWidth = .75 + .5 * g.k;
        ctx.beginPath(); ctx.ellipse(cx, cy, g.e * ax * s, g.e * ay * s, 0, 0, TAU); ctx.stroke();
      }
      // a release that credits several: a ray from the bird to each, drawn
      // out and gone again, bowed a little so they read as one gesture
      ctx.lineWidth = .75;
      for (const t of threads) {
        const q = (now - t.at) / 700, head = out(Math.min(1, q * 2.2)), tail = out(Math.max(0, q * 1.6 - .6));
        ctx.globalAlpha = (1 - q) * (.12 + .28 * t.k);
        for (const i of t.ps) {
          const c = cells[slot[i]]; if (!c) continue;
          const ex = c.x * s, ey = c.y * s, bx = -ey * .12, by = ex * .12;
          const at = (u) => [cx + ex * u + bx * 2 * u * (1 - u) * 2, cy + ey * u + by * 2 * u * (1 - u) * 2];
          ctx.beginPath();
          for (let j = 0; j <= 8; j++) { const [x, y] = at(tail + ((head - tail) * j) / 8); j ? ctx.lineTo(x, y) : ctx.moveTo(x, y); }
          ctx.stroke();
        }
      }
      // newcomers in flight, over everyone, each with a fading trail
      for (const n of flying) {
        const i = order[n], c = cells[n], q = out(Math.max(0, now - born[i]) / FLY);
        ctx.globalAlpha = (1 - q) * (.12 + .3 * k);
        ctx.strokeStyle = C.ink3; ctx.lineWidth = 1;
        ctx.beginPath();
        for (let j = 0; j <= 6; j++) { const [x, y] = flight(i, c, s, Math.max(0, q - .3) + (Math.min(q, .3) * j) / 6); j ? ctx.lineTo(x, y) : ctx.moveTo(x, y); }
        ctx.stroke();
        const [x, y] = flight(i, c, s, q);
        ctx.globalAlpha = Math.min(1, q * 2.5);
        face(ctx, i, x, y, r * (.55 + .45 * q), now);
      }
      ctx.globalAlpha = 1;
      const bs = Math.max(18, Math.min(40, s * 1.9)) * (1 + .07 * beat);
      const tf = `translate(${(cx - bs / 2).toFixed(2)}px, ${(cy - bs / 2).toFixed(2)}px) scale(${(bs / 40).toFixed(4)})`;
      if (tf !== birdAt) bird.style.transform = birdAt = tf;
      hud();
      drawHist();
    };
    let birdAt = "";

    // --- the clock: the music's when it plays, the page's otherwise ---
    let playing = false, last = 0, raf = 0, sound = false, music = null, actx = null;
    const setBtn = () => {
      btn.classList.toggle("on", playing);
      btn.setAttribute("aria-label", playing ? W.pause : W.play);
      btn.title = playing ? W.pause : W.play;
      snd.classList.toggle("on", sound);
      snd.setAttribute("aria-pressed", String(sound));
    };
    const frame = (now) => {
      raf = 0;
      const dt = Math.min(50, now - (last || now));
      last = now;
      if (playing) {
        advance(sound && music && music.playing ? music.pos() : pos + dt / STEP_MS, now, false);
        // the music rings on past the end by itself
        if (pos >= STEPS) { playing = false; setBtn(); }
      }
      const want = target();
      if (still.matches) cam = want;
      else cam += (want - cam) * (1 - Math.exp(-dt / 380));
      if (seen) draw(now);
      else { busy = false; hud(); }
      if (playing || (seen && (busy || Math.abs(want - cam) > .02))) raf = requestAnimationFrame(frame);
    };
    const wake = () => { if (!raf) { last = 0; raf = requestAnimationFrame(frame); } };
    const play = (on) => {
      if (on && pos >= STEPS) seek(0, performance.now());
      playing = on; setBtn();
      if (music && sound) on ? music.play(pos) : music.pause();
      wake();
    };
    let chosen = false; // the reader has played, paused or moved it
    btn.addEventListener("click", () => { chosen = true; play(!playing); });
    // sound on: the history from the start, unless it is playing now
    const withMusic = () => {
      if (!sound) return;
      if (!music) {
        if (!window.magpieMusic) { setTimeout(withMusic, 100); return; }
        music = window.magpieMusic(actx, { bpm: BPM, steps: STEPS, hits, bars });
      }
      if (playing) music.play(pos);
      else { seek(0, performance.now()); play(true); }
    };
    snd.addEventListener("click", () => {
      sound = !sound; setBtn(); chosen = true;
      if (!sound) { if (music) music.pause(); return; }
      // made and resumed inside the click, or Safari keeps it silent
      if (!actx) actx = new (window.AudioContext || window.webkitAudioContext)();
      actx.resume();
      withMusic();
    });
    setBtn();

    // --- the bar: drag or keys to move through time ---
    let dragging = false, was = false;
    const at = (e) => {
      const b = hist.getBoundingClientRect();
      return toStep(((e.clientX - b.left) / Math.max(1, b.width)) * END);
    };
    hist.addEventListener("pointerdown", (e) => {
      chosen = true; dragging = true; was = playing;
      if (playing) play(false);
      hist.setPointerCapture(e.pointerId);
      seek(at(e), performance.now());
    });
    hist.addEventListener("pointermove", (e) => { if (dragging) seek(at(e), performance.now()); });
    const drop = () => { if (!dragging) return; dragging = false; if (was && pos < STEPS) play(true); };
    hist.addEventListener("pointerup", drop);
    hist.addEventListener("pointercancel", drop);
    hist.addEventListener("keydown", (e) => {
      if (e.key in { ArrowLeft: 1, ArrowRight: 1, PageDown: 1, PageUp: 1, Home: 1, End: 1, " ": 1 }) chosen = true;
      const days = { ArrowLeft: -1, ArrowRight: 1, PageDown: -7, PageUp: 7 }[e.key];
      if (days) seek(toStep(T + days * 1440), performance.now());
      else if (e.key === "Home") seek(0, performance.now());
      else if (e.key === "End") seek(STEPS, performance.now());
      else if (e.key === " ") play(!playing);
      else return;
      e.preventDefault();
    });

    // --- a face: who it is, and their row on a click ---
    let tipFor = -1;
    const under = (e) => {
      const b = stage.getBoundingClientRect();
      const mx = e.clientX - b.left, my = e.clientY - b.top;
      const s = cam, wx = (mx - cx) / s, wy = (my - cy) / s;
      let best = -1, bd = .3;
      for (let k = 0; k < order.length && k < cells.length; k++) {
        const c = cells[k], dd = (c.x - wx) ** 2 + (c.y - wy) ** 2;
        if (dd < bd) { bd = dd; best = order[k]; }
      }
      return { i: best, mx, my };
    };
    const showTip = (i, mx, my) => {
      if (i < 0) { tip.classList.remove("on"); stage.classList.remove("hit"); tipFor = -1; return; }
      stage.classList.add("hit");
      if (i !== tipFor) {
        tipFor = i;
        tip.textContent = "";
        const [name, url] = d.p[i];
        let pic;
        if (url) {
          pic = new Image(32, 32);
          pic.referrerPolicy = "no-referrer";
          pic.className = "cw-av";
          pic.alt = "";
          pic.dataset.i = initial(name);
          pic.src = url;
        } else {
          pic = document.createElement("span");
          pic.className = "cw-av";
          pic.textContent = initial(name);
        }
        const text = document.createElement("div");
        const b = document.createElement("b");
        b.textContent = name;
        const sub = document.createElement("span");
        sub.textContent = W.who(total[i], first[i] >= 0 ? d.v[first[i]] : "");
        text.append(b, sub);
        tip.append(pic, text);
      }
      const tw = tip.offsetWidth, th = tip.offsetHeight;
      const left = mx + 14 + tw > w - 8 ? mx - 14 - tw : mx + 14;
      const top = Math.max(8, Math.min(h - th - 8, my - th - 10 < 8 ? my + 16 : my - th - 10));
      tip.style.left = Math.max(8, left) + "px";
      tip.style.top = top + "px";
      tip.style.transformOrigin = `${left < mx ? "right" : "left"} ${top < my ? "bottom" : "top"}`;
      tip.classList.add("on");
    };
    stage.addEventListener("pointermove", (e) => { if (e.pointerType === "mouse") { const u = under(e); showTip(u.i, u.mx, u.my); } });
    stage.addEventListener("pointerleave", () => showTip(-1));
    stage.addEventListener("click", (e) => {
      const u = under(e);
      if (u.i < 0) { showTip(-1); return; }
      // a tap first says who it is; a second tap on them goes to the row
      if (e.pointerType !== "mouse" && u.i !== tipFor) { showTip(u.i, u.mx, u.my); return; }
      if (d.p[u.i][2]) location.hash = encodeURIComponent(d.p[u.i][2]);
    });

    // --- size, theme, and starting when it is seen ---
    new ResizeObserver(() => { layout(); cam = target(); unkept(); histAt = -1; wake(); }).observe(stage);
    const recolour = () => { colours(); unkept(); histAt = -1; wake(); };
    new MutationObserver(recolour).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme", "class"] });
    matchMedia("(prefers-color-scheme: dark)").addEventListener?.("change", recolour);
    layout();
    // pictures once the page has loaded and the replay is within a screen
    // of being seen; frames only while any of it is on the screen
    let loaded = document.readyState === "complete", near = false;
    const gate = () => { if (!open && loaded && near) { open = true; pump(); } };
    if (!loaded) addEventListener("load", () => { loaded = true; gate(); }, { once: true });
    new IntersectionObserver((es) => {
      near = es[es.length - 1].isIntersecting; gate();
    }, { rootMargin: "100% 0px" }).observe(stage);
    new IntersectionObserver((es) => {
      seen = es[es.length - 1].isIntersecting;
      if (seen) wake();
    }).observe(stage);
    if (still.matches) {
      // nothing moves: everyone, as it is now
      seek(STEPS, performance.now());
    } else {
      seek(0, performance.now());
      // The first bars wait for the page to settle: its load, then the
      // analytics it loads after (PostHog, and its recorder half a second
      // later), then a run of smooth frames. Each of those is a long task
      // that, on a slower Mac in Safari, would stall the opening by a
      // tenth of a second or more. Never longer than SETTLE after load.
      const SETTLE = 3000, QUIET = 300;
      let calm = false, seenOnce = false;
      const settle = () => {
        let since = 0, ph = 0, smooth = 0, prev = 0;
        const tick = (t) => {
          since ||= t;
          // PostHog's stub has no __loaded until its own script has run
          const p = window.posthog;
          if (p && p.__loaded && !ph) ph = t;
          const analytics = !p || (ph && ((p.sessionRecording && p.sessionRecording.started) || t - ph > 1200));
          smooth = prev && t - prev < 40 ? smooth + (t - prev) : 0;
          prev = t;
          if ((analytics && smooth >= QUIET) || t - since > SETTLE) { calm = true; go(); }
          else requestAnimationFrame(tick);
        };
        requestAnimationFrame(tick);
      };
      // not if someone has already played, paused or moved it
      const go = () => { if (calm && seenOnce && !chosen) play(true); };
      if (document.readyState === "complete") settle(); else addEventListener("load", settle, { once: true });
      const io = new IntersectionObserver((es) => {
        if (es.some((x) => x.isIntersecting)) { io.disconnect(); seenOnce = true; go(); }
      }, { threshold: .35 });
      io.observe(stage);
    }
  }
})();
