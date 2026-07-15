package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"gossh/gin"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────── 常量 ───────────────────────────

const (
	frpcDefaultDir       = "/data/kano_plugins/frpc"
	frpcBinaryName       = "frpc"
	frpcConfigName       = "frpc.toml"
	frpcPIDName          = "frpc.pid"
	frpcAutostartMarker  = ".autostart"
	frpcDownloadBase     = "https://github.com/fatedier/frp/releases/download"
	frpcLatestVersion    = "v0.70.0"
	frpcGitHubRepo       = "fatedier/frp"
	frpcAPIRequestHeader = "WebSSH-u60pro-frpc"
)

// 内置 gh-proxy 列表（mihomo 未运行时使用）
var frpcProxies = []string{
	"https://ghfast.top/",
	"https://gh-proxy.org/",
	"https://gh-proxy.com/",
	"https://gh.llkk.cc/",
	"https://hub.gitmirror.com/",
}

// ─────────────────────────── 安装进度状态 ───────────────────────────

type FrpcInstallStatus struct {
	mu      sync.RWMutex
	State   string `json:"state"`   // idle / downloading / extracting / installing / done / failed / canceled
	Msg     string `json:"msg"`     // 人类可读描述
	Percent int    `json:"percent"` // 0-100
	Stage   string `json:"stage"`   // download / extract / install
}

var frpcInstallStatus = &FrpcInstallStatus{State: "idle", Msg: "暂无任务"}

func (s *FrpcInstallStatus) update(fn func(*FrpcInstallStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func (s *FrpcInstallStatus) get() FrpcInstallStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *s
}

// ─────────────────────────── 辅助函数 ───────────────────────────

func getFrpcDir() string {
	return frpcDefaultDir
}

func getFrpcBinary() string {
	return filepath.Join(getFrpcDir(), frpcBinaryName)
}

func getFrpcConfig() string {
	return filepath.Join(getFrpcDir(), frpcConfigName)
}

func getFrpcAutostartEnabled() bool {
	_, err := os.Stat(filepath.Join(getFrpcDir(), frpcAutostartMarker))
	return err == nil
}

// isFrpcRunning 检查 PID 文件并验证进程是否存活于 /proc 中。
func isFrpcRunning() (bool, int) {
	pidFile := filepath.Join(getFrpcDir(), frpcPIDName)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false, 0
	}
	if _, statErr := os.Stat(fmt.Sprintf("/proc/%d", pid)); statErr == nil {
		return true, pid
	}
	return false, 0
}

// getFrpcVersion 执行 frpc -v 获取版本号。
func getFrpcVersion() string {
	binPath := getFrpcBinary()
	if _, err := os.Stat(binPath); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// getFrpcWebServerInfo 从 frpc.toml 中解析 webServer 配置段，返回 addr, port, user, password。
func getFrpcWebServerInfo() (addr, port, user, password string) {
	configPath := getFrpcConfig()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", "", "", ""
	}

	inWebServer := false
	for _, line := range strings.Split(string(data), "\n") {
		trim := strings.TrimSpace(line)
		if idx := strings.Index(trim, "#"); idx >= 0 {
			trim = strings.TrimSpace(trim[:idx])
		}
		if trim == "" {
			continue
		}
		if trim == "[webServer]" {
			inWebServer = true
			continue
		}
		if strings.HasPrefix(trim, "[") && trim != "[webServer]" {
			inWebServer = false
			continue
		}
		if !inWebServer {
			continue
		}
		if strings.HasPrefix(trim, "addr") {
			addr = extractTOMLValue(trim, "addr")
		} else if strings.HasPrefix(trim, "port") {
			port = extractTOMLValue(trim, "port")
		} else if strings.HasPrefix(trim, "user") {
			user = extractTOMLValue(trim, "user")
		} else if strings.HasPrefix(trim, "password") {
			password = extractTOMLValue(trim, "password")
		}
	}
	return addr, port, user, password
}

