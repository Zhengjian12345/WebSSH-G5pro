package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gossh/gin"
)

// ─────────────────────────── 常量 ───────────────────────────

const (
	tailscaleDir             = "/data/plugins/tailscale"
	tailscaleAutostartMarker = ".autostart"
	tailscalePkgBase         = "https://pkgs.tailscale.com/stable"
	tailscaleDownloadURL     = tailscalePkgBase + "/tailscale_latest_arm64.tgz"
)

// 内置 gh-proxy 列表（mihomo 未运行时使用）
var tailscaleProxies = []string{
	"https://ghfast.top/",
	"https://gh-proxy.org/",
	"https://gh-proxy.com/",
	"https://gh.llkk.cc/",
	"https://hub.gitmirror.com/",
}

// ─────────────────────────── 安装进度状态 ───────────────────────────

type TailscaleInstallStatus struct {
	mu      sync.RWMutex
	State   string `json:"state"`   // idle / downloading / extracting / installing / done / failed / canceled
	Msg     string `json:"msg"`     // 人类可读描述
	Percent int    `json:"percent"` // 0-100
	Stage   string `json:"stage"`   // download / extract / install
}

var tsInstallStatus = &TailscaleInstallStatus{State: "idle", Msg: "暂无任务"}

func (s *TailscaleInstallStatus) update(fn func(*TailscaleInstallStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func (s *TailscaleInstallStatus) get() TailscaleInstallStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *s
}

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
	line := strings.TrimSpace(string(out))
	if idx := strings.Index(line, "\n"); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimPrefix(line, "tailscale ")
	line = strings.TrimSpace(line)
	return line
}

// getTailscaleRemoteVersion 从 pkgs.tailscale.com 获取最新稳定版本号。
func getTailscaleRemoteVersion() (string, error) {
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
	re := regexp.MustCompile(`tailscale[_-](\d+\.\d+\.\d+)`)
	matches := re.FindStringSubmatch(string(body))
	if len(matches) >= 2 {
		return matches[1], nil
	}
	return "", fmt.Errorf("无法从 pkgs.tailscale.com 解析最新版本号")
}

// getMihomoProxyURL 检测 mihomo 是否运行，若运行则返回 HTTP 代理地址（从 config.yaml 解析 mixed-port）。
func getMihomoProxyURL() string {
	dir := "/data/kano_plugins/mihomo"
	configPath := filepath.Join(dir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "mixed-port:") {
			val := strings.TrimSpace(strings.TrimPrefix(trim, "mixed-port:"))
			port := strings.TrimSpace(val)
			return "127.0.0.1:" + port
		}
	}
	// 也尝试 port 字段（http 代理端口）
	for _, line := range strings.Split(string(data), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "port:") {
			val := strings.TrimSpace(strings.TrimPrefix(trim, "port:"))
			port := strings.TrimSpace(val)
			return "127.0.0.1:" + port
		}
	}
	return ""
}

// buildTailscaleHTTPClient 构建带代理的 HTTP 客户端。
// 优先使用 mihomo 代理，不可用则直连（pkgs.tailscale.com 国内可直连）。
func buildTailscaleHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.IdleConnTimeout = 60 * time.Second

	proxyURL := getMihomoProxyURL()
	if proxyURL != "" {
		// 检查端口是否真的在监听
		conn, err := net.DialTimeout("tcp", proxyURL, 1*time.Second)
		if err == nil {
			conn.Close()
			parsed, parseErr := url.Parse("http://" + proxyURL)
			if parseErr == nil {
				transport.Proxy = http.ProxyURL(parsed)
				slog.Info("[tailscale] 使用 mihomo 代理下载", "proxy", proxyURL)
			}
		}
	}
	return &http.Client{Transport: transport, Timeout: 300 * time.Second}
}

