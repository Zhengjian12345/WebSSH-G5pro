package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"gossh/gin"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	frpcArm64Archive     = "frp_0.70.0_linux_arm64.tar.gz"
	frpcGitHubRepo       = "fatedier/frp"
	frpcAPIRequestHeader = "WebSSH-u60pro-frpc"
)

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
// frpc 的 TOML 配置格式示例：
//
//	[webServer]
//	addr = "127.0.0.1"
//	port = 7400
//	user = "admin"
//	password = "admin"
func getFrpcWebServerInfo() (addr, port, user, password string) {
	configPath := getFrpcConfig()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", "", "", ""
	}

	inWebServer := false
	for _, line := range strings.Split(string(data), "\n") {
		trim := strings.TrimSpace(line)
		// 去掉注释
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
	// 去掉引号
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

	url := base + apiPath
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
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", frpcAPIRequestHeader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Basic Auth
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

// downloadFrpc 下载指定版本的 frpc ARM64 tar.gz 到临时目录，解压提取 frpc 二进制并移动到目标目录。
func downloadFrpc(version string) error {
	dir := getFrpcDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 从 version (如 v0.70.0) 提取纯版本号用于文件名
	versionClean := strings.TrimPrefix(version, "v")
	archiveName := fmt.Sprintf("frp_%s_linux_arm64.tar.gz", versionClean)
	downloadURL := fmt.Sprintf("%s/%s/%s", frpcDownloadBase, version, archiveName)

	tmpDir, err := os.MkdirTemp("", "frpc-download-*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, archiveName)
	slog.Info("[frpc] 开始下载", "url", downloadURL, "dest", archivePath)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
		fmt.Sprintf("curl -fsSL --connect-timeout 15 -o '%s' '%s'", archivePath, downloadURL))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("下载失败: %w, output: %s", err, strings.TrimSpace(string(out)))
	}

	// 验证文件大小
	info, err := os.Stat(archivePath)
	if err != nil {
		return fmt.Errorf("下载文件不存在: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("下载文件为空")
	}

	// 解压
	slog.Info("[frpc] 正在解压", "archive", archiveName)
	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return fmt.Errorf("创建解压目录失败: %w", err)
	}

	cmd = exec.CommandContext(ctx, "/bin/sh", "-c",
		fmt.Sprintf("tar -xzf '%s' -C '%s'", archivePath, extractDir))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("解压失败: %w, output: %s", err, strings.TrimSpace(string(out)))
	}

	// 查找 frpc 二进制 —— tar.gz 内的顶层目录名可能是 frp_0.70.0_linux_arm64
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return fmt.Errorf("读取解压目录失败: %w", err)
	}

	var frpcBinPath string
	for _, entry := range entries {
		if entry.IsDir() {
			candidate := filepath.Join(extractDir, entry.Name(), frpcBinaryName)
			if _, statErr := os.Stat(candidate); statErr == nil {
				frpcBinPath = candidate
				break
			}
		}
	}
	// 也有可能直接在 extractDir 下
	if frpcBinPath == "" {
		candidate := filepath.Join(extractDir, frpcBinaryName)
		if _, statErr := os.Stat(candidate); statErr == nil {
			frpcBinPath = candidate
		}
	}
	if frpcBinPath == "" {
		return fmt.Errorf("解压后未找到 frpc 二进制")
	}

	// 停止正在运行的 frpc
	if running, pid := isFrpcRunning(); running {
		slog.Info("[frpc] 安装前停止运行中的 frpc", "pid", pid)
		stopFrpc()
		time.Sleep(500 * time.Millisecond)
	}

	// 备份旧二进制
	targetBin := getFrpcBinary()
	if _, err := os.Stat(targetBin); err == nil {
		_ = os.Rename(targetBin, targetBin+".bak")
	}

	// 移动到目标
	if err := os.Rename(frpcBinPath, targetBin); err != nil {
		return fmt.Errorf("移动二进制失败: %w", err)
	}
	if err := os.Chmod(targetBin, 0755); err != nil {
		return fmt.Errorf("设置权限失败: %w", err)
	}

	slog.Info("[frpc] 安装完成", "version", version, "path", targetBin)
	return nil
}

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

	// 确保目录存在
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

	// 写入 PID
	pidFile := filepath.Join(dir, frpcPIDName)
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0644); err != nil {
		slog.Warn("[frpc] 写入 PID 文件失败", "err", err.Error())
	}

	// 释放进程使其独立运行
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

	// 发送 SIGTERM
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("查找进程失败: %w", err)
	}
	if err := proc.Signal(nil); err != nil {
		// 进程不存在
		_ = os.Remove(pidFile)
		return nil
	}
	if err := proc.Terminate(); err != nil {
		return fmt.Errorf("终止进程失败: %w", err)
	}

	// 等待进程退出
	for i := 0; i < 10; i++ {
		time.Sleep(200 * time.Millisecond)
		if _, statErr := os.Stat(fmt.Sprintf("/proc/%d", pid)); statErr != nil {
			break
		}
		// 第二次发送 SIGKILL
		if i == 3 {
			_ = proc.Kill()
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
// 请求体: {"action": "start" | "stop" | "restart" | "reload"}
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
// 请求体: {"content": "toml内容"}
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
	// 备份旧配置
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
// 请求体: {"enabled": true/false}
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
// 调用 frpc 的 GET /api/status API，返回代理列表及状态。
func FrpcGetProxiesHandler(c *gin.Context) {
	rawBody, err := callFrpcAPI(http.MethodGet, "/api/status", nil)
	if err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "获取代理列表失败: " + err.Error()})
		return
	}

	// frpc /api/status 返回的 JSON 结构可能为:
	// {"proxies": [{"name": "...", "type": "...", "status": "...", ...}]}
	var statusResp struct {
		Proxies []json.RawMessage `json:"proxies"`
	}
	if err := json.Unmarshal(rawBody, &statusResp); err != nil {
		// 直接返回原始内容
		c.JSON(200, gin.H{"code": 0, "msg": "ok", "data": gin.H{"raw": json.RawMessage(rawBody)}})
		return
	}

	// 精简代理信息
	type ProxySummary struct {
		Name   string `json:"name"`
		Type   string `json:"type,omitempty"`
		Status string `json:"status,omitempty"`
	}

	proxies := make([]ProxySummary, 0, len(statusResp.Proxies))
	for _, raw := range statusResp.Proxies {
		var p ProxySummary
		if err := json.Unmarshal(raw, &p); err != nil {
			// 解析失败则保留原始 JSON
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
		"data": gin.H{
			"proxies": proxies,
			"total":   len(proxies),
		},
	})
}