// extractTOMLValue 从形如 `key = "value"` 或 `key = value` 的行中提取值。
func extractTOMLValue(line, key string) string {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) < 2 {
		return ""
	}
	val := strings.TrimSpace(parts[1])
	val = strings.Trim(val, `"'`)
	return val
}

// buildFrpcAPIBase 根据 webServer 信息拼接 API 基础 URL。
func buildFrpcAPIBase() string {
	addr, port, _, _ := getFrpcWebServerInfo()
	if addr == "" || port == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:%s", addr, port)
}

// callFrpcAPI 调用 frpc 的 webServer REST API，支持 Basic Auth。
func callFrpcAPI(method, apiPath string, body interface{}) (json.RawMessage, error) {
	base := buildFrpcAPIBase()
	if base == "" {
		return nil, fmt.Errorf("webServer 未配置或配置无效")
	}

	reqURL := base + apiPath
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("序列化请求体失败: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", frpcAPIRequestHeader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	_, _, user, password := getFrpcWebServerInfo()
	if user != "" {
		req.SetBasicAuth(user, password)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API 返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

// ─────────────────────────── 下载（带进度） ───────────────────────────

// buildFrpcHTTPClient 构建带代理的 HTTP 客户端。
// 优先使用 mihomo 代理，不可用则直连。
func buildFrpcHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.IdleConnTimeout = 60 * time.Second

	// 检测 mihomo 代理
	proxyURL := getMihomoProxyURL()
	if proxyURL != "" {
		conn, err := net.DialTimeout("tcp", proxyURL, 1*time.Second)
		if err == nil {
			conn.Close()
			parsed, parseErr := url.Parse("http://" + proxyURL)
			if parseErr == nil {
				transport.Proxy = http.ProxyURL(parsed)
				slog.Info("[frpc] 使用 mihomo 代理下载", "proxy", proxyURL)
			}
		}
	}
	return &http.Client{Transport: transport, Timeout: 300 * time.Second}
}

// downloadFrpcWithProgress 下载 frpc tar.gz 并实时上报进度。
func downloadFrpcWithProgress(ctx context.Context, version, destPath string, onProgress func(downloaded, total int64, msg string)) error {
	versionClean := strings.TrimPrefix(version, "v")
	archiveName := fmt.Sprintf("frp_%s_linux_arm64.tar.gz", versionClean)
	originalURL := fmt.Sprintf("%s/%s/%s", frpcDownloadBase, version, archiveName)

	// 构建下载 URL 列表
	tryURLs := []string{originalURL}
	proxyURL := getMihomoProxyURL()
	if proxyURL == "" {
		// mihomo 未运行，添加 gh-proxy
		for _, proxy := range frpcProxies {
			tryURLs = append(tryURLs, proxy+originalURL)
		}
	}

	client := buildFrpcHTTPClient()
	var lastErr error

	for i, u := range tryURLs {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		slog.Info("[frpc] 尝试下载", "url", u, "attempt", i+1, "total", len(tryURLs))

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", frpcAPIRequestHeader)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			slog.Warn("[frpc] 下载失败", "url", u, "err", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
			slog.Warn("[frpc] 下载失败", "url", u, "err", lastErr)
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

// ─────────────────────────── 进程管理 ───────────────────────────

// startFrpc 后台启动 frpc 并写入 PID 文件。
func startFrpc() error {
	binPath := getFrpcBinary()
	configPath := getFrpcConfig()
	dir := getFrpcDir()

	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("frpc 二进制不存在: %s", binPath)
	}
	if _, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("配置文件不存在: %s", configPath)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	cmd := exec.Command(binPath, "-c", configPath)
	cmd.Dir = dir
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动失败: %w", err)
	}

	pidFile := filepath.Join(dir, frpcPIDName)
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0644); err != nil {
		slog.Warn("[frpc] 写入 PID 文件失败", "err", err.Error())
	}

	go func() {
		_ = cmd.Wait()
	}()

	slog.Info("[frpc] 已启动", "pid", cmd.Process.Pid)
	return nil
}

// stopFrpc 读取 PID 文件，终止 frpc 进程。
func stopFrpc() error {
	pidFile := filepath.Join(getFrpcDir(), frpcPIDName)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("读取 PID 文件失败: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("解析 PID 失败: %w", err)
	}

	if _, statErr := os.Stat(fmt.Sprintf("/proc/%d", pid)); statErr == nil {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("查找进程失败: %w", err)
		}
		if err := exec.Command("kill", "-TERM", fmt.Sprintf("%d", pid)).Run(); err != nil {
			return fmt.Errorf("终止进程失败: %w", err)
		}
		for i := 0; i < 10; i++ {
			time.Sleep(200 * time.Millisecond)
			if _, statErr := os.Stat(fmt.Sprintf("/proc/%d", pid)); statErr != nil {
				break
			}
			if i == 3 {
				_ = proc.Kill()
			}
		}
	}

	_ = os.Remove(pidFile)
	slog.Info("[frpc] 已停止", "pid", pid)
	return nil
}

