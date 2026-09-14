#!/usr/bin/env bash
# water installer: curl -fsSL https://raw.githubusercontent.com/kranthi7899/water/main/install.sh | bash
# One binary. No Docker, no Python, no Node. Detects OS/arch, downloads the
# latest release, installs to ~/.local/bin (or /usr/local/bin with -g), and
# prints the next step.
set -euo pipefail

REPO="${WATER_REPO:-kranthi7899/water}"
VERSION="${WATER_VERSION:-latest}"
GLOBAL=0
# Private repository: export GITHUB_TOKEN (or GH_TOKEN) and the installer uses the
# GitHub API for both the release lookup and the asset download.
TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
auth=(); [ -n "$TOKEN" ] && auth=(-H "Authorization: Bearer $TOKEN")
for a in "$@"; do case "$a" in -g|--global) GLOBAL=1;; esac; done

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$os" in darwin|linux) ;; *) echo "water: unsupported OS: $os (darwin/linux only)" >&2; exit 1;; esac
case "$arch" in
  x86_64|amd64) arch=amd64;;
  arm64|aarch64) arch=arm64;;
  *) echo "water: unsupported architecture: $arch" >&2; exit 1;;
esac

api="https://api.github.com/repos/${REPO}/releases"
if [ "$VERSION" = "latest" ]; then
  rel="$(curl -fsSL "${auth[@]}" "${api}/latest" 2>/dev/null || curl -fsSL "${auth[@]}" "${api}?per_page=1" | sed 's/^\[//')"
  VERSION="$(printf '%s' "$rel" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  [ -n "$VERSION" ] || { echo "water: could not resolve the latest release (private repo? set GITHUB_TOKEN)" >&2; exit 1; }
else
  rel="$(curl -fsSL "${auth[@]}" "${api}/tags/${VERSION}")" || { echo "water: release ${VERSION} not found (private repo? set GITHUB_TOKEN)" >&2; exit 1; }
fi
ver="${VERSION#v}"
name="water_${ver}_${os}_${arch}.tar.gz"

# Asset download: the browser URL works for public repos; private repos need the
# API asset URL with an octet-stream Accept header.
asset_url() { # asset name
  if [ -n "$TOKEN" ]; then
    printf '%s' "$rel" | tr ',' '\n' | awk -v want="$1" '
      /"url": *"https:\/\/api\.github\.com\/repos\/[^"]*\/assets\/[0-9]+"/ { u=$0; sub(/.*"url": *"/, "", u); sub(/".*/, "", u) }
      /"name": *"/ { n=$0; sub(/.*"name": *"/, "", n); sub(/".*/, "", n); if (n == want && u != "") { print u; exit } }' 
  else
    echo "https://github.com/${REPO}/releases/download/${VERSION}/$1"
  fi
}
fetch() { # asset name, dest
  local u; u="$(asset_url "$1")"
  [ -n "$u" ] || { echo "water: asset $1 not found in release ${VERSION}" >&2; return 1; }
  curl -fsSL "${auth[@]}" -H "Accept: application/octet-stream" "$u" -o "$2"
}

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
echo "water: downloading ${name} from ${REPO} ${VERSION}"
fetch "$name" "$tmp/$name"
if fetch checksums.txt "$tmp/checksums.txt" 2>/dev/null; then
  want="$(grep " ${name}\$" "$tmp/checksums.txt" | awk '{print $1}')"
  if [ -n "$want" ]; then
    if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$tmp/$name" | awk '{print $1}')"; else got="$(shasum -a 256 "$tmp/$name" | awk '{print $1}')"; fi
    [ "$want" = "$got" ] || { echo "water: checksum mismatch" >&2; exit 1; }
  fi
fi
tar -xzf "$tmp/$name" -C "$tmp"

if [ "$GLOBAL" = 1 ]; then dest=/usr/local/bin; else dest="${XDG_BIN_HOME:-$HOME/.local/bin}"; fi
mkdir -p "$dest"
if [ -w "$dest" ]; then install -m 0755 "$tmp/water" "$dest/water"; else sudo install -m 0755 "$tmp/water" "$dest/water"; fi
echo "water: installed $("$dest/water" version 2>/dev/null || echo "$VERSION") to $dest/water"
case ":$PATH:" in *":$dest:"*) ;; *) echo "water: add $dest to your PATH, e.g.  export PATH=\"$dest:\$PATH\"";; esac
echo
echo "next:  water onboard"
