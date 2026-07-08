//go:build ignore

// probe_swfp: 用真实凭证对证通 entcreditapi 四产品码做一次聚合联调探测。
// 凭证从 CONFIG_FILE (默认 config.aliyun.prod.yaml, gitignored) 的
// versions.swfp.upstream 读取，不硬编码进本文件。
//
// 用法：
//   go run ./scripts/probe_swfp.go                            # 默认虚构信用代码（预期查无, 不计费）
//   SWFP_CREDIT_CODE=91xxxxxxxx go run ./scripts/probe_swfp.go # 指定真实企业（可能计费, 慎用）
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/infrastructure/upstream"
)

type fileConfig struct {
	Versions map[string]struct {
		Upstream struct {
			BaseURL         string   `yaml:"baseURL"`
			OrgCode         string   `yaml:"orgCode"`
			AccessKeyID     string   `yaml:"accessKeyId"`
			SecretAccessKey string   `yaml:"secretAccessKey"`
			Products        []string `yaml:"products"`
		} `yaml:"upstream"`
	} `yaml:"versions"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	path := os.Getenv("CONFIG_FILE")
	if path == "" {
		path = "config.aliyun.prod.yaml"
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
	uc := fc.Versions["swfp"].Upstream
	if uc.BaseURL == "" || uc.OrgCode == "" {
		fmt.Println("配置缺少 versions.swfp.upstream 的 baseURL/orgCode")
		os.Exit(1)
	}

	creditCode := os.Getenv("SWFP_CREDIT_CODE")
	if creditCode == "" {
		creditCode = "91330100MA2AAAAA0X" // 虚构但格式合法（预期查无, 不计费）
	}

	client := upstream.NewEntCredit(upstream.EntCreditConfig{
		Endpoint:        uc.BaseURL,
		OrgCode:         uc.OrgCode,
		AccessKeyID:     uc.AccessKeyID,
		SecretAccessKey: uc.SecretAccessKey,
		Products:        uc.Products,
	}, &http.Client{Timeout: 30 * time.Second})

	fmt.Printf("== 探测开始: endpoint=%s orgCode=%s creditCode=%s products=%v ==\n",
		uc.BaseURL, uc.OrgCode, creditCode, uc.Products)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := client.Query(ctx, &model.UpstreamRequest{CreditCode: creditCode, Reqid: "probe"})
	if err != nil {
		fmt.Println("\n== 聚合结果: 全部数据源失败 ==")
		fmt.Println("error:", err)
		os.Exit(1)
	}

	fmt.Printf("\n== 聚合结果: code=%s msg=%s uid=%s ==\n", result.Code, result.Msg, result.UID)
	if result.Range != "" {
		var pretty map[string]any
		_ = json.Unmarshal([]byte(result.Range), &pretty)
		out, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Println(string(out))
	}
}