// restartFrpc 先停止再启动 frpc。
func restartFrpc() error {
	if running, _ := isFrpcRunning(); running {
		if err := stopFrpc(); err != nil {
			slog.Warn("[frpc] restart: stop 失败", "err", err.Error())
		}
		time.Sleep(500 * time.Millisecond)
	}
	return startFrpc()
}

// reloadFrpc 尝试通过 API 热重载，不可达则 restart。
func reloadFrpc() error {
	_, err := callFrpcAPI(http.MethodGet, "/api/reload", nil)
	if err != nil {
		slog.Info("[frpc] API reload 不可达，执行 restart", "err", err.Error())
		return restartFrpc()
	}
	slog.Info("[frpc] API reload 成功")
	return nil
}

// checkFrpcAPIReachable 检查 frpc webServer API 是否可达。
func checkFrpcAPIReachable() bool {
	base := buildFrpcAPIBase()
	if base == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/version", nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", frpcAPIRequestHeader)
	_, _, user, password := getFrpcWebServerInfo()
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ─────────────────────────── Handlers: 状态 & 控制 ───────────────────────────

// FrpcStatusHandler GET /api/frpc/status
func FrpcStatusHandler(c *gin.Context) {
	dir := getFrpcDir()
	binPath := getFrpcBinary()
	configPath := getFrpcConfig()

	_, dirErr := os.Stat(dir)
	dirExists := dirErr == nil
	_, binErr := os.Stat(binPath)
	binaryExists := binErr == nil
	_, cfgErr := os.Stat(configPath)
	configExists := cfgErr == nil

	running, pid := isFrpcRunning()
	version := ""
	if binaryExists {
		version = getFrpcVersion()
	}

	addr, port, _, _ := getFrpcWebServerInfo()
	apiAddr := ""
	if addr != "" && port != "" {
		apiAddr = fmt.Sprintf("%s:%s", addr, port)
	}
	apiReachable := false
	if running {
		apiReachable = checkFrpcAPIReachable()
	}

	c.JSON(200, gin.H{
		"code": 0, "msg": "ok",
		"data": gin.H{
			"installed":         binaryExists,
			"running":            running,
			"pid":                pid,
			"version":            version,
			"api_reachable":      apiReachable,
			"api_addr":           apiAddr,
			"autostart_enabled":  getFrpcAutostartEnabled(),
			"config_exists":      configExists,
			"dir_exists":         dirExists,
			"frpc_dir":           dir,
		},
	})
}

