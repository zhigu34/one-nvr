#!/bin/sh
set -eu
lock=/tmp/media-packages.lock
snapshot=${ONE_NVR_DEBIAN_SNAPSHOT_URL:-https://snapshot.debian.org/archive/debian/20260919T000000Z/}
case "$snapshot" in https://*/) ;; *) echo 'Snapshot source requires HTTPS and trailing slash' >&2; exit 1;; esac
case "$snapshot" in *[\ \;]*) echo 'Invalid snapshot source' >&2; exit 1;; esac
rm -f /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources
printf 'deb [check-valid-until=no signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] %s bookworm main\n' "$snapshot" > /etc/apt/sources.list.d/media.list
version=$(awk '$1=="ffmpeg" {print $2}' "$lock")
test -n "$version"
apt-get -o Acquire::Retries=3 -o Acquire::https::Timeout=30 update
apt-get -o Acquire::Retries=3 -o Acquire::https::Timeout=30 --download-only --no-install-recommends -y install "ffmpeg=$version"
for archive in /var/cache/apt/archives/*.deb; do
 test -f "$archive" || continue
 package=$(dpkg-deb -f "$archive" Package)
 package_version=$(dpkg-deb -f "$archive" Version)
 arch=$(dpkg-deb -f "$archive" Architecture)
 digest=$(sha256sum "$archive" | awk '{print $1}')
 awk -v p="$package" -v v="$package_version" -v a="$arch" -v h="$digest" '$1==p && $2==v && $3==a && $4==h {matched=1} END {exit !matched}' "$lock" || { echo 'Downloaded media package does not match lock' >&2; exit 1; }
done
apt-get --no-download --no-install-recommends -y install "ffmpeg=$version"
ffmpeg -version | head -n 1
ffprobe -version | head -n 1
rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb
