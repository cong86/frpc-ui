#!/usr/bin/env bash
set -Eeuo pipefail
version=${1:?release version required}
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9][a-zA-Z0-9.-]*)?$ ]] || exit 1
[[ ! -e dist ]] || { printf 'dist already exists; use a fresh build directory\n' >&2; exit 1; }
mkdir dist
for arch in amd64 arm64; do
  mkdir "dist/linux_$arch"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags='-s -w' -o "dist/linux_$arch/frp-console" ./cmd/frp-console
  chmod 0755 "dist/linux_$arch/frp-console"
  tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -czf "dist/frp-console_${version}_linux_${arch}.tar.gz" -C "dist/linux_$arch" frp-console
done
cp scripts/install.sh dist/install.sh
printf 'version=%s\ncommit=%s\n' "$version" "$(git rev-parse HEAD)" > dist/BUILD_INFO.txt
(cd dist; sha256sum ./*.tar.gz install.sh BUILD_INFO.txt | sed 's@  ./@  @' > SHA256SUMS)