// FrpcControlHandler POST /api/frpc/control
func FrpcControlHandler(c *gin.Context) {
	var req struct {
		Action string `json:"action" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误: " + err.Error()})
		return
	}

	validActions := map[string]bool{
		"start": true, "stop": true, "restart": true, "reload": true,
	}
	if !validActions[req.Action] {
		c.JSON(200, gin.H{"code": 1, "msg": "无效操作: " + req.Action})
		return
	}

	slog.Info("[frpc] 执行控制命令", "action", req.Action)

	var err error
	switch req.Action {
	case "start":
		err = startFrpc()
	case "stop":
		err = stopFrpc()
	case "restart":
		err = restartFrpc()
	case "reload":
		err = reloadFrpc()
	}

	if err != nil {
		slog.Warn("[frpc] 控制命令失败", "action", req.Action, "err", err.Error())
		c.JSON(200, gin.H{"code": 1, "msg": "执行失败: " + err.Error()})
		return
	}

	c.JSON(200, gin.H{"code": 0, "msg": "ok", "action": req.Action})
}

// ─────────────────────────── Handlers: 配置文件 ───────────────────────────

// FrpcGetConfigHandler GET /api/frpc/config
func FrpcGetConfigHandler(c *gin.Context) {
	configPath := getFrpcConfig()
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(200, gin.H{"code": 0, "msg": "ok", "data": gin.H{"content": "", "exists": false}})
			return
		}
		c.JSON(200, gin.H{"code": 1, "msg": "读取配置失败: " + err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "msg": "ok", "data": gin.H{"content": string(data), "exists": true}})
}

// FrpcSaveConfigHandler PUT /api/frpc/config
func FrpcSaveConfigHandler(c *gin.Context) {
	var req struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误: " + err.Error()})
		return
	}

	dir := getFrpcDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "创建目录失败: " + err.Error()})
		return
	}

	configPath := getFrpcConfig()
	if existing, err := os.ReadFile(configPath); err == nil {
		_ = os.WriteFile(configPath+".bak", existing, 0644)
	}

	if err := os.WriteFile(configPath, []byte(req.Content), 0644); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "保存失败: " + err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "msg": "配置已保存"})
}

// ─────────────────────────── 开机自启 ───────────────────────────

// FrpcGetAutostartHandler GET /api/frpc/autostart
func FrpcGetAutostartHandler(c *gin.Context) {
	c.JSON(200, gin.H{"code": 0, "msg": "ok", "data": gin.H{"enabled": getFrpcAutostartEnabled()}})
}

// FrpcSetAutostartHandler POST /api/frpc/autostart
func FrpcSetAutostartHandler(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误: " + err.Error()})
		return
	}

	dir := getFrpcDir()
	marker := filepath.Join(dir, frpcAutostartMarker)
	if req.Enabled {
		if err := os.MkdirAll(dir, 0755); err != nil {
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

// InitFrpcAutostart 在 webssh 启动时检查自启标记，若存在则后台启动 frpc。
func InitFrpcAutostart() {
	if !getFrpcAutostartEnabled() {
		return
	}
	go func() {
		binPath := getFrpcBinary()
		if _, err := os.Stat(binPath); err != nil {
			slog.Warn("[frpc] autostart: frpc 二进制不存在", "path", binPath)
			return
		}
		configPath := getFrpcConfig()
		if _, err := os.Stat(configPath); err != nil {
			slog.Warn("[frpc] autostart: 配置文件不存在", "path", configPath)
			return
		}
		slog.Info("[frpc] autostart: 正在启动 frpc")
		if err := startFrpc(); err != nil {
			slog.Warn("[frpc] autostart: 启动失败", "err", err.Error())
		} else {
			slog.Info("[frpc] autostart: 启动成功")
		}
	}()
}

// ─────────────────────────── Handlers: 代理管理 ───────────────────────────

// FrpcGetProxiesHandler GET /api/frpc/proxies
func FrpcGetProxiesHandler(c *gin.Context) {
	rawBody, err := callFrpcAPI(http.MethodGet, "/api/status", nil)
	if err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "获取代理列表失败: " + err.Error()})
		return
	}

	var statusResp struct {
		Proxies []json.RawMessage `json:"proxies"`
	}
	if err := json.Unmarshal(rawBody, &statusResp); err != nil {
		c.JSON(200, gin.H{"code": 0, "msg": "ok", "data": gin.H{"raw": json.RawMessage(rawBody)}})
		return
	}

	type ProxySummary struct {
		Name   string `json:"name"`
		Type   string `json:"type,omitempty"`
		Status string `json:"status,omitempty"`
	}

	proxies := make([]ProxySummary, 0, len(statusResp.Proxies))
	for _, raw := range statusResp.Proxies {
		var p ProxySummary
		if err := json.Unmarshal(raw, &p); err != nil {
			var fallback map[string]interface{}
			if err := json.Unmarshal(raw, &fallback); err == nil {
				if name, ok := fallback["name"].(string); ok {
					p.Name = name
				}
			}
		}
		proxies = append(proxies, p)
	}

	c.JSON(200, gin.H{
		"code": 0, "msg": "ok",
		"data": gin.H{"proxies": proxies, "total": len(proxies)},
	})
}

// FrpcSetProxyHandler POST /api/frpc/proxies
func FrpcSetProxyHandler(c *gin.Context) {
	var req struct {
		Action string          `json:"action" binding:"required"`
		Proxy  json.RawMessage `json:"proxy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误: " + err.Error()})
		return
	}

	validActions := map[string]bool{"add": true, "delete": true, "update": true}
	if !validActions[req.Action] {
		c.JSON(200, gin.H{"code": 1, "msg": "无效操作: " + req.Action})
		return
	}

	switch req.Action {
	case "add":
		rawBody, err := callFrpcAPI(http.MethodPost, "/api/store/proxies", req.Proxy)
		if err != nil {
			c.JSON(200, gin.H{"code": 1, "msg": "添加代理失败: " + err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "msg": "代理已添加", "data": json.RawMessage(rawBody)})

	case "update":
		var proxyInfo struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Proxy, &proxyInfo); err != nil || proxyInfo.Name == "" {
			c.JSON(200, gin.H{"code": 1, "msg": "缺少代理名称 (name)"})
			return
		}
		apiPath := fmt.Sprintf("/api/store/proxies/%s", proxyInfo.Name)
		rawBody, err := callFrpcAPI(http.MethodPut, apiPath, req.Proxy)
		if err != nil {
			c.JSON(200, gin.H{"code": 1, "msg": "更新代理失败: " + err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "msg": "代理已更新", "data": json.RawMessage(rawBody)})

	case "delete":
		var proxyInfo struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Proxy, &proxyInfo); err != nil || proxyInfo.Name == "" {
			c.JSON(200, gin.H{"code": 1, "msg": "缺少代理名称 (name)"})
			return
		}
		apiPath := fmt.Sprintf("/api/store/proxies/%s", proxyInfo.Name)
		rawBody, err := callFrpcAPI(http.MethodDelete, apiPath, nil)
		if err != nil {
			c.JSON(200, gin.H{"code": 1, "msg": "删除代理失败: " + err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "msg": "代理已删除", "data": json.RawMessage(rawBody)})
	}
}

