#!/bin/sh
# magpie installer: curl -fsSL https://usemagpie.ai/install.sh | sh
#
# macOS: puts magpie.app in /Applications (~/Applications when that is not
# writable) and links the magpie command into ~/.local/bin.
# Linux: puts magpie in ~/.local/bin — the desktop app when GTK 3 and
# WebKitGTK 4.1 are installed (with a menu entry), the terminal build
# otherwise or with MAGPIE_CLI=1.
# Termux: installs the Android terminal build into $PREFIX/bin.
# Every download is checked against the release's SHA-256.
#
# Options (curl -fsSL https://usemagpie.ai/install.sh | sh -s -- --proxy …):
#   --proxy <url>      a proxy for every download: http://, https://,
#                      socks5:// or socks5h:// (MAGPIE_PROXY= does the same;
#                      without either, curl's own https_proxy / HTTPS_PROXY /
#                      ALL_PROXY are used as usual)
#   --mirror <prefix>  a GitHub download mirror: the release file is fetched
#                      from <prefix><its github.com URL> (MAGPIE_MIRROR= does
#                      the same). None is used unless given. The release
#                      list and its SHA-256 still come from usemagpie.ai,
#                      never from the mirror, so a file the mirror changed is
#                      refused.
set -eu

site=https://usemagpie.ai
bin="${MAGPIE_BIN_DIR:-$HOME/.local/bin}"
proxy="${MAGPIE_PROXY:-}"
mirror="${MAGPIE_MIRROR:-}"

say() { printf '  %s\n' "$*"; }
die() { printf 'magpie: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --proxy) [ $# -ge 2 ] || die "--proxy needs a value"; proxy=$2; shift 2 ;;
    --proxy=*) proxy=${1#--proxy=}; shift ;;
    --mirror) [ $# -ge 2 ] || die "--mirror needs a value"; mirror=$2; shift 2 ;;
    --mirror=*) mirror=${1#--mirror=}; shift ;;
    -h|--help)
      printf '%s\n' "usage: curl -fsSL $site/install.sh | sh -s -- [--proxy <url>] [--mirror <prefix>]" \
        "  --proxy <url>      http://, https://, socks5:// or socks5h:// proxy for the downloads (or MAGPIE_PROXY=)" \
        "  --mirror <prefix>  GitHub download mirror put before the release's github.com URL (or MAGPIE_MIRROR=);" \
        "                     the SHA-256 is still checked against $site's"
      exit 0 ;;
    *) die "unknown option $1 (--proxy <url>, --mirror <prefix>)" ;;
  esac
done
case "$proxy" in
  ''|http://?*|https://?*|socks5://?*|socks5h://?*) ;;
  *) die "--proxy is an address like http://127.0.0.1:7890 or socks5://127.0.0.1:1080, not $proxy" ;;
esac
case "$mirror" in
  '') ;;
  http://?*|https://?*) case "$mirror" in */) ;; *) mirror="$mirror/" ;; esac ;;
  *) die "--mirror is an http(s) address put before the github.com URL, like https://mirror.example/, not $mirror" ;;
esac

# get: curl, through the proxy when one was given
get() {
  if [ -n "$proxy" ]; then curl -fsSL --proxy "$proxy" "$@"; else curl -fsSL "$@"; fi
}

case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64) arch=amd64 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac
case "$(uname -s)" in
  Darwin) os=darwin; file="magpie-darwin-$arch.zip" ;;
  Linux)
    termux="${TERMUX_VERSION:-}"
    case "${PREFIX:-}" in */com.termux/files/usr) termux=1 ;; esac
    if [ -n "$termux" ]; then
      [ -n "${PREFIX:-}" ] || die "Termux requires PREFIX to locate its bin directory"
      os=android; file="magpie-cli-android-$arch"
      bin="${MAGPIE_BIN_DIR:-$PREFIX/bin}"
    else
      os=linux; file="magpie-cli-linux-$arch"
      if [ -z "${MAGPIE_CLI:-}" ] && { ldconfig -p 2>/dev/null | grep -q 'libwebkit2gtk-4\.1\.so\.0'; }; then
        file="magpie-linux-$arch"
      fi
    fi ;;
  *) die "unsupported system: $(uname -s); see $site" ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# the release list and its checksums: always from the site, never a mirror
