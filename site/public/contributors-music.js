// The contributors replay's music (contributors-replay.js), made in the
// browser with Web Audio: nothing is downloaded, and nothing plays until the
// reader turns the sound on. 124 BPM in D minor, a chord a bar (Dm9, Bbmaj9,
// Fmaj9, C6/9). A sixteenth is one step of the replay, and the people a step
// brings in are that step's notes, so the faces land on the beat. How much
// plays follows each bar's intensity: how many people it credits, and how far
// the history has come. The last step ends on the first chord, held.
(() => {
  const mtof = (m) => 440 * Math.pow(2, (m - 69) / 12);
  const CHORDS = [
    { bass: 38, pad: [50, 53, 57, 60, 64] }, // Dm9
    { bass: 34, pad: [50, 53, 57, 58, 60] }, // Bbmaj9
    { bass: 41, pad: [53, 55, 57, 60, 64] }, // Fmaj9
    { bass: 36, pad: [50, 52, 55, 57, 60] }, // C6/9
  ];
  // the arpeggio's walk through a chord's upper tones, a sixteenth each
  const WALK = [0, 2, 4, 7, 5, 3, 6, 8, 4, 2, 5, 7, 9, 6, 3, 1];
  // a layer plays in a bar whose intensity (0..1) is at least this
  const AT = { bass: .18, kick: .36, hat: .5, clap: .64, ride: .8 };

  window.magpieMusic = function (ctx, { bpm, steps, hits, bars }) {
    const SD = 60 / bpm / 4; // a sixteenth, in seconds
    const level = (s) => bars[Math.max(0, Math.min(bars.length - 1, Math.floor(s / 16)))] || 0;
    const on = (layer, s) => level(s) >= AT[layer];

    // --- the room: compressor, a plate-like reverb and a dotted-eighth echo ---
    const out = ctx.createGain();
    out.gain.value = 0;
    const comp = ctx.createDynamicsCompressor();
    comp.threshold.value = -16; comp.knee.value = 8; comp.ratio.value = 3; comp.attack.value = .004; comp.release.value = .18;
    out.connect(comp).connect(ctx.destination);
    const verb = ctx.createConvolver();
    {
      const len = Math.round(ctx.sampleRate * 3.2), ir = ctx.createBuffer(2, len, ctx.sampleRate);
      for (let c = 0; c < 2; c++) { const d = ir.getChannelData(c); for (let i = 0; i < len; i++) d[i] = (Math.random() * 2 - 1) * Math.pow(1 - i / len, 3); }
      verb.buffer = ir;
    }
    const verbIn = ctx.createGain();
    verbIn.gain.value = .28;
    verbIn.connect(verb).connect(out);
    const pan = (v) => {
      if (!ctx.createStereoPanner) return ctx.createGain();
      const p = ctx.createStereoPanner(); p.pan.value = v; return p;
    };
    const echoIn = ctx.createGain(), echo = ctx.createDelay(2), fb = ctx.createGain(), tone = ctx.createBiquadFilter(), echoPan = pan(.3);
    echoIn.gain.value = .24; echo.delayTime.value = SD * 3; fb.gain.value = .34;
    tone.type = "lowpass"; tone.frequency.value = 3200;
    echoIn.connect(echo); echo.connect(tone).connect(fb).connect(echo);
    tone.connect(echoPan); echoPan.connect(out); echoPan.connect(verbIn);
    // the pad and the bass dip under each kick
    const duck = ctx.createGain();
    duck.connect(out);
    const padTone = ctx.createBiquadFilter();
    padTone.type = "lowpass"; padTone.Q.value = .6; padTone.frequency.value = 600;
    padTone.connect(duck); padTone.connect(verbIn);
    const noise = ctx.createBuffer(1, ctx.sampleRate, ctx.sampleRate);
    { const d = noise.getChannelData(0); for (let i = 0; i < d.length; i++) d[i] = Math.random() * 2 - 1; }

    const env = (g, t, peak, a, d) => {
      g.gain.setValueAtTime(.0001, t);
      g.gain.exponentialRampToValueAtTime(peak, t + a);
      g.gain.exponentialRampToValueAtTime(.0001, t + a + d);
    };
    const osc = (type, f, t, end) => { const o = ctx.createOscillator(); o.type = type; o.frequency.value = f; o.start(t); o.stop(end); return o; };
    const hiss = (t, end) => { const n = ctx.createBufferSource(); n.buffer = noise; n.loop = true; n.start(t, Math.random() * .5); n.stop(end); return n; };
    const filter = (type, f, q = .7) => { const b = ctx.createBiquadFilter(); b.type = type; b.frequency.value = f; b.Q.value = q; return b; };

    // --- the voices ---
    const pluck = (t, m, v, p) => {
      const g = ctx.createGain(); env(g, t, v, .004, .55);
      const f = filter("lowpass", 5200);
      f.frequency.setValueAtTime(5200, t); f.frequency.exponentialRampToValueAtTime(900, t + .4);
      const air = ctx.createGain(); air.gain.value = .3;
      osc("triangle", mtof(m), t, t + .62).connect(f);
      osc("sine", mtof(m + 12), t, t + .62).connect(air).connect(f);
      const pp = pan(p);
      f.connect(g).connect(pp);
      pp.connect(out); pp.connect(echoIn); pp.connect(verbIn);
    };
    const bell = (t, m, v, p) => {
      const g = ctx.createGain(); env(g, t, v, .003, 1.1);
      const pp = pan(p);
      osc("sine", mtof(m), t, t + 1.2).connect(g);
      const over = ctx.createGain(); env(over, t, v * .5, .002, .35);
      osc("sine", mtof(m) * 2.76, t, t + .4).connect(over).connect(pp);
      g.connect(pp); pp.connect(out); pp.connect(verbIn); pp.connect(echoIn);
    };
    const kick = (t, v) => {
      const o = ctx.createOscillator(), g = ctx.createGain();
      o.frequency.setValueAtTime(150, t); o.frequency.exponentialRampToValueAtTime(42, t + .13);
      g.gain.setValueAtTime(v, t); g.gain.exponentialRampToValueAtTime(.0001, t + .4);
      o.connect(g).connect(out); o.start(t); o.stop(t + .42);
      duck.gain.cancelScheduledValues(t);
      duck.gain.setValueAtTime(.32, t);
      duck.gain.linearRampToValueAtTime(1, t + SD * 3.4);
    };
    const hat = (t, v, open) => {
      const g = ctx.createGain(); env(g, t, v, .002, open ? .16 : .04);
      const pp = pan(open ? .18 : -.15);
      hiss(t, t + .22).connect(filter("highpass", 7600)).connect(g).connect(pp).connect(out);
    };
    const clap = (t, v) => {
      const g = ctx.createGain();
      g.gain.setValueAtTime(.0001, t);
      for (const k of [0, .011, .022]) { g.gain.setValueAtTime(v, t + k); g.gain.exponentialRampToValueAtTime(v * .2, t + k + .009); }
      g.gain.setValueAtTime(v, t + .033); g.gain.exponentialRampToValueAtTime(.0001, t + .24);
      hiss(t, t + .26).connect(filter("bandpass", 1700, .9)).connect(g);
      g.connect(out); g.connect(verbIn);
    };
    const bass = (t, m, len, v, s) => {
      const g = ctx.createGain(); env(g, t, v, .006, len);
      const f = filter("lowpass", 260 + 900 * level(s));
      osc("sawtooth", mtof(m), t, t + len + .05).connect(f);
      const sub = ctx.createGain(); sub.gain.value = 1.6;
      osc("sine", mtof(m), t, t + len + .05).connect(sub).connect(g);
      f.connect(g).connect(duck);
    };
    const pad = (t, chord, len, v) => {
      for (const m of chord.pad) for (const cents of [-8, 8]) {
        const g = ctx.createGain();
        g.gain.setValueAtTime(.0001, t);
        g.gain.exponentialRampToValueAtTime(v, t + .4);
        g.gain.setValueAtTime(v, t + len);
        g.gain.exponentialRampToValueAtTime(.0001, t + len + 1);
        const o = osc("sawtooth", mtof(m), t, t + len + 1.05);
        o.detune.value = cents;
        o.connect(g).connect(padTone);
      }
    };
    const riser = (t, len) => {
      const g = ctx.createGain();
      g.gain.setValueAtTime(.0001, t); g.gain.exponentialRampToValueAtTime(.09, t + len); g.gain.setValueAtTime(.0001, t + len + .01);
      const f = filter("bandpass", 300, 1.4);
      f.frequency.setValueAtTime(300, t); f.frequency.exponentialRampToValueAtTime(7000, t + len);
      hiss(t, t + len + .02).connect(f).connect(g);
      g.connect(out); g.connect(verbIn);
    };
    const crash = (t) => {
      const g = ctx.createGain(); env(g, t, .1, .003, 2.4);
      hiss(t, t + 2.5).connect(filter("highpass", 5200)).connect(g);
      g.connect(out); g.connect(verbIn);
    };

    // --- a step ---
    const tones = (c) => [...c.pad.map((m) => m + 12), ...c.pad.map((m) => m + 24)].sort((a, b) => a - b);
    let padFor = -1;
    const step = (s, t) => {
      if (s >= steps) return ending(t);
      const bar = Math.floor(s / 16), b = s % 16, I = level(s);
      const chord = CHORDS[bar % CHORDS.length];
      if (padFor !== bar) {
        padFor = bar;
        const left = (16 - b) * SD;
        pad(t, chord, left, .016 + .008 * I);
        padTone.frequency.setTargetAtTime(500 + 2800 * Math.pow(I, 1.4), t, .6);
      }
      if (b % 4 === 0 && on("kick", s)) kick(t, .85);
      if (on("bass", s)) {
        if (on("kick", s)) { if (b % 4 === 2) bass(t, chord.bass + 12, SD * 1.6, .16, s); }
        else if (b === 0) bass(t, chord.bass, SD * 14, .13, s);
      }
      if (on("hat", s) && b % 4 === 2) hat(t, .09, true);
      if (on("ride", s) && b % 2 === 1) hat(t, .035, false);
      if (on("clap", s) && (b === 4 || b === 12)) clap(t, .32);
      // the last bar before the end, and the bar before the kick first comes in
      if (b === 0 && (bar === bars.length - 2 || (!on("kick", s) && on("kick", s + 16)))) riser(t, SD * 16);
      const [n, m] = hits[s] || [0, 0];
      if (n) {
        const up = tones(chord), lift = Math.round(I * 2);
        const v = Math.min(.11, .045 + .02 * Math.log2(1 + n));
        pluck(t, up[Math.min(up.length - 1, WALK[b] + lift)], v, b % 2 ? .28 : -.28);
        if (n > 3) pluck(t + .004, up[Math.min(up.length - 1, WALK[b] + lift + 2)], v * .45, b % 2 ? -.2 : .2);
      }
      if (m && (b % 2 === 0 || m > 2)) {
        const up = tones(chord);
        bell(t, up[(WALK[(b + 5) % 16] + 3) % up.length] + 12, Math.min(.035, .012 + .006 * m), (b % 3 - 1) * .4);
      }
    };
    const ending = (t) => {
      const c = CHORDS[0];
      kick(t, .9); crash(t);
      pad(t, c, 2.6, .028);
      padTone.frequency.setTargetAtTime(3200, t, .2);
      padTone.frequency.setTargetAtTime(700, t + 1.2, 1.4);
      bass(t, c.bass - 12, 3.2, .2, steps - 1);
      tones(c).slice(0, 8).forEach((m, k) => pluck(t + k * SD * .5, m, .07 - k * .005, k % 2 ? .3 : -.3));
    };

    // --- the clock: steps scheduled a little ahead of the audio clock ---
    let playing = false, next = 0, anchorS = 0, anchorT = 0, timer = 0;
    const schedule = () => {
      const until = ctx.currentTime + .14;
      while (playing && next <= steps && anchorT + (next - anchorS) * SD < until) {
        step(next, Math.max(ctx.currentTime, anchorT + (next - anchorS) * SD));
        next++;
      }
      if (next > steps) { clearInterval(timer); timer = 0; }
    };
    const latency = () => (ctx.outputLatency || 0) + (ctx.baseLatency || 0);
    return {
      // play from step s (a fraction is fine); the replay's clock follows pos()
      play(s) {
        if (ctx.state !== "running") ctx.resume();
        playing = true;
        anchorS = s;
        anchorT = ctx.currentTime + .05;
        next = Math.ceil(s - 1e-6);
        padFor = -1;
        out.gain.cancelScheduledValues(ctx.currentTime);
        out.gain.setTargetAtTime(.8, ctx.currentTime, .02);
        clearInterval(timer);
        timer = setInterval(schedule, 25);
        schedule();
      },
      pause() {
        playing = false;
        clearInterval(timer); timer = 0;
        out.gain.cancelScheduledValues(ctx.currentTime);
        out.gain.setTargetAtTime(0, ctx.currentTime, .05);
      },
      pos() { return Math.max(anchorS, anchorS + (ctx.currentTime - latency() - anchorT) / SD); },
      get playing() { return playing; },
      // how hard the beat is landing at step s: 1 on a kick, falling to 0
      beat(s) { return on("kick", s) ? Math.pow(Math.max(0, 1 - (s % 4) / 1.6), 2) : 0; },
    };
  };
})();