// ─────────────────────────── Handlers: 版本检查 ───────────────────────────

// FrpcCheckBinaryVersionHandler GET /api/frpc/binary/version
func FrpcCheckBinaryVersionHandler(c *gin.Context) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", frpcGitHubRepo)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "创建请求失败: " + err.Error()})
		return
	}
	req.Header.Set("User-Agent", frpcAPIRequestHeader)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "请求 GitHub API 失败: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		c.JSON(200, gin.H{"code": 1, "msg": fmt.Sprintf("GitHub API 返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))})
		return
	}

	var release struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "解析 GitHub 响应失败: " + err.Error()})
		return
	}

	if release.TagName == "" && release.Message != "" {
		c.JSON(200, gin.H{"code": 1, "msg": "GitHub API: " + release.Message})
		return
	}

	localVersion := getFrpcVersion()
	binaryExists := true
	if _, err := os.Stat(getFrpcBinary()); err != nil {
		binaryExists = false
		localVersion = ""
	}

	remoteVersion := release.TagName
	if remoteVersion == "" {
		remoteVersion = frpcLatestVersion
	}

	installed := binaryExists
	hasUpdate := !installed || (remoteVersion != "" && localVersion != "" && remoteVersion != localVersion)

	c.JSON(200, gin.H{
		"code": 0, "msg": "ok",
		"data": gin.H{
			"installed":      installed,
			"local_version":  localVersion,
			"remote_version": remoteVersion,
			"has_update":     hasUpdate,
		},
	})
}

