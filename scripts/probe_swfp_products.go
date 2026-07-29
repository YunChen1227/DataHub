//go:build ignore

// probe_swfp_products: 逐产品直连证通 entcreditapi，验证四份 PDF 对应接口是否调通。
//
//   SWFP_ENDPOINT=https://cisp.zenitera.com
//   SWFP_ORG_CODE=...
//   SWFP_ACCESS_KEY_ID=...
//   SWFP_SECRET_ACCESS_KEY=...   # Base64
//   go run ./scripts/probe_swfp_products.go
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/infrastructure/upstream"
)

var products = []struct {
	Code  string
	Label string
	Doc   string
}{
	{"P0130081", "invoice1", "发票数据聚合查询-part1.pdf"},
	{"P0130083", "invoice2", "发票数据聚合查询-part2.pdf"},
	{"P0130082", "tax1", "税务数据聚合查询-part1.pdf"},
	{"P0130084", "tax2", "税务数据聚合查询-part2.pdf"},
}

func main() {
	endpoint := os.Getenv("SWFP_ENDPOINT")
	org := os.Getenv("SWFP_ORG_CODE")
	ak := os.Getenv("SWFP_ACCESS_KEY_ID")
	sk := os.Getenv("SWFP_SECRET_ACCESS_KEY")
	if endpoint == "" || org == "" || ak == "" || sk == "" {
		fmt.Println("请设置 SWFP_ENDPOINT / SWFP_ORG_CODE / SWFP_ACCESS_KEY_ID / SWFP_SECRET_ACCESS_KEY")
		os.Exit(1)
	}

	creditCode := os.Getenv("SWFP_CREDIT_CODE")
	if creditCode == "" {
		creditCode = "91330100MA2AAAAA0X"
	}

	httpClient := &http.Client{
		Timeout: 45 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
	}

	fmt.Printf("== 四产品直连探测 endpoint=%s orgCode=%s creditCode=%s ==\n\n", endpoint, org, creditCode)
	fmt.Printf("%-12s | %-10s | %-8s | %-12s | result\n", "product", "label", "code", "msg")
	fmt.Println("--------------------------------------------------------------------------------")

	ok, authFail, other := 0, 0, 0
	for _, p := range products {
		client := upstream.NewEntCredit(upstream.EntCreditConfig{
			Endpoint:        endpoint,
			OrgCode:         org,
			AccessKeyID:     ak,
			SecretAccessKey: sk,
			Product:         p.Code,
		}, httpClient)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		start := time.Now()
		res, err := client.Query(ctx, &model.UpstreamRequest{CreditCode: creditCode, Reqid: "probe-" + p.Label})
		cancel()
		elapsed := time.Since(start)
		if err != nil {
			msg := err.Error()
			tag := "FAIL"
			if contains(msg, "E1009") || contains(msg, "accessKeyId") {
				tag = "AUTH"
				authFail++
			} else {
				other++
			}
			fmt.Printf("%-12s | %-10s | %-8s | %-12s | %s (%s)\n", p.Code, p.Label, tag, fmt.Sprintf("%.1fs", elapsed.Seconds()), msg, p.Doc)
			continue
		}
		ok++
		fmt.Printf("%-12s | %-10s | %-8s | %-12s | OK code=%s uid=%s (%s)\n",
			p.Code, p.Label, "OK", fmt.Sprintf("%.1fs", elapsed.Seconds()), res.Code, res.UID, p.Doc)
	}

	fmt.Printf("\n汇总: 业务成功=%d 鉴权失败=%d 其它失败=%d / 4\n", ok, authFail, other)
	if authFail > 0 {
		fmt.Println("\n结论: 网络/签名链路已通，但 accessKeyId/凭证无效 —— 需填入证通商务分配的真实 AK/SK/orgCode。")
		os.Exit(2)
	}
	if ok == 0 {
		os.Exit(1)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}
