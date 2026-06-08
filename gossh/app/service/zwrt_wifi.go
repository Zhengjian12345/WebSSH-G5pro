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
// 使用 iw dev {iface} station dump 获取信号和速率（iwinfo assoclist 在 G5Pro 上不可用）
// 返回格式与前端 buildRfMapFromApiResponse 兼容
func WifiClientsGetHandler(c *gin.Context) {
	ifaces := []string{"ra0", "rai0", "rai1"}
	allClients := make(map[string]interface{})

	for _, iface := range ifaces {
		clients := parseIwStationDump(iface)
		allClients[iface] = gin.H{
			"clients": clients,
		}
	}

	c.JSON(200, gin.H{
		"code": 0,
		"data": allClients,
	})
}

// parseIwStationDump 解析 iw dev {iface} station dump 的文本输出
// 输出格式:
//   Station AA:BB:CC:DD:EE:FF (on ra0)
//       signal: -68 dBm
//       tx bitrate: 2402 MBit/s MCS 15 160MHz HE-NSS 2 HE-GI 0
//       rx bitrate: 1201 MBit/s MCS 11 80MHz HE-NSS 2 HE-GI 0
// 返回 MAC -> {signal, rate: {tx, rx}} 映射
func parseIwStationDump(iface string) map[string]interface{} {
	clients := make(map[string]interface{})
	cmd := exec.Command("sh", "-c", fmt.Sprintf("iw dev %s station dump 2>/dev/null", iface))
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

		// 匹配 Station 行: Station AA:BB:CC:DD:EE:FF (on ra0)
		if strings.HasPrefix(line, "Station ") {
			re := regexp.MustCompile(`Station\s+([0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2})`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				currentMac = strings.ToUpper(matches[1])
				clients[currentMac] = map[string]interface{}{
					"signal": 0.0,
					"rate":   map[string]interface{}{"tx": 0.0, "rx": 0.0},
				}
			}
			continue
		}

		if currentMac == "" {
			continue
		}

		// 匹配信号行: signal: -68 dBm  或  signal avg: -65 dBm
		if strings.HasPrefix(line, "signal:") && !strings.HasPrefix(line, "signal avg:") && !strings.HasPrefix(line, "signal ") {
			re := regexp.MustCompile(`signal:\s+(-?\d+(?:\.\d+)?)\s*dBm`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				val, err := strconv.ParseFloat(matches[1], 64)
				if err == nil {
					setNestedField(clients, currentMac, "signal", val)
				}
			}
			continue
		}

		// 匹配 tx bitrate: 2402 MBit/s MCS 15 ...
		if strings.HasPrefix(line, "tx bitrate:") {
			re := regexp.MustCompile(`tx bitrate:\s+(\d+(?:\.\d+)?)\s*MBit/s`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				val, err := strconv.ParseFloat(matches[1], 64)
				if err == nil {
					setNestedRate(clients, currentMac, "tx", val)
				}
			}
			continue
		}

		// 匹配 rx bitrate: 1201 MBit/s MCS 11 ...
		if strings.HasPrefix(line, "rx bitrate:") {
			re := regexp.MustCompile(`rx bitrate:\s+(\d+(?:\.\d+)?)\s*MBit/s`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				val, err := strconv.ParseFloat(matches[1], 64)
				if err == nil {
					setNestedRate(clients, currentMac, "rx", val)
				}
			}
			continue
		}
	}

	return clients
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

func setNestedField(clients map[string]interface{}, mac, field string, value interface{}) {
	if entry, exists := clients[mac]; exists {
		if entryMap, ok := entry.(map[string]interface{}); ok {
			entryMap[field] = value
		}
	}
}