// ─────────────────────────── Handlers: 安装/更新（带进度） ───────────────────────────

// FrpcInstallHandler POST /api/frpc/install
// {"action": "install" | "update" | "uninstall", "mode": "soft" | "full"}
// install/update 改为后台执行，通过 SSE 推送进度。
func FrpcInstallHandler(c *gin.Context) {
	var req struct {
		Action string `json:"action" binding:"required"`
		Mode   string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误: " + err.Error()})
		return
	}

	if req.Action == "uninstall" {
		doFrpcUninstall(c, req.Mode)
		return
	}

	if req.Action != "install" && req.Action != "update" {
		c.JSON(200, gin.H{"code": 1, "msg": "无效操作: " + req.Action})
		return
	}

	current := frpcInstallStatus.get()
	if current.State == "downloading" || current.State == "extracting" || current.State == "installing" {
		c.JSON(200, gin.H{"code": 1, "msg": "安装任务正在进行中"})
		return
	}

	go doFrpcInstall(req.Action)
	c.JSON(200, gin.H{"code": 0, "msg": "已开始"})
}

// doFrpcInstall 后台执行 frpc 安装/更新，实时更新 frpcInstallStatus。
func doFrpcInstall(action string) {
	proxyURL := getMihomoProxyURL()
	useMihomo := proxyURL != ""

	frpcInstallStatus.update(func(s *FrpcInstallStatus) {
		s.State = "downloading"
		s.Percent = 0
		s.Stage = "download"
		if useMihomo {
			s.Msg = "正在通过 mihomo 代理下载..."
		} else {
			s.Msg = "正在下载 frpc..."
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	version := frpcLatestVersion

	// 下载
	tmpDir, err := os.MkdirTemp("", "frpc-download-*")
	if err != nil {
		frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "failed"; s.Msg = "创建临时目录失败" })
		return
	}
	defer os.RemoveAll(tmpDir)

	versionClean := strings.TrimPrefix(version, "v")
	archiveName := fmt.Sprintf("frp_%s_linux_arm64.tar.gz", versionClean)
	archivePath := filepath.Join(tmpDir, archiveName)

	err = downloadFrpcWithProgress(ctx, version, archivePath, func(downloaded, total int64, msg string) {
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
		frpcInstallStatus.update(func(s *FrpcInstallStatus) {
			s.Percent = pct
			s.Msg = proxyHint + sizeStr
		})
	})

	if err != nil {
		if ctx.Err() != nil {
			frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "canceled"; s.Msg = "已取消" })
		} else {
			frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "failed"; s.Msg = "下载失败: " + err.Error() })
		}
		return
	}

	// 验证
	info, err := os.Stat(archivePath)
	if err != nil || info.Size() == 0 {
		frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "failed"; s.Msg = "下载文件为空" })
		return
	}

	// 解压
	frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "extracting"; s.Msg = "正在解压..."; s.Percent = 75; s.Stage = "extract" })
	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "failed"; s.Msg = "创建解压目录失败" })
		return
	}
	cmd := exec.CommandContext(ctx, "tar", "-xzf", archivePath, "-C", extractDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "failed"; s.Msg = "解压失败: " + strings.TrimSpace(string(out)) })
		return
	}

	// 查找 frpc 二进制
	var frpcBinPath string
	entries, _ := os.ReadDir(extractDir)
	for _, entry := range entries {
		if entry.IsDir() {
			candidate := filepath.Join(extractDir, entry.Name(), frpcBinaryName)
			if _, statErr := os.Stat(candidate); statErr == nil {
				frpcBinPath = candidate
				break
			}
		}
	}
	if frpcBinPath == "" {
		candidate := filepath.Join(extractDir, frpcBinaryName)
		if _, statErr := os.Stat(candidate); statErr == nil {
			frpcBinPath = candidate
		}
	}
	if frpcBinPath == "" {
		frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "failed"; s.Msg = "解压后未找到 frpc 二进制" })
		return
	}

	// 安装
	frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "installing"; s.Msg = "正在安装..."; s.Percent = 90; s.Stage = "install" })

	// 停止运行中的 frpc
	if running, pid := isFrpcRunning(); running {
		slog.Info("[frpc] 安装前停止运行中的 frpc", "pid", pid)
		_ = stopFrpc()
		time.Sleep(500 * time.Millisecond)
	}

	dir := getFrpcDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "failed"; s.Msg = "创建目录失败" })
		return
	}

	targetBin := getFrpcBinary()
	// 备份
	if _, err := os.Stat(targetBin); err == nil {
		_ = os.Rename(targetBin, targetBin+".bak")
	}
	// 移动
	src, err := os.Open(frpcBinPath)
	if err != nil {
		frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "failed"; s.Msg = "读取源文件失败" })
		return
	}
	defer src.Close()
	dst, err := os.OpenFile(targetBin, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "failed"; s.Msg = "写入目标文件失败" })
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		frpcInstallStatus.update(func(s *FrpcInstallStatus) { s.State = "failed"; s.Msg = "复制二进制失败: " + err.Error() })
		return
	}

	newVersion := getFrpcVersion()
	slog.Info("[frpc] 安装/更新成功", "action", action, "version", newVersion)
	frpcInstallStatus.update(func(s *FrpcInstallStatus) {
		s.State = "done"
		s.Percent = 100
		s.Msg = "安装完成 (" + newVersion + ")"
		s.Stage = "done"
	})
}

