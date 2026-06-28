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
