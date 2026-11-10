// A provider's Headers can be filled from what a common client sends
// (l3123109756 on X). app.js draws the Presets button and imports this when
// it is pressed, so the list weighs nothing on the core. Each value is that
// client's own, cited above it; none imitates Claude Code. A client's name
// is not translated. Versions are the latest on 2026-11-11.
const HEADER_PRESETS = [
  // codex-rs login/src/auth/default_client.rs (originator, User-Agent); @openai/codex 0.167.0 on npm
  { name: "Codex CLI", headers: [["originator", "codex_cli_rs"], ["User-Agent", "codex_cli_rs/0.167.0 (Mac OS 26.6.0; arm64) Apple_Terminal/455"], ["version", "0.167.0"]] },
  // google-gemini/gemini-cli's Code Assist User-Agent; @google/gemini-cli 0.67.0 on npm
  { name: "Gemini CLI", headers: [["User-Agent", "GeminiCLI/0.67.0/gemini-2.5-pro (darwin; arm64; terminal)"]] },
  // packages/core/src/app.ts: opencode/${channel}/${version}/${name}; @opencode/cli 2.2.7 on npm
  { name: "OpenCode", headers: [["User-Agent", "opencode/latest/2.2.7/cli"]] },
  // Kilo-Org/kilocode request.ts and kilo-gateway headers.ts; @kilocode/cli 7.8.27 on npm
  { name: "Kilo Code", headers: [["HTTP-Referer", "https://kilocode.ai"], ["X-Title", "Kilo Code"], ["User-Agent", "Kilo-Code/7.8.27"], ["X-KILOCODE-EDITORNAME", "Kilo CLI 7.8.27"]] },
  // cline/cline sdk/packages/llms/src/providers/cline-client-headers.ts, desktop client
  { name: "Cline", headers: [["HTTP-Referer", "https://cline.bot"], ["X-Title", "Cline"], ["X-IS-MULTIROOT", "false"], ["X-CLIENT-TYPE", "cline-desktop"], ["X-CLIENT-VERSION", "3.5.54"], ["User-Agent", "Cline/3.5.54"]] },
  // RooCodeInc/Roo-Code src/api/providers/constants.ts DEFAULT_HEADERS; src/package.json 3.53.0
  { name: "Roo Code", headers: [["HTTP-Referer", "https://github.com/RooVetGit/Roo-Cline"], ["X-Title", "Roo Code"], ["User-Agent", "RooCode/3.53.0"]] },
  // CherryHQ/cherry-studio src/main/utils/http.ts defaultAppHeaders
  { name: "Cherry Studio", headers: [["HTTP-Referer", "https://cherry-ai.com"], ["X-Title", "Cherry Studio"]] },
];
// applyHeaderPreset adds a preset's headers to the draft: a header the draft
// already has with a value keeps the user's own, one with no value yet is
// filled, the rest become rows.
function applyHeaderPreset(rows, preset) {
  for (let i = rows.length - 1; i >= 0; i--) if (!(rows[i][0] || "").trim() && !(rows[i][1] || "").trim()) rows.splice(i, 1);
  for (const [k, v] of preset.headers) {
    const had = rows.find((r) => (r[0] || "").trim().toLowerCase() === k.toLowerCase());
    if (!had) rows.push([k, v, false]);
    else if (!(had[1] || "").trim()) had[1] = v;
  }
}
window.headerPresetsOpen = function open(b, rows, redraw) {
  openProtoMenu(b, HEADER_PRESETS.map((p) => ({ v: p.name, name: p.name, literalName: true, note: "" })), "", (v) => {
    applyHeaderPreset(rows, HEADER_PRESETS.find((p) => p.name === v));
    redraw();
  }, "Presets");
};
