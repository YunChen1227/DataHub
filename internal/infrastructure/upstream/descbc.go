package upstream

import (
	"crypto/cipher"
	"crypto/des" //nolint:gosec // 上游灵犀分契约指定 DES，非我方选型
	"encoding/hex"
	"fmt"
	"strings"
)

// DES/CBC/PKCS5Padding 工具，供灵犀分 (lxscore) 上游计算 sign 与解密 data。
//
// 上游文档 (docs/灵犀分-score_195_v1-接口文档.pdf §2.5 调用示例) 只写
// `sign = DesCbcSecurity.encrypt(signStr, encryptKey)`，未给出 IV / 填充 / 输出编码。
// 编码形态由文档自带的返回示例反推确定，非臆测：
//   明文 `{"score_195_v1": "600"}` 恰为 23 字节 → PKCS5 补齐 24 字节 → DES 密文
//   24 字节 → 文档示例 data "10574F8E…09089C58" 恰为 48 个大写十六进制字符。
// 因此：DES/CBC/PKCS5Padding，密文以**大写 hex** 表示，sign 与 data 同一套算法。
//
// IV 取密钥本身——DesCbcSecurity 这类工具类最常见的约定，文档未明示。若联调报
// 「认证失败(2031208)」而参数确认无误，只需改 desIV 一处即可。

// desIV 返回 CBC 初始向量（约定取密钥本身，见上方说明）。
func desIV(key []byte) []byte { return key }

// desKeyBytes 把配置里的 encryptKey 规整为 DES 要求的 8 字节密钥。
// 凭证的编码形态必须验算而非想当然：上游以邮件下发 encryptKey，可能是 8 个 ASCII
// 字符，也可能是 16 个十六进制字符（= 8 字节）。两种都识别；其余长度一律显式报错，
// 禁止截断/补零之类的静默降级（静默降级会让线上"一个请求都发不出去"却只留加密日志）。
func desKeyBytes(encryptKey string) ([]byte, error) {
	k := strings.TrimSpace(encryptKey)
	switch {
	case len(k) == des.BlockSize: // 8 个 ASCII 字符
		return []byte(k), nil
	case len(k) == 2*des.BlockSize: // 16 个 hex 字符
		if b, err := hex.DecodeString(k); err == nil {
			return b, nil
		}
		return nil, fmt.Errorf("encryptKey 长度 16 但不是合法 hex")
	default:
		return nil, fmt.Errorf("encryptKey 长度非法 (%d)，DES 密钥须为 8 个字符或 16 个 hex 字符", len(k))
	}
}

// desEncryptHex 以 DES/CBC/PKCS5Padding 加密并返回大写 hex 密文。
func desEncryptHex(plain []byte, key []byte) (string, error) {
	block, err := des.NewCipher(key) //nolint:gosec // 上游契约指定
	if err != nil {
		return "", fmt.Errorf("des new cipher: %w", err)
	}
	padded := pkcs5Pad(plain, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, desIV(key)).CryptBlocks(out, padded)
	return strings.ToUpper(hex.EncodeToString(out)), nil
}

// desDecryptHex 解开 DES/CBC/PKCS5Padding 的 hex 密文（大小写均接受）。
func desDecryptHex(cipherHex string, key []byte) ([]byte, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(cipherHex))
	if err != nil {
		return nil, fmt.Errorf("hex decode: %w", err)
	}
	block, err := des.NewCipher(key) //nolint:gosec // 上游契约指定
	if err != nil {
		return nil, fmt.Errorf("des new cipher: %w", err)
	}
	bs := block.BlockSize()
	if len(raw) == 0 || len(raw)%bs != 0 {
		return nil, fmt.Errorf("密文长度 %d 不是 %d 的整数倍", len(raw), bs)
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, desIV(key)).CryptBlocks(out, raw)
	// PKCS5 与 PKCS7 在 8 字节块上等价，复用 aesecb.go 的填充助手。
	return pkcs5Unpad(out, bs)
}
