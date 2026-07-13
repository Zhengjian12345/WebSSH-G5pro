package service

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gossh/gin"
)

// cleanupLegacyTailscaleRcLocal 清理 /etc/rc.local 中旧版 Tailscale 自启动残留
func cleanupLegacyTailscaleRcLocal() {
	data, err := os.ReadFile("/etc/rc.local")
	if err != nil {
		return
	}
	content := string(data)
	marker := "# [WebSSH] Tailscale autostart"
	if !strings.Contains(content, marker) {
		return
	}
	lines := strings.Split(content, "\n")
	var out []string
	skip := 0
	for _, line := range lines {
		if strings.Contains(line, marker) {
			skip = 9 // marker + 最多 9 行内容
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		out = append(out, line)
	}
	_ = os.WriteFile("/etc/rc.local", []byte(strings.Join(out, "\n")), 0755)
}

const tailscaleDir = "/data/plugins/tailscale"
const tailscaleAutostartMarker = ".autostart"

func getTailscaleAutostartEnabled() bool {
	_, err := os.Stat(filepath.Join(tailscaleDir, tailscaleAutostartMarker))
	return err == nil
}

// InitTailscaleAutostart 在 webssh 启动时检查自启标记，若存在则后台启动 tailscaled。
func InitTailscaleAutostart() {
	cleanupLegacyTailscaleRcLocal()
	if !getTailscaleAutostartEnabled() {
		return
	}
	go func() {
		binPath := filepath.Join(tailscaleDir, "bin", "tailscaled")
		if _, err := os.Stat(binPath); err != nil {
			slog.Warn("tailscale autostart: tailscaled not found", "path", binPath)
			return
		}

		// 等待网络就绪（最多 60 秒）
		for i := 0; i < 12; i++ {
			cmd := exec.Command("ping", "-c", "1", "-W", "2", "223.5.5.5")
			if err := cmd.Run(); err == nil {
				break
			}
			slog.Info("tailscale autostart: waiting for network", "attempt", i+1)
		}

		// 创建 TUN 设备
		if _, err := os.Stat("/dev/net/tun"); err != nil {
			_ = os.MkdirAll("/dev/net", 0755)
			_ = exec.Command("mknod", "/dev/net/tun", "c", "10", "200").Run()
			_ = os.Chmod("/dev/net/tun", 0600)
		}

		// 启动 tailscaled
		logPath := filepath.Join(tailscaleDir, "tailscaled.log")
		pidPath := filepath.Join(tailscaleDir, "tailscaled.pid")
		cmd := exec.Command(
			binPath,
			"--socket=/tmp/tailscale-ufi.sock",
			"--state="+filepath.Join(tailscaleDir, "tailscaled.state"),
			"--tun=tailscale0",
		)
		f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if f != nil {
			cmd.Stdout = f
			cmd.Stderr = f
			defer f.Close()
		}
		if err := cmd.Start(); err != nil {
			slog.Warn("tailscale autostart: start failed", "err", err)
			return
		}
		_ = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644)
		slog.Info("tailscale autostart: started", "pid", cmd.Process.Pid)
	}()
}

// TailscaleGetAutostartHandler GET /api/tailscale/autostart
func TailscaleGetAutostartHandler(c *gin.Context) {
	c.JSON(200, gin.H{"code": 0, "msg": "ok", "data": gin.H{"enabled": getTailscaleAutostartEnabled()}})
}

// TailscaleSetAutostartHandler POST /api/tailscale/autostart {"enabled": true/false}
func TailscaleSetAutostartHandler(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误: " + err.Error()})
		return
	}
	marker := filepath.Join(tailscaleDir, tailscaleAutostartMarker)
	if req.Enabled {
		if err := os.MkdirAll(tailscaleDir, 0755); err != nil {
			c.JSON(200, gin.H{"code": 1, "msg": "创建目录失败: " + err.Error()})
			return
		}
		if err := os.WriteFile(marker, []byte(""), 0644); err != nil {
			c.JSON(200, gin.H{"code": 1, "msg": "写入标记失败: " + err.Error()})
			return
		}
	} else {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			c.JSON(200, gin.H{"code": 1, "msg": "删除标记失败: " + err.Error()})
			return
		}
	}
	c.JSON(200, gin.H{"code": 0, "msg": "ok", "data": gin.H{"enabled": req.Enabled}})
}
