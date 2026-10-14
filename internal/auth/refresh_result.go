package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// Cache bearer credentials encrypted with a domain-separated key shared by all instances.
func refreshResultCipher() (cipher.AEAD, error) {
	if len(jwtSecret) == 0 {
		return nil, errors.New("jwt secret not initialized")
	}
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte("go-chat/refresh-result/v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SealRefreshResult encrypts a token pair and binds it to its refresh operation.
func SealRefreshResult(plaintext []byte, binding string) (string, error) {
	aead, err := refreshResultCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plaintext, []byte(binding))), nil
}

// OpenRefreshResult authenticates the operation binding before decrypting.
func OpenRefreshResult(sealed, binding string) ([]byte, error) {
	aead, err := refreshResultCipher()
	if err != nil {
		return nil, err
	}
	data, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	if len(data) < aead.NonceSize() {
		return nil, errors.New("invalid encrypted refresh result")
	}
	return aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(binding))
}
