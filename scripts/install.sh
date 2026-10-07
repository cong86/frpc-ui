#!/usr/bin/env bash
# Keep the complete entry point inside a function so a truncated pipe cannot start it.
main() (
  set -Eeuo pipefail
  umask 077
  console_version=v0.1.0-preview.10
  source_name=gitee
  frp_archive=''
  verify_only=false
  mode=auto
  die() { printf 'FRP 控制台：%s\n' "$*" >&2; exit 1; }
  while (($#)); do
    case "$1" in
      --version) (($# >= 2)) || die '--version 需要版本号'; console_version=$2; shift 2 ;;
      --source) (($# >= 2)) || die '--source 需要指定 gitee 或 github'; source_name=$2; shift 2 ;;
      --frp-archive) (($# >= 2)) || die '--frp-archive 需要本地归档路径'; frp_archive=$2; shift 2 ;;
      --verify-only) verify_only=true; shift ;;
      --mode) (($# >= 2)) || die '--mode 需要指定 new 或 adopt'; mode=$2; shift 2 ;;
      --help) printf '用法：bash install.sh [--mode new|adopt] [--source gitee|github] [--version vX.Y.Z[-suffix]] [--frp-archive /path/to/official.tar.gz] [--verify-only]\n'; exit 0 ;;
      *) die "未知参数：$1" ;;
    esac
  done
  [[ $console_version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9][a-zA-Z0-9.-]*)?$ ]] || die '发布版本号无效'
  [[ $mode == auto || $mode == new || $mode == adopt ]] || die '模式必须为 new（新安装）或 adopt（只读接入）'
  case "$source_name" in
    gitee) base="https://gitee.com/wangcong886/frpc-ui/releases/download/${console_version}" ;;
    github) base="https://github.com/cong86/frpc-ui/releases/download/${console_version}" ;;
    *) die '下载源必须为 gitee 或 github' ;;
  esac
  [[ $(uname -s) == Linux ]] || die '当前仅支持 Linux'
  [[ $(id -u) == 0 ]] || die '请以 root 执行；普通用户可使用 curl ... | sudo bash'
  for tool in curl sha256sum tar mktemp chmod stat systemctl grep cp; do
    command -v "$tool" >/dev/null || die "缺少必要工具：$tool"
  done
  case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die '当前仅支持 amd64 和 arm64 架构' ;; esac
  [[ -r /etc/os-release ]] || die '无法识别 Linux 发行版'
  . /etc/os-release
  [[ ${ID:-} == debian || ${ID:-} == ubuntu ]] || die '当前仅支持 Debian 和 Ubuntu'
  systemctl show --property=Version --value >/dev/null 2>&1 || die '需要正在运行的 systemd'
  if ! $verify_only; then
    { exec 3<>/dev/tty; } 2>/dev/null || die '需要交互终端；仅检查下载可使用 --verify-only'
    [[ -t 3 ]] || die '需要交互终端'
    if [[ $mode == auto ]]; then
      printf 'FRP 控制台安装方式：\n  1) 新安装客户端、服务端或两者\n  2) 接入已有 FRPS 与 Nginx（只读管理）\n请选择 [1]：' >&3
      IFS= read -r selection <&3 || die '未获得选项输入，未执行安装'
      case ${selection:-1} in 1) mode=new ;; 2) mode=adopt ;; *) die '请选择 1 或 2' ;; esac
    fi
  fi
  [[ $mode != auto ]] || mode=new
  temp=$(mktemp -d /var/tmp/frp-console-bootstrap.XXXXXXXX)
  trap 'rm -rf -- "$temp"' EXIT
  asset="frp-console_${console_version}_linux_${arch}.tar.gz"
  download() {
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 \
      --connect-timeout 15 --max-time 180 --retry 2 "${2:-$base}/$1" --output "$temp/$1" || die "下载失败，请检查网络连接或下载源：$1"
  }
  printf '正在下载 FRP 控制台 %s（Linux/%s，下载源：%s）…\n' "$console_version" "$arch" "$source_name"
  download SHA256SUMS
  download "$asset"
  verify() {
    local expected='' sum filename extra
    while read -r sum filename extra; do
      if [[ $filename == "$1" ]]; then
        [[ -z $expected && -z ${extra:-} && $sum =~ ^[0-9a-f]{64}$ ]] || die '摘要条目无效'
        expected=$sum
      fi
    done < "$temp/SHA256SUMS"
    [[ -n $expected ]] || die '发布清单缺少所需摘要'
    printf '%s  %s\n' "$expected" "$temp/$1" | sha256sum --check --status || die '摘要不匹配，未执行程序或安装'
  }
  verify "$asset"
  [[ $(tar -tzf "$temp/$asset") == frp-console ]] || die '归档包含不符合预期的文件'
  [[ $(tar -tvzf "$temp/$asset") == -* ]] || die '归档必须只包含一个普通程序文件'
  tar -xzf "$temp/$asset" --no-same-owner --no-same-permissions -C "$temp"
  [[ -f $temp/frp-console && ! -L $temp/frp-console && $(stat -c %h "$temp/frp-console") == 1 ]] || die '程序文件无效'
  chmod 0755 "$temp/frp-console"
  if [[ $mode == adopt ]]; then
    if $verify_only; then printf '接入所需的管理程序已下载并通过 SHA-256 校验；未下载官方 FRP，未执行安装。\n'; exit 0; fi
  else
    download FRP_VERSION
    verify FRP_VERSION
    frp_version=$(cat "$temp/FRP_VERSION")
    [[ $frp_version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die '官方 FRP 版本号无效'
    frp_asset="frp_${frp_version}_linux_${arch}.tar.gz"
    if [[ -n $frp_archive ]]; then
      [[ -f $frp_archive && ! -L $frp_archive && -r $frp_archive ]] || die 'FRP 归档必须是可读取的普通本地文件'
      cp -- "$frp_archive" "$temp/$frp_asset"
      printf '使用本地官方 FRP 归档，仍需通过摘要校验。\n'
    elif [[ $source_name == gitee ]]; then
      printf 'Gitee 不接受官方 FRP 附件，正在从官方 GitHub 下载；GitHub 不可达时可用 --frp-archive 指定本地归档。\n'
      download "$frp_asset" "https://github.com/fatedier/frp/releases/download/v${frp_version}"
    else
      download "$frp_asset"
    fi
    verify "$frp_asset"
    if $verify_only; then
      printf '下载及 SHA-256 校验通过，未执行安装。\n'
      exit 0
    fi
  fi
  [[ -d /root && ! -L /root && $(stat -c %u /root) == 0 ]] || die '需要归 root 所有的 /root 目录'
  root_mode=$(stat -c %a /root)
  (( (8#$root_mode & 8#022) == 0 )) || die '/root 不能允许其他用户写入'
  plan="/root/frp-console-install-${temp##*/}.json"
  printf '下载校验通过，正在启动安装向导；私有计划：%s\n' "$plan"
  if [[ $mode == adopt ]]; then
    "$temp/frp-console" install adopt-wizard --out "$plan" <&3 >&3 2>&3
  else
    "$temp/frp-console" install wizard --archive "$temp/$frp_asset" --out "$plan" <&3 >&3 2>&3
  fi
)
main "$@"
