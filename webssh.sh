#!/bin/sh

Module_dir="/data/kano_plugins/kano_web_ssh"
BASE_URL="https://github.com/cdwangtao/WebSSH-u60pro/releases/latest/download"

BIN_FILE="$Module_dir/webssh"
VERSION_FILE="$Module_dir/version.txt"

CACHE_FILE="/tmp/webssh_version_cache"
CACHE_TIME=10

get_latest_version() {
    curl -fsSL "$BASE_URL/version.txt" 2>/dev/null
}

get_latest_version_cached() {
    now=$(date +%s)

    if [ -f "$CACHE_FILE" ]; then
        last=$(date -r "$CACHE_FILE" +%s 2>/dev/null)
        if [ -n "$last" ]; then
            diff=$((now - last))
            if [ "$diff" -lt "$CACHE_TIME" ]; then
                cat "$CACHE_FILE"
                return
            fi
        fi
    fi

    v=$(get_latest_version)
    if [ -n "$v" ]; then
        echo "$v" > "$CACHE_FILE"
    fi
    echo "$v"
}

get_local_version() {
    if [ -f "$VERSION_FILE" ]; then
        cat "$VERSION_FILE"
    else
        echo "none"
    fi
}

compare_version() {
    LOCAL=$(get_local_version)
    LATEST=$(get_latest_version_cached)

    if [ -z "$LATEST" ]; then
        UPDATE_MSG="获取失败"
    elif [ "$LOCAL" = "$LATEST" ]; then
        UPDATE_MSG="已是最新版本"
    else
        UPDATE_MSG="发现新版本！"
    fi
}

download_binary() {
    VERSION="$1"
    curl -fSL "$BASE_URL/webssh_$VERSION" -o "$Module_dir/webssh_new"
    if [ $? -ne 0 ]; then
        return 1
    fi
    chmod +x "$Module_dir/webssh_new"
    return 0
}

add_alias() {
    ALIAS_CMD='alias webssh="ash /data/kano_plugins/kano_web_ssh/webssh.sh"'

    for f in /etc/shinit /etc/profile; do
        if [ ! -f "$f" ]; then
            touch "$f"
        fi

        grep -F "$ALIAS_CMD" "$f" >/dev/null 2>&1
        if [ $? -ne 0 ]; then
            echo "$ALIAS_CMD" >> "$f"
        fi
    done
}

start() {
    if [ ! -f "$BIN_FILE" ]; then
        echo "未安装"
        return
    fi
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

install() {
    mkdir -p "$Module_dir"

    LATEST=$(get_latest_version)
    if [ -z "$LATEST" ]; then
        echo "获取版本失败"
        return
    fi

    LOCAL=$(get_local_version)

    echo "当前版本: $LOCAL"
    echo "最新版本: $LATEST"

    if [ "$LOCAL" != "$LATEST" ]; then
        echo "下载中..."

        download_binary "$LATEST"
        if [ $? -ne 0 ]; then
            echo "下载失败"
            return
        fi

        mv -f "$Module_dir/webssh_new" "$BIN_FILE"
        echo "$LATEST" > "$VERSION_FILE"
    else
        echo "已是最新版本"
    fi

    add_alias
    restart

    echo "安装/升级完成"
}

update() {
    install
}

remove() {
    stop 2>/dev/null
    rm -rf "$Module_dir"
    echo "已卸载"
}

menu() {
    while true; do
        clear
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

        printf "请输入选择: "
        read choice </dev/tty

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

        printf "按回车继续..."
        read dummy </dev/tty
    done
}

case "$1" in
    start) start ;;
    stop) stop ;;
    restart) restart ;;
    install) install ;;
    update) update ;;
    remove) remove ;;
    *)
        menu
        ;;
esac