feed=$(get "$site/api/latest") || die "could not reach $site${proxy:+ through $proxy}"
# The feed is one line of JSON; pull this file's url and hash out of it.
entry=$(printf '%s' "$feed" | sed -n "s/.*\"$file\":{\([^}]*\)}.*/\1/p")
url=$(printf '%s' "$entry" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')
sum=$(printf '%s' "$entry" | sed -n 's/.*"sha256":"\([^"]*\)".*/\1/p')
version=$(printf '%s' "$feed" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
if [ "$os" = android ] && { [ -z "$url" ] || [ -z "$sum" ]; }; then
  die "the newest release has no $file; build in Termux with go build -tags nogui (see $site/docs/start)"
fi
[ -n "$url" ] && [ -n "$sum" ] || die "the newest release has no $file"

from=$url
case "$url" in
  https://github.com/*) [ -z "$mirror" ] || from="$mirror$url" ;;
esac
if [ "$from" != "$url" ]; then say "downloading magpie $version ($file) through $mirror"
else say "downloading magpie $version ($file)"; fi
get -o "$tmp/$file" "$from" || die "could not download $from"
if command -v sha256sum >/dev/null; then got=$(sha256sum "$tmp/$file" | cut -d' ' -f1)
else got=$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1); fi
if [ "$got" != "$sum" ]; then
  [ "$from" = "$url" ] || die "$file from $mirror does not match its checksum from $site; not installed"
  die "$file does not match its checksum"
fi

mkdir -p "$bin"
if [ "$os" = darwin ]; then
  apps=/Applications
  [ -w "$apps" ] || { apps="$HOME/Applications"; mkdir -p "$apps"; }
  ditto -x -k "$tmp/$file" "$tmp/x"
  # the new app beside the old one first: a move failing (a full disk)
  # leaves the old app in place
  rm -rf "$apps/.magpie.app.new"
  mv "$tmp/x/magpie.app" "$apps/.magpie.app.new"
  rm -rf "$apps/magpie.app"
  mv "$apps/.magpie.app.new" "$apps/magpie.app"
  ln -sf "$apps/magpie.app/Contents/MacOS/magpie" "$bin/magpie"
  say "installed $apps/magpie.app"
else
  install -m 755 "$tmp/$file" "$bin/magpie"
  if [ "$file" = "magpie-linux-$arch" ]; then
    share="${XDG_DATA_HOME:-$HOME/.local/share}"
    mkdir -p "$share/applications" "$share/icons/hicolor/256x256/apps"
    get -o "$share/icons/hicolor/256x256/apps/magpie.png" "$site/img/icon-256.png" || true
    cat > "$share/applications/magpie.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=magpie
Comment=Every agent's model. One place.
Exec=$bin/magpie %u
Icon=magpie
Categories=Development;Utility;
MimeType=x-scheme-handler/magpie;
Terminal=false
EOF
    # magpie://import links open in magpie
    command -v update-desktop-database >/dev/null && update-desktop-database "$share/applications" 2>/dev/null || true
    command -v xdg-mime >/dev/null && xdg-mime default magpie.desktop x-scheme-handler/magpie 2>/dev/null || true
    say "installed the magpie desktop app (menu entry: magpie)"
  elif [ "$os" = android ]; then
    say "installed the Termux build; use magpie web for the browser UI or magpie tui for the terminal UI"
  else
    say "installed the terminal build; for the desktop app, install WebKitGTK 4.1 (libwebkit2gtk-4.1-0) and run this again"
  fi
fi
say "installed $bin/magpie"
case ":$PATH:" in
  *":$bin:"*) ;;
  *) say "add $bin to your PATH to run magpie from a terminal" ;;
esac
case "$file" in
  *.zip) say "open it: open -a magpie" ;;
  magpie-cli-android-*) say "run it: magpie web" ;;
  magpie-linux-*) say "open it from your app menu, or run: magpie app" ;;
  *) say "run it: magpie" ;;
esac
