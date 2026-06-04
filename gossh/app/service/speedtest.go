package service

import (
	"net/http"
	"strconv"

	"gossh/gin"
)

var speedTestLimiter = make(chan struct{}, 4)

var speedTestChunk = func() []byte {
	buf := make([]byte, 1024*1024)
	for i := range buf {
		buf[i] = 0x66
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

	totalSize := int64(len(speedTestChunk) * chunks)
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(totalSize, 10))
	c.Header("Content-Disposition", "attachment; filename=speedtest.bin")
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Header("Pragma", "no-cache")

	flusher, _ := c.Writer.(http.Flusher)
	for i := 0; i < chunks; i++ {
		select {
		case <-c.Request.Context().Done():
			return
		default:
		}
		if _, err := c.Writer.Write(speedTestChunk); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
