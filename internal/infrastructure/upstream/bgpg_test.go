package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// 背景评估的 encryptKey 是 hex 文本（官方 demo 用 Hex.decodeHex 取密钥），必须显式
// 验算：直接把 32 个 hex 字符当密钥字节交给 aes.NewCipher 会得到 invalid key size 32，
// 线上表现是"一个请求都发不出去"而日志里只有加密失败。长度不合法必须报错，不静默降级。
func TestAESKeyFromHex(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want int // 期望字节数；0 表示期望报错
	}{
		{"32 hex 字符 → AES-128", "0031cee6808eb6d5b0e07536218f1234", 16},
		{"48 hex 字符 → AES-192", strings.Repeat("ab", 24), 24},
		{"64 hex 字符 → AES-256", strings.Repeat("cd", 32), 32},
		{"长度合法但非 hex", strings.Repeat("z", 32), 0},
		{"hex 但长度不合法 (20 字节)", strings.Repeat("ef", 20), 0},
		{"奇数个 hex 字符", "abc", 0},
		{"空串", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := aesKeyFromHex(tc.key)
			if tc.want == 0 {
				if err == nil {
					t.Fatalf("期望报错，实际得到 %d 字节密钥", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("意外报错: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("密钥长度 = %d, 期望 %d", len(got), tc.want)
			}
		})
	}
}

// IV 是 16 个 ASCII 字符 '0'（0x30），不是 16 个零字节——写错则与上游互不可解。
func TestBgPGIVIsASCIIZeros(t *testing.T) {
	if len(bgpgIV) != 16 {
		t.Fatalf("IV 长度 = %d, 期望 16", len(bgpgIV))
	}
	for i := 0; i < len(bgpgIV); i++ {
		if bgpgIV[i] != '0' {
			t.Fatalf("IV[%d] = %#x, 期望 ASCII '0' (0x30)", i, bgpgIV[i])
		}
	}
}

func TestAESCBCRoundTrip(t *testing.T) {
	key, err := aesKeyFromHex("0031cee6808eb6d5b0e07536218f1234")
	if err != nil {
		t.Fatalf("aesKeyFromHex: %v", err)
	}
	plain := `{"idCard":"330129199109094312","name":"石磊"}`
	enc, err := aesCBCEncryptBase64([]byte(plain), key, []byte(bgpgIV))
	if err != nil {
		t.Fatalf("aesCBCEncryptBase64: %v", err)
	}
	dec, err := aesCBCDecryptBase64(enc, key, []byte(bgpgIV))
	if err != nil {
		t.Fatalf("aesCBCDecryptBase64: %v", err)
	}
	if string(dec) != plain {
		t.Fatalf("往返结果 = %q, 期望 %q", dec, plain)
	}
	// IV 不匹配必须解不出原文（证明 IV 真的参与了运算，没被忽略）。
	if got, err := aesCBCDecryptBase64(enc, key, []byte("1111111111111111")); err == nil && string(got) == plain {
		t.Fatal("换了 IV 仍能解出原文，说明 IV 未生效")
	}
}

// encryptKey 配了但形态非法 → NewBgPG 直接报错，令服务启动期即暴露；未配置 →
// 允许构造（memory/未配置模式服务仍要能启动），调用时才报错。
func TestNewBgPGKeyValidation(t *testing.T) {
	if _, err := NewBgPG(BgPGConfig{EncryptKey: "not-hex-at-all"}, nil); err == nil {
		t.Fatal("非法 encryptKey 应让 NewBgPG 报错")
	}
	c, err := NewBgPG(BgPGConfig{}, nil)
	if err != nil {
		t.Fatalf("未配置 encryptKey 不应阻塞构造: %v", err)
	}
	if c.cfg.ProdID != BgPGProdID {
		t.Fatalf("prodId 缺省 = %q, 期望 %q", c.cfg.ProdID, BgPGProdID)
	}
	if _, err := c.Query(context.Background(), &model.UpstreamRequest{}); err == nil {
		t.Fatal("未配置 encryptKey 时 Query 应报错")
	}
}

const testEncryptKey = "0031cee6808eb6d5b0e07536218f1234"

// newTestBgPG 起一个按 code 应答的挡板：请求体 data 解密后回显给校验函数。
func newTestBgPG(t *testing.T, handle func(params bgpgParams, key []byte) (code, uuid, retMsg, dataPlain string)) (*BgPGClient, *httptest.Server) {
	t.Helper()
	key, err := aesKeyFromHex(testEncryptKey)
	if err != nil {
		t.Fatalf("aesKeyFromHex: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req bgpgRequest
		_ = json.Unmarshal(raw, &req)
		plain, derr := aesCBCDecryptBase64(req.Data, key, []byte(bgpgIV))
		if derr != nil {
			writeBgPGJSON(w, bgpgResponse{Code: "2-501", UUID: "u-dec", RetMsg: "解密失败"})
			return
		}
		var p bgpgParams
		_ = json.Unmarshal(plain, &p)
		code, uuid, retMsg, dataPlain := handle(p, key)
		resp := bgpgResponse{Code: code, UUID: uuid, RetMsg: retMsg}
		if dataPlain != "" {
			enc, eerr := aesCBCEncryptBase64([]byte(dataPlain), key, []byte(bgpgIV))
			if eerr != nil {
				t.Errorf("挡板加密失败: %v", eerr)
			}
			resp.Data = enc
		}
		writeBgPGJSON(w, resp)
	}))
	client, err := NewBgPG(BgPGConfig{BaseURL: srv.URL, AccountID: "acct-1", EncryptKey: testEncryptKey}, srv.Client())
	if err != nil {
		t.Fatalf("NewBgPG: %v", err)
	}
	return client, srv
}

