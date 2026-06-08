package service

import (
	"fmt"
	"os/exec"
	"strings"
	"gossh/gin"
	"regexp"
	"strconv"
)

// 核心 导出函数 (Go 规则:首字母大写 = 可导出(public), 首字母小写只能当前package用)
func NetAmbrGetHandler(c *gin.Context) {
	// 1.1 获取 ambr
	raw_ambr, err := getLatestAmbr()
	if err != nil {
		c.JSON(200, gin.H{
			"code": 1,
			"msg":  err.Error(),
		})
		return
	}
	// 1.2 解析 ambr
	dl, ul, dlUnitRaw, dlUnitNum, ulUnitRaw, ulUnitNum := parseAmbr(raw_ambr)
	// 2.1 获取 qci
	raw_qci, err := getLatestQci()
	// 2.2 解析 qci
	qci1, qci2 := parseQci(raw_qci)

	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"dl": gin.H{
				"value":    dl,
				"unit":     "Mbps",
				"unit_num": dlUnitNum,
				"unit_raw": dlUnitRaw,
			},
			"ul": gin.H{
				"value":    ul,
				"unit":     "Mbps",
				"unit_num": ulUnitNum,
				"unit_raw": ulUnitRaw,
			},
			"qci1": qci1,
			"qci2": qci2,
			"raw_ambr":   raw_ambr,
			"raw_qci":    raw_qci,
		},
	})
}

// 1.1 获取最新ambr (适配 G5Pro 格式: [DATA] cid1 NW_APN=xxx DL_AMBR=1024000Kbps UL_AMBR=204800Kbps QCI=9)
func getLatestAmbr() (string, error) {
	cmd := exec.Command("sh", "-c", `grep -E "DL_AMBR=.*UL_AMBR=" /logfs/key.log 2>/dev/null | tail -n 1`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("get ambr error: %s", string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// 1.2 AMBR 解析（适配 G5Pro 格式）
func parseAmbr(line string) (float64, float64, string, int, string, int) {
	dlVal, dlUnitRaw := extractAmbrValue(line, "DL_AMBR=")
	ulVal, ulUnitRaw := extractAmbrValue(line, "UL_AMBR=")

	dlUnitNum := ambrUnitToNum(dlUnitRaw)
	ulUnitNum := ambrUnitToNum(ulUnitRaw)

	dl := convertAmbrToMbps(dlVal, dlUnitRaw)
	ul := convertAmbrToMbps(ulVal, ulUnitRaw)

	return dl, ul, dlUnitRaw, dlUnitNum, ulUnitRaw, ulUnitNum
}

// 从 AMBR 字段提取数值和单位 (例如 DL_AMBR=1024000Kbps → 1024000, "Kbps")
func extractAmbrValue(line, key string) (int, string) {
	idx := strings.Index(line, key)
	if idx == -1 {
		return 0, ""
	}
	sub := line[idx+len(key):]
	// 找到空格或行尾结束
	end := strings.IndexAny(sub, " \t\n")
	if end == -1 {
		end = len(sub)
	}
	valStr := sub[:end]

	// 分离数字和单位
	re := regexp.MustCompile(`^(\d+)([KMG]?bps)$`)
	matches := re.FindStringSubmatch(valStr)
	if len(matches) >= 3 {
		val, _ := strconv.Atoi(matches[1])
		return val, matches[2]
	}
	return 0, ""
}

// AMBR 单位字符串转数字编码
func ambrUnitToNum(unit string) int {
	switch unit {
	case "Kbps":
		return 3
	case "Mbps":
		return 6
	case "Gbps":
		return 9
	}
	return 0
}

// AMBR 转换为 Mbps
func convertAmbrToMbps(val int, unit string) float64 {
	switch unit {
	case "Kbps":
		return float64(val) / 1000
	case "Mbps":
		return float64(val)
	case "Gbps":
		return float64(val) * 1000
	}
	return 0
}

// 2.1 获取最新qci (与 ambr 在同一行)
func getLatestQci() (string, error) {
	cmd := exec.Command("sh", "-c", `grep -E "DL_AMBR=.*UL_AMBR=" /logfs/key.log 2>/dev/null | tail -n 1`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("get qci error: %s", string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// 2.2 QCI 获取（支持两个值）
func parseQci(line string) (int, int) {
	idx := strings.Index(strings.ToUpper(line), "QCI=")
	if idx == -1 {
		return 0, 0
	}
	sub := line[idx+4:]
	// 提取 QCI 后的第一个数字
	re := regexp.MustCompile(`^(\d+)`)
	matches := re.FindStringSubmatch(sub)
	if len(matches) >= 2 {
		qci1, _ := strconv.Atoi(matches[1])
		// 尝试找第二个 QCI
		rest := sub[len(matches[0]):]
		idx2 := strings.Index(strings.ToUpper(rest), "QCI=")
		if idx2 != -1 {
			sub2 := rest[idx2+4:]
			matches2 := re.FindStringSubmatch(sub2)
			if len(matches2) >= 2 {
				qci2, _ := strconv.Atoi(matches2[1])
				return qci1, qci2
			}
		}
		return qci1, 0
	}
	return 0, 0
}
