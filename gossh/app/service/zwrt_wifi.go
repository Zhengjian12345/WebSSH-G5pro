package service

import (
	// "gossh/app/utils"
	// "log/slog"
	// "gossh/gin"
	// "net/http"
	// "sort"
	// "sync"
	"encoding/json"
	"fmt"
	"os/exec"
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
// G5Pro 上 iwinfo assoclist、iw dev station dump、iwpriv show stainfo 均不可用
// 使用 ubus call hostapd.{iface} get_clients 获取信号和速率
// 信号：准确（dBm）；速率：近似值（hostapd rate.tx 为 MediaTek 内部编码）
//   - 2.4G (ra0): 除以 92307 近似换算为 Mbps
//   - 5G (rai0/rai1): 除以 2600 近似换算为 Mbps
// 返回格式与前端 buildRfMapFromApiResponse 兼容
func WifiClientsGetHandler(c *gin.Context) {
	ifaces := []string{"ra0", "rai0", "rai1"}
	allClients := make(map[string]interface{})

	for _, iface := range ifaces {
		clients := parseHostapdClients(iface)
		allClients[iface] = gin.H{
			"clients": clients,
		}
	}

	c.JSON(200, gin.H{
		"code": 0,
		"data": allClients,
	})
}

// parseHostapdClients 通过 ubus call hostapd.{iface} get_clients 获取客户端信息
// hostapd 返回格式:
//   {"freq":5180,"clients":{"AA:BB:CC:DD:EE:FF":{"signal":-56,"rate":{"rx":0,"tx":6232000},...}}}
// signal 单位: dBm（准确）；rate.tx 单位: MediaTek 内部编码
func parseHostapdClients(iface string) map[string]interface{} {
	clients := make(map[string]interface{})
	cmd := exec.Command("sh", "-c", fmt.Sprintf("ubus call hostapd.%s get_clients 2>/dev/null", iface))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return clients
	}

	var data struct {
		Clients map[string]struct {
			Signal float64 `json:"signal"`
			Rate   struct {
				Rx float64 `json:"rx"`
				Tx float64 `json:"tx"`
			} `json:"rate"`
		} `json:"clients"`
	}

	if err := json.Unmarshal(output, &data); err != nil {
		return clients
	}

	// 根据频段选择不同的换算因子
	// 2.4G (ra0): 实测 rate.tx≈6000000 → 实际≈65Mbps → 因子≈92307
	// 5G (rai0/rai1): 实测 rate.tx≈6232000 → 实际≈2402Mbps → 因子≈2600
	divisor := 2600.0
	if iface == "ra0" {
		divisor = 92307.0
	}

	for mac, st := range data.Clients {
		mac = strings.ToUpper(mac)
		clients[mac] = map[string]interface{}{
			"signal": st.Signal,
			"rate": map[string]interface{}{
				"tx": st.Rate.Tx / divisor,
				"rx": st.Rate.Rx / divisor,
			},
		}
	}

	return clients
}

