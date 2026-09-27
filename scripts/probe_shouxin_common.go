//go:build ignore

// 守信 shouxin168 三条新路由 (dtjd / snhmd / dtly) 上游直连探测的共用逻辑。
// 由 probe_dtjd.go、probe_snhmd.go、probe_dtly.go 引用，勿单独 go run。
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/infrastructure/oss"
	"github.com/datahub/relay/internal/infrastructure/upstream"
)

type shouxinUpstream struct {
	Kind          string  `yaml:"kind"`
	BaseURL       string  `yaml:"baseURL"`
	InstitutionID string  `yaml:"institutionId"`
	AESKey        string  `yaml:"aesKey"`
	Service       string  `yaml:"service"`
	Mode          string  `yaml:"mode"`
	LicenseFile   string  `yaml:"licenseFile"`
	LicenseType   int     `yaml:"licenseType"`
	OSS           fileOSS `yaml:"oss"`
}

type fileOSS struct {
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"accessKeyId"`
	AccessKeySecret string `yaml:"accessKeySecret"`
	Bucket          string `yaml:"bucket"`
	ObjectPrefix    string `yaml:"objectPrefix"`
}

type fileVersion struct {
	Upstreams []shouxinUpstream `yaml:"upstreams"`
	Upstream  shouxinUpstream   `yaml:"upstream"`
}

type shouxinFileConfig struct {
	Versions map[string]fileVersion `yaml:"versions"`
}

func (fv fileVersion) firstUpstream() shouxinUpstream {
	if len(fv.Upstreams) > 0 {
		return fv.Upstreams[0]
	}
	return fv.Upstream
}

func loadShouxinUpstream(route string) (shouxinUpstream, string, error) {
	path := os.Getenv("CONFIG_FILE")
	if path == "" {
		path = "config.aliyun.prod.yaml"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return shouxinUpstream{}, path, fmt.Errorf("读取配置: %w", err)
	}
	var fc shouxinFileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		return shouxinUpstream{}, path, fmt.Errorf("解析配置: %w", err)
	}
	fv, ok := fc.Versions[route]
	if !ok {
		return shouxinUpstream{}, path, fmt.Errorf("配置中无 versions.%s", route)
	}
	u := fv.firstUpstream()
	if u.BaseURL == "" {
		return shouxinUpstream{}, path, fmt.Errorf("versions.%s 缺少 baseURL", route)
	}
	return u, path, nil
}

func placeholder(s string) bool {
	return s == "" || strings.HasPrefix(s, "REPLACE_")
}

func uploadLicense(u shouxinUpstream) (string, error) {
	if u.LicenseFile == "" || placeholder(u.LicenseFile) {
		return "", fmt.Errorf("licenseFile 未配置或为占位符")
	}
	if placeholder(u.OSS.AccessKeyID) || placeholder(u.OSS.AccessKeySecret) {
		return "", fmt.Errorf("oss accessKey 仍为占位符，无法上传授权书")
	}
	prefix := u.OSS.ObjectPrefix
	if prefix == "" {
		prefix = "approve_files/"
	}
	return oss.UploadFile(oss.Config{
		Endpoint:        u.OSS.Endpoint,
		AccessKeyID:     u.OSS.AccessKeyID,
		AccessKeySecret: u.OSS.AccessKeySecret,
		Bucket:          u.OSS.Bucket,
		ObjectPrefix:    prefix,
	}, u.LicenseFile)
}

func probePerson() (name, idCard, mobile string) {
	name = os.Getenv("PROBE_NAME")
	idCard = os.Getenv("PROBE_IDCARD")
	mobile = os.Getenv("PROBE_MOBILE")
	if name == "" {
		name = "陈韫"
	}
	if idCard == "" {
		idCard = "440303200002163115"
	}
	if mobile == "" {
		mobile = "13670010670"
	}
	return name, idCard, mobile
}

// runShouxinProbe 直连守信上游一次 Query。连通成功 = 无 error 且归一化码 001(查得) 或 999(查无)。
func runShouxinProbe(route, title string) int {
	u, cfgPath, err := loadShouxinUpstream(route)
	if err != nil {
		fmt.Println("FAIL:", err)
		return 1
	}

	fmt.Printf("== %s 上游联通探测 ==\n", title)
	fmt.Printf("  config=%s\n", cfgPath)
	fmt.Printf("  route=%s kind=%s mode=%s\n", route, u.Kind, u.Mode)
	fmt.Printf("  endpoint=%s\n", u.BaseURL)
	fmt.Printf("  institutionId=%s\n", maskID(u.InstitutionID))

	// 网络层探测：凭证未到位时先验证「ECS 能否连上上游 + 出口 IP 是否已加白」。
	// 上游文档 §2.3 规定 institution_id 必传，故这里拿不到真值就不做业务调用。
	if os.Getenv("PROBE_NET_ONLY") == "1" {
		return runNetProbe(u)
	}

	if placeholder(u.InstitutionID) {
		fmt.Println("FAIL: institutionId 仍为占位符，请先在配置中填入上游分配的机构号")
		return 1
	}
	if placeholder(u.AESKey) {
		fmt.Println("FAIL: aesKey 仍为占位符，请先在配置中填入上游分配的密钥")
		return 1
	}

	licenseURL, err := uploadLicense(u)
	if err != nil {
		fmt.Println("FAIL: 授权书 OSS:", err)
		return 1
	}
	fmt.Printf("  licenseUrl=%s\n", trunc(licenseURL, 72))

	name, idCard, mobile := probePerson()
	fmt.Printf("  探测三要素: name=%s idCard=%s mobile=%s\n", name, maskID(idCard), maskMobile(mobile))
	if os.Getenv("PROBE_NAME") == "" {
		fmt.Println("  (可通过环境变量 PROBE_NAME / PROBE_IDCARD / PROBE_MOBILE 覆盖)")
	}

	httpClient := &http.Client{Timeout: 45 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	req := &model.UpstreamRequest{
		Name:   name,
		IDCard: idCard,
		Mobile: mobile,
		Reqid:  "probe-" + route,
	}

	var result *model.UpstreamResult
	switch route {
	case "dtjd":
		result, err = upstream.NewMultiLoan(upstream.MultiLoanConfig{
			BaseURL:       u.BaseURL,
			InstitutionID: u.InstitutionID,
			AESKey:        u.AESKey,
			Service:       u.Service,
			Mode:          u.Mode,
			LicenseURL:    licenseURL,
			LicenseType:   u.LicenseType,
		}, httpClient).Query(ctx, req)
	case "snhmd":
		result, err = upstream.NewCompassBlack(upstream.CompassBlackConfig{
			BaseURL:       u.BaseURL,
			InstitutionID: u.InstitutionID,
			AESKey:        u.AESKey,
			Service:       u.Service,
			Mode:          u.Mode,
			LicenseURL:    licenseURL,
			LicenseType:   u.LicenseType,
		}, httpClient).Query(ctx, req)
	case "dtly":
		result, err = upstream.NewManyOverdue(upstream.ManyOverdueConfig{
			BaseURL:       u.BaseURL,
			InstitutionID: u.InstitutionID,
			AESKey:        u.AESKey,
			Service:       u.Service,
			Mode:          u.Mode,
			LicenseURL:    licenseURL,
			LicenseType:   u.LicenseType,
		}, httpClient).Query(ctx, req)
	default:
		fmt.Println("FAIL: 未知路由", route)
		return 1
	}

	if err != nil {
		fmt.Printf("FAIL: 上游调用失败: %v\n", err)
		printHints(err)
		return 1
	}

	fmt.Printf("  OK: normalizedCode=%s upstreamUid=%s\n", result.Code, result.UID)
	fmt.Printf("  range(截断)=%s\n", trunc(result.Range, 200))

	switch result.Code {
	case "001":
		fmt.Println("== 结论: PASS（查得 001，上游认证成功且返回数据）==")
		return 0
	case "999":
		fmt.Println("== 结论: PASS（查无 999，上游已连通、凭证有效，该样本无记录）==")
		return 0
	default:
		fmt.Printf("FAIL: 未预期的归一化码 %s\n", result.Code)
		return 1
	}
}

// runNetProbe 只验证网络可达性与 IP 白名单：拿配置里的 institution_id（哪怕是占位符）
// 加一段无效 biz_data 发一次真实 form POST。只要上游回了 HTTP 响应（哪怕是业务错误码），
// 就说明 TLS 通、IP 已加白；连不上才是网络/白名单问题。
func runNetProbe(u shouxinUpstream) int {
	fmt.Println("  模式: 仅网络连通性 (PROBE_NET_ONLY=1)，不做业务调用")

	form := url.Values{}
	form.Set("institution_id", u.InstitutionID)
	form.Set("biz_data", "net-probe-invalid-ciphertext")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.BaseURL, strings.NewReader(form.Encode()))
	if err != nil {
		fmt.Println("FAIL: 构造请求:", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json;charset=utf-8")

	start := time.Now()
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		fmt.Printf("FAIL: 无法连接上游 (%.1fs): %v\n", time.Since(start).Seconds(), err)
		fmt.Println("  提示: 本机出口 IP 很可能未加入守信白名单，或 ECS 安全组/网络不通")
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	fmt.Printf("  HTTP=%d 耗时=%.1fs\n", resp.StatusCode, time.Since(start).Seconds())
	fmt.Printf("  响应=%s\n", trunc(strings.TrimSpace(string(body)), 300))
	fmt.Println("== 结论: PASS（网络可达、上游已应答；凭证是否有效需填真值后再测）==")
	return 0
}

func printHints(err error) {
	msg := err.Error()
	if strings.Contains(msg, "multiloan call") || strings.Contains(msg, "compassblack call") || strings.Contains(msg, "manyoverdue call") {
		fmt.Println("  提示: 网络不可达或 TLS 被拒 → 常见为出口 IP 未加入守信白名单")
	}
	if strings.Contains(msg, "SW0034") || strings.Contains(msg, "解密") {
		fmt.Println("  提示: aesKey 与 institutionId 是否配对正确")
	}
	if strings.Contains(msg, "SW0040") {
		fmt.Println("  提示: 该产品(mode)未开通或 institutionId 无权限")
	}
	if strings.Contains(msg, "license") || strings.Contains(msg, "SW0017") {
		fmt.Println("  提示: licenseUrl/licenseType 或授权书 OSS 路径是否符合上游要求")
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func maskID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:4] + "****" + s[len(s)-4:]
}

func maskMobile(s string) string {
	if len(s) < 7 {
		return s
	}
	return s[:3] + "****" + s[len(s)-4:]
}