// FrpcSetProxyHandler POST /api/frpc/proxies
// 请求体: {"action": "add" | "delete" | "update", "proxy": {...}}
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
		// POST /api/store/proxies
		rawBody, err := callFrpcAPI(http.MethodPost, "/api/store/proxies", req.Proxy)
		if err != nil {
			c.JSON(200, gin.H{"code": 1, "msg": "添加代理失败: " + err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "msg": "代理已添加", "data": json.RawMessage(rawBody)})

	case "update":
		// 从 proxy 对象中提取 name 以构建 URL
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
		// 从 proxy 对象中提取 name 以构建 URL
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

// ─────────────────────────── Handlers: 版本检查 & 安装 ───────────────────────────

// FrpcCheckBinaryVersionHandler GET /api/frpc/binary/version
// 通过 GitHub API 检查 frpc 最新版本号。
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

	// 如果 API 返回速率限制等消息
	if release.TagName == "" && release.Message != "" {
		c.JSON(200, gin.H{"code": 1, "msg": "GitHub API: " + release.Message})
		return
	}

	// 本地已安装版本
	localVersion := getFrpcVersion()
	binaryExists := true
	if _, err := os.Stat(getFrpcBinary()); err != nil {
		binaryExists = false
		localVersion = ""
	}

	// 默认使用 frpcLatestVersion 作为 fallback
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

// FrpcInstallHandler POST /api/frpc/install
// 请求体: {"action": "install" | "update" | "uninstall", "mode": "soft" | "full"}
func FrpcInstallHandler(c *gin.Context) {
	var req struct {
		Action string `json:"action" binding:"required"`
		Mode   string `json:"mode"` // uninstall 时的模式: soft(仅删二进制) / full(全删)
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "参数错误: " + err.Error()})
		return
	}

	validActions := map[string]bool{"install": true, "update": true, "uninstall": true}
	if !validActions[req.Action] {
		c.JSON(200, gin.H{"code": 1, "msg": "无效操作: " + req.Action})
		return
	}

	switch req.Action {
	case "install", "update":
		version := frpcLatestVersion
		if err := downloadFrpc(version); err != nil {
			c.JSON(200, gin.H{"code": 1, "msg": "安装失败: " + err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "msg": "安装完成", "data": gin.H{"version": version}})

	case "uninstall":
		if req.Mode == "" {
			req.Mode = "soft"
		}

		// 先停止运行中的 frpc
		if running, pid := isFrpcRunning(); running {
			slog.Info("[frpc] 卸载前停止", "pid", pid)
			_ = stopFrpc()
		}

		if req.Mode == "full" {
			// 删除整个目录
			dir := getFrpcDir()
			if err := os.RemoveAll(dir); err != nil {
				c.JSON(200, gin.H{"code": 1, "msg": "删除失败: " + err.Error()})
				return
			}
			c.JSON(200, gin.H{"code": 0, "msg": "已删除全部 frpc 文件"})
			return
		}

		// soft: 仅删除二进制，保留配置
		binPath := getFrpcBinary()
		_ = os.Remove(binPath + ".bak")
		if err := os.Remove(binPath); err != nil && !os.IsNotExist(err) {
			c.JSON(200, gin.H{"code": 1, "msg": "删除二进制失败: " + err.Error()})
			return
		}
		c.JSON(200, gin.H{"code": 0, "msg": "已删除 frpc 二进制（配置已保留）"})
	}
}
