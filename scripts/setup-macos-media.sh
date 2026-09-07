#!/bin/sh
# Build the validated UnRAR CLI without changing system directories or Gatekeeper.
set -eu
if [ "$(uname -s)" != Darwin ]; then
    echo 'This setup is for native macOS. Linux containers include the tools.' >&2
    exit 1
fi
for tool in curl shasum make clang++; do
    command -v "$tool" >/dev/null || { echo "Missing $tool; install Xcode Command Line Tools." >&2; exit 1; }
done
work=$(mktemp -d "${TMPDIR:-/tmp}/local-dl-unrar.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
curl --fail --show-error --location --proto '=https' --proto-redir '=https' \
    https://www.rarlab.com/rar/unrarsrc-7.2.7.tar.gz -o "$work/source.tar.gz"
(cd "$work" && printf '%s  source.tar.gz\n' 01d903a7dcf413cb2925696d7796e48e38d471f79bfe7ef3ad2aebf6c12dbefd | shasum -a 256 -c -)
tar -xzf "$work/source.tar.gz" -C "$work"
make -C "$work/unrar" -j2 CXX=clang++
destination="$HOME/.local/share/local-dl/bin"
mkdir -p "$destination"
install -m 755 "$work/unrar/unrar" "$destination/unrar.new"
mv -f "$destination/unrar.new" "$destination/unrar"
install -m 644 "$work/unrar/license.txt" "$destination/unrar-license.txt"
printf '\nInstalled native %s UnRAR: %s/unrar\n' "$(uname -m)" "$destination"
missing=0
for tool in ffprobe ffmpeg; do
    if command -v "$tool" >/dev/null || [ -x "/opt/homebrew/bin/$tool" ] || [ -x "/usr/local/bin/$tool" ]; then
        printf '%s found\n' "$tool"
    else
        printf '%s missing: install ffmpeg with Homebrew, or set its explicit *_PATH.\n' "$tool" >&2
        missing=1
    fi
done
exit "$missing"
