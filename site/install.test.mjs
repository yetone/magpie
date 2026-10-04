import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, statSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const installer = fileURLToPath(new URL("./public/install.sh", import.meta.url));

function install(t, { termux = true, arch = "aarch64", override = false, prefixOnly = false, missing = false, corrupt = false, desktop = false, github = false, args = [], env: extra = {}, tamper = "" } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "magpie-install-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const tools = join(dir, "tools");
  const home = join(dir, "home");
  // forward slashes on Windows too: install.sh knows Termux by
  // */com.termux/files/usr, which a backslashed path never matches
  const prefix = join(dir, "com.termux/files/usr").replaceAll("\\", "/");
  const bin = override ? join(dir, "custom bin") : termux ? join(prefix, "bin") : join(home, ".local/bin");
  mkdirSync(tools, { recursive: true });
  mkdirSync(bin, { recursive: true });
  writeFileSync(join(bin, "magpie"), "old installation");
  const payload = "test binary\n";
  const cpu = arch === "aarch64" ? "arm64" : "amd64";
  const file = termux ? `magpie-cli-android-${cpu}` : desktop ? `magpie-linux-${cpu}` : `magpie-cli-linux-${cpu}`;
  const assets = {};
  // A Linux asset is present even when Android's is missing: never use it.
  for (const name of [file, `magpie-cli-linux-${cpu}`]) {
    if (missing && name === file) continue;
    assets[name] = { url: github ? `https://github.com/yetone/magpie-releases/releases/download/vtest/${name}` : `https://download.invalid/${name}`, sha256: corrupt ? "0".repeat(64) : createHash("sha256").update(payload).digest("hex") };
  }
  const requests = join(dir, "requests");
  writeFileSync(join(tools, "uname"), `#!/bin/sh\ncase "$1" in -m) echo '${arch}';; -s) echo Linux;; esac\n`, { mode: 0o755 });
  writeFileSync(join(tools, "ldconfig"), "#!/bin/sh\necho libwebkit2gtk-4.1.so.0\n", { mode: 0o755 });
  writeFileSync(join(tools, "curl"), `#!${process.execPath}
const fs = require("node:fs");
const args = process.argv.slice(2);
const url = args.at(-1);
fs.appendFileSync(process.env.INSTALL_REQUESTS, url + String.fromCharCode(10));
fs.appendFileSync(process.env.INSTALL_ARGS, JSON.stringify(args) + String.fromCharCode(10));
const tamper = process.env.INSTALL_TAMPER;
if (url.endsWith("/api/latest")) process.stdout.write(process.env.INSTALL_FEED);
else fs.writeFileSync(args[args.indexOf("-o") + 1], tamper && url.startsWith(tamper) ? "changed by the mirror" : process.env.INSTALL_PAYLOAD);
`, { mode: 0o755 });
  const env = {
    ...process.env,
    HOME: home,
    PREFIX: termux ? prefix : "",
    TERMUX_VERSION: termux && !prefixOnly ? "test" : "",
    MAGPIE_BIN_DIR: override ? bin : "",
    MAGPIE_CLI: desktop ? "" : "1",
    PATH: tools + ":" + process.env.PATH,
    XDG_CONFIG_HOME: join(dir, "config"),
    XDG_DATA_HOME: join(dir, "share"),
    INSTALL_REQUESTS: requests,
    INSTALL_FEED: JSON.stringify({ version: "test", assets }),
    INSTALL_PAYLOAD: payload,
    INSTALL_ARGS: join(dir, "args"),
    INSTALL_TAMPER: tamper,
    MAGPIE_PROXY: "",
    MAGPIE_MIRROR: "",
    ...extra,
  };
  const result = spawnSync("sh", [installer, ...args], { env, encoding: "utf8" });
  const read = (p) => (existsSync(p) ? readFileSync(p, "utf8") : "");
  const calls = read(env.INSTALL_ARGS).trim().split("\n").filter(Boolean).map((l) => JSON.parse(l));
  return { ...result, bin, file, payload, share: env.XDG_DATA_HOME, requests: read(requests), calls };
}

test("Termux downloads Android arm64 into PREFIX/bin even with WebKit installed", (t) => {
  const r = install(t, { desktop: true });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(readFileSync(join(r.bin, "magpie"), "utf8"), r.payload);
  // NTFS keeps no execute bit, so Windows reports none
  if (process.platform !== "win32") assert.ok(statSync(join(r.bin, "magpie")).mode & 0o111);
  assert.match(r.requests, /magpie-cli-android-arm64/);
  assert.doesNotMatch(r.requests, /magpie-linux-|icon-256/);
  assert.equal(existsSync(join(r.share, "applications/magpie.desktop")), false);
  assert.match(r.stdout, /run it: magpie web/);
  assert.doesNotMatch(r.stdout, /install WebKit/);
});

