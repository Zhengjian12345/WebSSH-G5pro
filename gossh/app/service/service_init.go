package service

import (
	"context"
	"gossh/app/config"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// OnlineClients 存储的客户端信息
var OnlineClients = sync.Map{}

func DeleteOnlineClient(sessionId string) {
	defer func() {
		if err := recover(); err != nil {
			slog.Error("DeleteOnlineClient recover error:", "err_msg", err)
		}
	}()
	cli, ok := OnlineClients.Load(sessionId)
	if !ok || cli == nil {
		slog.Info("OnlineClient sessionId not exist")
		return
	}

	conn, ok := cli.(*SshConn)
	if !ok || conn == nil {
		slog.Error("DeleteOnlineClient type Asset error")
		return
	}

	// 从map 中删除会话
	defer OnlineClients.Delete(sessionId)

	// 关闭 ssh 客户端
	defer func() {
		err := conn.sshClient.Close()
		if err == io.EOF {
			slog.Info("sshClient.Close EOF")
			return
		}
		if err != nil {
			slog.Error("DeleteOnlineClient.Close sshClient error:", "err_msg", err)
		}
	}()

	// 关闭 sftp 客户端
	defer func() {
		err := conn.sftpClient.Close()
		if err == io.EOF {
			slog.Info("sftpClient.Close EOF")
			return
		}
		if err != nil {
			slog.Error("DeleteOnlineClient.Close sftpClient error:", "err_msg", err)
		}
	}()

	// 关闭 ssh 会话
	defer func() {
		err := conn.sshSession.Close()
		if err == io.EOF {
			slog.Info("sshSession.Close EOF")
			return
		}
		if err != nil {
			slog.Error("DeleteOnlineClient.Close sshSession error:", "err_msg", err)
		}
	}()

	// 关闭 websocket
	defer func() {
		err := conn.ws.Close()
		if err != nil {
			slog.Error("DeleteOnlineClient.Close ws error:", "err_msg", err)
		}
	}()

}

// 清理不活跃的会话
func cleanNoActiveSession() {
	defer func() {
		if err := recover(); err != nil {
			slog.Error("cleanNoActiveSession error:", "err_msg", err)
		}
	}()
	OnlineClients.Range(func(key, value any) bool {
		// 对键进行类型断言
		if sessionId, ok := key.(string); ok {
			// 对值进行类型断言
			if conn, ok := value.(*SshConn); ok {
				if conn.LastActiveTime.Add(time.Minute).Before(time.Now()) {
					slog.Info("clean not active session:", "sid", sessionId)
					DeleteOnlineClient(sessionId)
				}
			}
		}
		return true
	})
}

func initApp() {
	defer func() {
		if err := recover(); err != nil {
			slog.Error("service init error")
		}
	}()
	if config.DefaultConfig.IsInit {
		isStartSshd <- true
		go autoFixSystemTime()
	}
	for {
		cleanNoActiveSession()
		time.Sleep(config.DefaultConfig.ClientCheck)
	}
}

// autoFixSystemTime 启动时自动检测系统时间偏差，超过阈值则自动校正
func autoFixSystemTime() {
	defer func() {
		if err := recover(); err != nil {
			slog.Error("autoFixSystemTime recover error:", "err_msg", err)
		}
	}()
	// 等待网络就绪（最多等 60 秒）
	for i := 0; i < 12; i++ {
		time.Sleep(5 * time.Second)
		if networkReady() {
			break
		}
	}

	// 获取网络真实 UTC
	netDate := getHTTPDate()
	if netDate == "" {
		slog.Info("autoFixSystemTime: 无法从网络获取时间，跳过自动校正")
		return
	}

	// 解析网络时间为 epoch
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
		`date -u -D "%a, %d %b %Y %H:%M:%S" -d "`+netDate+`" +%s`)
	out, err := cmd.Output()
	if err != nil {
		slog.Error("autoFixSystemTime: 解析网络时间失败", "err", err)
		return
	}
	netEpoch := strings.TrimSpace(string(out))
	if netEpoch == "" {
		return
	}

	// 获取当前系统 epoch
	cmd2 := exec.CommandContext(ctx, "/bin/sh", "-c", "date -u +%s")
	out2, err := cmd2.Output()
	if err != nil {
		return
	}
	curEpoch := strings.TrimSpace(string(out2))

	if netEpoch == curEpoch {
		slog.Info("autoFixSystemTime: 系统时间准确，无需校正")
		return
	}

	// 偏差超过 60 秒则自动校正
	slog.Info("autoFixSystemTime: 检测到时间偏差，开始自动校正", "current", curEpoch, "network", netEpoch)
	runFixTimeScript()
}

