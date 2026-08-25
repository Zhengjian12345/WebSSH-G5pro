package service

import (
	"encoding/json"
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
		c.JSON(200, gin.H{"code": 1, "msg": info.RawError, "data": info})
		return
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		info.RawError = "设备返回为空"
		c.JSON(200, gin.H{"code": 1, "msg": info.RawError, "data": info})
		return
	}

	// 使用 encoding/json 正确解析 ubus 返回的标准 JSON
	var ubusData map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &ubusData); err != nil {
		info.RawError = "JSON 解析失败: " + err.Error() + ", raw: " + raw
		c.JSON(200, gin.H{"code": 1, "msg": info.RawError, "data": info})
		return
	}

	nt := toStr(ubusData["network_type"])
	info.NetType = nt
	info.Operator = toStr(ubusData["network_provider"])

	ntUp := strings.ToUpper(nt)
	is5G := strings.Contains(ntUp, "NR") ||
		strings.Contains(ntUp, "5G") ||
		strings.Contains(ntUp, "SA") ||
		strings.Contains(ntUp, "NSA") ||
		strings.Contains(ntUp, "ENDC")
	info.Is5G = is5G

	if is5G {
		info.PCI = toStr(ubusData["nr5g_pci"])
		info.EARFCN = toStr(ubusData["nr5g_action_channel"])
		info.CellID = toStr(ubusData["nr5g_cell_id"])
		info.RSRP = toStr(ubusData["nr5g_rsrp"])
	} else {
		info.PCI = toStr(ubusData["lte_pci"])
		info.EARFCN = toStr(ubusData["lte_action_channel"])
		info.CellID = toStr(ubusData["cell_id"])
		info.RSRP = toStr(ubusData["lte_rsrp"])
	}

	c.JSON(200, gin.H{"code": 0, "data": info})
}

// toStr 将 interface{} 转为 string
func toStr(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		// JSON 数字解析为 float64，整数去掉小数
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%v", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
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