test("Termux prefix detection and amd64 target", (t) => {
  const r = install(t, { prefixOnly: true, arch: "x86_64" });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.requests, /magpie-cli-android-amd64/);
});

test("Termux respects a custom bin directory containing spaces", (t) => {
  const r = install(t, { override: true });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(readFileSync(join(r.bin, "magpie"), "utf8"), r.payload);
});

test("missing Android release never falls back to Linux", (t) => {
  const r = install(t, { missing: true });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /no magpie-cli-android-arm64.*build in Termux/);
  assert.equal(readFileSync(join(r.bin, "magpie"), "utf8"), "old installation");
  assert.equal(r.requests.trim().split("\n").length, 1);
});

test("a checksum mismatch leaves the existing installation intact", (t) => {
  const r = install(t, { corrupt: true });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /does not match its checksum/);
  assert.equal(readFileSync(join(r.bin, "magpie"), "utf8"), "old installation");
});

test("ordinary Linux still selects its CLI or desktop build", (t) => {
  for (const desktop of [false, true]) {
    const r = install(t, { termux: false, desktop });
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.requests, desktop ? /magpie-linux-arm64/ : /magpie-cli-linux-arm64/);
    assert.equal(existsSync(join(r.share, "applications/magpie.desktop")), desktop);
  }
});

// akic404 on Discord: installing without a way round the firewall. A proxy
// and a GitHub mirror can be given; neither is used unless given.
test("no proxy and no mirror unless given", (t) => {
  const r = install(t, { termux: false, github: true });
  assert.equal(r.status, 0, r.stderr);
  for (const a of r.calls) assert.ok(!a.includes("--proxy"), JSON.stringify(a));
  assert.match(r.requests, /^https:\/\/github\.com\/yetone\/magpie-releases\//m);
});

test("--proxy takes every download through it, as does MAGPIE_PROXY", (t) => {
  for (const [opts, want] of [
    [{ args: ["--proxy", "socks5h://127.0.0.1:1080"] }, "socks5h://127.0.0.1:1080"],
    [{ args: ["--proxy=http://127.0.0.1:7890"] }, "http://127.0.0.1:7890"],
    [{ env: { MAGPIE_PROXY: "http://10.0.0.1:3128" } }, "http://10.0.0.1:3128"],
  ]) {
    const r = install(t, { termux: false, desktop: true, github: true, ...opts });
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.calls.length, 3); // the feed, the file, the icon
    for (const a of r.calls) assert.equal(a[a.indexOf("--proxy") + 1], want, JSON.stringify(a));
  }
  const bad = install(t, { termux: false, args: ["--proxy", "ftp://x"] });
  assert.notEqual(bad.status, 0);
  assert.match(bad.stderr, /--proxy is an address/);
  assert.equal(bad.calls.length, 0);
});

test("--mirror fetches the file through the mirror, the feed and its checksum from the site", (t) => {
  const r = install(t, { termux: false, github: true, args: ["--mirror", "https://gh.mirror.example"] });
  assert.equal(r.status, 0, r.stderr);
  const [feed, file] = r.requests.trim().split("\n");
  assert.equal(feed, "https://usemagpie.ai/api/latest");
  assert.equal(file, "https://gh.mirror.example/https://github.com/yetone/magpie-releases/releases/download/vtest/magpie-cli-linux-arm64");
  assert.equal(readFileSync(join(r.bin, "magpie"), "utf8"), r.payload);
  assert.match(r.stdout, /through https:\/\/gh\.mirror\.example\//);

  const env = install(t, { termux: false, github: true, env: { MAGPIE_MIRROR: "https://gh.mirror.example/" } });
  assert.equal(env.status, 0, env.stderr);
  assert.match(env.requests, /^https:\/\/gh\.mirror\.example\/https:\/\/github\.com\//m);
});

test("a file the mirror changed is refused and the installed magpie kept", (t) => {
  const r = install(t, { termux: false, github: true, args: ["--mirror", "https://gh.mirror.example/"], tamper: "https://gh.mirror.example/" });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /from https:\/\/gh\.mirror\.example\/ does not match its checksum from https:\/\/usemagpie\.ai; not installed/);
  assert.equal(readFileSync(join(r.bin, "magpie"), "utf8"), "old installation");
  const bad = install(t, { termux: false, args: ["--mirror", "gh.mirror.example"] });
  assert.notEqual(bad.status, 0);
  assert.match(bad.stderr, /--mirror is an http\(s\) address/);
});
