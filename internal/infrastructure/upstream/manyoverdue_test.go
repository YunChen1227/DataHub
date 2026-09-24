package upstream

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// 16 字节 (AES-128) 测试密钥, 与兄弟产品 zlf 的沙箱密钥形态一致 (原始 ASCII)。
const manyOverdueTestKey = "0123456789abcdef"

func manyOverdueClient(t *testing.T, srv *httptest.Server) *ManyOverdueClient {
	t.Helper()
	return NewManyOverdue(ManyOverdueConfig{
		BaseURL:       srv.URL,
		InstitutionID: "inst-1",
		AESKey:        manyOverdueTestKey,
		LicenseURL:    "https://shouwei.oss-cn-shanghai.aliyuncs.com/approve_files/x.pdf",
		LicenseType:   1,
	}, srv.Client())
}

func manyOverdueReq() *model.UpstreamRequest {
	return &model.UpstreamRequest{
		Name:   "张三",
		IDCard: "330129199109094312",
		Mobile: "13809091009",
		Reqid:  "r1",
	}
}

func manyOverdueServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json;charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 密钥形态必须验算, 不能想当然 (verify-source-doc skill 第 4 条)：AES 只接受
// 16/24/32 字节。64 个十六进制字符是 32 字节 AES-256 密钥 (必须 hex 解码), **不是**
// 64 字节 ASCII; Base64 形态的要先 Base64 解码。非法长度必须立刻报错, 禁止静默降级。
// 本产品的 institution_id/aesKey **可能与 zlf/dtjd/snhmd 不同**, 不得假定共用同一把密钥。
func TestManyOverdueAESKeyDerivation(t *testing.T) {
	hex32 := hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef")) // 64 hex 字符 → 32 字节
	// Base64(32 字节) = 44 个字符: 长度不在 {16,24,32} 里, 才会走到 Base64 分支。
	b64of32 := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

	cases := []struct {
		name    string
		key     string
		wantLen int // 0 表示期望报错
	}{
		{"16 字节原始 ASCII (zlf 沙箱密钥形态)", "0123456789abcdef", 16},
		{"24 字节原始 ASCII", "012345678901234567890123", 24},
		{"32 字节原始 ASCII", "0123456789abcdef0123456789abcdef", 32},
		{"64 个十六进制字符 → hex 解码成 32 字节 AES-256", hex32, 32},
		{"Base64 文本 (44 字符) → 解码成 32 字节", b64of32, 32},
		{"Base64(24 字节)=32 字符 → 本身即合法长度, 按原始字节采用", base64.StdEncoding.EncodeToString([]byte("012345678901234567890123")), 32},
		{"空密钥 → 报错", "", 0},
		{"长度非法且非 hex/Base64 → 报错", "tooshort", 0},
		{"63 个十六进制字符 (奇数位, hex 解不开) → 报错", strings.Repeat("a", 63), 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := manyOverdueAESKey(tc.key)
			if tc.wantLen == 0 {
				if err == nil {
					t.Fatalf("期望报错, 实际解出 %d 字节", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("manyOverdueAESKey: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("密钥长度 = %d, 期望 %d", len(got), tc.wantLen)
			}
		})
	}
}

// 密钥非法时不得静默降级: 每次 Query 都要直接失败, 而不是发出一个上游必然拒绝的请求。
func TestManyOverdueIllegalKeyFailsEveryQuery(t *testing.T) {
	srv := manyOverdueServer(t, `{"resp_code":"SW0000"}`)
	c := NewManyOverdue(ManyOverdueConfig{BaseURL: srv.URL, InstitutionID: "i", AESKey: "badkey"}, srv.Client())
	if _, err := c.Query(context.Background(), manyOverdueReq()); err == nil {
		t.Fatal("密钥非法时 Query 必须报错")
	}
}

// 请求侧契约逐字对齐文档 §2.1–2.5：POST + x-www-form-urlencoded、form 只有
// institution_id 与 biz_data 两个字段、biz_data 是 AES/ECB/PKCS5 + Base64 的业务数据
// 密文, 解开后七个字段名与默认 service/mode 一字不差。
// **mode 必须是 mode_many_overdue_behavior**: 同端点靠 mode 区分产品, 抄成兄弟产品的
// mode_loan_intent_v1 / mode_compass_black / 租赁分的 mode 会静默查到另一个产品的数据
// (不报错, 更难发现)。
func TestManyOverdueRequestContract(t *testing.T) {
	var (
		gotMethod, gotCT string
		gotForm          map[string]string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		gotForm = map[string]string{}
		for k := range r.PostForm {
			gotForm[k] = r.PostForm.Get(k)
		}
		_, _ = w.Write([]byte(`{"resp_code":"SW0000","resp_msg":"查询成功","resp_order":"lgt1","timestamp":1790141283846,"resp_data":{"xyp_cpl0001":"1"}}`))
	}))
	defer srv.Close()

	if _, err := manyOverdueClient(t, srv).Query(context.Background(), manyOverdueReq()); err != nil {
		t.Fatalf("Query: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, 期望 POST", gotMethod)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q, 期望 application/x-www-form-urlencoded", gotCT)
	}
	if gotForm["institution_id"] != "inst-1" {
		t.Fatalf("institution_id = %q", gotForm["institution_id"])
	}
	if len(gotForm) != 2 {
		t.Fatalf("form 字段应只有 institution_id + biz_data, 实际 %v", gotForm)
	}

	plain, err := aesECBDecryptBase64(gotForm["biz_data"], []byte(manyOverdueTestKey))
	if err != nil {
		t.Fatalf("biz_data 不是 AES/ECB/PKCS5+Base64 密文: %v", err)
	}
	var biz map[string]any
	if err := json.Unmarshal(plain, &biz); err != nil {
		t.Fatalf("biz_data 明文不是 JSON: %v (%s)", err, plain)
	}
	want := map[string]any{
		"name":         "张三",
		"ident_number": "330129199109094312",
		"phone":        "13809091009",
		"service":      "financial_rent_service",
		"mode":         "mode_many_overdue_behavior",
		"licenseUrl":   "https://shouwei.oss-cn-shanghai.aliyuncs.com/approve_files/x.pdf",
		"licenseType":  float64(1),
	}
	if len(biz) != len(want) {
		t.Fatalf("biz_data 字段集合不符 (七个必传字段一个都不能少), 实际 %v", biz)
	}
	for k, v := range want {
		if biz[k] != v {
			t.Fatalf("biz_data[%q] = %v, 期望 %v", k, biz[k], v)
		}
	}
}

