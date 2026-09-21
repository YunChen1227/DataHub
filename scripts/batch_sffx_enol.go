//go:build ignore

// batch_sffx_enol: 在阿里云 ECS 上直连应诺尔 enol「身份风险 V107」批量测试。
// CSV 的 id/name 已是 MD5 摘要时原样上送（encryptionType=2，不再二次哈希）。
//
// 用法（在 ECS 上）:
//   cd /workspace/DataHub
//   go run ./scripts/batch_sffx_enol.go /path/to/zrr_200.csv
//   go run ./scripts/batch_sffx_enol.go zrr_200.csv -o zrr_200_enol_result.csv -w 8
//
// 环境变量可覆盖默认生产凭证:
//   ENOL_URL / ENOL_APP_ID / ENOL_APP_SECRET / ENOL_API_KEY
package main

import (
	"bytes"
	"crypto/md5"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultURL    = "https://api.enolfax.com/enol/api/v1/doCheck"
	defaultAppID  = "3QhkJCiC"
	defaultSecret = "a44ff24166206291f95e214a5f9f9aa2c7a5616d"
	defaultAPIKey = "idRiskTagV107"
	encType       = 2
)

type kind int

const (
	kindSuccess kind = iota // busiCode=10 查得
	kindNotFound            // busiCode=1000 查无
	kindError               // 网络/HTTP/全局 code≠0 / 已知账户类错误
	kindFail                // 其它未归类业务码
)

func (k kind) String() string {
	switch k {
	case kindSuccess:
		return "成功"
	case kindNotFound:
		return "查无"
	case kindError:
		return "报错"
	default:
		return "失败"
	}
}

type rowIn struct {
	idx     int
	cusNo   string
	idMD5   string
	nameMD5 string
	raw     []string
}

