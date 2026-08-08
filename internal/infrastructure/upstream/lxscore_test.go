package upstream

import (
	"strings"
	"testing"
)

// 灵犀分 encryptKey 由上游邮件下发，编码形态不确定：8 个 ASCII 字符或 16 个 hex
// 字符都可能。必须显式验算，非法长度要报错而不是静默截断（静默降级会让线上
// "一个请求都发不出去"却只在日志里留加密失败）。
func TestDesKeyBytes(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want int // 期望字节数；0 表示期望报错
	}{
		{"8 个 ASCII 字符", "abcdefgh", 8},
		{"16 个 hex 字符", "0123456789abcdef", 8},
		{"两侧空白应被裁掉", "  abcdefgh  ", 8},
		{"16 位但非 hex", "abcdefghijklmnop", 0},
		{"长度不足", "abc", 0},
		{"空串", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := desKeyBytes(tc.key)
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

func TestDesCbcRoundTrip(t *testing.T) {
	key, err := desKeyBytes("abcdefgh")
	if err != nil {
		t.Fatalf("desKeyBytes: %v", err)
	}
	plain := `{"score_195_v1": "600"}`
	enc, err := desEncryptHex([]byte(plain), key)
	if err != nil {
		t.Fatalf("desEncryptHex: %v", err)
	}
	if enc != strings.ToUpper(enc) {
		t.Fatalf("密文应为大写 hex: %s", enc)
	}
	// 文档 §2.5 返回示例的 data 是 48 个 hex 字符
	// ("10574F8E489F1E58EA6EB71FB4934198C3A5E19009089C58")。本用例锁定这一推断：
	// 23 字节明文 → PKCS5 补齐 24 字节 → DES 密文 24 字节 → 48 个 hex 字符。
	// 若此断言失败，说明填充或输出编码的判断被改坏了。
	if len(enc) != 48 {
		t.Fatalf("密文 hex 长度 = %d, 期望 48 (与文档返回示例一致)", len(enc))
	}
	dec, err := desDecryptHex(enc, key)
	if err != nil {
		t.Fatalf("desDecryptHex: %v", err)
	}
	if string(dec) != plain {
		t.Fatalf("往返结果 = %q, 期望 %q", dec, plain)
	}
	// 大小写不敏感地接受密文
	if _, err := desDecryptHex(strings.ToLower(enc), key); err != nil {
		t.Fatalf("小写 hex 密文应可解密: %v", err)
	}
}

// 待签名串必须按文档 §2.2 参数表的固定字段顺序拼接（customerId, customerProdId,
// customerRequestId, name, mobile, idCardNo, timestamp），而非 ASCII 升序。
// 经直连上游联调验证：升序拼串会被判 2031208 签名验证失败，文档顺序才通过。
func TestLXScoreSignStr(t *testing.T) {
	got := lxScoreSignStr(map[string]string{
		"customerId":        "cid",
		"customerProdId":    "pid",
		"customerRequestId": "rid",
		"name":              "n",
		"mobile":            "m",
		"idCardNo":          "i",
		"timestamp":         "1682391550719",
	})
	want := "customerId=cid&customerProdId=pid&customerRequestId=rid" +
		"&name=n&mobile=m&idCardNo=i&timestamp=1682391550719"
	if got != want {
		t.Fatalf("signStr =\n  %s\n期望\n  %s", got, want)
	}
	if !strings.HasPrefix(got, "customerId=cid&customerProdId=pid&customerRequestId=rid&") {
		t.Fatalf("开头与文档示例不符: %s", got)
	}
}

// 文档 §2.2 给出的姓名缺省值必须等于 MD5("")，据此可确认 PII 摘要取的是 MD5
// 而非 SHA256。
func TestLXScoreEmptyNameDefaultIsMD5OfEmpty(t *testing.T) {
	if got := md5Hex(""); got != lxScoreEmptyNameMD5 {
		t.Fatalf("md5(\"\") = %s, 期望文档默认值 %s", got, lxScoreEmptyNameMD5)
	}
}
