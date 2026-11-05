import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, statSync, rmSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const installer = fileURLToPath(new URL("./public/install.sh", import.meta.url));

// systemPath: a folder of links to what the script runs, without sudo, for
// a computer that has none
function systemPath(dir) {
  const sys = join(dir, "sys");
  mkdirSync(sys, { recursive: true });
  for (const n of ["sh", "dash", "bash", "sed", "head", "tr", "grep", "cut", "mktemp", "rm", "mkdir", "install", "cat", "sha256sum", "shasum", "ls", "rmdir", "mv", "ln", "chmod", "env", "perl"]) {
    const w = spawnSync("sh", ["-c", `command -v ${n}`], { encoding: "utf8" }).stdout.trim();
    if (w.startsWith("/")) symlinkSync(w, join(sys, n));
  }
  return sys;
}

function install(t, { termux = true, arch = "aarch64", override = false, prefixOnly = false, missing = false, corrupt = false, desktop = false, github = false, args = [], env: extra = {}, tamper = "", libs = desktop, cli = !desktop, distro = "ID=ubuntu\nID_LIKE=debian\n", sessions = [], login = "", tty = false, uid = 1000, sudo = true, pmFails = false, payload = "test binary\n" } = {}) {
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
  const cpu = arch === "aarch64" ? "arm64" : "amd64";
  const file = termux ? `magpie-cli-android-${cpu}` : desktop ? `magpie-linux-${cpu}` : `magpie-cli-linux-${cpu}`;
  const assets = {};
  // A Linux asset is present even when Android's is missing: never use it.
  for (const name of new Set([file, `magpie-cli-linux-${cpu}`, `magpie-linux-${cpu}`])) {
    if (missing && name === file) continue;
    assets[name] = { url: github ? `https://github.com/yetone/magpie-releases/releases/download/vtest/${name}` : `https://download.invalid/${name}`, sha256: corrupt ? "0".repeat(64) : createHash("sha256").update(payload).digest("hex") };
  }
  const requests = join(dir, "requests");
  writeFileSync(join(tools, "uname"), `#!/bin/sh\ncase "$1" in -m) echo '${arch}';; -s) echo Linux;; esac\n`, { mode: 0o755 });
  // ldconfig -p as glibc prints it: the libraries the desktop app needs
  // when libs, and another library always (a cache that was read)
  const cache = ["libc.so.6", ...(libs ? ["libgtk-3.so.0", "libwebkit2gtk-4.1.so.0"] : [])]
    .map((n) => `\t${n} (libc6,x86-64) => /usr/lib/x86_64-linux-gnu/${n}`).join("\n");
  writeFileSync(join(tools, "ldconfig.txt"), cache + "\n");
  writeFileSync(join(tools, "ldconfig"), `#!/bin/sh\ncat "${join(tools, "ldconfig.txt").replaceAll("\\", "/")}"\n`, { mode: 0o755 });
  // logind: the graphical session this user is logged in to, if any
  // (INSTALL_LOGIN "<id> <type>"); logind names a tty login when there is
  // no graphical one
  writeFileSync(join(tools, "loginctl"), `#!/bin/sh
set -- "$@" "" "" "" "" ""
id=\${INSTALL_LOGIN%% *} type=\${INSTALL_LOGIN#* }
[ "$1 $3 $4 $5" = "show-user -p Display --value" ] && [ -n "$id" ] && echo "$id"
[ "$1 $2 $3 $4 $5" = "show-session $id -p Type --value" ] && [ -n "$id" ] && echo "$type"
exit 0
`, { mode: 0o755 });
  // the package managers, sudo and id: each call logged ("sudo" before a
  // command sudo ran), and an install puts the libraries in ldconfig's
  // cache unless it fails
  const pmLog = join(dir, "pm-log");
  const ldTxt = join(tools, "ldconfig.txt").replaceAll("\\", "/");
  for (const pm of ["apt-get", "dnf", "pacman", "zypper"]) {
    writeFileSync(join(tools, pm), `#!/bin/sh
echo "\${INSTALL_SUDO:+sudo }${pm} $*" >> "$INSTALL_PM_LOG"
case "$*" in update|"") exit 0 ;; esac
[ -z "$INSTALL_PM_FAILS" ] || exit 100
[ "$INSTALL_PM_FAILS_ONCE" != 1 ] || [ -e "$INSTALL_PM_LOG.once" ] || { : > "$INSTALL_PM_LOG.once"; exit 100; }
printf '\\tlibgtk-3.so.0 (libc6,x86-64) => /usr/lib/libgtk-3.so.0\\n\\tlibwebkit2gtk-4.1.so.0 (libc6,x86-64) => /usr/lib/libwebkit2gtk-4.1.so.0\\n' >> "${ldTxt}"
`, { mode: 0o755 });
  }
  if (sudo) writeFileSync(join(tools, "sudo"), `#!/bin/sh
INSTALL_SUDO=1 exec "$@"
`, { mode: 0o755 });
  writeFileSync(join(tools, "id"), `#!/bin/sh
case "$1" in -u) echo ${uid};; *) echo tester;; esac
`, { mode: 0o755 });
  const dataDirs = join(dir, "data-dirs");
  for (const s of sessions) {
    mkdirSync(join(dataDirs, s.split("/")[0]), { recursive: true });
    writeFileSync(join(dataDirs, s), "[Desktop Entry]\n");
  }
  writeFileSync(join(dir, "os-release"), distro);
  // xdg-mime as xdg-utils writes the default: one line in mimeapps.list
  writeFileSync(join(tools, "xdg-mime"), `#!/bin/sh
[ "$1" = default ] || exit 0
mkdir -p "$XDG_CONFIG_HOME"
printf '[Default Applications]\\n' >> "$XDG_CONFIG_HOME/mimeapps.list"
printf '%s=%s\\n' "$3" "$2" >> "$XDG_CONFIG_HOME/mimeapps.list"
`, { mode: 0o755 });
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
    MAGPIE_CLI: cli ? "1" : "",
    // a headless shell unless a test gives it a desktop
    DISPLAY: "",
    WAYLAND_DISPLAY: "",
    XDG_CURRENT_DESKTOP: "",
    XDG_SESSION_DESKTOP: "",
    DESKTOP_SESSION: "",
    XDG_SESSION_TYPE: "",
    XDG_DATA_DIRS: dataDirs,
    INSTALL_LOGIN: login,
    MAGPIE_OS_RELEASE: join(dir, "os-release"),
    // no terminal unless a test gives it one: the run never reaches a real
    // sudo or package manager
    MAGPIE_TTY: tty ? join(dir, "tty") : join(dir, "no-tty"),
    INSTALL_PM_LOG: pmLog,
    INSTALL_PM_FAILS: pmFails ? "1" : "",
    PATH: tools + ":" + (sudo ? process.env.PATH : systemPath(dir)),
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
  if (tty) writeFileSync(join(dir, "tty"), "");
  const read = (p) => (existsSync(p) ? readFileSync(p, "utf8") : "");
  const run = (args) => {
    rmSync(requests, { force: true });
    rmSync(env.INSTALL_ARGS, { force: true });
    const result = spawnSync("sh", [installer, ...args], { env, encoding: "utf8" });
    const calls = read(env.INSTALL_ARGS).trim().split("\n").filter(Boolean).map((l) => JSON.parse(l));
    return { ...result, requests: read(requests), calls, pm: read(pmLog) };
  };
  return { ...run(args), run, bin, file, payload, home, env, share: env.XDG_DATA_HOME, config: env.XDG_CONFIG_HOME };
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
    // the dock binds the window (app_id/WM_CLASS "magpie") to the entry (#2626)
    if (desktop) assert.match(readFileSync(join(r.share, "applications/magpie.desktop"), "utf8"), /^StartupWMClass=magpie$/m);
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

// pota on Discord: the script installed magpie and had no way to take it
// off. --uninstall takes off what it put there and keeps the user's
// settings; --purge has magpie delete them too. Both have magpie
// disconnect the agents first: taken off alone, it left Codex pointed at
// a gateway that was gone (whimsical_wolf_15477 on Discord).
//
// magpie itself, as far as the uninstall asks it anything: <who> in each
// call it logs; old, one from before --keep-data
const fakeMagpie = (who, old = false) => `#!/bin/sh
echo "${who}$*" >> "$HOME/magpie-calls"
[ "$1 $2" = "uninstall --help" ] && { echo "usage: magpie uninstall [--yes]${old ? "" : " [--keep-data]"}"; exit 0; }
[ "$1 $2" = "daemon uninstall" ] && rm -f "$XDG_CONFIG_HOME/systemd/user/magpie.service"
[ "$1 $2 $3" = "uninstall --yes --keep-data" ] && { [ -z "$MAGPIE_FAIL" ] || exit 1; ${old ? "exit 2" : "echo disconnected > \"$HOME/agents\""}; }
[ "$1 $2 $3" = "uninstall --yes " ] && { [ -z "$MAGPIE_FAIL" ] || exit 1; echo disconnected > "$HOME/agents"; rm -rf "$XDG_CONFIG_HOME/magpie" "$XDG_DATA_HOME/applications/magpie.desktop"; }
exit 0
`;
function installedDesktop(t, opts = {}) {
  // sudo: false, the system's tools only: a magpie on this computer's
  // PATH would be another one
  const r = install(t, { termux: false, desktop: true, sudo: false, payload: fakeMagpie("downloaded: "), ...opts });
  assert.equal(r.status, 0, r.stderr);
  const exe = join(r.bin, "magpie");
  writeFileSync(exe, fakeMagpie("", opts.old), { mode: 0o755 });
  const settings = join(r.config, "magpie", "settings.json");
  mkdirSync(join(r.config, "magpie"), { recursive: true });
  writeFileSync(settings, "{}");
  const calls = () => (existsSync(join(r.home, "magpie-calls")) ? readFileSync(join(r.home, "magpie-calls"), "utf8") : "");
  const agents = () => existsSync(join(r.home, "agents"));
  return { ...r, exe, settings, calls, agents, entry: join(r.share, "applications/magpie.desktop"), icon: join(r.share, "icons/hicolor/256x256/apps/magpie.png"), mime: join(r.config, "mimeapps.list") };
}

test("--uninstall takes off the command, menu entry, icon and magpie:// default, and keeps the settings", (t) => {
  const r = installedDesktop(t);
  for (const p of [r.exe, r.entry, r.icon]) assert.ok(existsSync(p), p);
  writeFileSync(r.mime, readFileSync(r.mime, "utf8") + "text/html=firefox.desktop\n");
  assert.match(readFileSync(r.mime, "utf8"), /^x-scheme-handler\/magpie=magpie\.desktop$/m);
  const u = r.run(["--uninstall"]);
  assert.equal(u.status, 0, u.stderr);
  for (const p of [r.exe, r.entry, r.icon]) assert.equal(existsSync(p), false, p);
  assert.equal(readFileSync(r.mime, "utf8"), "[Default Applications]\ntext/html=firefox.desktop\n");
  // with no line of the user's, the list xdg-mime made goes with it
  const bare = installedDesktop(t);
  const bu = bare.run(["--uninstall"]);
  assert.equal(bu.status, 0, bu.stderr);
  assert.equal(existsSync(bare.mime), false);
  assert.ok(existsSync(r.settings), "the settings stay");
  assert.match(r.calls(), /^uninstall --yes --keep-data$/m, "magpie disconnects the agents first");
  assert.ok(r.agents());
  assert.doesNotMatch(r.calls(), /^uninstall --yes$/m);
  assert.equal(u.calls.length, 0, "nothing is downloaded");
  assert.match(u.stdout, /kept your settings.*--purge/);
  // again: nothing there, nothing wrong
  const again = r.run(["--uninstall"]);
  assert.equal(again.status, 0, again.stderr);
  assert.match(again.stdout, /no magpie installed by this script/);
});

// magpie deleted by hand (or the app dragged to the Trash) left the agents
// on its gateway, with nothing left to put them back: the newest magpie is
// downloaded, checked, and does it, and nothing of it stays.
test("--uninstall with magpie deleted already downloads one to disconnect the agents", (t) => {
  const r = installedDesktop(t);
  rmSync(r.exe);
  const u = r.run(["--uninstall"]);
  assert.equal(u.status, 0, u.stderr);
  assert.match(u.requests, /\/api\/latest\n.*magpie-cli-linux-arm64$/m, "the terminal build, which needs no libraries");
  assert.match(r.calls(), /^downloaded: uninstall --yes --keep-data$/m);
  assert.ok(r.agents());
  assert.ok(existsSync(r.settings), "the settings stay");
  for (const p of [r.entry, r.icon]) assert.equal(existsSync(p), false, p);
  assert.equal(existsSync(r.exe), false, "the download isn't installed");

  // --purge the same, deleting the settings
  const p = installedDesktop(t);
  rmSync(p.exe);
  const pu = p.run(["--purge"]);
  assert.equal(pu.status, 0, pu.stderr);
  assert.match(p.calls(), /^downloaded: uninstall --yes$/m);
  assert.equal(existsSync(p.settings), false);

  // a download that isn't the release's: never run, nothing taken off
  const c = installedDesktop(t, { env: {} });
  rmSync(c.exe);
  c.env.INSTALL_PAYLOAD = "changed\n";
  const cu = c.run(["--uninstall"]);
  assert.notEqual(cu.status, 0);
  assert.match(cu.stderr, /does not match its checksum/);
  assert.equal(c.calls(), "");
  assert.ok(existsSync(c.entry));

  // magpie never run here (no settings): nothing to put back, nothing downloaded
  const n = installedDesktop(t);
  rmSync(n.exe);
  rmSync(join(n.config, "magpie"), { recursive: true });
  const nu = n.run(["--uninstall"]);
  assert.equal(nu.status, 0, nu.stderr);
  assert.equal(nu.calls.length, 0);
});

// another magpie on the PATH (not this script's) serves the agents still:
// it is the one to ask, and nothing is downloaded
test("--uninstall with magpie deleted leaves another magpie's agents to it", (t) => {
  const r = installedDesktop(t);
  rmSync(r.exe);
  const other = join(r.env.PATH.split(":")[0], "magpie");
  writeFileSync(other, fakeMagpie("other: "), { mode: 0o755 });
  const u = r.run(["--uninstall"]);
  assert.equal(u.status, 0, u.stderr);
  assert.equal(u.calls.length, 0);
  assert.equal(r.calls(), "");
  assert.match(u.stdout, /magpie at .*tools\/magpie isn't one this script installed.*uninstall/);
});

// one from before --keep-data refuses it and changes nothing: the newest
// does it instead
test("--uninstall with an older magpie disconnects with the newest", (t) => {
  const r = installedDesktop(t, { old: true });
  const u = r.run(["--uninstall"]);
  assert.equal(u.status, 0, u.stderr);
  assert.doesNotMatch(r.calls(), /^uninstall --yes --keep-data$/m);
  assert.match(r.calls(), /^downloaded: uninstall --yes --keep-data$/m);
  assert.ok(r.agents());
  assert.ok(existsSync(r.settings));
  assert.equal(existsSync(r.exe), false);
});

// magpie unable to (an agent it couldn't put back, an app still open): the
// command stays, to run it again
test("--uninstall leaves the install when magpie can't disconnect the agents", (t) => {
  const f = installedDesktop(t, { env: { MAGPIE_FAIL: "1" } });
  const fu = f.run(["--uninstall"]);
  assert.notEqual(fu.status, 0);
  assert.match(fu.stderr, /couldn't disconnect your agents/);
  for (const p of [f.exe, f.entry, f.settings]) assert.ok(existsSync(p), p);
});

test("--uninstall takes the service and login entry that start this magpie, never another's", (t) => {
  const r = installedDesktop(t);
  const unit = join(r.config, "systemd/user/magpie.service");
  const login = join(r.config, "autostart/magpie.desktop");
  mkdirSync(join(r.config, "systemd/user"), { recursive: true });
  mkdirSync(join(r.config, "autostart"), { recursive: true });
  writeFileSync(unit, `[Service]\nExecStart="${r.exe}" serve\n`);
  writeFileSync(login, `[Desktop Entry]\nExec="${r.exe}" --autostart\n`);
  const u = r.run(["--uninstall"]);
  assert.equal(u.status, 0, u.stderr);
  assert.equal(existsSync(unit), false);
  assert.equal(existsSync(login), false);
  assert.match(r.calls(), /^daemon uninstall$/m);

  // another magpie's (one still there) are left alone
  const o = installedDesktop(t);
  const other = join(o.home, "elsewhere/magpie");
  mkdirSync(join(o.home, "elsewhere"), { recursive: true });
  writeFileSync(other, "", { mode: 0o755 });
  mkdirSync(join(o.config, "systemd/user"), { recursive: true });
  mkdirSync(join(o.config, "autostart"), { recursive: true });
  const ounit = join(o.config, "systemd/user/magpie.service");
  const ologin = join(o.config, "autostart/magpie.desktop");
  writeFileSync(ounit, `[Service]\nExecStart="${other}" serve\n`);
  writeFileSync(ologin, `[Desktop Entry]\nExec="${other}" --autostart\n`);
  writeFileSync(o.entry, `[Desktop Entry]\nExec=${other} %u\n`);
  const ou = o.run(["--uninstall"]);
  assert.equal(ou.status, 0, ou.stderr);
  for (const p of [ounit, ologin, o.entry]) assert.ok(existsSync(p), p);
  assert.equal(existsSync(o.exe), false);
});

test("--purge has magpie uninstall itself first, then takes off the install", (t) => {
  const r = installedDesktop(t);
  const u = r.run(["--purge"]);
  assert.equal(u.status, 0, u.stderr);
  assert.match(r.calls(), /^uninstall --yes$/m);
  for (const p of [r.exe, r.entry, r.icon, r.settings]) assert.equal(existsSync(p), false, p);
  // magpie took the menu entry itself; its icon and magpie:// default go too
  assert.equal(existsSync(r.mime), false);
  assert.doesNotMatch(u.stdout, /kept your settings/);

  // magpie failing to: the command stays, to run it again
  const f = installedDesktop(t, { env: { MAGPIE_FAIL: "1" } });
  const fu = f.run(["--uninstall", "--purge"]);
  assert.notEqual(fu.status, 0);
  assert.match(fu.stderr, /magpie uninstall didn't finish/);
  for (const p of [f.exe, f.entry, f.settings]) assert.ok(existsSync(p), p);

});

test("--uninstall in Termux takes off PREFIX/bin/magpie", (t) => {
  const r = install(t, {});
  assert.equal(r.status, 0, r.stderr);
  const u = r.run(["--uninstall"]);
  assert.equal(u.status, 0, u.stderr);
  assert.equal(existsSync(join(r.bin, "magpie")), false);
  assert.match(u.stdout, /removed .*com\.termux\/files\/usr\/bin\/magpie/);
});

// shuhai on Discord: on a Linux desktop the script installed the terminal
// build, with one line at the end about WebKitGTK. The build is chosen with
// nothing asked: the desktop app when its libraries are there; on a
// desktop without them, the terminal build and, said last, the command
// that installs them on this distribution; --gui and --tui choose instead.
function linux(t, opts) {
  const r = install(t, { termux: false, cli: false, ...opts });
  assert.equal(r.status, 0, r.stderr);
  const gui = /magpie-linux-arm64/.test(r.requests);
  assert.notEqual(gui, /magpie-cli-linux-arm64/.test(r.requests), r.requests);
  assert.equal(existsSync(join(r.share, "applications/magpie.desktop")), gui);
  return { ...r, gui };
}
const last = (r, n) => r.stdout.trimEnd().split("\n").slice(-n).join("\n");

test("Linux with GTK 3 and WebKitGTK 4.1 gets the desktop app, with nothing to install", (t) => {
  for (const env of [{ DISPLAY: ":0" }, { WAYLAND_DISPLAY: "wayland-0" }, {}]) {
    const r = linux(t, { libs: true, env });
    assert.ok(r.gui, JSON.stringify(env));
    assert.doesNotMatch(r.stdout, /WebKitGTK|apt install/);
  }
});

test("a desktop without WebKitGTK gets the terminal build and its distribution's command, said last", (t) => {
  const desktops = [
    { env: { DISPLAY: ":0" } },
    { env: { WAYLAND_DISPLAY: "wayland-0" } },
    { env: { XDG_CURRENT_DESKTOP: "XFCE" } },
    { env: { XDG_SESSION_TYPE: "wayland" } },
    // ssh into a computer whose user is logged in to its desktop
    { login: "2 x11" },
    { login: "c3 wayland" },
    // or that has a desktop to log in to
    { sessions: ["xsessions/xfce.desktop"] },
    { sessions: ["wayland-sessions/gnome.desktop"] },
  ];
  for (const opts of desktops) {
    const r = linux(t, { libs: false, ...opts });
    assert.equal(r.gui, false, JSON.stringify(opts));
    assert.match(r.stdout, /has a desktop, but the desktop app needs GTK 3 and WebKitGTK 4\.1/);
    assert.match(last(r, 4), /sudo apt install libwebkit2gtk-4\.1-0/, JSON.stringify(opts));
    assert.match(last(r, 4), /install\.sh \| sh -s -- --gui/);
  }
  for (const [distro, want] of [
    ["ID=fedora\n", /sudo dnf install webkit2gtk4\.1/],
    ['ID="linuxmint"\nID_LIKE="ubuntu debian"\n', /sudo apt install libwebkit2gtk-4\.1-0/],
    ["ID=endeavouros\nID_LIKE=arch\n", /sudo pacman -S --needed webkit2gtk-4\.1/],
    ['ID="opensuse-tumbleweed"\nID_LIKE="opensuse suse"\n', /sudo zypper install libwebkit2gtk-4_1-0/],
    ["ID=nixos\n", /install WebKitGTK 4\.1 \(webkit2gtk 4\.1\) from your distribution/],
  ]) {
    const r = linux(t, { libs: false, distro, env: { DISPLAY: ":0" } });
    assert.match(r.stdout, want, distro);
  }
});

test("a headless Linux without WebKitGTK gets the terminal build and no desktop advice", (t) => {
  // no login at all, or a tty login (an ssh shell's, on a server or in WSL)
  for (const login of ["", "135 tty"]) {
    const r = linux(t, { libs: false, login });
    assert.equal(r.gui, false);
    assert.match(r.stdout, /installed the terminal build/);
    assert.doesNotMatch(r.stdout, /WebKitGTK|apt install|--gui/, login);
  }
});

test("--gui installs the desktop app before its libraries, and says what installs them", (t) => {
  const r = linux(t, { libs: false, args: ["--gui"] });
  assert.ok(r.gui);
  assert.match(last(r, 2), /aren't installed; it opens once they are/);
  assert.match(last(r, 2), /sudo apt install libwebkit2gtk-4\.1-0/);
  // the flag wins over MAGPIE_CLI=1
  const over = linux(t, { libs: true, cli: true, args: ["--gui"] });
  assert.ok(over.gui);
  assert.doesNotMatch(over.stdout, /aren't installed/);
});

test("--tui and MAGPIE_CLI=1 install the terminal build on a desktop with WebKitGTK", (t) => {
  for (const opts of [{ args: ["--tui"] }, { cli: true }]) {
    const r = linux(t, { libs: true, env: { DISPLAY: ":0" }, ...opts });
    assert.equal(r.gui, false, JSON.stringify(opts));
    assert.doesNotMatch(r.stdout, /WebKitGTK/);
  }
  const help = install(t, { termux: false, args: ["--help"] });
  assert.equal(help.status, 0);
  assert.match(help.stdout, /--gui .*desktop app/);
  assert.match(help.stdout, /--tui .*terminal build/);
});

// shuhai on Discord: "为什么不能帮用户直接装好？" A desktop without
// WebKitGTK 4.1 has it installed with the distribution's package manager,
// through sudo, when someone is at the terminal (curl | sh: /dev/tty, not
// stdin), and then gets the desktop app. Anything else keeps the terminal
// build and the command.
test("a desktop without WebKitGTK at a terminal has it installed through sudo and gets the desktop app", (t) => {
  for (const [distro, want] of [
    ["ID=ubuntu\nID_LIKE=debian\n", "sudo apt-get install -y libwebkit2gtk-4.1-0"],
    ["ID=fedora\n", "sudo dnf install -y webkit2gtk4.1"],
    ["ID=endeavouros\nID_LIKE=arch\n", "sudo pacman -S --needed --noconfirm webkit2gtk-4.1"],
    ['ID="opensuse-tumbleweed"\nID_LIKE="opensuse suse"\n', "sudo zypper --non-interactive install libwebkit2gtk-4_1-0"],
  ]) {
    const r = linux(t, { libs: false, tty: true, distro, env: { DISPLAY: ":0" } });
    assert.ok(r.gui, distro + r.stdout);
    assert.equal(r.pm, want + "\n", distro);
    // what and why, said before sudo asks
    assert.match(r.stdout, new RegExp(`needs WebKitGTK 4\\.1, so installing it: ${want.replace(/[.+]/g, "\\$&")} \\(sudo may ask for your password\\)`));
    assert.doesNotMatch(r.stdout, /aren't installed|didn't work|run this again/);
  }
});

test("as root the package manager runs without sudo", (t) => {
  const r = linux(t, { libs: false, tty: true, uid: 0, env: { DISPLAY: ":0" } });
  assert.ok(r.gui);
  assert.equal(r.pm, "apt-get install -y libwebkit2gtk-4.1-0\n");
  assert.doesNotMatch(r.stdout, /sudo/);
});

test("with no terminal, no sudo, an unknown distribution or a failed install, the terminal build and the command", (t) => {
  for (const opts of [
    { tty: false },
    { tty: true, sudo: false },
    { tty: true, distro: "ID=void\n" },
    { tty: true, distro: "ID=nixos\n" },
  ]) {
    const r = linux(t, { libs: false, env: { DISPLAY: ":0" }, ...opts });
    assert.equal(r.gui, false, JSON.stringify(opts));
    assert.equal(r.pm, "", JSON.stringify(opts));
    assert.doesNotMatch(r.stdout, /installing it|didn't work/);
    assert.match(r.stdout, /has a desktop, but the desktop app needs GTK 3 and WebKitGTK 4\.1, which aren't installed/);
  }
  const f = linux(t, { libs: false, tty: true, pmFails: true, env: { DISPLAY: ":0" } });
  assert.equal(f.gui, false);
  // apt read its lists again and tried once more
  assert.equal(f.pm, "sudo apt-get install -y libwebkit2gtk-4.1-0\nsudo apt-get update\nsudo apt-get install -y libwebkit2gtk-4.1-0\n");
  assert.match(last(f, 5), /installing GTK 3 and WebKitGTK 4\.1 for the desktop app didn't work/);
  assert.match(last(f, 4), /sudo apt install libwebkit2gtk-4\.1-0/);
});

test("a stale apt list is read again and the install tried once more", (t) => {
  const r = linux(t, { libs: false, tty: true, env: { DISPLAY: ":0", INSTALL_PM_FAILS_ONCE: "1" } });
  assert.ok(r.gui, r.stdout);
  assert.equal(r.pm, "sudo apt-get install -y libwebkit2gtk-4.1-0\nsudo apt-get update\nsudo apt-get install -y libwebkit2gtk-4.1-0\n");
});

test("--gui installs WebKitGTK at a terminal too; --tui, MAGPIE_CLI=1 and a headless computer never do", (t) => {
  const g = linux(t, { libs: false, tty: true, args: ["--gui"] });
  assert.ok(g.gui);
  assert.equal(g.pm, "sudo apt-get install -y libwebkit2gtk-4.1-0\n");
  assert.doesNotMatch(g.stdout, /aren't installed/);
  for (const opts of [{ args: ["--tui"], env: { DISPLAY: ":0" } }, { cli: true, env: { DISPLAY: ":0" } }, {}]) {
    const r = linux(t, { libs: false, tty: true, ...opts });
    assert.equal(r.gui, false, JSON.stringify(opts));
    assert.equal(r.pm, "", JSON.stringify(opts));
  }
});
