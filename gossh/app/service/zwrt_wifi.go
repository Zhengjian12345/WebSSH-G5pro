package service

import (
	// "gossh/app/utils"
	// "log/slog"
	// "gossh/gin"
	// "net/http"
	// "sort"
	// "sync"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"gossh/gin"
)

func WifiPsmGetHandler(c *gin.Context) {
	var req struct {
		Ifaces []string `json:"ifaces"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 1, "msg": "bad request"})
		return
	}
	data, _ := BatchGetPowerSave(req.Ifaces)
	c.JSON(200, gin.H{
		"code": 0,
		"data": data,
	})
}
func BatchGetPowerSave(ifaces []string) (map[string]string, error) {
	result := make(map[string]string)
	for _, iface := range ifaces {
		// power save
		psm, err := getPowerSave(iface)
		if err != nil {
			psm = "unknown"
		}
		result[iface+"_psm"] = psm
		// link status
		status, err := getLinkStatus(iface)
		if err != nil {
			status = "unknown"
		}
		result[iface+"_status"] = status
	}
	return result, nil
}
func getPowerSave(iface string) (string, error) {
	cmd := exec.Command("iw", "dev", iface, "get", "power_save")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("iw error: %s", string(out))
	}
	// 输出一般是：Power save: on
	fields := strings.Fields(string(out))
	return fields[len(fields)-1], nil
}
func getLinkStatus(iface string) (string, error) {
	cmd := exec.Command("ip", "link", "show", iface)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ip link error: %s", string(out))
	}
	output := string(out)
	if strings.Contains(output, "state UP") {
		return "up", nil
	}
	if strings.Contains(output, "state DOWN") {
		return "down", nil
	}
	return "unknown", nil
}

func WifiPsmSetHandler(c *gin.Context) {
	var req struct {
		Ifaces []string `json:"ifaces"`
		Mode   string   `json:"mode"` // on / off
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 1, "msg": "bad request"})
		return
	}
	result := BatchSetPowerSave(req.Ifaces, req.Mode)
	c.JSON(200, gin.H{
		"code": 0,
		"result": result,
	})
}
func BatchSetPowerSave(ifaces []string, mode string) map[string]string {
	result := make(map[string]string)

	for _, iface := range ifaces {
		if err := setPowerSave(iface, mode); err != nil {
			result[iface] = "fail"
		} else {
			result[iface] = "ok"
		}
	}

	return result
}
func setPowerSave(iface, mode string) error {
	if mode != "on" && mode != "off" {
		return fmt.Errorf("invalid mode")
	}
	cmd := exec.Command("iw", "dev", iface, "set", "power_save", mode)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("iw error: %s", string(out))
	}
	return nil
}



func setWifiState(iface string, up bool) error {
	state := "down"
	if up {
		state = "up"
	}

	cmd := exec.Command("ifconfig", iface, state)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ifconfig %s %s failed: %s", iface, state, string(out))
	}
	return nil
}
func BatchSetWifiState(ifaces []string, up bool) map[string]string {
	result := make(map[string]string)

	for _, iface := range ifaces {
		if err := setWifiState(iface, up); err != nil {
			result[iface] = "fail"
		} else {
			result[iface] = "ok"
		}
	}

	return result
}
func WifiStateSetHandler(c *gin.Context) {
	var req struct {
		Ifaces []string `json:"ifaces"`
		Up     bool     `json:"up"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 1, "msg": "bad request"})
		return
	}

	result := BatchSetWifiState(req.Ifaces, req.Up)

	c.JSON(200, gin.H{
		"code":   0,
		"result": result,
	})
}

// WifiClientsGetHandler 获取所有无线频段的客户端信息（信号+速率）
// 使用 iwinfo assoclist 获取信号和速率，返回格式与前端 buildRfMapFromApiResponse 兼容
func WifiClientsGetHandler(c *gin.Context) {
	ifaces := []string{"ra0", "rai0", "rai1"}
	allClients := make(map[string]interface{})

	for _, iface := range ifaces {
		clients := parseIwinfoAssoclist(iface)
		allClients[iface] = gin.H{
			"clients": clients,
		}
	}

	c.JSON(200, gin.H{
		"code": 0,
		"data": allClients,
	})
}

// parseIwinfoAssoclist 解析 iwinfo {iface} assoclist 的文本输出
// 输出格式:
//   AA:BB:CC:DD:EE:FF -68 dBm / -95 dBm (SNR 27) 120 ms ago
//    RX: 72.0 MBit/s 100 Pkts.
//    TX: 58.0 MBit/s 200 Pkts.
// 返回 MAC -> {signal, rate: {tx, rx}} 映射
func parseIwinfoAssoclist(iface string) map[string]interface{} {
	clients := make(map[string]interface{})
	cmd := exec.Command("sh", "-c", fmt.Sprintf("iwinfo %s assoclist 2>/dev/null", iface))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return clients
	}

	lines := strings.Split(string(output), "\n")
	var currentMac string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 匹配 MAC 地址行: AA:BB:CC:DD:EE:FF -68 dBm ...
		if mac := extractMacFromAssocLine(line); mac != "" {
			currentMac = mac
			// 提取信号强度
			signal := extractSignalFromAssocLine(line)
			if _, exists := clients[currentMac]; !exists {
				clients[currentMac] = map[string]interface{}{
					"signal": signal,
					"rate":   map[string]interface{}{"tx": 0, "rx": 0},
				}
			} else {
				clients[currentMac].(map[string]interface{})["signal"] = signal
			}
			continue
		}

		// 匹配速率行: RX: 72.0 MBit/s 或 TX: 58.0 MBit/s
		if currentMac != "" {
			if strings.HasPrefix(line, "RX:") {
				rate := extractRateFromLine(line)
				if rate > 0 {
					setNestedRate(clients, currentMac, "rx", rate)
				}
			} else if strings.HasPrefix(line, "TX:") {
				rate := extractRateFromLine(line)
				if rate > 0 {
					setNestedRate(clients, currentMac, "tx", rate)
				}
			}
		}
	}

	return clients
}

func extractMacFromAssocLine(line string) string {
	// MAC 地址格式: XX:XX:XX:XX:XX:XX
	re := regexp.MustCompile(`^([0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2})`)
	matches := re.FindStringSubmatch(line)
	if len(matches) >= 2 {
		return strings.ToUpper(matches[1])
	}
	return ""
}

func extractSignalFromAssocLine(line string) float64 {
	// 信号格式: -68 dBm
	re := regexp.MustCompile(`(-?\d+)\s*dBm`)
	matches := re.FindStringSubmatch(line)
	if len(matches) >= 2 {
		val, err := strconv.ParseFloat(matches[1], 64)
		if err == nil {
			return val
		}
	}
	return 0
}

func extractRateFromLine(line string) float64 {
	// 速率格式: 72.0 MBit/s 或 2402 MBit/s
	re := regexp.MustCompile(`(\d+(?:\.\d+)?)\s*MBit/s`)
	matches := re.FindStringSubmatch(line)
	if len(matches) >= 2 {
		val, err := strconv.ParseFloat(matches[1], 64)
		if err == nil {
			return val
		}
	}
	return 0
}

func setNestedRate(clients map[string]interface{}, mac, direction string, rate float64) {
	if entry, exists := clients[mac]; exists {
		if entryMap, ok := entry.(map[string]interface{}); ok {
			if rateMap, ok := entryMap["rate"].(map[string]interface{}); ok {
				rateMap[direction] = rate
			}
		}
	}
}

