package upstream

import (
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"fmt"
)

// aesECBEncryptBase64 encrypts plaintext with AES/ECB/PKCS5Padding and returns
// the Base64 (std) encoding of the ciphertext. 租赁分V2-D 上游约定: 分组模式 ECB,
// 填充 PKCS5Padding (== PKCS7, block=16)。Go 标准库不提供 ECB 模式, 故手写按块加密。
func aesECBEncryptBase64(plaintext, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes new cipher (key len=%d): %w", len(key), err)
	}
	bs := block.BlockSize()
	padded := pkcs5Pad(plaintext, bs)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += bs {
		block.Encrypt(out[i:i+bs], padded[i:i+bs])
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// pkcs5Pad appends PKCS5/PKCS7 padding so the data length is a multiple of
// blockSize. 当数据本身是块整数倍时, 仍追加一整块填充 (与 Java PKCS5Padding 一致)。
func pkcs5Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

// aesECBDecryptBase64 is the inverse of aesECBEncryptBase64: Base64 解码后按块
// AES/ECB 解密并去除 PKCS5 填充。rental 等上游应答解密复用。
func aesECBDecryptBase64(cipherB64 string, key []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes new cipher (key len=%d): %w", len(key), err)
	}
	bs := block.BlockSize()
	if len(raw) == 0 || len(raw)%bs != 0 {
		return nil, fmt.Errorf("密文长度 %d 不是块大小 %d 的整数倍", len(raw), bs)
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i += bs {
		block.Decrypt(out[i:i+bs], raw[i:i+bs])
	}
	return pkcs5Unpad(out, bs)
}

// pkcs5Unpad strips PKCS5/PKCS7 padding, validating the pad bytes.
func pkcs5Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("空明文")
	}
	pad := int(data[len(data)-1])
	if pad <= 0 || pad > blockSize || pad > len(data) {
		return nil, fmt.Errorf("非法填充长度 %d", pad)
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, fmt.Errorf("填充字节不一致")
		}
	}
	return data[:len(data)-pad], nil
}
