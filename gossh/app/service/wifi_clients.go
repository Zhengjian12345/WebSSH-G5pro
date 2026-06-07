package service

import (
	"encoding/json"
	"fmt"
	"gossh/gin"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// WifiClient 表示一个 WiFi 连接设备
type WifiClient struct {
	MAC        string  `json:"mac"`
	Interface  string  `json:"interface"`
	Signal     int     `json:"signal"`
	RxRate     float64 `json:"rx_rate"`
	TxRate     float64 `json:"tx_rate"`
	Connected  bool    `json:"connected"`
	MLO        bool    `json:"mlo"`
	MLOLinks   []string `json:"mlo_links,omitempty"`
}

// 常见 WiFi 接口名（G5Pro 适配）
var defaultWifiIfaces = []string{"ra0", "rai0", "rai1"}

// WifiClientsHandler GET /api/wifi/clients
func WifiClientsHandler(c *gin.Context) {
	ifaceParam := strings.TrimSpace(c.Query("iface"))
	var ifaces []string
	if ifaceParam != "" {
		ifaces = strings.Split(ifaceParam, ",")
	} else {
		ifaces = defaultWifiIfaces
	}

	clients := getWifiClients(ifaces)
	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"msg":  "ok",
		"data": gin.H{
			"clients": clients,
			"ifaces":  ifaces,
			"count":   len(clients),
		},
	})
}

func getWifiClients(ifaces []string) []WifiClient {
	var allClients []WifiClient
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, iface := range ifaces {
		iface := strings.TrimSpace(iface)
		if iface == "" {
			continue
		}
		wg.Add(1)
		go func(ifname string) {
			defer wg.Done()
			clients := getClientsForIface(ifname)
			mu.Lock()
			allClients = append(allClients, clients...)
			mu.Unlock()
		}(iface)
	}
	wg.Wait()

	// MLO 设备 MAC 去重：如果同一 MAC 出现在多个接口，合并为 MLO
	allClients = mergeMLOClients(allClients)
	return allClients
}

func getClientsForIface(iface string) []WifiClient {
	// 优先尝试 iwinfo
	clients, ok := getClientsFromIwinfo(iface)
	if ok && len(clients) > 0 {
		return clients
	}
	// 回退到 iw
	clients, ok = getClientsFromIw(iface)
	if ok && len(clients) > 0 {
		return clients
	}
	// 回退到 ubus
	clients, ok = getClientsFromUbus(iface)
	if ok && len(clients) > 0 {
		return clients
	}
	return nil
}

// iwinfo 解析（OpenWrt 常见）
func getClientsFromIwinfo(iface string) ([]WifiClient, bool) {
	cmd := exec.Command("iwinfo", iface, "assoclist")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, false
	}
	return parseIwinfoOutput(iface, string(out)), true
}

func parseIwinfoOutput(iface, output string) []WifiClient {
	var clients []WifiClient
	lines := strings.Split(output, "\n")
	var current *WifiClient
	macRe := regexp.MustCompile(`^([0-9A-Fa-f:]{17})\s`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if current != nil {
				clients = append(clients, *current)
				current = nil
			}
			continue
		}
		if m := macRe.FindStringSubmatch(line); m != nil {
			if current != nil {
				clients = append(clients, *current)
			}
			current = &WifiClient{
				MAC:       strings.ToUpper(m[1]),
				Interface: iface,
				Connected: true,
			}
			continue
		}
		if current == nil {
			continue
		}
		if strings.Contains(line, "Signal") {
			current.Signal = extractIntWiFi(line, "Signal:", " dBm")
		}
		if strings.Contains(line, "RX") && strings.Contains(line, "rate") {
			current.RxRate = extractFloatRate(line)
		}
		if strings.Contains(line, "TX") && strings.Contains(line, "rate") {
			current.TxRate = extractFloatRate(line)
		}
	}
	if current != nil {
		clients = append(clients, *current)
	}
	return clients
}

// iw 解析
func getClientsFromIw(iface string) ([]WifiClient, bool) {
	cmd := exec.Command("iw", "dev", iface, "station", "dump")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, false
	}
	return parseIwOutput(iface, string(out)), true
}