// doFrpcUninstall 卸载 frpc。
func doFrpcUninstall(c *gin.Context, mode string) {
	if mode == "" {
		mode = "soft"
	}

	if running, pid := isFrpcRunning(); running {
		slog.Info("[frpc] 卸载前停止", "pid", pid)
		_ = stopFrpc()
	}

	if mode == "full" {
		dir := getFrpcDir()
		if err := os.RemoveAll(dir); err != nil {
			c.JSON(200, gin.H{"code": 1, "msg": "删除失败: " + err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "msg": "已删除全部 frpc 文件"})
		return
	}

	binPath := getFrpcBinary()
	_ = os.Remove(binPath + ".bak")
	if err := os.Remove(binPath); err != nil && !os.IsNotExist(err) {
		c.JSON(200, gin.H{"code": 1, "msg": "删除二进制失败: " + err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "msg": "已删除 frpc 二进制（配置已保留）"})
}

// FrpcInstallProgressHandler GET /api/frpc/install/progress
// SSE 流式推送安装进度。
func FrpcInstallProgressHandler(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	c.Stream(func(w io.Writer) bool {
		status := frpcInstallStatus.get()
		data, _ := json.Marshal(status)
		fmt.Fprintf(w, "data: %s\n\n", data)
		c.Writer.Flush()

		if status.State == "done" || status.State == "failed" || status.State == "canceled" || status.State == "idle" {
			return false
		}
		time.Sleep(500 * time.Millisecond)
		return true
	})
}

// FrpcCancelInstallHandler POST /api/frpc/install/cancel
func FrpcCancelInstallHandler(c *gin.Context) {
	frpcInstallStatus.update(func(s *FrpcInstallStatus) {
		if s.State == "downloading" || s.State == "extracting" || s.State == "installing" {
			s.State = "canceled"
			s.Msg = "正在取消..."
		}
	})
	c.JSON(200, gin.H{"code": 0, "msg": "ok"})
}