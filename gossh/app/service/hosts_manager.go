package service

import (
	"context"
	"fmt"
	"gossh/gin"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	hostsFilePath   = "/etc/hosts"
	hostsBackupDir  = "/data/plugins/hosts"
	hostsBackupFile = "/data/plugins/hosts/hosts.bak"
)

// HostsStatus hosts 文件状态
type HostsStatus struct {
	Content    string `json:"content"`
	Lines      int    `json:"lines"`
	Entries    int    `json:"entries"`
	HasBackup  bool   `json:"has_backup"`
	BackupSize int64  `json:"backup_size"`
	Dnsmasq    bool   `json:"dnsmasq"`
	Writable   bool   `json:"writable"`
}

// HostsGetHandler GET /api/system/hosts
func HostsGetHandler(c *gin.Context) {
	status, err := getHostsStatus()
	if err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": status})
}

// HostsSaveHandler PUT /api/system/hosts
func HostsSaveHandler(c *gin.Context) {
	var req struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": err.Error()})
		return
	}

	// 备份
	if _, err := os.Stat(hostsFilePath); err == nil {
		_ = os.MkdirAll(hostsBackupDir, 0755)
		_ = copyFile(hostsFilePath, hostsBackupFile)
	}

	// 写入
	if err := os.WriteFile(hostsFilePath, []byte(req.Content), 0644); err != nil {
		c.JSON(200, gin.H{"code": 2, "msg": "写入失败: " + err.Error()})
		return
	}

	c.JSON(200, gin.H{"code": 0, "msg": "保存成功"})
}

// HostsRestoreHandler POST /api/system/hosts/restore
func HostsRestoreHandler(c *gin.Context) {
	if _, err := os.Stat(hostsBackupFile); err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "没有备份文件"})
		return
	}
	if err := copyFile(hostsBackupFile, hostsFilePath); err != nil {
		c.JSON(200, gin.H{"code": 2, "msg": "恢复失败: " + err.Error()})
		return
	}
	_ = os.Chmod(hostsFilePath, 0644)
	c.JSON(200, gin.H{"code": 0, "msg": "已从备份恢复"})
}

// HostsReloadDnsmasqHandler POST /api/system/hosts/reload-dns
func HostsReloadDnsmasqHandler(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if _, err := os.Stat("/etc/init.d/dnsmasq"); err == nil {
		// 尝试 reload，失败则 restart
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", "/etc/init.d/dnsmasq reload 2>&1 || /etc/init.d/dnsmasq restart 2>&1")
	} else {
		c.JSON(200, gin.H{"code": 0, "msg": "dnsmasq 不存在，hosts 由 libc 直接解析，无需刷新"})
		return
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "刷新失败: " + err.Error(), "output": string(out)})
		return
	}
	c.JSON(200, gin.H{"code": 0, "msg": "dnsmasq 已刷新", "output": string(out)})
}

// ─────────────────────────── 内部函数 ───────────────────────────

func getHostsStatus() (*HostsStatus, error) {
	data, err := os.ReadFile(hostsFilePath)
	if err != nil {
		return nil, fmt.Errorf("读取 hosts 失败: %w", err)
	}
	content := string(data)
	lines := strings.Count(content, "\n")
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		lines++
	}
	entries := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entries++
	}

	info, _ := os.Stat(hostsBackupFile)
	hasBackup := info != nil
	backupSize := int64(0)
	if info != nil {
		backupSize = info.Size()
	}

	_, dnsmasqErr := os.Stat("/etc/init.d/dnsmasq")
	dnsmasq := dnsmasqErr == nil

	writable := true
	if err := testWritePermission(hostsFilePath); err != nil {
		writable = false
	}

	return &HostsStatus{
		Content:    content,
		Lines:      lines,
		Entries:    entries,
		HasBackup:  hasBackup,
		BackupSize: backupSize,
		Dnsmasq:    dnsmasq,
		Writable:   writable,
	}, nil
}

func testWritePermission(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	f.Close()
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	_ = os.MkdirAll(dir, 0755)
	return os.WriteFile(dst, data, 0644)
}