func writeBgPGJSON(w http.ResponseWriter, resp bgpgResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

// 查得：code 200 → 001，range 为解密后业务对象的 compact JSON，**全字段**透出
// (含 xm/sfz/jfdw/grsf，不裁剪)；uuid 同时落 UID 与 LogID 供后台对账。
func TestBgPGQueryFound(t *testing.T) {
	const bizJSON = `{"jfsj":"202603","cbjfzt":"1","jfjs":"6800","xm":"石磊","sfz":"430481199","jfdw":"珠海科技有限公司","grsf":"24"}`
	client, srv := newTestBgPG(t, func(p bgpgParams, _ []byte) (string, string, string, string) {
		if p.IDCard != "330129199109094312" || p.Name != "石磊" {
			t.Errorf("上游收到的业务参数不符: %+v", p)
		}
		return "200", "uuid-ok", "成功", bizJSON
	})
	defer srv.Close()

	res, err := client.Query(context.Background(), &model.UpstreamRequest{
		IDCard: "330129199109094312", Name: "石磊", Reqid: "r1",
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Code != "001" {
		t.Fatalf("code = %s, 期望 001", res.Code)
	}
	if res.UID != "uuid-ok" || res.LogID != "uuid-ok" {
		t.Fatalf("UID/LogID = %q/%q, 期望均为 uuid-ok", res.UID, res.LogID)
	}
	for _, f := range []string{"jfsj", "cbjfzt", "jfjs", "xm", "sfz", "jfdw", "grsf"} {
		if !strings.Contains(res.Range, `"`+f+`"`) {
			t.Fatalf("range 缺字段 %s: %s", f, res.Range)
		}
	}
}

// 查无：2-404 与 3-404 两个码都要归一为 999，且仍带 uuid 落审计。
func TestBgPGQueryNotFoundCodes(t *testing.T) {
	for _, code := range []string{bgpgCodeNotFound, bgpgCodeNotFoundOuter} {
		t.Run(code, func(t *testing.T) {
			client, srv := newTestBgPG(t, func(bgpgParams, []byte) (string, string, string, string) {
				return code, "uuid-nf", "没有查询到数据", ""
			})
			defer srv.Close()
			res, err := client.Query(context.Background(), &model.UpstreamRequest{
				IDCard: "330129199109094312", Name: "石磊", Reqid: "r2",
			})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if res.Code != "999" {
				t.Fatalf("code = %s, 期望 999", res.Code)
			}
			if res.UID != "uuid-nf" || res.LogID != "uuid-nf" {
				t.Fatalf("查无路径也必须带 uuid: UID=%q LogID=%q", res.UID, res.LogID)
			}
		})
	}
}

// 失败路径铁律：必须返回 *model.UpstreamError 且带全 code/msg/uid/logID，否则后台
// 「操作记录」的上游 code/uid/logId 三列会变空，运营无法向上游对账。
func TestBgPGQueryFailureCarriesUpstreamIDs(t *testing.T) {
	client, srv := newTestBgPG(t, func(bgpgParams, []byte) (string, string, string, string) {
		return "2-508", "uuid-err", "请求ip不在白名单内，请联系管理员", ""
	})
	defer srv.Close()

	_, err := client.Query(context.Background(), &model.UpstreamRequest{
		IDCard: "330129199109094312", Name: "石磊", Reqid: "r3",
	})
	if err == nil {
		t.Fatal("期望上游业务失败返回 error")
	}
	var ue *model.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("错误类型 = %T, 期望 *model.UpstreamError", err)
	}
	if ue.Code != "2-508" || ue.UID != "uuid-err" || ue.LogID != "uuid-err" {
		t.Fatalf("上游标识未带全: %+v", ue)
	}
	if !strings.Contains(ue.Msg, "白名单") {
		t.Fatalf("msg 未透出上游 retMsg: %q", ue.Msg)
	}
}
