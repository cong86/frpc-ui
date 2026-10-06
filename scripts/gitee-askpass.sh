#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  *Username*) printf '%s\n' wangcong886 ;;
  *Password*) printf '%s\n' "${GITEE_TOKEN:?GITEE_TOKEN required}" ;;
  *) exit 1 ;;
esac
