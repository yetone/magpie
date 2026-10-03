import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, statSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const installer = fileURLToPath(new URL("./public/install.sh", import.meta.url));

function install(t, { termux = true, arch = "aarch64", override = false, prefixOnly = false, missing = false, corrupt = false, desktop = false } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "magpie-install-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const tools = join(dir, "tools");
  const home = join(dir, "home");
  const prefix = join(dir, "com.termux/files/usr");
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
    assets[name] = { url: `https://download.invalid/${name}`, sha256: corrupt ? "0".repeat(64) : createHash("sha256").update(payload).digest("hex") };
  }
  const requests = join(dir, "requests");
  writeFileSync(join(tools, "uname"), `#!/bin/sh\ncase "$1" in -m) echo '${arch}';; -s) echo Linux;; esac\n`, { mode: 0o755 });
  writeFileSync(join(tools, "ldconfig"), "#!/bin/sh\necho libwebkit2gtk-4.1.so.0\n", { mode: 0o755 });
  writeFileSync(join(tools, "curl"), `#!${process.execPath}
const fs = require("node:fs");
const args = process.argv.slice(2);
const url = args.at(-1);
fs.appendFileSync(process.env.INSTALL_REQUESTS, url + String.fromCharCode(10));
if (url.endsWith("/api/latest")) process.stdout.write(process.env.INSTALL_FEED);
else fs.writeFileSync(args[args.indexOf("-o") + 1], process.env.INSTALL_PAYLOAD);
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
  };
  const result = spawnSync("sh", [installer], { env, encoding: "utf8" });
  return { ...result, bin, file, payload, share: env.XDG_DATA_HOME, requests: readFileSync(requests, "utf8") };
}

test("Termux downloads Android arm64 into PREFIX/bin even with WebKit installed", (t) => {
  const r = install(t, { desktop: true });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(readFileSync(join(r.bin, "magpie"), "utf8"), r.payload);
  assert.ok(statSync(join(r.bin, "magpie")).mode & 0o111);
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
