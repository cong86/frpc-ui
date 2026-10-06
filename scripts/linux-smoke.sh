#!/usr/bin/env bash
set -euo pipefail
project_root=$(cd "$(dirname "$0")/.." && pwd)
qa_root="$HOME/.local/share/frp-console-qa/20261007"
mkdir -p "$qa_root"
tar -xzf "$project_root/bin/frp_0.71.0_linux_amd64.tar.gz" -C "$qa_root"
export FRP_TEST_DIR="$qa_root/frp_0.71.0_linux_amd64"
"$FRP_TEST_DIR/frpc" --version
for name in config server filelock state templates; do
    chmod +x "$project_root/bin/$name-linux.test"
    "$project_root/bin/$name-linux.test" -test.v
done
chmod +x "$project_root/bin/frp-console-linux-amd64"
"$project_root/bin/frp-console-linux-amd64" --help
