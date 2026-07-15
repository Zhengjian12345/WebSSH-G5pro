package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gossh/gin"
)

// ─────────────────────────── 常量 ───────────────────────────

const (
	tailscaleDir             = "/data/plugins/tailscale"
	tailscaleAutostartMarker = ".autostart"
	tailscalePkgBase         = "https://pkgs.tailscale.com/stable"
	tailscaleDownloadURL     = tailscalePkgBase + "/tailscale_latest_arm64.tgz"
	tailscaleVersionCheckURL = tailscalePkgBase + "/?mode=json&arch=arm64"
)

// ─────────────────────────── 辅助函数 ───────────────────────────

func getTailscaleAutostartEnabled() bool {
	_, err := os.Stat(filepath.Join(tailscaleDir, tailscaleAutostartMarker))
	return err == nil
}

func getTailscaleBin() string {
	return filepath.Join(tailscaleDir, "bin", "tailscale")
}

func getTailscaledBin() string {
	return filepath.Join(tailscaleDir, "bin", "tailscaled")
}

// getTailscaleLocalVersion 获取本地已安装的 Tailscale 版本号。
func getTailscaleLocalVersion() string {
	binPath := getTailscaleBin()
	if _, err := os.Stat(binPath); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	// 输出第一行即版本号，如 "1.78.0" 或 "tailscale v1.78.0"
	line := strings.TrimSpace(string(out))
	if idx := strings.Index(line, "\n"); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimPrefix(line, "tailscale ")
	line = strings.TrimSpace(line)
	return line
}

// getTailscaleRemoteVersion 从 pkgs.tailscale.com 获取最新稳定版本号。
// pkgs.tailscale.com 国内可直接访问，无需代理。
func getTailscaleRemoteVersion() (string, error) {
	// 方式1: 访问 pkgs.tailscale.com/stable/ 页面，从 HTML 中提取版本号
	// 版本号出现在文件名中，如 tailscale_1.98.2_arm64.tgz
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tailscalePkgBase+"/", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "WebSSH-u60pro-tailscale-updater")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	html := string(body)

	// 匹配 tailscale_1.xx.x_arm64.tgz 或 Tailscale_1.xx.x-1_arm_64.qpkg 中的版本号
	re := regexp.MustCompile(`tailscale[_-](\d+\.\d+\.\d+)`)
	matches := re.FindStringSubmatch(html)
	if len(matches) >= 2 {
		return matches[1], nil
	}
	return "", fmt.Errorf("无法从 pkgs.tailscale.com 解析最新版本号")
}

// ─────────────────────────── 自启动 ───────────────────────────

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

// InitTailscaleAutostart 在 webssh 启动时检查自启标记，若存在则后台启动 tailscaled。
func InitTailscaleAutostart() {
	cleanupLegacyTailscaleRcLocal()
	if !getTailscaleAutostartEnabled() {
		return
	}
	go func() {
		binPath := getTailscaledBin()
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

// ─────────────────────────── Handlers: 自启动 ───────────────────────────

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

// ─────────────────────────── Handlers: 版本检查 & 安装/更新 ───────────────────────────

// TailscaleCheckUpdateHandler GET /api/tailscale/check-update
// 检查 Tailscale 是否有新版本可用。
func TailscaleCheckUpdateHandler(c *gin.Context) {
	localVersion := getTailscaleLocalVersion()
	binExists := localVersion != ""

	remoteVersion, err := getTailscaleRemoteVersion()
	if err != nil {
		slog.Warn("[tailscale] 检查远程版本失败", "err", err)
		c.JSON(200, gin.H{
			"code": 1,
			"msg":  "获取最新版本失败: " + err.Error(),
			"data": gin.H{
				"installed":      binExists,
				"local_version":  localVersion,
				"remote_version": "",
				"has_update":     false,
			},
		})
		return
	}

	hasUpdate := !binExists || (localVersion != "" && remoteVersion != "" && localVersion != remoteVersion)

	c.JSON(200, gin.H{
		"code": 0, "msg": "ok",
		"data": gin.H{
			"installed":      binExists,
			"local_version":  localVersion,
			"remote_version": remoteVersion,
			"has_update":     hasUpdate,
		},
	})
}

// TailscaleInstallHandler POST /api/tailscale/install
// {"action": "install" | "update"}
// 下载并安装/更新 Tailscale。pkgs.tailscale.com 国内可直接访问。
func TailscaleInstallHandler(c *gin.Context) {
	var req struct {
		Action string `json:"action" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误: " + err.Error()})
		return
	}
	if req.Action != "install" && req.Action != "update" {
		c.JSON(200, gin.H{"code": 1, "msg": "无效操作: " + req.Action})
		return
	}

	// 确定目标版本号
	remoteVersion := ""
	if req.Action == "update" {
		v, err := getTailscaleRemoteVersion()
		if err != nil {
			c.JSON(200, gin.H{"code": 1, "msg": "获取最新版本失败: " + err.Error()})
			return
		}
		remoteVersion = v
	}

	// 执行安装脚本
	dir := tailscaleDir
	binDir := filepath.Join(dir, "bin")
	script := fmt.Sprintf(`
DIR="%s"
BIN_DIR="%s"
mkdir -p "$DIR" "$BIN_DIR"
echo "下载 Tailscale..."
curl -fsSL --connect-timeout 15 "%s" -o /tmp/tailscale.tgz 2>&1 || { echo "下载失败"; exit 1; }
echo "解压..."
tar -xzf /tmp/tailscale.tgz -C /tmp/ 2>&1
cp /tmp/tailscale_*/tailscale "$BIN_DIR/" 2>&1
cp /tmp/tailscale_*/tailscaled "$BIN_DIR/" 2>&1
chmod +x "$BIN_DIR/tailscale" "$BIN_DIR/tailscaled"
"$BIN_DIR/tailscale" version 2>/dev/null | head -n 1
rm -rf /tmp/tailscale.tgz /tmp/tailscale_*
echo "安装完成"
`, dir, binDir, tailscaleDownloadURL)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))

	if err != nil {
		slog.Warn("[tailscale] 安装/更新失败", "action", req.Action, "err", err, "output", output)
		c.JSON(200, gin.H{"code": 1, "msg": "安装失败", "data": gin.H{"output": output}})
		return
	}

	// 获取安装后的版本
	newVersion := getTailscaleLocalVersion()

	slog.Info("[tailscale] 安装/更新成功", "action", req.Action, "version", newVersion)
	c.JSON(200, gin.H{
		"code": 0,
		"msg":  "ok",
		"data": gin.H{
			"version":        newVersion,
			"remote_version": remoteVersion,
			"output":         output,
		},
	})
}

// TailscaleUninstallHandler POST /api/tailscale/uninstall
func TailscaleUninstallHandler(c *gin.Context) {
	// 先停止运行中的 tailscaled
	pidFile := filepath.Join(tailscaleDir, "tailscaled.pid")
	if data, err := os.ReadFile(pidFile); err == nil {
		pid := strings.TrimSpace(string(data))
		if pid != "" {
			_ = exec.Command("kill", "-TERM", pid).Run()
			time.Sleep(500 * time.Millisecond)
			_ = exec.Command("kill", "-9", pid).Run()
		}
	}

	// 删除整个目录
	if err := os.RemoveAll(tailscaleDir); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "删除失败: " + err.Error()})
		return
	}

	// 清理自启动标记（冗余保护）
	_ = cleanupLegacyTailscaleRcLocal()

	c.JSON(200, gin.H{"code": 0, "msg": "已卸载 Tailscale"})
}