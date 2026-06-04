package service

import (
	"net/http"
	"strconv"

	"gossh/gin"
)

// 允许的最大并发测速流。前端会发起多条并行连接以打满链路，
// 因此这里要留出足够余量（> 前端并发流数），否则多余的流会被 429 拒绝。
var speedTestLimiter = make(chan struct{}, 16)

// 预生成 1MB 随机数据块。
// 使用伪随机内容（而非全 0/全 0x66）是为了让数据不可压缩：
// 一旦链路上存在 gzip / 透明代理压缩，规则数据会被压成极小体积，
// 前端却按解压后的字节数计速，导致测速结果严重虚高或失真。
// 这里用一个零依赖的 xorshift 填充，init 期一次性生成后全程复用。
var speedTestChunk = func() []byte {
	buf := make([]byte, 1024*1024)
	seed := uint32(2463534242)
	for i := range buf {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		buf[i] = byte(seed)
	}
	return buf
}()

func SpeedTestHandler(c *gin.Context) {
	chunks := 200
	if raw := c.Query("size"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			chunks = n
		}
	}
	if raw := c.Query("ckSize"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			chunks = n
		}
	}
	if chunks < 1 {
		chunks = 1
	}
	if chunks > 1024 {
		chunks = 1024
	}

	select {
	case speedTestLimiter <- struct{}{}:
		defer func() { <-speedTestLimiter }()
	default:
		c.JSON(http.StatusTooManyRequests, gin.H{"code": 1, "msg": "测速请求过多，请稍后再试"})
		return
	}

	totalSize := int64(len(speedTestChunk)) * int64(chunks)
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(totalSize, 10))
	c.Header("Content-Disposition", "attachment; filename=speedtest.bin")
	// 显式声明不压缩，避免任何中间层对测速数据做内容编码。
	c.Header("Content-Encoding", "identity")
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Header("Pragma", "no-cache")
	c.Writer.WriteHeaderNow()

	w := c.Writer
	for i := 0; i < chunks; i++ {
		select {
		case <-c.Request.Context().Done():
			return
		default:
		}
		if _, err := w.Write(speedTestChunk); err != nil {
			return
		}
		// 1MB 大块写入会直接落到底层连接，无需每块都 Flush（每块 Flush 反而增加开销）。
		// 这里仅周期性 Flush，既保证数据持续下行（前端进度/速度实时刷新），又避免频繁系统调用。
		if (i+1)%16 == 0 {
			w.Flush()
		}
	}
}
