package upstream

import (
	"crypto/des" //nolint:gosec // 上游收入A_g版 (grgjj) 契约指定 3DES，非我方选型
	"encoding/base64"
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