type rowOut struct {
	in       rowIn
	kind     kind
	typeKey  string // 用于汇总「所有返回值类型」
	result   string
	raw      string
	elapsed  time.Duration
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

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

func looksMD5(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// asUpstreamPII: CSV 已是 32 位 hex 则原样上送；否则按 encryptionType=2 再算一次 MD5。
func asUpstreamPII(v string) string {
	v = strings.TrimSpace(v)
	if looksMD5(v) {
		return strings.ToLower(v)
	}
	return md5Hex(v)
}

type enolResp struct {
	Code  int    `json:"code"`
	Msg   string `json:"msg"`
	SeqNo string `json:"seqNo"`
	Data  struct {
		BusiCode int             `json:"busiCode"`
		BusiMsg  string          `json:"busiMsg"`
		Result   json.RawMessage `json:"result"`
	} `json:"data"`
}

var errorBusi = map[int]bool{
	-1: true, 1001: true, 1002: true, 1003: true, 1004: true,
	1005: true, 1006: true, 1007: true, 1009: true, 1015: true,
}

func classify(httpStatus int, raw string, err error) (kind, string, string) {
	if err != nil {
		msg := err.Error()
		return kindError, "网络/客户端错误: " + msg, "ERROR|" + msg
	}
	if httpStatus != 200 {
		key := fmt.Sprintf("HTTP %d", httpStatus)
		return kindError, key, fmt.Sprintf("%s|%s", key, raw)
	}
	var r enolResp
	if e := json.Unmarshal([]byte(raw), &r); e != nil {
		return kindError, "响应JSON无法解析", "ERROR|bad json|" + raw
	}
	compact := strings.TrimSpace(string(r.Data.Result))
	if compact == "null" {
		compact = ""
	}
	switch {
	case r.Code != 0:
		key := fmt.Sprintf("全局code=%d msg=%s busiCode=%d busiMsg=%s", r.Code, r.Msg, r.Data.BusiCode, r.Data.BusiMsg)
		return kindError, key, fmt.Sprintf("报错|%s|seqNo=%s", key, r.SeqNo)
	case r.Data.BusiCode == 10:
		key := "busiCode=10 查得"
		return kindSuccess, key, fmt.Sprintf("成功|%s|%s|seqNo=%s", r.Data.BusiMsg, compact, r.SeqNo)
	case r.Data.BusiCode == 1000:
		key := "busiCode=1000 查无"
		return kindNotFound, key, fmt.Sprintf("查无|%s|seqNo=%s", r.Data.BusiMsg, r.SeqNo)
	case errorBusi[r.Data.BusiCode]:
		key := fmt.Sprintf("busiCode=%d %s", r.Data.BusiCode, r.Data.BusiMsg)
		return kindError, key, fmt.Sprintf("报错|%s|seqNo=%s", key, r.SeqNo)
	default:
		key := fmt.Sprintf("busiCode=%d %s", r.Data.BusiCode, r.Data.BusiMsg)
		return kindFail, key, fmt.Sprintf("失败|%s|%s|seqNo=%s", key, compact, r.SeqNo)
	}
}

func callEnol(client *http.Client, url, appID, secret, apiKey, nameMD5, idMD5 string) (int, string, error) {
	body := map[string]string{
		"name":   asUpstreamPII(nameMD5),
		"idCard": asUpstreamPII(idMD5),
	}
	env := map[string]any{
		"encryptionType": encType,
		"appId":          appID,
		"sign":           signGama(body, secret),
		"apiKey":         apiKey,
		"body":           body,
	}
	payload, _ := json.Marshal(env)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), nil
}

func readCSV(path string) ([]rowIn, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(records) < 2 {
		return nil, nil, fmt.Errorf("csv 至少需要表头+1行数据")
	}
	header := records[0]
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	idIdx, okID := col["id"]
	nameIdx, okName := col["name"]
	if !okID || !okName {
		return nil, nil, fmt.Errorf("csv 需要 id 与 name 列，实际表头: %v", header)
	}
	cusIdx, okCus := col["cus_no"]
	out := make([]rowIn, 0, len(records)-1)
	for i, rec := range records[1:] {
		if len(rec) == 0 {
			continue
		}
		get := func(idx int) string {
			if idx >= 0 && idx < len(rec) {
				return strings.TrimSpace(rec[idx])
			}
			return ""
		}
		cus := ""
		if okCus {
			cus = get(cusIdx)
		} else {
			cus = fmt.Sprintf("%d", i+1)
		}
		out = append(out, rowIn{idx: i, cusNo: cus, idMD5: get(idIdx), nameMD5: get(nameIdx), raw: rec})
	}
	return out, header, nil
}

func main() {
	workers := flag.Int("w", 8, "并发数")
	outPath := flag.String("o", "", "结果 csv 路径（默认在输入文件名后加 _enol_result）")
	timeout := flag.Duration("timeout", 8*time.Second, "单次请求超时")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "用法: go run ./scripts/batch_sffx_enol.go [flags] <csv>\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	inPath := flag.Arg(0)
	if *outPath == "" {
		base := strings.TrimSuffix(inPath, ".csv")
		*outPath = base + "_enol_result.csv"
	}

	url := envOr("ENOL_URL", defaultURL)
	appID := envOr("ENOL_APP_ID", defaultAppID)
	secret := envOr("ENOL_APP_SECRET", defaultSecret)
	apiKey := envOr("ENOL_API_KEY", defaultAPIKey)

	rows, header, err := readCSV(inPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读 csv 失败: %v\n", err)
		os.Exit(1)
	}

	already := 0
	for _, r := range rows {
		if looksMD5(r.idMD5) && looksMD5(r.nameMD5) {
			already++
		}
	}

	fmt.Println("== 应诺尔 enol 身份风险 V107 直连批量测试 ==")
	fmt.Printf("url=%s\n", url)
	fmt.Printf("appId=%s apiKey=%s encryptionType=%d\n", appID, apiKey, encType)
	fmt.Printf("csv=%s  行数=%d  其中 id+name 已是MD5=%d\n", inPath, len(rows), already)
	fmt.Printf("并发=%d  超时=%s  输出=%s\n", *workers, timeout, *outPath)
	fmt.Println("规则: 32位hex 原样上送，不再二次 MD5；明文才会再哈希。")
	fmt.Println()

	client := &http.Client{Timeout: *timeout}
	out := make([]rowOut, len(rows))
	var done atomic.Int64
	sem := make(chan struct{}, *workers)
	var wg sync.WaitGroup
	t0 := time.Now()

	for i, in := range rows {
		wg.Add(1)
		go func(i int, in rowIn) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st := time.Now()
			httpStatus, raw, callErr := callEnol(client, url, appID, secret, apiKey, in.nameMD5, in.idMD5)
			k, typeKey, result := classify(httpStatus, raw, callErr)
			out[i] = rowOut{in: in, kind: k, typeKey: typeKey, result: result, raw: raw, elapsed: time.Since(st)}
			n := done.Add(1)
			if n%20 == 0 || n == int64(len(rows)) {
				fmt.Printf("  进度 %d/%d  最近: cus_no=%s %s (%s)\n", n, len(rows), in.cusNo, k, typeKey)
			}
		}(i, in)
	}
	wg.Wait()
	elapsed := time.Since(t0)

	// 写结果 csv
	of, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "写结果失败: %v\n", err)
		os.Exit(1)
	}
	w := csv.NewWriter(of)
	newHeader := append(append([]string{}, header...), "enol_kind", "enol_type", "enol_result")
	_ = w.Write(newHeader)
	for _, o := range out {
		base := make([]string, len(header))
		copy(base, o.in.raw)
		rec := append(base, o.kind.String(), o.typeKey, o.result)
		_ = w.Write(rec)
	}
	w.Flush()
	of.Close()

	counts := map[kind]int{}
	typeCount := map[string]int{}
	typeKind := map[string]kind{}
	for _, o := range out {
		counts[o.kind]++
		typeCount[o.typeKey]++
		typeKind[o.typeKey] = o.kind
	}

	fmt.Println()
	fmt.Println("========== 汇总 ==========")
	fmt.Printf("成功: %d\n", counts[kindSuccess])
	fmt.Printf("失败: %d\n", counts[kindFail])
	fmt.Printf("查无: %d\n", counts[kindNotFound])
	fmt.Printf("报错: %d\n", counts[kindError])
	fmt.Printf("合计: %d   耗时: %s\n", len(rows), elapsed.Round(time.Millisecond))
	fmt.Println()
	fmt.Println("---------- 所有返回值类型 ----------")
	keys := make([]string, 0, len(typeCount))
	for k := range typeCount {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if typeCount[keys[i]] != typeCount[keys[j]] {
			return typeCount[keys[i]] > typeCount[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		fmt.Printf("  [%s] %4d  %s\n", typeKind[k], typeCount[k], k)
	}
	fmt.Println()
	fmt.Printf("结果已写入: %s\n", *outPath)
}
