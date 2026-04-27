#!/bin/sh

Module_dir="/data/kano_plugins/kano_web_ssh"
BOOT_CMD="sh $Module_dir/service.sh start"
STOP_CMD="sh $Module_dir/service.sh stop"
FILE="/etc/rc.local"

BASE_URL="https://github.com/cdwangtao/WebSSH-u60pro/releases/latest/download"

VERSION_FILE="$Module_dir/version.txt"
BIN_FILE="$Module_dir/webssh"

CACHE_FILE="/tmp/webssh_version_cache"
CACHE_TIME=10

# ================= 基础函数 =================

get_latest_version() {
curl -fsSL "$BASE_URL/version.txt" 2>/dev/null
}

get_latest_version_cached() {
now=$(date +%s)

```
if [ -f "$CACHE_FILE" ]; then
    last=$(stat -c %Y "$CACHE_FILE" 2>/dev/null)
    if [ $((now - last)) -lt $CACHE_TIME ]; then
        cat "$CACHE_FILE"
        return
    fi
fi

v=$(get_latest_version)
[ -n "$v" ] && echo "$v" > "$CACHE_FILE"
echo "$v"
```

}

get_local_version() {
[ -f "$VERSION_FILE" ] && cat "$VERSION_FILE" || echo "none"
}

compare_version() {
LOCAL=$(get_local_version)
LATEST=$(get_latest_version_cached)

```
if [ -z "$LATEST" ]; then
    UPDATE_MSG="获取失败"
elif [ "$LOCAL" = "$LATEST" ]; then
    UPDATE_MSG="已是最新版本"
else
    UPDATE_MSG="发现新版本！"
fi
```

}

download_binary() {
VERSION="$1"
curl -fSL "$BASE_URL/webssh_$VERSION" -o "$Module_dir/webssh_new" || return 1
chmod +x "$Module_dir/webssh_new"
}

# ================= alias 自动写入 =================

add_alias() {
ALIAS_CMD='alias webssh="ash /data/kano_plugins/kano_web_ssh/service.sh"'

```
for f in /etc/shinit /etc/profile; do
    [ ! -f "$f" ] && touch "$f"

    if grep -F "$ALIAS_CMD" "$f" >/dev/null 2>&1; then
        echo "alias 已存在: $f"
    else
        echo "$ALIAS_CMD" >> "$f"
        echo "已写入 alias 到: $f"
    fi
done
```

}

# ================= 安装 / 更新 =================

install() {
mkdir -p "$Module_dir"

```
LATEST=$(get_latest_version)
[ -z "$LATEST" ] && echo "获取版本失败" && return

LOCAL=$(get_local_version)

echo "当前版本: $LOCAL"
echo "最新版本: $LATEST"

if [ "$LOCAL" != "$LATEST" ]; then
    echo "下载中..."
    download_binary "$LATEST" || {
        echo "下载失败"
        return
    }

    mv -f "$Module_dir/webssh_new" "$BIN_FILE"
    echo "$LATEST" > "$VERSION_FILE"
else
    echo "已是最新版本"
fi

chmod 755 "$BIN_FILE" 2>/dev/null || true

# 开机自启
if [ -f "$FILE" ]; then
    if ! grep -F "$BOOT_CMD" "$FILE" >/dev/null 2>&1; then
        sed -i "/^exit 0/i $BOOT_CMD" "$FILE"
    fi
fi

add_alias
restart

echo "安装/升级完成"
```

}

update() {
install
}

# ================= 服务控制 =================

check_is_installed() {
[ ! -f "$BIN_FILE" ] && echo "未安装" && exit 1
}

start() {
check_is_installed
"$BIN_FILE" &
echo "已启动"
}

stop() {
pkill -f "$BIN_FILE" 2>/dev/null
echo "已停止"
}

restart() {
stop
sleep 1
start
}

remove() {
stop 2>/dev/null
[ -f "$FILE" ] && sed -i "/kano_web_ssh/d" "$FILE"
rm -rf "$Module_dir"
echo "已卸载"
}

# ================= 菜单 =================

run_menu() {
while true; do
clear

```
    compare_version

    echo "======================================"
    echo "        WebSSH 管理脚本"
    echo "--------------------------------------"
    echo "  当前版本: $(get_local_version)"
    echo "  最新版本: $(get_latest_version_cached)"
    echo "  更新状态: $UPDATE_MSG"
    echo "--------------------------------------"
    echo "  1) 安装/升级"
    echo "  2) 更新"
    echo "  3) 启动"
    echo "  4) 停止"
    echo "  5) 重启"
    echo "  6) 卸载"
    echo "  0) 退出"
    echo "======================================"

    read -rp "请输入选择: " choice </dev/tty

    case "$choice" in
        1) install ;;
        2) update ;;
        3) start ;;
        4) stop ;;
        5) restart ;;
        6) remove ;;
        0) exit 0 ;;
        *) echo "无效输入"; sleep 1 ;;
    esac

    read -rp "按回车继续..." dummy </dev/tty
done
```

}

# ================= 入口 =================

case "$1" in
start) start ;;
stop) stop ;;
restart) restart ;;
install) install ;;
update) update ;;
remove) remove ;;
*)
run_menu
;;
esac