// 归一化口径逐条对齐文档 §4「响应状态码对照表」整表 19 个码。
// 这张表是计费正确性的唯一防线, 改动前先回上游文档核对 (billing-scope skill)。
func TestManyOverdueNormalization(t *testing.T) {
	const foundData = `{"xyp_cpl0001":"1","xyp_cpl0044":"0","xyp_model_score_mid":"-1"}`

	cases := []struct {
		name     string
		body     string
		wantCode string // "" 表示期望 *model.UpstreamError
	}{
		{
			name:     "SW0000 认证成功【收费】→ 001 查得计费",
			body:     `{"resp_code":"SW0000","resp_msg":"查询成功","resp_order":"lgt-0","resp_data":` + foundData + `}`,
			wantCode: "001",
		},
		// 本产品文档 §4 明确 SW0001【不收费】→ 纯上游侧错误。
		// **不要照抄 dtjd**: 那份文档的 SW0001 标【收费】, 被迫归一成 999, 口径相反。
		{name: "SW0001 认证失败【不收费】→ 上游侧错误 (与 dtjd 口径相反)", body: `{"resp_code":"SW0001","resp_msg":"认证失败","resp_order":"lgt-1"}`},
		{
			name:     "SW0002 查询无记录【不收费】→ 999 查无",
			body:     `{"resp_code":"SW0002","resp_msg":"查询无记录","resp_order":"lgt-2"}`,
			wantCode: "999",
		},
		{name: "SW0003 通道超时或异常 → 上游侧错误", body: `{"resp_code":"SW0003","resp_msg":"通道超时或异常","resp_order":"lgt-3"}`},
		{name: "SW0017 biz_data参数错误 → 上游侧错误", body: `{"resp_code":"SW0017","resp_msg":"biz_data参数错误"}`},
		{name: "SW0018 参数错误 → 上游侧错误", body: `{"resp_code":"SW0018","resp_msg":"参数错误"}`},
		{name: "SW0030 签名为空 → 上游侧错误", body: `{"resp_code":"SW0030","resp_msg":"签名为空"}`},
		{name: "SW0031 客户公钥为空 → 上游侧错误", body: `{"resp_code":"SW0031","resp_msg":"客户公钥为空"}`},
		{name: "SW0032 验签失败 → 上游侧错误", body: `{"resp_code":"SW0032","resp_msg":"验签失败"}`},
		{name: "SW0033 验签错误 → 上游侧错误", body: `{"resp_code":"SW0033","resp_msg":"验签错误"}`},
		{name: "SW0034 解密失败 → 上游侧错误", body: `{"resp_code":"SW0034","resp_msg":"解密失败"}`},
		{name: "SW0040 产品未开通 → 上游侧错误", body: `{"resp_code":"SW0040","resp_msg":"产品未开通"}`},
		{name: "SW0041 调用量已达上限 → 上游侧错误", body: `{"resp_code":"SW0041","resp_msg":"调用量已达上限"}`},
		{name: "SW0042 账户余额不足 → 上游侧错误", body: `{"resp_code":"SW0042","resp_msg":"账户余额不足"}`},
		{name: "SW1009 姓名格式有误 → 上游侧错误", body: `{"resp_code":"SW1009","resp_msg":"姓名格式有误"}`},
		{name: "SW1010 身份证号格式有误 → 上游侧错误", body: `{"resp_code":"SW1010","resp_msg":"身份证号格式有误"}`},
		{name: "SW1011 卡号格式有误 → 上游侧错误", body: `{"resp_code":"SW1011","resp_msg":"卡号格式有误"}`},
		{name: "SW1012 手机号格式有误 → 上游侧错误", body: `{"resp_code":"SW1012","resp_msg":"手机号格式有误"}`},
		{name: "SW9999 系统错误 → 上游侧错误", body: `{"resp_code":"SW9999","resp_msg":"系统错误"}`},
		// 成功码但主体为空/不可解析: 拿不到数据不得计费 (billing-scope 第四节第 5 条)。
		{name: "SW0000 但 resp_data 缺失 → 上游侧错误不计费", body: `{"resp_code":"SW0000","resp_msg":"查询成功","resp_order":"lgt-e"}`},
		{name: "SW0000 但 resp_data 为空对象 → 上游侧错误不计费", body: `{"resp_code":"SW0000","resp_msg":"查询成功","resp_order":"lgt-e","resp_data":{}}`},
		{name: "SW0000 但 resp_data 为 null → 上游侧错误不计费", body: `{"resp_code":"SW0000","resp_order":"lgt-e","resp_data":null}`},
		// 文档「其余按此顺序拓展」这类开放式约定: 未列的新码一律上游侧错误, 不得误计费。
		{name: "文档未列的新码 → 上游侧错误", body: `{"resp_code":"SW7777","resp_msg":"未知"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := manyOverdueServer(t, tc.body)
			res, err := manyOverdueClient(t, srv).Query(context.Background(), manyOverdueReq())

			if tc.wantCode == "" {
				if err == nil {
					t.Fatalf("期望上游侧错误, 实际成功: code=%s range=%s", res.Code, res.Range)
				}
				var be *model.UpstreamError
				if !errors.As(err, &be) {
					t.Fatalf("期望 *model.UpstreamError (带上游 code/uid 落审计), 实际 %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if res.Code != tc.wantCode {
				t.Fatalf("code = %s, 期望 %s", res.Code, tc.wantCode)
			}
		})
	}
}

// 回归护栏: 本产品的 SW0001 必须保持 error, 不得被"统一"成兄弟产品 dtjd 的 999。
// 两份文档的计费列相反 (本产品不收费 / dtjd 收费), 归一口径因此必须不同;
// 若哪天有人为了"守信系几条路由一致"把这里改成 999, 本用例会立刻失败。
func TestManyOverdueAuthFailIsUpstreamErrorNotNotFound(t *testing.T) {
	srv := manyOverdueServer(t, `{"resp_code":"SW0001","resp_msg":"认证失败","resp_order":"lgt-af"}`)
	res, err := manyOverdueClient(t, srv).Query(context.Background(), manyOverdueReq())
	if err == nil {
		t.Fatalf("SW0001 本产品标【不收费】, 应按上游侧错误返回, 实际得到 code=%s", res.Code)
	}
	var be *model.UpstreamError
	if !errors.As(err, &be) {
		t.Fatalf("期望 *model.UpstreamError, 实际 %T", err)
	}
	if be.Code != "SW0001" {
		t.Fatalf("上游 code 未落审计: %+v", be)
	}
}

// 上游 resp_order 是唯一上游标识: 成功(001)、查无(999)、业务失败三条路径都必须同时落
// UID 与 LogID, 否则管理后台「上游uid」「上游logId」列为空, 运营无法向上游对账
// (add-upstream skill 的「无论成功失败都必须落审计」铁律)。
func TestManyOverdueCarriesUpstreamOrderOnEveryPath(t *testing.T) {
	paths := []struct {
		name string
		body string
	}{
		{"查得 001", `{"resp_code":"SW0000","resp_msg":"查询成功","resp_order":"lgt-ok","resp_data":{"xyp_cpl0001":"1"}}`},
		{"查无 999 (SW0002)", `{"resp_code":"SW0002","resp_msg":"查询无记录","resp_order":"lgt-nf"}`},
		{"业务失败 (SW0001 认证失败)", `{"resp_code":"SW0001","resp_msg":"认证失败","resp_order":"lgt-af"}`},
		{"业务失败 (SW0042 余额不足)", `{"resp_code":"SW0042","resp_msg":"账户余额不足","resp_order":"lgt-err"}`},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			srv := manyOverdueServer(t, p.body)
			res, err := manyOverdueClient(t, srv).Query(context.Background(), manyOverdueReq())
			if err != nil {
				var be *model.UpstreamError
				if !errors.As(err, &be) {
					t.Fatalf("期望 *model.UpstreamError, 实际 %T", err)
				}
				if be.UID == "" || be.LogID == "" {
					t.Fatalf("失败路径 UID/LogID 不得为空: %+v", be)
				}
				return
			}
			if res.UID == "" || res.LogID == "" {
				t.Fatalf("成功/查无路径 UID/LogID 不得为空: %+v", res)
			}
		})
	}
}

// 文档 §3.2.3 的成功示例里绝大多数因子是空串、三个模型分是 "-1"(未命中)——这同样是
// **有效结论**, 属查得必须计费; 不得因为值大多为空就改判 999 (与 blk/snhmd 同理)。
func TestManyOverdueMostlyEmptyFactorsStillFound(t *testing.T) {
	const docExample = `{"xyp_t01aazhzz":"","xyp_t01abdzbz":"","xyp_model_score_mid":"-1","xyp_model_score_high":"-1","xyp_model_score_low":"-1","xyp_var1":"","xyp_cpl0001":""}`
	srv := manyOverdueServer(t, `{"resp_code":"SW0000","resp_msg":"查询成功","timestamp":1790141283846,"resp_order":"lgt17901412838070","resp_data":`+docExample+`}`)

	res, err := manyOverdueClient(t, srv).Query(context.Background(), manyOverdueReq())
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Code != "001" {
		t.Fatalf("因子大多为空/模型分 -1 仍是查得结论, code 应为 001, 实际 %s", res.Code)
	}
}

// resp_data 富对象整体透出: 357 个 xyp_* 因子一个都不许丢、字段名不许改写;
// 上游订单号 resp_order 只进审计, 不得出现在下游 result.range 里。
func TestManyOverdueRangeCarriesWholeRespData(t *testing.T) {
	const data = `{"xyp_cpl0001":"2","xyp_cpl0044":"1","xyp_t01aazhzz":"3","xyp_t02cchzza_cchzzz":"1","xyp_t03td148":"2","xyp_t0400002":"0.5","xyp_model_score_high":"720","xyp_var10":""}`
	srv := manyOverdueServer(t, `{"resp_code":"SW0000","resp_msg":"查询成功","resp_order":"lgt-range","resp_data":`+data+`}`)

	res, err := manyOverdueClient(t, srv).Query(context.Background(), manyOverdueReq())
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, k := range []string{
		"xyp_cpl0001", "xyp_cpl0044", "xyp_t01aazhzz", "xyp_t02cchzza_cchzzz",
		"xyp_t03td148", "xyp_t0400002", "xyp_model_score_high", "xyp_var10",
	} {
		if !strings.Contains(res.Range, `"`+k+`"`) {
			t.Fatalf("result.range 缺少字段 %q: %s", k, res.Range)
		}
	}
	if strings.Contains(res.Range, "lgt-range") {
		t.Fatalf("result.range 不应包含上游订单号: %s", res.Range)
	}
	// 上游将来补新因子无需改代码: 不做字段白名单。
	srv2 := manyOverdueServer(t, `{"resp_code":"SW0000","resp_order":"lgt-new","resp_data":{"xyp_cpl0001":"1","xyp_cpl9999":"7"}}`)
	res2, err := manyOverdueClient(t, srv2).Query(context.Background(), manyOverdueReq())
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !strings.Contains(res2.Range, `"xyp_cpl9999"`) {
		t.Fatalf("新增因子未透传 (不得做字段白名单): %s", res2.Range)
	}
}

// 文档 §3.2.1 把 resp_data 标为 String、§3.2.3 示例给的是对象——两种形态都必须认,
// 不要挑一个赌 (verify-source-doc skill 第 8 条)。timestamp 同样标 String 而示例是
// 数字, 用 RawMessage 收下不参与业务判断。
func TestManyOverdueAcceptsRespDataAsJSONString(t *testing.T) {
	srv := manyOverdueServer(t, `{"resp_code":"SW0000","resp_msg":"查询成功","timestamp":"1790141283846","resp_order":"lgt-s","resp_data":"{\"xyp_cpl0001\":\"1\",\"xyp_model_score_low\":\"-1\"}"}`)

	res, err := manyOverdueClient(t, srv).Query(context.Background(), manyOverdueReq())
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, k := range []string{"xyp_cpl0001", "xyp_model_score_low"} {
		if !strings.Contains(res.Range, `"`+k+`"`) {
			t.Fatalf("String 形态的 resp_data 未被解开, 缺 %q: %s", k, res.Range)
		}
	}
}

// Requery 未联调前返回 Reachable=false, 记录保持 PENDING 由复查/对账兜底。
func TestManyOverdueRequeryNotReachable(t *testing.T) {
	srv := manyOverdueServer(t, `{}`)
	got, err := manyOverdueClient(t, srv).Requery(context.Background(), "r1")
	if err != nil {
		t.Fatalf("Requery: %v", err)
	}
	if got.Reachable {
		t.Fatal("未联调对账接口前 Reachable 应为 false")
	}
}
