//go:build ignore

// probe_e2e_latency_rlbd_sfzhy: 对阿里云 relay 的 rlbd1/rlbd2/sfzhy 做 E2E 耗时测试。
// 用法:
//   go run ./scripts/probe_e2e_latency_rlbd_sfzhy.go
//   RELAY_BASE_URL=http://aiszcloud.cn:8080 RLBD1_APP_KEY=... RLBD1_APP_SECRET=... ...
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/datahub/relay/test/harness"
)

const (
	repeat   = 5
	interval = 500 * time.Millisecond
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func loadB64(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	xs := append([]float64(nil), values...)
	sort.Float64s(xs)
	if len(xs) == 1 {
		return xs[0]
	}
	k := (float64(len(xs)) - 1) * p / 100.0
	f := int(math.Floor(k))
	c := int(math.Ceil(k))
	if f == c {
		return xs[f]
	}
	return xs[f] + (xs[c]-xs[f])*(k-float64(f))
}

func stats(label string, client, server []float64) {
	fmt.Printf("--- %s ---\n", label)
	if len(client) == 0 {
		fmt.Println("  (无有效请求)")
		return
	}
	fmt.Printf("  client_ms: count=%d min=%.1f max=%.1f avg=%.1f p50=%.1f p95=%.1f\n",
		len(client), client[0], client[len(client)-1], avg(client), percentile(client, 50), percentile(client, 95))
	if len(server) > 0 {
		fmt.Printf("  server_ms: count=%d min=%.1f max=%.1f avg=%.1f p50=%.1f p95=%.1f\n",
			len(server), server[0], server[len(server)-1], avg(server), percentile(server, 50), percentile(server, 95))
	}
}

func avg(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	return sum / float64(len(v))
}

type runResult struct {
	client []float64
	server []float64
	last   harness.X1Result
}

func runRoute(version, appKey, secret string, body map[string]string) runResult {
	var out runResult
	if appKey == "" || secret == "" {
		fmt.Printf("\n== %s == SKIP (未设置 APP_KEY/APP_SECRET)\n", strings.ToUpper(version))
		return out
	}

	fmt.Printf("\n== %s E2E 耗时测试 (repeat=%d) ==\n", strings.ToUpper(version), repeat)
	fmt.Printf("  route: %s\n", harness.QueryPath(version))
	fmt.Printf("  appKey: %s\n", appKey)

	for i := 0; i < repeat; i++ {
		t0 := time.Now()
		r := harness.Query(version, appKey, secret, body, nil)
		clientMs := float64(time.Since(t0).Milliseconds())

		serverMs := parseServerMs(r.Raw)
		out.client = append(out.client, clientMs)
		if serverMs >= 0 {
			out.server = append(out.server, serverMs)
		}
		out.last = r

		fmt.Printf("  [#%d/%d] errorCode=%s bodyCode=%s client_ms=%.0f server_ms=%.0f logId=%s\n",
			i+1, repeat, r.ErrorCode, r.BodyCode, clientMs, serverMs, extractLogID(r.Raw))

		if i+1 < repeat {
			time.Sleep(interval)
		}
	}

	sort.Float64s(out.client)
	sort.Float64s(out.server)
	stats(version, out.client, out.server)

	ok := out.last.ErrorCode == "0" && out.last.BodyCode == "001"
	if ok {
		fmt.Printf("  结果: OK (全链路成功 body.code=001)\n")
	} else {
		fmt.Printf("  结果: WARN/FAIL errorCode=%s bodyCode=%s\n", out.last.ErrorCode, out.last.BodyCode)
		if len(out.last.Raw) < 400 {
			fmt.Printf("  raw: %s\n", out.last.Raw)
		} else {
			fmt.Printf("  raw: %s...\n", out.last.Raw[:400])
		}
	}
	return out
}

func parseServerMs(raw string) float64 {
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return -1
	}
	head, _ := data["head"].(map[string]any)
	if head == nil {
		return -1
	}
	switch t := head["time"].(type) {
	case float64:
		return t
	case int:
		return float64(t)
	default:
		return -1
	}
}

