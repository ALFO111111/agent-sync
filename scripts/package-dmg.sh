#!/bin/bash
# 把 .app 做成 DMG 安装包：打开后左边是 App，右边是“应用程序”的快捷方式，拖过去就装好了。
# 只能在 macOS 上运行（用系统自带的 hdiutil）。
#
#   scripts/package-dmg.sh build/bin/agent-sync.app dist/agent-sync-0.3.1-macos-arm64.dmg
set -euo pipefail

app="${1:?用法: package-dmg.sh <App 路径> <输出 dmg 路径>}"
out="${2:?用法: package-dmg.sh <App 路径> <输出 dmg 路径>}"
[ -d "$app" ] || { echo "找不到 $app" >&2; exit 1; }

stage="$(mktemp -d)"
mount="$(mktemp -d)"
probe="$(mktemp -d)"
attached=0
cleanup() {
    if [ "$attached" -eq 1 ]; then
        hdiutil detach "$mount" || hdiutil detach -force "$mount" || true
    fi
    rm -rf "$stage" "$mount" "$probe"
}
trap cleanup EXIT

# ditto 复制会保留 .app 里的符号链接、权限和签名
ditto "$app" "$stage/$(basename "$app")"
ln -s /Applications "$stage/Applications"

rm -f "$out"
hdiutil create -volname "agent-sync" -srcfolder "$stage" -fs HFS+ -format UDZO -ov "$out"
hdiutil verify "$out"

# 验证用户实际拿到的镜像，而不只验证打包前 build/bin 里的 App。
hdiutil attach -readonly -nobrowse -mountpoint "$mount" "$out"
attached=1
packaged="$mount/$(basename "$app")"
test -d "$packaged"
test -L "$mount/Applications"
ditto "$packaged" "$probe/agent-sync.app"
exe="$probe/agent-sync.app/Contents/MacOS/agent-sync"
test -x "$exe"
lipo "$exe" -verify_arch arm64
mkdir "$probe/state"
# macOS /var 是系统别名，离线自检的数据目录必须使用真实路径。
probe="$(cd "$probe" && pwd -P)"
"$probe/agent-sync.app/Contents/MacOS/agent-sync" --check-host-bundle "$probe/state"
hdiutil detach "$mount"
attached=0
echo 'Mounted DMG offline host check passed.'
