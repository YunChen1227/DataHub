//go:build ignore

// 直连 grgjj 备用源 (bgjj / 备用公积金 / jeoho) 的连通性与取数探针——**不经过 relay**，
// 直接用生产同款客户端 upstream.NewBgJJ 打上游，用于在阿里云服务器上验证：
// P12 双向认证是否成立、出口 IP 是否已加白、三要素能否查得数据。
//
// Run (在仓库根目录 /workspace/DataHub 下)：
//
//	go run ./scripts/probe_bgjj_direct.go
//
// 可用环境变量覆盖 (默认值取自 docs/备用公积金1/对接事项.txt)：
//
//	BGJJ_URL / BGJJ_MERCHANT_ID / BGJJ_MERCHANT_KEY / BGJJ_P12 / BGJJ_P12_PASS
//	BGJJ_NAME / BGJJ_IDCARD / BGJJ_MOBILE
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/infrastructure/upstream"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// 生产凭证与被查三要素（写死，可用同名环境变量覆盖）。
const (
	defaultURL        = "https://pf.jeoho.com/api/nlv2/zl4"
	defaultMerchantID = "0000000000005077"
	defaultMerchantKe = "P8rT2wXyZ9aBcDeFgHiJkLmNoPqRsTuV"
	defaultP12Path    = "docs/备用公积金1/0000000000005077.p12"
	defaultP12Pass    = "KiC1VjLLRmNL0yCK"

	defaultName   = "陈韫"
	defaultIDCard = "440303200002163115"
	defaultMobile = "13670010670"
)

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// mask 只回显密钥首尾，避免探针输出被复制粘贴时泄漏完整商户密钥。
func mask(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}

func main() {
	// bgjj.go 用 slog.Debug 打请求元信息与**上游原始响应**，调到 Debug 才能看到原文。
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	url := env("BGJJ_URL", defaultURL)
	merchantID := env("BGJJ_MERCHANT_ID", defaultMerchantID)
	merchantKey := env("BGJJ_MERCHANT_KEY", defaultMerchantKe)
	p12Path := env("BGJJ_P12", defaultP12Path)
	p12Pass := env("BGJJ_P12_PASS", defaultP12Pass)

	name := env("BGJJ_NAME", defaultName)
	idCard := env("BGJJ_IDCARD", defaultIDCard)
	mobile := env("BGJJ_MOBILE", defaultMobile)

	fmt.Println("==== grgjj 备用源 (bgjj / jeoho) 直连探针 ====")
	fmt.Printf("接口:      POST %s\n", url)
	fmt.Printf("商户号:    %s\n", merchantID)
	fmt.Printf("商户密钥:  %s\n", mask(merchantKey))
	fmt.Printf("P12 证书:  %s\n", p12Path)
	fmt.Printf("三要素:    name=%s idCard=%s mobile=%s\n\n", name, idCard, mobile)

	printOutboundIP()
	if !printCertInfo(p12Path, p12Pass) {
		os.Exit(1)
	}

	client, err := upstream.NewBgJJ(upstream.BgJJConfig{
		BaseURL:     url,
		MerchantID:  merchantID,
		MerchantKey: merchantKey,
		CertPath:    p12Path,
		CertPass:    p12Pass,
	}, &http.Client{Timeout: 20 * time.Second})
	if err != nil {
		fmt.Printf("\n[FAIL] 构建客户端失败: %v\n", err)
		fmt.Println("提示: 多为 P12 路径不对或证书密码错误。")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	fmt.Println("\n---- 发起查询 ----")
	start := time.Now()
	res, err := client.Query(ctx, &model.UpstreamRequest{
		Name:   name,
		IDCard: idCard,
		Mobile: mobile,
		Reqid:  fmt.Sprintf("probe-%d", time.Now().UnixMilli()),
	})
	elapsed := time.Since(start)

	fmt.Printf("\n---- 结果 (耗时 %s) ----\n", elapsed.Round(time.Millisecond))
	if err != nil {
		var ue *model.UpstreamError
		if errors.As(err, &ue) {
			fmt.Printf("[上游业务失败] code=%s msg=%s orderid=%s\n", ue.Code, ue.Msg, ue.UID)
			explain(ue.Code)
		} else {
			fmt.Printf("[传输/协议失败] %v\n", err)
			fmt.Println("提示: TLS 握手失败多为 P12 证书不被上游接受；超时多为出口 IP 未加白或网络不通。")
		}
		os.Exit(1)
	}

	switch res.Code {
	case "001":
		fmt.Printf("[查得] orderid=%s\n", res.UID)
		if res.Range == "" {
			fmt.Println("range 为空（上游返回 code=100 但 data 为空对象）")
		} else {
			fmt.Printf("range (已归一为 grgjj 下游契约): %s\n", res.Range)
		}
	case "999":
		fmt.Printf("[查无] orderid=%s —— 链路是通的，只是该三要素在备用源无记录\n", res.UID)
	default:
		fmt.Printf("[未知归一码] code=%s msg=%s\n", res.Code, res.Msg)
	}
}

// printOutboundIP 打印本机出口公网 IP，便于向上游报白名单（失败不影响主流程）。
func printOutboundIP() {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("https://ifconfig.me/ip")
	if err != nil {
		fmt.Printf("出口 IP:   获取失败 (%v)，可在服务器上执行 curl ifconfig.me 自查\n", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	fmt.Printf("出口 IP:   %s  (需已报 jeoho 加白，否则 code=301)\n", strings.TrimSpace(string(body)))
}

// printCertInfo 解析 P12 并打印证书主体与有效期，提前暴露"证书过期/密码错"这类问题。
func printCertInfo(path, pass string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("[FAIL] 读取 P12 失败: %v\n", err)
		fmt.Println("提示: 请在仓库根目录 /workspace/DataHub 下运行本脚本（路径是相对根目录的）。")
		return false
	}
	_, cert, _, err := pkcs12.DecodeChain(raw, pass)
	if err != nil {
		fmt.Printf("[FAIL] 解析 P12 失败（密码是否正确?）: %v\n", err)
		return false
	}
	fmt.Printf("证书主体:  %s\n", cert.Subject.String())
	fmt.Printf("证书签发:  %s\n", cert.Issuer.CommonName)
	fmt.Printf("有效期:    %s ~ %s", cert.NotBefore.Format("2006-01-02"), cert.NotAfter.Format("2006-01-02"))
	if time.Now().After(cert.NotAfter) {
		fmt.Print("  [已过期!]")
	}
	fmt.Println()
	return true
}

// explain 对常见上游错误码给出排查方向。
func explain(code string) {
	switch code {
	case "301":
		fmt.Println("提示: 301 = 非白名单 IP，把上面打印的出口 IP 报给 jeoho 加白即可。")
	case "302", "303":
		fmt.Println("提示: 3xx 多为商户号/签名/权限问题，核对 merchant_id 与 merchantKey。")
	default:
		fmt.Println("提示: 上方 Debug 日志里的 bgjj response raw=... 是上游原始报文，据此定位。")
	}
}
