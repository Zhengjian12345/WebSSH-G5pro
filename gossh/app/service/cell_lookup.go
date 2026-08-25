package service

import (
	"fmt"
	"gossh/gin"
	"os"
	"os/exec"
	"strings"
)

const (
	cellLookupDataDir  = "/data/plugins/cell_lookup"
	cellLookupDataFile = cellLookupDataDir + "/cell_data.json.gz"
)

// CellLookupStatus 数据状态
type CellLookupStatus struct {
	Exists   bool   `json:"exists"`
	FileSize int64  `json:"file_size"`
	SizeText string `json:"size_text"`
}

// CellLookupDeviceInfo 设备当前小区参数
type CellLookupDeviceInfo struct {
	NetType   string `json:"net_type"`
	Operator  string `json:"operator"`
	PCI       string `json:"pci"`
	EARFCN    string `json:"earfcn"`
	CellID    string `json:"cell_id"`
	RSRP      string `json:"rsrp"`
	Is5G      bool   `json:"is_5g"`
	RawError  string `json:"raw_error,omitempty"`
}

// CellLookupGetStatusHandler GET /api/system/cell-lookup/status
func CellLookupGetStatusHandler(c *gin.Context) {
	status := CellLookupStatus{}
	if info, err := os.Stat(cellLookupDataFile); err == nil {
		status.Exists = true
		status.FileSize = info.Size()
		status.SizeText = formatFileSize(info.Size())
	}
	c.JSON(200, gin.H{"code": 0, "data": status})
}

// CellLookupGetDataHandler GET /api/system/cell-lookup/data
// 直接返回 .gz 文件，前端用 DecompressionStream 解压
// 注意：不能设置 Content-Encoding: gzip，否则浏览器会透明解压，
// 导致前端 DecompressionStream 二次解压失败
func CellLookupGetDataHandler(c *gin.Context) {
	if _, err := os.Stat(cellLookupDataFile); err != nil {
		c.JSON(404, gin.H{"code": 1, "msg": "数据文件不存在"})
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.File(cellLookupDataFile)
}

// CellLookupUploadHandler POST /api/system/cell-lookup/upload
func CellLookupUploadHandler(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(200, gin.H{"code": 1, "msg": "获取上传文件失败: " + err.Error()})
		return
	}

	// 确保目录存在
	if err := os.MkdirAll(cellLookupDataDir, 0755); err != nil {
		c.JSON(200, gin.H{"code": 2, "msg": "创建目录失败: " + err.Error()})
		return
	}

	dst := cellLookupDataFile
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(200, gin.H{"code": 3, "msg": "保存文件失败: " + err.Error()})
		return
	}

	info, _ := os.Stat(dst)
	c.JSON(200, gin.H{"code": 0, "msg": "上传成功", "data": CellLookupStatus{
		Exists:   true,
		FileSize: info.Size(),
		SizeText: formatFileSize(info.Size()),
	}})
}

// CellLookupUninstallHandler POST /api/system/cell-lookup/uninstall
func CellLookupUninstallHandler(c *gin.Context) {
	_ = os.RemoveAll(cellLookupDataDir)
	// 清理可能的临时文件
	_ = os.Remove("/data/plugins/uploads/cell_data.json")
	_ = os.Remove("/data/plugins/uploads/cell_data.json.gz")
	c.JSON(200, gin.H{"code": 0, "msg": "数据已卸载"})
}

// CellLookupDeviceInfoHandler GET /api/system/cell-lookup/device-info
// G5 Pro 通过 ubus (zte_nwinfo_api) 获取当前小区参数，与 U60 Pro 的 goform API 不同
func CellLookupDeviceInfoHandler(c *gin.Context) {
	info := CellLookupDeviceInfo{}

	// G5 Pro: ubus call zte_nwinfo_api nwinfo_get_netinfo
	out, err := exec.Command("sh", "-c",
		"ubus call zte_nwinfo_api nwinfo_get_netinfo 2>/dev/null").CombinedOutput()
	if err != nil {
		info.RawError = "ubus 调用失败: " + err.Error()
		c.JSON(200, gin.H{"code": 1, "msg": info.RawError})
		return
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		info.RawError = "设备返回为空"
		c.JSON(200, gin.H{"code": 1, "msg": info.RawError})
		return
	}

	// ubus 返回标准 JSON，直接解析
	data := parseGoformOutput(raw)
	nt := getStr(data, "network_type")
	info.NetType = nt
	info.Operator = getStr(data, "network_provider")

	is5G := strings.Contains(strings.ToUpper(nt), "NR") ||
		strings.Contains(strings.ToUpper(nt), "5G") ||
		strings.Contains(strings.ToUpper(nt), "SA") ||
		strings.Contains(strings.ToUpper(nt), "NSA") ||
		strings.Contains(strings.ToUpper(nt), "ENDC")
	info.Is5G = is5G

	if is5G {
		info.PCI = getStr(data, "nr5g_pci")
		info.EARFCN = getStr(data, "nr5g_action_channel")
		info.CellID = getStr(data, "nr5g_cell_id")
		info.RSRP = getStr(data, "nr5g_rsrp")
	} else {
		info.PCI = getStr(data, "lte_pci")
		info.EARFCN = getStr(data, "lte_action_channel")
		info.CellID = getStr(data, "cell_id")
		info.RSRP = getStr(data, "lte_rsrp")
	}

	c.JSON(200, gin.H{"code": 0, "data": info})
}

func formatFileSize(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%dB", b)
	}
	if b < 1048576 {
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(b)/1048576)
}

func parseGoformOutput(raw string) map[string]string {
	data := map[string]string{}
	// 尝试 JSON
	if strings.HasPrefix(raw, "{") {
		// 简单 JSON 解析，避免引入额外依赖
		lines := strings.Split(raw, ",")
		for _, line := range lines {
			line = strings.Trim(line, "{}\"")
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				k := strings.Trim(parts[0], "\" ")
				v := strings.Trim(parts[1], "\" ")
				data[k] = v
			}
		}
		return data
	}
	// key=value 格式
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			data[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return data
}

func getStr(m map[string]string, key string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return ""
}
