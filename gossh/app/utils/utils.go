package utils

import (
	"crypto/rand"
	"math/big"
	"path/filepath"
	"os"
	"unicode/utf8"
)

// RandString 生成指定长度随机字符串（密码学安全）
func RandString(length int) string {
	const charset = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			// fallback: 不应发生，但防止 panic
			result[i] = charset[i%len(charset)]
			continue
		}
		result[i] = charset[n.Int64()]
	}
	return string(result)
}

func TruncateString(s string, length int) string {
	if utf8.RuneCountInString(s) <= length {
		return s
	}

	runes := []rune(s)
	if length < 1 {
		return ""
	}
	return string(runes[:length])
}

func GetExecDir() string {
	// 获取当前工作目录
	dir, err := os.Getwd()
	if err == nil && dir != "" {
		absDir, err2 := filepath.Abs(dir)
		if err2 == nil {
			return absDir
		}
		return dir
	}

	// os.Getwd 失败时 fallback
	tmpDir := os.TempDir()
	if tmpDir != "" {
		return tmpDir
	}

	// 最后兜底
	return "."
}