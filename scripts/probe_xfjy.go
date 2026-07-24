//go:build ignore

// probe_xfjy: 用配置中的凭证对 xfjy(consumetxn/data-bean) 做一次联调探测。
// 支持 versions.<route>.upstream 与 upstreams 两种 YAML 写法。
//
// 用法: CONFIG_FILE=config.aliyun.prod.yaml go run ./scripts/probe_xfjy.go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
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
	APIKey    string `yaml:"apiKey"`
}

type fileVersion struct {
	Upstreams []upstreamBlock `yaml:"upstreams"`
	Upstream  upstreamBlock   `yaml:"upstream"`
}

type fileConfig struct {
	Versions map[string]fileVersion `yaml:"versions"`
}

func (fv fileVersion) firstUpstream() upstreamBlock {
	if len(fv.Upstreams) > 0 {
		return fv.Upstreams[0]
	}
	return fv.Upstream
}

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

	u := fc.Versions["xfjy"].firstUpstream()
	fmt.Println("== xfjy / consumetxn 联通探测 ==")
	fmt.Printf("  config=%s\n", path)
	if u.BaseURL == "" {
		fmt.Println("FAIL: 配置缺少 versions.xfjy.upstream(s)")
		os.Exit(1)
	}
	if u.AppID == "" || strings.HasPrefix(u.AppID, "REPLACE_") {
		fmt.Println("FAIL: appId(sceneid) 仍为占位符，请先在配置中填入 data-bean 真实 sceneid")
		os.Exit(1)
	}
	if u.AppSecret == "" || strings.HasPrefix(u.AppSecret, "REPLACE_") {
		fmt.Println("FAIL: appSecret(appkey) 仍为占位符，请先在配置中填入 data-bean 真实 appkey")
		os.Exit(1)
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := upstream.NewConsumeTxn(upstream.ConsumeTxnConfig{
		BaseURL: u.BaseURL,
		SceneID: u.AppID,
		AppKey:  u.AppSecret,
		Procode: u.APIKey,
	}, httpClient)
	fmt.Printf("  endpoint=%s sceneid=%s...\n", u.BaseURL, trunc(u.AppID, 8))
	result, err := client.Query(ctx, &model.UpstreamRequest{
		Name:   "张三",
		IDCard: "330129199109094312",
		Mobile: "13809091009",
		Reqid:  "probe-xfjy",
	})
	if err != nil {
		fmt.Printf("  FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  OK: code=%s uid=%s range=%s\n", result.Code, result.UID, trunc(result.Range, 160))
	fmt.Println("== 结论: 上游已调通（凭证有效、网络可达）==")
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
