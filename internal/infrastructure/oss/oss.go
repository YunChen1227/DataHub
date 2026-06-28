// Package oss wraps the阿里云 OSS SDK with a minimal upload helper used by the
// 租赁分V2-D (rental) 上游接入: 启动时把固定授权书文件上传到 OSS, 取回 licenseUrl
// 供后续所有查询复用 (DESIGN §6 租赁分上游)。
package oss

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"time"

	alioss "github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// Config holds the OSS endpoint + 凭证 + 目标 bucket/前缀。来自上游商务分配,
// 仅落 YAML 配置, 不入代码。
type Config struct {
	Endpoint        string
	AccessKeyID     string
	AccessKeySecret string
	Bucket          string
	ObjectPrefix    string // 必须以 "approve_files/" 开头 (上游约束)
}

// configured reports whether enough fields are set to attempt an upload.
func (c Config) configured() bool {
	return c.Endpoint != "" && c.AccessKeyID != "" && c.AccessKeySecret != "" && c.Bucket != ""
}

// UploadFile uploads localPath to OSS and returns the公网可访问 URL。objectName 由
// 前缀 + 时间戳 + 随机串 + 原文件名拼成, 满足上游 "approve_files/" 前缀约束。
func UploadFile(cfg Config, localPath string) (string, error) {
	if !cfg.configured() {
		return "", fmt.Errorf("oss 未配置 (endpoint/accessKey/bucket 缺失)")
	}
	if localPath == "" {
		return "", fmt.Errorf("oss 上传: 授权书文件路径为空")
	}
	client, err := alioss.New(cfg.Endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		return "", fmt.Errorf("oss new client: %w", err)
	}
	bucket, err := client.Bucket(cfg.Bucket)
	if err != nil {
		return "", fmt.Errorf("oss bucket %q: %w", cfg.Bucket, err)
	}

	prefix := cfg.ObjectPrefix
	if prefix == "" {
		prefix = "approve_files/"
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	objectName := prefix + time.Now().Format("20060102150405") + randSuffix(6) + "_" + filepath.Base(localPath)

	if err := bucket.PutObjectFromFile(objectName, localPath); err != nil {
		return "", fmt.Errorf("oss put object %q: %w", objectName, err)
	}

	// 公网访问 URL: https://{bucket}.{endpoint}/{objectName}
	endpoint := strings.TrimPrefix(strings.TrimPrefix(cfg.Endpoint, "https://"), "http://")
	return fmt.Sprintf("https://%s.%s/%s", cfg.Bucket, endpoint, objectName), nil
}

func randSuffix(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