// downloadTailscaleWithProgress 下载 Tailscale tgz 并实时上报进度。
func downloadTailscaleWithProgress(ctx context.Context, destPath string, onProgress func(downloaded, total int64, msg string)) error {
	client := buildTailscaleHTTPClient()
	downloadURL := tailscaleDownloadURL

	// 如果没有使用 mihomo 代理（直连），尝试 gh-proxy 加速
	proxyURL := getMihomoProxyURL()
	tryURLs := []string{downloadURL}
	if proxyURL == "" {
		for _, proxy := range tailscaleProxies {
			tryURLs = append(tryURLs, proxy+downloadURL)
		}
	}

	var lastErr error
	for _, u := range tryURLs {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "WebSSH-u60pro-tailscale-installer")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			slog.Warn("[tailscale] 下载失败", "url", u, "err", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
			slog.Warn("[tailscale] 下载失败", "url", u, "err", lastErr)
			continue
		}

		total := resp.ContentLength
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			resp.Body.Close()
			return err
		}

		buf := make([]byte, 64*1024)
		var downloaded int64
		var writeErr error
		for {
			if ctx.Err() != nil {
				out.Close()
				resp.Body.Close()
				_ = os.Remove(destPath)
				return ctx.Err()
			}
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				written, werr := out.Write(buf[:n])
				downloaded += int64(written)
				if onProgress != nil {
					onProgress(downloaded, total, "正在下载...")
				}
				if werr != nil {
					writeErr = werr
					break
				}
				if written != n {
					writeErr = io.ErrShortWrite
					break
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				writeErr = readErr
				break
			}
		}
		resp.Body.Close()
		out.Close()

		if writeErr != nil {
			_ = os.Remove(destPath)
			lastErr = writeErr
			continue
		}
		return nil
	}
	return fmt.Errorf("所有下载线路均失败: %w", lastErr)
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
			skip = 9
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

		for i := 0; i < 12; i++ {
			cmd := exec.Command("ping", "-c", "1", "-W", "2", "223.5.5.5")
			if err := cmd.Run(); err == nil {
				break
			}
			slog.Info("tailscale autostart: waiting for network", "attempt", i+1)
		}

		if _, err := os.Stat("/dev/net/tun"); err != nil {
			_ = os.MkdirAll("/dev/net", 0755)
			_ = exec.Command("mknod", "/dev/net/tun", "c", "10", "200").Run()
			_ = os.Chmod("/dev/net/tun", 0600)
		}

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

// ─────────────────────────── Handlers: 版本检查 ───────────────────────────

// TailscaleCheckUpdateHandler GET /api/tailscale/check-update
func TailscaleCheckUpdateHandler(c *gin.Context) {
	localVersion := getTailscaleLocalVersion()
	binExists := localVersion != ""

	remoteVersion, err := getTailscaleRemoteVersion()
	if err != nil {
		slog.Warn("[tailscale] 检查远程版本失败", "err", err)
		c.JSON(200, gin.H{
			"code": 1, "msg": "获取最新版本失败: " + err.Error(),
			"data": gin.H{"installed": binExists, "local_version": localVersion, "remote_version": "", "has_update": false},
		})
		return
	}

	hasUpdate := !binExists || (localVersion != "" && remoteVersion != "" && localVersion != remoteVersion)
	c.JSON(200, gin.H{
		"code": 0, "msg": "ok",
		"data": gin.H{"installed": binExists, "local_version": localVersion, "remote_version": remoteVersion, "has_update": hasUpdate},
	})
}

// ─────────────────────────── Handlers: 安装/更新（带进度） ───────────────────────────

// TailscaleInstallHandler POST /api/tailscale/install
// {"action": "install" | "update"}
// 在后台执行安装，通过 SSE 推送进度。
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

	current := tsInstallStatus.get()
	if current.State == "downloading" || current.State == "extracting" || current.State == "installing" {
		c.JSON(200, gin.H{"code": 1, "msg": "安装任务正在进行中"})
		return
	}

	// 后台执行安装
	go doTailscaleInstall(req.Action)

	c.JSON(200, gin.H{"code": 0, "msg": "已开始"})
}

