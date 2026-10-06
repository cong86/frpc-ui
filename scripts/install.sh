#!/usr/bin/env bash
# Keep the complete entry point inside a function so a truncated pipe cannot start it.
main() (
  set -Eeuo pipefail
  umask 077
  console_version=v0.1.0-preview.2
  verify_only=false
  die() { printf 'FRP Console: %s\n' "$*" >&2; exit 1; }
  while (($#)); do
    case "$1" in
      --version) (($# >= 2)) || die '--version requires a value'; console_version=$2; shift 2 ;;
      --verify-only) verify_only=true; shift ;;
      --help) printf 'Usage: bash install.sh [--version vX.Y.Z[-suffix]] [--verify-only]\n'; exit 0 ;;
      *) die "Unknown option: $1" ;;
    esac
  done
  [[ $console_version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9][a-zA-Z0-9.-]*)?$ ]] || die 'Invalid release version'
  [[ $(uname -s) == Linux ]] || die 'Only Linux is supported'
  [[ $(id -u) == 0 ]] || die 'Run with curl ... | sudo bash'
  for tool in curl sha256sum tar mktemp chmod stat systemctl grep; do
    command -v "$tool" >/dev/null || die "Missing dependency: $tool"
  done
  case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die 'Only amd64 and arm64 are supported' ;; esac
  [[ -r /etc/os-release ]] || die 'Cannot identify this Linux distribution'
  . /etc/os-release
  [[ ${ID:-} == debian || ${ID:-} == ubuntu ]] || die 'Only Debian and Ubuntu are supported'
  systemctl show --property=Version --value >/dev/null 2>&1 || die 'A running systemd is required'
  if ! $verify_only; then
    { exec 3<>/dev/tty; } 2>/dev/null || die 'An interactive terminal is required; use --verify-only to check downloads'
    [[ -t 3 ]] || die 'An interactive terminal is required'
  fi
  temp=$(mktemp -d /var/tmp/frp-console-bootstrap.XXXXXXXX)
  trap 'rm -rf -- "$temp"' EXIT
  asset="frp-console_${console_version}_linux_${arch}.tar.gz"
  base="https://github.com/cong86/frpc-ui/releases/download/${console_version}"
  download() {
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 \
      --connect-timeout 15 --max-time 180 --retry 2 "$base/$1" --output "$temp/$1"
  }
  printf 'Downloading FRP Console %s (linux/%s)...\n' "$console_version" "$arch"
  download SHA256SUMS
  download "$asset"
  expected=''
  while read -r sum filename extra; do
    if [[ $filename == "$asset" ]]; then
      [[ -z $expected && -z ${extra:-} && $sum =~ ^[0-9a-f]{64}$ ]] || die 'Invalid checksum entry'
      expected=$sum
    fi
  done < "$temp/SHA256SUMS"
  [[ -n $expected ]] || die 'Release checksum missing'
  printf '%s  %s\n' "$expected" "$temp/$asset" | sha256sum --check --status || die 'Checksum mismatch; nothing executed'
  [[ $(tar -tzf "$temp/$asset") == frp-console ]] || die 'Unexpected archive members'
  [[ $(tar -tvzf "$temp/$asset") == -* ]] || die 'Archive must contain one regular binary'
  tar -xzf "$temp/$asset" --no-same-owner --no-same-permissions -C "$temp"
  [[ -f $temp/frp-console && ! -L $temp/frp-console && $(stat -c %h "$temp/frp-console") == 1 ]] || die 'Invalid binary'
  chmod 0755 "$temp/frp-console"
  if $verify_only; then
    printf 'Download and SHA-256 verified; no installation performed.\n'
    exit 0
  fi
  [[ -d /root && ! -L /root && $(stat -c %u /root) == 0 ]] || die 'A root-owned /root directory is required'
  mode=$(stat -c %a /root)
  (( (8#$mode & 8#022) == 0 )) || die '/root must not be writable by other users'
  plan="/root/frp-console-install-${temp##*/}.json"
  printf 'Download verified. Starting installation wizard; private plan: %s\n' "$plan"
  "$temp/frp-console" install wizard --out "$plan" <&3 >&3 2>&3
)
main "$@"
