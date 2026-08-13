package upstream

import (
	"crypto/des" //nolint:gosec // 上游收入A_g版 (grgjj) 契约指定 3DES，非我方选型
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// 3DES(DESede)/ECB/PKCS5Padding 工具，供收入A_g版 (grgjj / incomeag) 上游加密请求
// data 字段与解密响应 result 字段。算法形态**非臆测**，直接对齐上游官方 demo
// (yrzx_common_demo/ThreeDesUtil.java)：
//   - KEY_ALGORITHM   = "DESede"
//   - CIPHER_ALGORITHM= "DESede/ECB/PKCS5Padding"
//   - 密钥 = Base64.decodeBase64(密钥) 后交给 DESedeKeySpec —— 后者只接受 24 字节
//     (kg.init(168) 生成的三密钥形态)；16 字节的双密钥形态 Java DESedeKeySpec 不收。
//   - data   = Base64( 3DES( 加密前 JSON 串 ) )               (加密版本.java encMap)
//   - result = 3DES 解密( Base64.decode( 响应 result ) )        (加密版本.java 解密段)
// ECB 无 IV；Go 标准库不提供 ECB 模式，故按块手工加解密 (同 aesecb.go 的写法)。

// tripleDESKey 把配置里的 3DES 密钥 (Base64 编码) 解码为 24 字节 DESede 密钥。
// 凭证编码形态必须验算而非想当然：上游以 Base64 字符串下发密钥，解码后必须恰为
// 24 字节，否则显式报错（禁止截断/补零之类静默降级——静默降级会让线上"一个请求
// 都发不出去"却只留加密失败日志）。
func tripleDESKey(secB64 string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(secB64))
	if err != nil {
		return nil, fmt.Errorf("3des 密钥 Base64 解码失败: %w", err)
	}
	if len(k) != 24 {
		return nil, fmt.Errorf("3des 密钥长度非法 (Base64 解码后 %d 字节)，DESede 须为 24 字节", len(k))
	}
	return k, nil
}