// doTailscaleInstall 后台执行 Tailscale 安装/更新，实时更新 tsInstallStatus。
func doTailscaleInstall(action string) {
	proxyURL := getMihomoProxyURL()
	useMihomo := proxyURL != ""

	tsInstallStatus.update(func(s *TailscaleInstallStatus) {
		s.State = "downloading"
		s.Percent = 0
		s.Stage = "download"
		if useMihomo {
			s.Msg = "正在通过 mihomo 代理下载..."
		} else {
			s.Msg = "正在下载 Tailscale..."
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 下载
	tmpDir, err := os.MkdirTemp("", "ts-install-*")
	if err != nil {
		tsInstallStatus.update(func(s *TailscaleInstallStatus) {
			s.State = "failed"; s.Msg = "创建临时目录失败: " + err.Error()
		})
		return
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, "tailscale.tgz")
	err = downloadTailscaleWithProgress(ctx, archivePath, func(downloaded, total int64, msg string) {
		pct := 0
		if total > 0 {
			pct = int(downloaded * 100 / total)
			if pct > 100 {
				pct = 100
			}
		}
		sizeStr := fmt.Sprintf("%.1f MB / %.1f MB", float64(downloaded)/1024/1024, float64(total)/1024/1024)
		if total <= 0 {
			sizeStr = fmt.Sprintf("%.1f MB", float64(downloaded)/1024/1024)
		}
		proxyHint := ""
		if useMihomo {
			proxyHint = "[mihomo] "
		}
		tsInstallStatus.update(func(s *TailscaleInstallStatus) {
			s.Percent = pct
			s.Msg = proxyHint + sizeStr
		})
	})

	if err != nil {
		if ctx.Err() != nil {
			tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "canceled"; s.Msg = "已取消" })
		} else {
			tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "failed"; s.Msg = "下载失败: " + err.Error() })
		}
		return
	}

	// 验证文件
	info, err := os.Stat(archivePath)
	if err != nil || info.Size() == 0 {
		tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "failed"; s.Msg = "下载文件为空" })
		return
	}

	// 解压
	tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "extracting"; s.Msg = "正在解压..."; s.Percent = 75; s.Stage = "extract" })
	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "failed"; s.Msg = "创建解压目录失败" })
		return
	}
	cmd := exec.CommandContext(ctx, "tar", "-xzf", archivePath, "-C", extractDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "failed"; s.Msg = "解压失败: " + strings.TrimSpace(string(out)) })
		return
	}

	// 查找二进制
	var tsBinPath, tsdBinPath string
	entries, _ := os.ReadDir(extractDir)
	for _, entry := range entries {
		if entry.IsDir() {
			c1 := filepath.Join(extractDir, entry.Name(), "tailscale")
			c2 := filepath.Join(extractDir, entry.Name(), "tailscaled")
			if _, e1 := os.Stat(c1); e1 == nil {
				tsBinPath = c1
			}
			if _, e2 := os.Stat(c2); e2 == nil {
				tsdBinPath = c2
			}
		}
	}
	if tsBinPath == "" {
		c1 := filepath.Join(extractDir, "tailscale")
		if _, e := os.Stat(c1); e == nil {
			tsBinPath = c1
		}
	}
	if tsdBinPath == "" {
		c2 := filepath.Join(extractDir, "tailscaled")
		if _, e := os.Stat(c2); e == nil {
			tsdBinPath = c2
		}
	}
	if tsBinPath == "" || tsdBinPath == "" {
		tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "failed"; s.Msg = "解压后未找到 tailscale/tailscaled 二进制" })
		return
	}

	// 安装
	tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "installing"; s.Msg = "正在安装..."; s.Percent = 90; s.Stage = "install" })
	binDir := filepath.Join(tailscaleDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "failed"; s.Msg = "创建目录失败" })
		return
	}

	for _, src := range []string{tsBinPath, tsdBinPath} {
		dst := filepath.Join(binDir, filepath.Base(src))
		if err := copyFile(src, dst); err != nil {
			tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "failed"; s.Msg = "复制文件失败: " + err.Error() })
			return
		}
		if err := os.Chmod(dst, 0755); err != nil {
			tsInstallStatus.update(func(s *TailscaleInstallStatus) { s.State = "failed"; s.Msg = "设置权限失败" })
			return
		}
	}

	newVersion := getTailscaleLocalVersion()
	slog.Info("[tailscale] 安装/更新成功", "action", action, "version", newVersion)
	tsInstallStatus.update(func(s *TailscaleInstallStatus) {
		s.State = "done"
		s.Percent = 100
		s.Msg = "安装完成 (" + newVersion + ")"
		s.Stage = "done"
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// TailscaleInstallProgressHandler GET /api/tailscale/install/progress
// SSE 流式推送安装进度。
func TailscaleInstallProgressHandler(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	c.Stream(func(w io.Writer) bool {
		status := tsInstallStatus.get()
		data, _ := json.Marshal(status)
		fmt.Fprintf(w, "data: %s\n\n", data)
		c.Writer.Flush()

		// 如果任务已完成或空闲，关闭流
		if status.State == "done" || status.State == "failed" || status.State == "canceled" || status.State == "idle" {
			return false
		}
		time.Sleep(500 * time.Millisecond)
		return true
	})
}

// TailscaleCancelInstallHandler POST /api/tailscale/install/cancel
func TailscaleCancelInstallHandler(c *gin.Context) {
	tsInstallStatus.update(func(s *TailscaleInstallStatus) {
		if s.State == "downloading" || s.State == "extracting" || s.State == "installing" {
			s.State = "canceled"
			s.Msg = "正在取消..."
		}
	})
	c.JSON(200, gin.H{"code": 0, "msg": "ok"})
}

// ─────────────────────────── Handlers: 卸载 ───────────────────────────

// TailscaleUninstallHandler POST /api/tailscale/uninstall
func TailscaleUninstallHandler(c *gin.Context) {
	pidFile := filepath.Join(tailscaleDir, "tailscaled.pid")
	if data, err := os.ReadFile(pidFile); err == nil {
		pid := strings.TrimSpace(string(data))
		if pid != "" {
			_ = exec.Command("kill", "-TERM", pid).Run()
			time.Sleep(500 * time.Millisecond)
			_ = exec.Command("kill", "-9", pid).Run()
		}
	}

	if err := os.RemoveAll(tailscaleDir); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "删除失败: " + err.Error()})
		return
	}

	cleanupLegacyTailscaleRcLocal()
	c.JSON(200, gin.H{"code": 0, "msg": "已卸载 Tailscale"})
}