// networkReady 检查网络是否可用
func networkReady() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
		`curl -sI --connect-timeout 3 http://www.baidu.com 2>/dev/null | head -1`)
	out, err := cmd.Output()
	return err == nil && strings.Contains(string(out), "200")
}

// getHTTPDate 从网络获取 HTTP Date 头
func getHTTPDate() string {
	servers := []string{
		"http://www.baidu.com",
		"http://www.qq.com",
		"http://connectivitycheck.gstatic.com/generate_204",
	}
	for _, u := range servers {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
			`curl -sI --connect-timeout 5 "`+u+`" 2>/dev/null | tr -d '\r' | grep -i '^date:' | head -1 | sed 's/^[^:]*:[[:space:]]*//; s/[[:space:]]*GMT.*//'`)
		out, err := cmd.Output()
		cancel()
		if err == nil {
			d := strings.TrimSpace(string(out))
			if d != "" {
				return d
			}
		}
	}
	return ""
}

// runFixTimeScript 执行完整的时间校正脚本
func runFixTimeScript() {
	defer func() {
		if err := recover(); err != nil {
			slog.Error("runFixTimeScript recover error:", "err_msg", err)
		}
	}()
	script := `
TZSTR='CST-8'
TZB64='VFppZjIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAAAAQAAHCAAABDU1QAVFppZjIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAAAAQAAHCAAABDU1QACkNTVC04Cg=='
get_date() {
    for u in http://www.baidu.com http://www.qq.com http://connectivitycheck.gstatic.com/generate_204 http://detectportal.firefox.com; do
        d=$(curl -sI --connect-timeout 8 "$u" 2>/dev/null | tr -d '\r' | grep -i '^date:' | head -1)
        [ -z "$d" ] && d=$(wget -q -S -O /dev/null "$u" 2>&1 | tr -d '\r' | grep -i 'date:' | head -1)
        d=$(echo "$d" | sed 's/^[^:]*:[[:space:]]*//; s/[[:space:]]*GMT.*//')
        [ -n "$d" ] && { echo "$d"; return 0; }
    done
    return 1
}
NET=$(get_date) || { echo "autoFixTime: 无法获取网络时间"; return; }
EPOCH=$(date -u -D "%a, %d %b %Y %H:%M:%S" -d "$NET" +%s 2>/dev/null)
[ -z "$EPOCH" ] && { echo "autoFixTime: 解析时间失败"; return; }
date -u -s @"$EPOCH" >/dev/null 2>&1
echo "$TZB64" | base64 -d > /etc/localtime.zoneinfo 2>/dev/null
[ -s /etc/localtime.zoneinfo ] && ln -sf /etc/localtime.zoneinfo /etc/localtime
echo "$TZSTR" > /tmp/TZ 2>/dev/null
if command -v uci >/dev/null 2>&1; then
    uci set system.@system[0].zonename='Asia/Shanghai' 2>/dev/null
    uci set system.@system[0].timezone="$TZSTR" 2>/dev/null
    uci commit system 2>/dev/null
fi
[ -x /etc/init.d/zte_topsw_ntp ] && /etc/init.d/zte_topsw_ntp restart >/dev/null 2>&1
[ -x /etc/init.d/zte_time_manager ] && /etc/init.d/zte_time_manager restart >/dev/null 2>&1
[ -x /etc/init.d/zte_topsw_devui ] && /etc/init.d/zte_topsw_devui restart >/dev/null 2>&1
echo "autoFixTime: done"
`
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("autoFixSystemTime: 校正执行失败", "err", err, "output", string(out))
		return
	}
	slog.Info("autoFixSystemTime: 校正完成", "output", strings.TrimSpace(string(out)))
}

func InitSessionClean() {
	go initApp()
}