// tripleDESKeyFlexible 把「获取秘钥」接口下发的 result.key 归一为 24 字节 DESede
// 密钥。上游文档没有明确 key 的编码形态（只说"采用 3des+base64 加密方式"），联调
// 前无法确定，故按 skill「凭证编码形态必须验算而非想当然」的要求，依次尝试三种最
// 常见形态并只接受能得到恰好 24 字节的那一种：
//   - 原始 24 个 ASCII 字符（直接当密钥字节）；
//   - Base64 编码（解码后 24 字节）；
//   - Hex 编码（解码后 24 字节，即 48 个十六进制字符）。
// 三种都不成立则显式报错（禁止截断/补零静默降级）。真实 key 形态一旦联调确认，可
// 把此函数收敛为唯一形态。
func tripleDESKeyFlexible(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("3des 密钥为空")
	}
	if len(s) == 24 {
		return []byte(s), nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 24 {
		return b, nil
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == 24 {
		return b, nil
	}
	return nil, fmt.Errorf("3des 密钥无法归一为 24 字节 DESede 密钥 (原始长度 %d，既非 24 字节原文/也非 Base64/Hex(24))", len(s))
}

// desedeExpand 把 8/16 字节密钥料补足为 24 字节 DESede：16→K1K2K1、8→K1K1K1、24 原样。
// Java 常见做法：商户下发 16 字节双密钥，DESede 需 24 字节，取前 8 字节补到末尾。
func desedeExpand(b []byte) ([]byte, error) {
	switch len(b) {
	case 24:
		return b, nil
	case 16:
		out := make([]byte, 24)
		copy(out, b)
		copy(out[16:], b[:8]) // K1K2K1
		return out, nil
	case 8:
		out := make([]byte, 24)
		copy(out, b)
		copy(out[8:], b)
		copy(out[16:], b)
		return out, nil
	default:
		return nil, fmt.Errorf("密钥料 %d 字节，无法补足为 8/16/24→24 字节 DESede", len(b))
	}
}

// merchantDESedeKey 由商户 key（ASCII 字符串）得到解密「获取秘钥」下发密文所用的 24
// 字节 DESede 密钥。商户 key 常为 16 字符（如 NO43H7l6R58c918B）→ 按 K1K2K1 补 24。
func merchantDESedeKey(signKey string) ([]byte, error) {
	return desedeExpand([]byte(signKey))
}

// deriveSessionKey 解出「获取秘钥」接口下发的动态 3DES 会话密钥。ShowDoc 明确「获取
// 密钥……采用 3des+base64 加密方式」——即 result.key = Base64(3DES/ECB/PKCS5(真实密钥))，
// 加密密钥为商户 key。故先用商户 key 解密 result.key 得到真实会话密钥；若解密/去填充
// 失败（说明 result.key 实为明文密钥），退回按明文（原文/Base64/Hex 的 24 字节）归一。
func deriveSessionKey(resultKey, signKey string) ([]byte, error) {
	resultKey = strings.TrimSpace(resultKey)
	if resultKey == "" {
		return nil, fmt.Errorf("result.key 为空")
	}
	if mk, err := merchantDESedeKey(signKey); err == nil {
		if plain, derr := tripleDESDecryptBase64Key(resultKey, mk); derr == nil {
			// 解密成功：明文可能是 24 字节原始密钥、16/8 字节需补齐、或再套一层 Base64/Hex。
			if k, kerr := desedeExpand(plain); kerr == nil {
				return k, nil
			}
			if k, kerr := tripleDESKeyFlexible(strings.TrimSpace(string(plain))); kerr == nil {
				return k, nil
			}
		}
	}
	// 回退：result.key 本身即明文密钥。
	return tripleDESKeyFlexible(resultKey)
}

// tripleDESEncryptBase64Key/tripleDESDecryptBase64Key 与上面的 *Base64 版本等价，
// 区别是直接接收已解好的 24 字节密钥（供动态获取的秘钥复用，避免再做一次 Base64
// 往返）。
func tripleDESEncryptBase64Key(plain, key []byte) (string, error) {
	block, err := des.NewTripleDESCipher(key) //nolint:gosec // 上游契约指定
	if err != nil {
		return "", fmt.Errorf("3des new cipher: %w", err)
	}
	bs := block.BlockSize()
	padded := pkcs5Pad(plain, bs)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += bs {
		block.Encrypt(out[i:i+bs], padded[i:i+bs])
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

func tripleDESDecryptBase64Key(cipherB64 string, key []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cipherB64))
	if err != nil {
		return nil, fmt.Errorf("result base64 decode: %w", err)
	}
	block, err := des.NewTripleDESCipher(key) //nolint:gosec // 上游契约指定
	if err != nil {
		return nil, fmt.Errorf("3des new cipher: %w", err)
	}
	bs := block.BlockSize()
	if len(raw) == 0 || len(raw)%bs != 0 {
		return nil, fmt.Errorf("result 密文长度 %d 不是块大小 %d 的整数倍", len(raw), bs)
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i += bs {
		block.Decrypt(out[i:i+bs], raw[i:i+bs])
	}
	return pkcs5Unpad(out, bs)
}

// tripleDESEncryptBase64 以 DESede/ECB/PKCS5Padding 加密并返回 Base64(std) 密文，
// 对齐上游 demo ThreeDesUtil.encrypt + Base64.encodeBase64String。
func tripleDESEncryptBase64(plain []byte, secB64 string) (string, error) {
	key, err := tripleDESKey(secB64)
	if err != nil {
		return "", err
	}
	block, err := des.NewTripleDESCipher(key) //nolint:gosec // 上游契约指定
	if err != nil {
		return "", fmt.Errorf("3des new cipher: %w", err)
	}
	bs := block.BlockSize()
	padded := pkcs5Pad(plain, bs) // 复用 aesecb.go 的 PKCS5/PKCS7 填充助手
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += bs {
		block.Encrypt(out[i:i+bs], padded[i:i+bs])
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// tripleDESDecryptBase64 是上面的逆操作：Base64 解码后按块 DESede/ECB 解密并去除
// PKCS5 填充。用于解开上游响应的 result 密文 (对齐 demo 加密版本.java 的
// ThreeDesUtil.decrypt(Base64.decodeBase64(result), 密钥))。
func tripleDESDecryptBase64(cipherB64 string, secB64 string) ([]byte, error) {
	key, err := tripleDESKey(secB64)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cipherB64))
	if err != nil {
		return nil, fmt.Errorf("result base64 decode: %w", err)
	}
	block, err := des.NewTripleDESCipher(key) //nolint:gosec // 上游契约指定
	if err != nil {
		return nil, fmt.Errorf("3des new cipher: %w", err)
	}
	bs := block.BlockSize()
	if len(raw) == 0 || len(raw)%bs != 0 {
		return nil, fmt.Errorf("result 密文长度 %d 不是块大小 %d 的整数倍", len(raw), bs)
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i += bs {
		block.Decrypt(out[i:i+bs], raw[i:i+bs])
	}
	return pkcs5Unpad(out, bs) // 复用 aesecb.go 的去填充助手
}
