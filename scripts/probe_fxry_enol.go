//go:build ignore

// probe_fxry_enol: 直连应诺尔 enol「风险人员 V108」doCheck，验证 appId/appSecret 与
// MD5 加签（与 sffx 身份风险V107 同端点同信封，仅 apiKey 与业务参数集合不同）。
// 在阿里云 ECS 上跑即可（出口 IP 须已在上游加白，文档 §1.2）。
//
// 用法:
//   PROBE_NAME=张三 PROBE_IDCARD=310000199001010010 go run ./scripts/probe_fxry_enol.go
//
// 环境变量:
//   PROBE_NAME / PROBE_IDCARD   必填，查询入参（风险人员V108 仅需姓名+身份证，无手机号）
//   PROBE_ENCTYPE               加密类型，1=明文【文档 §1.4 默认】/ 2=MD5 摘要；缺省 1
//   ENOL_URL / ENOL_APP_ID / ENOL_APP_SECRET / ENOL_API_KEY  覆盖默认生产凭证
package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 生产环境（风险人员V108 文档 §2.1；与 x1/gama、blk、sffx 共用应诺尔账号）。
const (
	enolURL    = "https://api.enolfax.com/enol/api/v1/doCheck"
	enolAppID  = "3QhkJCiC"
	enolSecret = "a44ff24166206291f95e214a5f9f9aa2c7a5616d"
	apiKey     = "idRiskTagV108" // ★ 固定产品编号，抄成别的会静默查到别的产品
)

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// signGama: body 非空参数按 key ASCII 升序拼「键值」+ secret → MD5 小写 hex（文档 §3.1）。
// appId/sign/apiKey/encryptionType 不参与加签，只覆盖实际发出的 body 参数。
func signGama(params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(params[k])
	}
	sb.WriteString(secret)
	return md5Hex(sb.String())
}

// encodePII: encryptionType=2 时对 PII 取 MD5 小写 hex（文档 §1.4）；明文模式原样返回。
// 注意：加签在摘要之后进行（对实际发送的值加签），与网关 upstream/riskperson.go 一致。
func encodePII(v string, encryptionType int) string {
	if encryptionType != 2 {
		return v
	}
	return md5Hex(v)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func call(url, appID, secret, apiKey string, encType int, name, idCard, label string) {
	// 风险人员V108 §2.2 body 参数表：name/idCard 必填、tradeNo 选填（不注入），无 mobile。
	body := map[string]string{}
	if name != "" {
		body["name"] = encodePII(name, encType)
	}
	if idCard != "" {
		body["idCard"] = encodePII(idCard, encType)
	}
	envelope := map[string]any{
		"encryptionType": encType,
		"appId":          appID,
		"sign":           signGama(body, secret),
		"apiKey":         apiKey,
		"body":           body,
	}
	payload, _ := json.Marshal(envelope)

	fmt.Printf("\n== [%s] ==\n", label)
	fmt.Printf("POST %s\n", url)
	fmt.Printf("encryptionType=%d（%s） apiKey=%s\n", encType, encTypeDesc(encType), apiKey)
	fmt.Printf("body(明文): name=%q idCard=%q\n", name, idCard)
	fmt.Printf("body(实际上游): %s\n", mustJSON(body))
	fmt.Printf("sign(MD5)=%s\n", envelope["sign"])

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		fmt.Printf("build request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	t0 := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(t0)
	if err != nil {
		fmt.Printf("HTTP error (%v): %v\n", elapsed, err)
		return
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("HTTP %d (%v)\n", resp.StatusCode, elapsed)
	fmt.Printf("响应: %s\n", prettyJSON(raw))

	var parsed struct {
		Code  int    `json:"code"`
		Msg   string `json:"msg"`
		SeqNo string `json:"seqNo"`
		Data  struct {
			BusiCode int             `json:"busiCode"`
			BusiMsg  string          `json:"busiMsg"`
			Result   json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		switch {
		case parsed.Code != 0:
			fmt.Printf("解读: 全局 code=%d → 响应异常\n", parsed.Code)
		case parsed.Data.BusiCode == 10:
			fmt.Printf("解读: busiCode=10 → 查得【计费】，resultCode=%s\n", riskCode(parsed.Data.Result))
		case parsed.Data.BusiCode == 1000:
			fmt.Printf("解读: busiCode=1000 → 数据未查得【计费】（本路由查无也计费）\n")
		case parsed.Data.BusiCode == 1001:
			fmt.Printf("解读: busiCode=1001 → 账户余额不足\n")
		case parsed.Data.BusiCode == 1005:
			fmt.Printf("解读: busiCode=1005 → 账号信息异常（检查 appSecret 与 body 字段加签）\n")
		default:
			fmt.Printf("解读: busiCode=%d msg=%s（1002~1009 见文档 §1.6）\n", parsed.Data.BusiCode, parsed.Data.BusiMsg)
		}
	}
}

func encTypeDesc(encType int) string {
	switch encType {
	case 2:
		return "MD5 摘要"
	default:
		return "明文"
	}
}

// riskCode 从 result 中取出 25 位风险码：线上键为 riskCode，文档 §2.3 示例为 resultCode，两者都认。
func riskCode(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return string(raw)
	}
	for _, k := range []string{"resultCode", "riskCode"} {
		if v, ok := m[k]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err == nil {
				return s
			}
		}
	}
	return string(raw)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func prettyJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}

func main() {
	url := env("ENOL_URL", enolURL)
	appID := env("ENOL_APP_ID", enolAppID)
	secret := env("ENOL_APP_SECRET", enolSecret)
	key := env("ENOL_API_KEY", apiKey)

	encType := 1 // 文档 §1.4 默认明文；设 PROBE_ENCTYPE=2 走 MD5 摘要
	if v := os.Getenv("PROBE_ENCTYPE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			encType = n
		}
	}

	fmt.Println("== fxry 直连应诺尔 enol 上游探测（风险人员 V108）==")
	fmt.Printf("url=%s\n", url)
	fmt.Printf("appId=%s\n", appID)
	fmt.Printf("appSecret=%s...%s\n", secret[:6], secret[len(secret)-4:])

	queryName, queryIDCard := os.Getenv("PROBE_NAME"), os.Getenv("PROBE_IDCARD")
	if queryName == "" || queryIDCard == "" {
		fmt.Println("缺少环境变量 PROBE_NAME / PROBE_IDCARD")
		os.Exit(2)
	}
	fmt.Printf("name=%s idCard=%s encryptionType=%d\n", queryName, queryIDCard, encType)

	call(url, appID, secret, key, encType, queryName, queryIDCard, "主查")
}
