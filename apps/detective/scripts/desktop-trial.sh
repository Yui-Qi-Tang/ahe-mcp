#!/bin/sh
# An explicit native trial owns a fresh workspace, never the default profile.
set -eu
umask 077

if [ "$#" -ne 0 ]; then
    printf '%s\n' '用法：make desktop-trial（不接受額外參數）' >&2
    exit 2
fi

repo_dir=$(CDPATH= cd -P "$(dirname "$0")/.." && pwd -P)
app_executable="$repo_dir/build/bin/AHE Detective.app/Contents/MacOS/AHE Detective"
if [ ! -f "$app_executable" ] || [ ! -x "$app_executable" ]; then
    printf '%s\n' '找不到可執行的 Desktop；請先執行 make desktop。' >&2
    exit 1
fi

# /tmp is a symlink on macOS; Desktop requires a canonical private path.
trial_dir=$(mktemp -d /private/tmp/detective-desktop-trial.XXXXXX)
printf 'Desktop 隔離試用資料目錄：%s\n' "$trial_dir"
printf '%s\n' '將啟動新的離線工作階段；不複製舊設定、不關閉舊視窗。' \
    '試用目錄不自動刪除；請先保留需要的內容。退出碼不代表 UI 已驗收。'

# Direct execution avoids LaunchServices forwarding to an existing window.
# No exit trap: retain captures even when startup fails or the trial is stopped.
exec "$app_executable" -data-dir "$trial_dir"
