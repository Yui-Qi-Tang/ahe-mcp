#!/bin/sh
# The adjacent app owns workspace validation and persistence, not this launcher.
set -eu
umask 077

if [ "$#" -gt 1 ] || { [ "$#" -eq 1 ] && [ "$1" != '--version' ]; }; then
    printf '%s\n' '用法：直接開啟 Start.command，或使用 Start.command --version。' >&2
    exit 2
fi

preview_package=$(CDPATH= cd -P "$(dirname "$0")" && pwd -P)
preview_executable="$preview_package/AHE Detective.app/Contents/MacOS/AHE Detective"
if [ ! -f "$preview_executable" ] || [ ! -x "$preview_executable" ]; then
    printf '%s\n' '找不到相鄰且可執行的 AHE Detective.app；請保留完整試用包。' >&2
    exit 1
fi

if [ "$#" -eq 1 ]; then
    exec "$preview_executable" -version
fi

case "${HOME-}" in
    /*) ;;
    *) printf '%s\n' '無法確認使用者家目錄；未啟動試用。' >&2; exit 1 ;;
esac
if ! preview_home=$(CDPATH= cd -P "$HOME" 2>/dev/null && pwd -P); then
    printf '%s\n' '無法確認使用者家目錄；未啟動試用。' >&2
    exit 1
fi
preview_workspace="${preview_home%/}/Library/Application Support/Detective Preview"
printf 'Detective 預覽工作區：%s\n' "$preview_workspace"
printf '%s\n' '不會自動連線；重開沿用已保存設定，未保存的聊天不會恢復。' \
    '不使用日常 Detective Desktop 工作區；工作區由 app 檢查及建立。'

# Direct execution preserves the app exit code and does not reuse another window.
exec "$preview_executable" -data-dir "$preview_workspace"