func extractLogID(raw string) string {
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return ""
	}
	head, _ := data["head"].(map[string]any)
	if head == nil {
		return ""
	}
	if v, ok := head["logId"].(string); ok {
		return v
	}
	return ""
}

func main() {
	os.Setenv("RELAY_BASE_URL", env("RELAY_BASE_URL", "http://aiszcloud.cn:8080"))
	base := harness.BaseURL()
	fmt.Println("=== 阿里云 RLBD1 / RLBD2 / SFZHY E2E 耗时测试 ===")
	fmt.Printf("baseURL: %s\n", base)

	// 测试人像：rlbd 可用较大图；sfzhy 需 ≤50KB，优先 rl_small.jpg
	rlPhoto := firstExisting(
		`c:\workspace\DataHub\test\rl.jpg`,
		`C:\Users\XMMXYY\.cursor\projects\c-workspace\assets\c__workspace_DataHub_test_rl.jpg`,
		`c:\workspace\DataHub\docs\fpsw.jpg`,
	)
	sfzPhoto := firstExisting(
		`c:\workspace\DataHub\test\rl_small.jpg`,
		rlPhoto,
	)
	if rlPhoto == "" {
		fmt.Println("WARN: 未找到 rlbd 测试照片，rlbd 将跳过")
	}
	if sfzPhoto == "" {
		fmt.Println("WARN: 未找到 sfzhy 测试照片，sfzhy 将跳过")
	}

	name := env("E2E_NAME", "陈韫")
	idCard := env("E2E_ID_CARD", "440303200002163115")

	rlbd1Key := env("RLBD1_APP_KEY", "ubijqg8k6698")
	rlbd1Secret := env("RLBD1_APP_SECRET", "624e149ffe7571e69bb8a20387cedb76")
	rlbd2Key := env("RLBD2_APP_KEY", "")
	rlbd2Secret := env("RLBD2_APP_SECRET", "")
	sfzKey := env("SFZHY_APP_KEY", "xg5b6hhqz2n4")
	sfzSecret := env("SFZHY_APP_SECRET", "b973f1406b1979d5416353102d540dd5")

	var rlbdBody map[string]string
	if rlPhoto != "" {
		img, err := loadB64(rlPhoto)
		if err != nil {
			fmt.Printf("WARN: 读取 rlbd 照片失败: %v\n", err)
		} else {
			fmt.Printf("rlbd photo: %s (b64_len=%d)\n", rlPhoto, len(img))
			rlbdBody = map[string]string{"name": name, "idCard": idCard, "image": img}
		}
	}

	var sfzBody map[string]string
	if sfzPhoto != "" {
		img, err := loadB64(sfzPhoto)
		if err != nil {
			fmt.Printf("WARN: 读取 sfzhy 照片失败: %v\n", err)
		} else {
			fmt.Printf("sfzhy photo: %s (b64_len=%d)\n", sfzPhoto, len(img))
			sfzBody = map[string]string{"name": name, "idCard": idCard, "profilePicture": img}
		}
	}

	r1 := runRoute("rlbd1", rlbd1Key, rlbd1Secret, rlbdBody)
	r2 := runRoute("rlbd2", rlbd2Key, rlbd2Secret, rlbdBody)
	r3 := runRoute("sfzhy", sfzKey, sfzSecret, sfzBody)

	fmt.Println("\n=== 汇总 ===")
	printSummary("RLBD1", r1)
	printSummary("RLBD2", r2)
	printSummary("SFZHY", r3)
}

func printSummary(name string, r runResult) {
	if len(r.client) == 0 {
		fmt.Printf("%s: SKIP\n", name)
		return
	}
	ok := r.last.ErrorCode == "0" && r.last.BodyCode == "001"
	status := "FAIL"
	if ok {
		status = "OK"
	}
	fmt.Printf("%s: %s | client_ms avg=%.1f p50=%.1f p95=%.1f | server_ms avg=%.1f | last errorCode=%s bodyCode=%s\n",
		name, status,
		avg(r.client), percentile(r.client, 50), percentile(r.client, 95),
		avg(r.server),
		r.last.ErrorCode, r.last.BodyCode,
	)
}
