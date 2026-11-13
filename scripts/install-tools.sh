#!/usr/bin/env bash
# Installs k3d and kubectl into .tools/bin (no sudo). Downloads are verified against the SHA-256 published by the
# projects. Docker itself must be installed by the administrator (sudo apt-get install docker.io on Ubuntu).
set -euo pipefail

K3D_VERSION="v5.9.0"
KUBECTL_VERSION="v1.37.1"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/.tools/bin"
mkdir -p "$BIN"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "unsupported CPU architecture: $(uname -m)" >&2; exit 1 ;;
esac
case "$os" in linux | darwin) ;; *) echo "unsupported OS: $os" >&2; exit 1 ;; esac

sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

fetch() { curl -fsSL --retry 3 --connect-timeout 15 -o "$2" "$1"; }

verify() { # file expected-hash name
  local got
  got="$(sha256 "$1")"
  if [ "$got" != "$2" ]; then
    echo "checksum mismatch for $3: expected $2, got $got" >&2
    rm -f "$1"
    exit 1
  fi
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if ! command -v k3d >/dev/null 2>&1 && [ ! -x "$BIN/k3d" ]; then
  echo "installing k3d $K3D_VERSION"
  base="https://github.com/k3d-io/k3d/releases/download/$K3D_VERSION"
  fetch "$base/k3d-$os-$arch" "$tmp/k3d"
  fetch "$base/checksums.txt" "$tmp/checksums.txt"
  want="$(grep " _dist/k3d-$os-$arch\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
  [ -n "$want" ] || { echo "no checksum for k3d-$os-$arch" >&2; exit 1; }
  verify "$tmp/k3d" "$want" k3d
  install -m 0755 "$tmp/k3d" "$BIN/k3d"
fi

if ! command -v kubectl >/dev/null 2>&1 && [ ! -x "$BIN/kubectl" ]; then
  echo "installing kubectl $KUBECTL_VERSION"
  base="https://dl.k8s.io/release/$KUBECTL_VERSION/bin/$os/$arch"
  fetch "$base/kubectl" "$tmp/kubectl"
  fetch "$base/kubectl.sha256" "$tmp/kubectl.sha256"
  verify "$tmp/kubectl" "$(cut -d' ' -f1 "$tmp/kubectl.sha256")" kubectl
  install -m 0755 "$tmp/kubectl" "$BIN/kubectl"
fi

echo "tools ready: $(command -v k3d || echo "$BIN/k3d"), $(command -v kubectl || echo "$BIN/kubectl")"
