//go:build ignore

// probe_rlbd1_sfzhy: 用配置中的真实凭证对 rlbd1(facecompare) 与 sfzhy(idverify)
// 各做一次联调探测。凭证从 CONFIG_FILE (默认 config.example.yaml) 读取。
//
// 用法: go run ./scripts/probe_rlbd1_sfzhy.go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/infrastructure/upstream"
)

type upstreamBlock struct {
	Kind      string `yaml:"kind"`
	BaseURL   string `yaml:"baseURL"`
	AppID     string `yaml:"appId"`
	AppSecret string `yaml:"appSecret"`
}

type fileConfig struct {
	Versions map[string]struct {
		Upstreams []upstreamBlock `yaml:"upstreams"`
	} `yaml:"versions"`
}

// firstUpstream 取该路由的首个上游子源 (rlbd1/sfzhy 均为单源列表)。
func (fc fileConfig) firstUpstream(route string) upstreamBlock {
	if ups := fc.Versions[route].Upstreams; len(ups) > 0 {
		return ups[0]
	}
	return upstreamBlock{}
}

// 1x1 PNG base64，用于联调探测（体积极小，满足格式校验）。
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

func main() {
	path := os.Getenv("CONFIG_FILE")
	if path == "" {
		path = "config.example.yaml"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("读取配置失败:", err)
		os.Exit(1)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		fmt.Println("解析配置失败:", err)
		os.Exit(1)
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	anyFail := false

	fmt.Println("== rlbd1 / facecompare 联通探测 ==")
	if u := fc.firstUpstream("rlbd1"); u.BaseURL == "" || u.AppID == "" {
		fmt.Println("FAIL: 配置缺少 versions.rlbd1.upstreams")
		anyFail = true
	} else {
		client := upstream.NewFaceCompare(upstream.FaceCompareConfig{
			BaseURL:   u.BaseURL,
			AppID:     u.AppID,
			AppSecret: u.AppSecret,
		}, httpClient)
		fmt.Printf("  endpoint=%s appId=%s...\n", u.BaseURL, trunc(u.AppID, 8))
		result, err := client.Query(ctx, &model.UpstreamRequest{
			Name:   "张三",
			IDCard: "420101198012010011",
			URL:    "https://via.placeholder.com/100.jpg",
			Reqid:  "probe-rlbd1",
		})
		if err != nil {
			fmt.Printf("  FAIL: %v\n", err)
			anyFail = true
		} else {
			fmt.Printf("  OK: code=%s uid=%s range=%s\n", result.Code, result.UID, trunc(result.Range, 120))
		}
	}

	fmt.Println()
	fmt.Println("== sfzhy / idverify 联通探测 ==")
	if u := fc.firstUpstream("sfzhy"); u.BaseURL == "" || u.AppID == "" {
		fmt.Println("FAIL: 配置缺少 versions.sfzhy.upstreams")
		anyFail = true
	} else {
		client := upstream.NewIDVerify(upstream.IDVerifyConfig{
			BaseURL:   u.BaseURL,
			AppID:     u.AppID,
			AppSecret: u.AppSecret,
		}, httpClient)
		fmt.Printf("  endpoint=%s appId=%s...\n", u.BaseURL, trunc(u.AppID, 8))
		result, err := client.Query(ctx, &model.UpstreamRequest{
			Name:           "张三",
			IDCard:         "420101198012010011",
			ProfilePicture: tinyPNG,
			Reqid:          "probe-sfzhy",
		})
		if err != nil {
			fmt.Printf("  FAIL: %v\n", err)
			anyFail = true
		} else {
			fmt.Printf("  OK: code=%s uid=%s range=%s\n", result.Code, result.UID, trunc(result.Range, 120))
		}
	}

	fmt.Println()
	if anyFail {
		fmt.Println("== 结论: 至少一个上游未调通 ==")
		os.Exit(1)
	}
	fmt.Println("== 结论: 两个上游均已调通（凭证有效、网络可达）==")
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