func parseIwOutput(iface, output string) []WifiClient {
	var clients []WifiClient
	lines := strings.Split(output, "\n")
	var current *WifiClient
	macRe := regexp.MustCompile(`^Station\s+([0-9A-Fa-f:]{17})`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if current != nil {
				clients = append(clients, *current)
				current = nil
			}
			continue
		}
		if m := macRe.FindStringSubmatch(line); m != nil {
			if current != nil {
				clients = append(clients, *current)
			}
			current = &WifiClient{
				MAC:       strings.ToUpper(m[1]),
				Interface: iface,
				Connected: true,
			}
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "signal:") {
			current.Signal = extractIntWiFi(line, "signal:", " dBm")
		}
		if strings.HasPrefix(line, "rx bitrate:") {
			current.RxRate = extractFloatRate(line)
		}
		if strings.HasPrefix(line, "tx bitrate:") {
			current.TxRate = extractFloatRate(line)
		}
	}
	if current != nil {
		clients = append(clients, *current)
	}
	return clients
}

// ubus 解析（OpenWrt hostapd）
func getClientsFromUbus(iface string) ([]WifiClient, bool) {
	// 尝试通过 ubus 获取 hostapd 客户端列表
	cmd := exec.Command("ubus", "call", "hostapd."+iface, "get_clients")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, false
	}
	return parseUbusClients(iface, string(out)), true
}

func parseUbusClients(iface, output string) []WifiClient {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(output), &data); err != nil {
		return nil
	}
	stations, ok := data["clients"].(map[string]interface{})
	if !ok {
		return nil
	}
	var clients []WifiClient
	for mac, info := range stations {
		client := WifiClient{
			MAC:       strings.ToUpper(mac),
			Interface: iface,
			Connected: true,
		}
		if m, ok := info.(map[string]interface{}); ok {
			if sig, ok := m["signal"].(float64); ok {
				client.Signal = int(sig)
			}
			if rx, ok := m["rx_rate"].(map[string]interface{}); ok {
				if v, ok := rx["bitrate"].(float64); ok {
					client.RxRate = v
				}
			}
			if tx, ok := m["tx_rate"].(map[string]interface{}); ok {
				if v, ok := tx["bitrate"].(float64); ok {
					client.TxRate = v
				}
			}
		}
		clients = append(clients, client)
	}
	return clients
}

// mergeMLOClients 合并多接口上同一 MAC 的设备为 MLO
func mergeMLOClients(clients []WifiClient) []WifiClient {
	macMap := make(map[string]*WifiClient)
	for i := range clients {
		c := &clients[i]
		mac := c.MAC
		if existing, ok := macMap[mac]; ok {
			existing.MLO = true
			existing.MLOLinks = append(existing.MLOLinks, c.Interface)
			// 保留更强的信号
			if c.Signal < existing.Signal { // 信号为负值，越小越强
				existing.Signal = c.Signal
			}
			if c.RxRate > existing.RxRate {
				existing.RxRate = c.RxRate
			}
			if c.TxRate > existing.TxRate {
				existing.TxRate = c.TxRate
			}
		} else {
			cp := *c
			cp.MLOLinks = []string{cp.Interface}
			macMap[mac] = &cp
		}
	}
	result := make([]WifiClient, 0, len(macMap))
	for _, c := range macMap {
		result = append(result, *c)
	}
	return result
}

func extractIntWiFi(s, prefix, suffix string) int {
	idx := strings.Index(s, prefix)
	if idx == -1 {
		return 0
	}
	sub := s[idx+len(prefix):]
	if suffix != "" {
		if e := strings.Index(sub, suffix); e != -1 {
			sub = sub[:e]
		}
	}
	var val int
	fmt.Sscanf(strings.TrimSpace(sub), "%d", &val)
	return val
}

func extractFloatRate(s string) float64 {
	// 尝试提取类似 "650.0 MBit/s" 或 "650 MBit/s" 中的数字
	re := regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*MBit/s`)
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	var val float64
	fmt.Sscanf(m[1], "%f", &val)
	return val
}
