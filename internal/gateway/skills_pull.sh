#!/bin/sh
# Pulls the skills in the Library of the magpie at @BASE@ into this
# computer's agents' folders. Run it again to bring them up to date:
#   export MAGPIE_KEY=<a gateway key of that magpie>
#   curl -fsSL -H "Authorization: Bearer $MAGPIE_KEY" @BASE@/v1/magpie/skills/pull.sh | sh
# It writes to ~/.agents/skills, and to Claude Code's skills folder when
# Claude Code is here; MAGPIE_SKILLS_DIRS (folders joined by :) says others.
# A folder of your own is never written over, nor one you changed after
# it was pulled: each pulled skill has a .magpie-pulled saying where it
# came from and what it held.
set -eu

base='@BASE@'
key=${MAGPIE_KEY:-}
if [ -z "$key" ]; then
	echo "magpie: set MAGPIE_KEY to a gateway key of $base first" >&2
	exit 1
fi
if [ -n "${MAGPIE_SKILLS_DIRS:-}" ]; then
	dirs=$MAGPIE_SKILLS_DIRS
else
	dirs=$HOME/.agents/skills
	claude=${CLAUDE_CONFIG_DIR:-$HOME/.claude}
	if [ -d "$claude" ]; then
		dirs=$dirs:$claude/skills
	fi
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/magpie-skills.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' INT TERM HUP
code=$(curl -sS -H "Authorization: Bearer $key" -A magpie-skills-pull -D "$tmp/head" -o "$tmp/skills.tar.gz" -w '%{http_code}' "$base/v1/magpie/skills.tar.gz") || {
	echo "magpie: couldn't reach $base; nothing was changed" >&2
	exit 1
}
if [ "$code" != 200 ]; then
	why=$(sed -n 's/.*"message":"\([^"]*\)".*/\1/p' "$tmp/skills.tar.gz" | head -n 1)
	why=${why%.}
	echo "magpie: $base answered $code${why:+: $why}; nothing was changed" >&2
	exit 1
fi
mkdir "$tmp/new"
tar -xzf "$tmp/skills.tar.gz" -C "$tmp/new"
if [ ! -f "$tmp/new/.magpie-skills" ]; then
	echo "magpie: $base sent no list of its skills; nothing was changed" >&2
	exit 1
fi

# sum is what a skill's folder holds, its mark left out
sum() {
	(cd "$1" && find . -type f ! -name .magpie-pulled -exec cksum {} + | LC_ALL=C sort | cksum)
}
mark() {
	sed -n "s/^$2=//p" "$1/.magpie-pulled" 2>/dev/null | head -n 1
}
listed() {
	grep -qxF "$1" "$tmp/new/.magpie-skills"
}

got=0 kept=0 gone=0
oldifs=$IFS
IFS=:
for d in $dirs; do
	IFS=$oldifs
	[ -n "$d" ] || continue
	mkdir -p "$d"
	for s in "$tmp/new"/*/; do
		[ -d "$s" ] || continue
		name=$(basename "$s")
		t=$d/$name
		new=$(sum "$s")
		if [ -L "$t" ]; then
			echo "kept $t: a link, not magpie's"
			kept=$((kept + 1))
			continue
		fi
		if [ -e "$t" ]; then
			if [ ! -f "$t/.magpie-pulled" ]; then
				echo "kept $t: yours, not pulled from magpie"
				kept=$((kept + 1))
				continue
			fi
			if [ "$(mark "$t" from)" != "$base" ]; then
				echo "kept $t: pulled from $(mark "$t" from)"
				kept=$((kept + 1))
				continue
			fi
			if [ "$(sum "$t")" != "$(mark "$t" sum)" ]; then
				echo "kept $t: changed here since it was pulled"
				kept=$((kept + 1))
				continue
			fi
			if [ "$new" = "$(mark "$t" sum)" ]; then
				continue
			fi
			rm -rf "$t"
		fi
		rm -rf "$t.magpie-new"
		cp -R "$s" "$t.magpie-new"
		printf 'from=%s\nsum=%s\n' "$base" "$new" >"$t.magpie-new/.magpie-pulled"
		mv "$t.magpie-new" "$t"
		echo "pulled $t"
		got=$((got + 1))
	done
	for t in "$d"/*/; do
		t=${t%/}
		[ -d "$t" ] && [ ! -L "$t" ] && [ -f "$t/.magpie-pulled" ] || continue
		name=$(basename "$t")
		if listed "$name" || [ "$(mark "$t" from)" != "$base" ]; then
			continue
		fi
		if [ "$(sum "$t")" != "$(mark "$t" sum)" ]; then
			echo "kept $t: gone from magpie's Library, but changed here"
			kept=$((kept + 1))
			continue
		fi
		rm -rf "$t"
		echo "removed $t: gone from magpie's Library"
		gone=$((gone + 1))
	done
	IFS=:
done
IFS=$oldifs

left=$(sed -n 's/^[Xx]-[Mm]agpie-[Ss]kills-[Ll]eft: *//p' "$tmp/head" | tr -d '\r')
if [ -n "$left" ]; then
	echo "too big to pull: $left"
fi
echo "magpie: $got pulled, $gone removed, $kept kept as they were ($(grep -c . "$tmp/new/.magpie-skills" || true) skills in $base's Library)"
