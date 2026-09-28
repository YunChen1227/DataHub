//go:build ignore

// 守信 shouxin168 三条新路由 (dtjd / snhmd / dtly) 上游直连探测的共用逻辑。
// 由 probe_dtjd.go、probe_snhmd.go、probe_dtly.go 引用，勿单独 go run。
package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

	// 上游口径（2026-09-28 书面回复，原文未载）：测试/生产环境均不传 licenseUrl / licenseType。
	// 未配 licenseFile 时不上传授权书，客户端会从 biz_data 中整体省略这两个字段。
	licenseURL := ""
	if placeholder(u.LicenseFile) {
		fmt.Println("  licenseFile 未配置 → 不上传授权书，biz_data 不带 licenseUrl / licenseType")
	} else {
		licenseURL, err = uploadLicense(u)
		if err != nil {
			fmt.Println("FAIL: 授权书 OSS:", err)
			return 1
		}
		fmt.Printf("  licenseUrl=%s\n", trunc(licenseURL, 72))
	}

	name, idCard, mobile := probePerson()
	fmt.Printf("  探测三要素: name=%s idCard=%s mobile=%s\n", name, maskID(idCard), maskMobile(mobile))
	if os.Getenv("PROBE_NAME") == "" {
		fmt.Println("  (可通过环境变量 PROBE_NAME / PROBE_IDCARD / PROBE_MOBILE 覆盖)")
	}

	httpClient := &http.Client{Timeout: 45 * time.Second, Transport: rawDumpTransport{base: http.DefaultTransport, key: probeAESKey(u.AESKey)}}
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

	if err != nil && strings.Contains(err.Error(), "aesKey") {
		// 客户端对非 16/24/32 字节的密钥拒绝加密（正式服务禁止静默降级）。探测不在本地下
		// 结论：补齐后真加密、真发业务请求，结论以上游返回为准。
		return probeWithPaddedKey(u, route, licenseURL, name, idCard, mobile, httpClient)
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

var probeDefaultMode = map[string]string{
	"dtjd":  "mode_loan_intent_v1",
	"snhmd": "mode_compass_black",
	"dtly":  "mode_many_overdue_behavior",
}

type probeBizData struct {
	Name        string `json:"name"`
	IdentNumber string `json:"ident_number"`
	Phone       string `json:"phone"`
	Service     string `json:"service"`
	Mode        string `json:"mode"`
	LicenseURL  string `json:"licenseUrl,omitempty"`
	LicenseType *int   `json:"licenseType,omitempty"`
}

// padAESKey 按 PHP openssl_encrypt 的做法处理非标准长度密钥：末尾补 \0 到下一个合法长度
// (16/24/32)，超过 32 截断。上游提供了 PHP demo，其服务端可能同样如此处理短密钥。
func padAESKey(raw []byte) []byte {
	for _, n := range []int{16, 24, 32} {
		if len(raw) <= n {
			k := make([]byte, n)
			copy(k, raw)
			return k
		}
	}
	return raw[:32]
}

// probeAESKey 复刻客户端的密钥解读顺序（原始字节 → hex → Base64，取第一个 16/24/32 字节的），
// 都不合法时按 padAESKey 补齐——与 probeWithPaddedKey 实际加密用的密钥一致。
func probeAESKey(s string) []byte {
	valid := func(n int) bool { return n == 16 || n == 24 || n == 32 }
	raw := []byte(s)
	if valid(len(raw)) {
		return raw
	}
	if b, err := hex.DecodeString(s); err == nil && valid(len(b)) {
		return b
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && valid(len(b)) {
		return b
	}
	return padAESKey(raw)
}

func aesECBPKCS5DecryptBase64(cipherText string, key []byte) (string, error) {
	ct, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return "", fmt.Errorf("密文不是 Base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	bs := block.BlockSize()
	if len(ct) == 0 || len(ct)%bs != 0 {
		return "", fmt.Errorf("密文长度 %d 不是 %d 的整数倍", len(ct), bs)
	}
	out := make([]byte, len(ct))
	for i := 0; i < len(ct); i += bs {
		block.Decrypt(out[i:i+bs], ct[i:i+bs])
	}
	pad := int(out[len(out)-1])
	if pad == 0 || pad > bs || pad > len(out) {
		return "", fmt.Errorf("PKCS5 填充非法")
	}
	return string(out[:len(out)-pad]), nil
}

func aesECBPKCS5Base64(plain, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	bs := block.BlockSize()
	pad := bs - len(plain)%bs
	data := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += bs {
		block.Encrypt(out[i:i+bs], data[i:i+bs])
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// probeWithPaddedKey 密钥长度非 16/24/32 时不在本地拦截：补齐后用真实三要素加密、真发业务
// 请求，原样打印上游返回，PASS/FAIL 只看上游的 resp_code。
func probeWithPaddedKey(u shouxinUpstream, route, licenseURL, name, idCard, mobile string, httpClient *http.Client) int {
	raw := []byte(u.AESKey)
	key := padAESKey(raw)
	fmt.Printf("  aesKey 原始 %d 字节（非 16/24/32）：不在本地拦截，按 PHP openssl 方式补 \\0 到 %d 字节加密，由上游判定\n", len(raw), len(key))
	var nonASCII []string
	for _, b := range raw {
		if b > 0x7e || b < 0x20 {
			nonASCII = append(nonASCII, fmt.Sprintf("%02x", b))
		}
	}
	if len(nonASCII) > 0 {
		fmt.Printf("  ⚠ aesKey 含非 ASCII 字节 %v（如中文标点「」），多半是复制时带入，原样参与加密\n", nonASCII)
	}

	mode := u.Mode
	if mode == "" {
		mode = probeDefaultMode[route]
	}
	service := u.Service
	if service == "" {
		service = "financial_rent_service"
	}
	biz := probeBizData{Name: name, IdentNumber: idCard, Phone: mobile, Service: service, Mode: mode, LicenseURL: licenseURL}
	if licenseURL != "" {
		lt := u.LicenseType
		biz.LicenseType = &lt
	}
	plain, err := json.Marshal(biz)
	if err != nil {
		fmt.Println("FAIL: 序列化 biz_data:", err)
		return 1
	}
	cipher, err := aesECBPKCS5Base64(plain, key)
	if err != nil {
		fmt.Println("FAIL: 加密 biz_data:", err)
		return 1
	}

	form := url.Values{}
	form.Set("institution_id", u.InstitutionID)
	form.Set("biz_data", cipher)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.BaseURL, strings.NewReader(form.Encode()))
	if err != nil {
		fmt.Println("FAIL: 构造请求:", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json;charset=utf-8")
	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Println("FAIL: 无法连接上游:", err)
		fmt.Println("  提示: 本机出口 IP 很可能未加入守信白名单，或 ECS 安全组/网络不通")
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var r struct {
		RespCode  string `json:"resp_code"`
		RespMsg   string `json:"resp_msg"`
		RespOrder string `json:"resp_order"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		fmt.Println("FAIL: 上游返回不是 JSON，见上方原始返回")
		return 1
	}
	fmt.Printf("  上游判定: resp_code=%s resp_msg=%s resp_order=%s\n", r.RespCode, r.RespMsg, r.RespOrder)
	switch r.RespCode {
	case "SW0000", "SW0002":
		fmt.Println("== 结论: PASS（上游接受了补齐后的密钥）→ 正式服务需同样补齐才能用，先别上线，回来改代码 ==")
		return 0
	default:
		fmt.Println("== 结论: FAIL（上游未接受；SW9999 AES解密错误即说明密钥本身不对/缺字符，找上游核对）==")
		return 1
	}
}

// runNetProbe 只验证网络可达性与 IP 白名单：拿配置里的 institution_id（哪怕是占位符）
// 加一段无效 biz_data 发一次真实 form POST。只要上游回了 HTTP 响应（哪怕是业务错误码），
// 就说明 TLS 通、IP 已加白；连不上才是网络/白名单问题。
func runNetProbe(u shouxinUpstream) int {
	fmt.Println("  模式: 仅网络连通性 (PROBE_NET_ONLY=1)，不做业务调用")
	if !sendRawForm(u, "net-probe-invalid-ciphertext") {
		return 1
	}
	fmt.Println("== 结论: PASS（网络可达、上游已应答；凭证是否有效需填真值后再测）==")
	return 0
}

// sendRawForm 用配置里的 institution_id + 给定的 biz_data 发一次真实 form POST，原样打印
// 上游返回。返回值表示是否拿到了 HTTP 响应（拿到即说明网络通、出口 IP 已加白）。
func sendRawForm(u shouxinUpstream, bizData string) bool {
	form := url.Values{}
	form.Set("institution_id", u.InstitutionID)
	form.Set("biz_data", bizData)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.BaseURL, strings.NewReader(form.Encode()))
	if err != nil {
		fmt.Println("  构造请求失败:", err)
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json;charset=utf-8")

	start := time.Now()
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		fmt.Printf("  [原始返回] 无 HTTP 响应 (%.1fs): %v\n", time.Since(start).Seconds(), err)
		fmt.Println("  提示: 本机出口 IP 很可能未加入守信白名单，或 ECS 安全组/网络不通")
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	fmt.Printf("  [原始返回] HTTP %d  耗时 %.1fs  Content-Type=%s\n",
		resp.StatusCode, time.Since(start).Seconds(), resp.Header.Get("Content-Type"))
	fmt.Printf("  [原始返回] body=%s\n", body)
	return true
}

// rawDumpTransport 原样打印发给上游的请求与上游 HTTP 返回，便于与上游逐字段核对。
// 请求侧：表单字段原文 + 用 key 把 biz_data 密文解密回来的明文（即实际发出的业务数据）；
// 返回侧：状态码 + 完整 body，不截断不解析。body 读出后放回，客户端逻辑照常执行。
type rawDumpTransport struct {
	base http.RoundTripper
	key  []byte
}

func (t rawDumpTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		reqBody, _ := io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		fmt.Printf("  [请求] %s %s\n", req.Method, req.URL)
		if form, err := url.ParseQuery(string(reqBody)); err == nil {
			fmt.Printf("  [请求] institution_id=%s\n", form.Get("institution_id"))
			fmt.Printf("  [请求] biz_data(密文)=%s\n", form.Get("biz_data"))
			if plain, err := aesECBPKCS5DecryptBase64(form.Get("biz_data"), t.key); err == nil {
				fmt.Printf("  [请求] biz_data(明文)=%s\n", plain)
			} else {
				fmt.Printf("  [请求] biz_data 无法解密回明文: %v\n", err)
			}
		} else {
			fmt.Printf("  [请求] body=%s\n", reqBody)
		}
	}
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		fmt.Printf("  [原始返回] 无 HTTP 响应 (%.1fs): %v\n", time.Since(start).Seconds(), err)
		return nil, err
	}
	body, rerr := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	fmt.Printf("  [原始返回] HTTP %d  耗时 %.1fs  Content-Type=%s\n",
		resp.StatusCode, time.Since(start).Seconds(), resp.Header.Get("Content-Type"))
	fmt.Printf("  [原始返回] body=%s\n", body)
	if rerr != nil {
		fmt.Printf("  [原始返回] 读取 body 中途出错: %v\n", rerr)
	}
	return resp, nil
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
