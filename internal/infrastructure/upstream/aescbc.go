package upstream

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// AES/CBC/PKCS5Padding + Base64 工具，供背景评估 (bgpg) 上游加密 data 入参、解密
// data 出参。与 aesecb.go (ECB) / descbc.go (DES) 并列；PKCS5 填充复用 pkcs5Pad /
// pkcs5Unpad（AES 分组为 16 字节时 PKCS5 与 PKCS7 等价）。

// aesCBCEncryptBase64 以 AES/CBC/PKCS5Padding 加密并返回 Base64(std) 密文。
func aesCBCEncryptBase64(plaintext, key, iv []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes new cipher (key len=%d): %w", len(key), err)
	}
	if len(iv) != block.BlockSize() {
		return "", fmt.Errorf("iv 长度 %d 不等于块大小 %d", len(iv), block.BlockSize())
	}
	padded := pkcs5Pad(plaintext, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out), nil
}

// aesCBCDecryptBase64 是 aesCBCEncryptBase64 的逆运算：Base64 解码后 AES/CBC 解密
// 并去除 PKCS5 填充。
func aesCBCDecryptBase64(cipherB64 string, key, iv []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes new cipher (key len=%d): %w", len(key), err)
	}
	bs := block.BlockSize()
	if len(iv) != bs {
		return nil, fmt.Errorf("iv 长度 %d 不等于块大小 %d", len(iv), bs)
	}
	if len(raw) == 0 || len(raw)%bs != 0 {
		return nil, fmt.Errorf("密文长度 %d 不是块大小 %d 的整数倍", len(raw), bs)
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, raw)
	return pkcs5Unpad(out, bs)
}

// aesKeyFromHex 把 hex 字符串形态的密钥解码成 AES 密钥字节串并校验长度。上游下发的
// encryptKey 是 hex 文本（官方 demo 用 Hex.decodeHex(key.toCharArray()) 取密钥），
// 32/48/64 个 hex 字符分别对应 AES-128/192/256——**不是**把字符串本身当密钥字节，
// 直接 []byte() 会得到 invalid key size。长度不合法立即报错，不做静默降级。
func aesKeyFromHex(hexKey string) ([]byte, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("密钥非 hex 文本 (%d 字符): %w", len(hexKey), err)
	}
	switch len(key) {
	case 16, 24, 32:
		return key, nil
	default:
		return nil, fmt.Errorf("密钥 hex 解码后 %d 字节, 仅支持 16/24/32 (AES-128/192/256)", len(key))
	}
}
