#!/usr/bin/env bash
# water installer: curl -fsSL https://raw.githubusercontent.com/kranthi7899/water/main/install.sh | bash
# One binary. No Docker, no Python, no Node. Detects OS/arch, downloads the
# latest release, installs to ~/.local/bin (or /usr/local/bin with -g), and
# prints the next step.
set -euo pipefail

REPO="${WATER_REPO:-kranthi7899/water}"
VERSION="${WATER_VERSION:-latest}"
GLOBAL=0
for a in "$@"; do case "$a" in -g|--global) GLOBAL=1;; esac; done

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$os" in darwin|linux) ;; *) echo "water: unsupported OS: $os (darwin/linux only)" >&2; exit 1;; esac
case "$arch" in
  x86_64|amd64) arch=amd64;;
  arm64|aarch64) arch=arm64;;
  *) echo "water: unsupported architecture: $arch" >&2; exit 1;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  [ -n "$VERSION" ] || { echo "water: could not resolve the latest release" >&2; exit 1; }
fi
ver="${VERSION#v}"
name="water_${ver}_${os}_${arch}.tar.gz"
url="https://github.com/${REPO}/releases/download/${VERSION}/${name}"
sums="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
echo "water: downloading ${url}"
curl -fsSL "$url" -o "$tmp/$name"
if curl -fsSL "$sums" -o "$tmp/checksums.txt" 2>/dev/null; then
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
