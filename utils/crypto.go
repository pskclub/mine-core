package utils

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

func NewSha256(str string) string {
	hash := sha256.Sum256([]byte(str))
	return fmt.Sprintf("%x", hash[:])
}

func GetMD5Hash(text string) string {
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}

func BytesToString(data []byte) string {
	return string(data[:])
}

func StringToBytes(data string) []byte {
	return []byte(data)
}

func HexToBytesString(data string) ([]byte, error) {
	return hex.DecodeString(data)
}
func StructToHexString(s interface{}) string {
	j := JSONToString(s)
	return hex.EncodeToString([]byte(j))
}

func BytesToHexString(b []byte) string {
	return hex.EncodeToString(b)
}

func Base64Decode(_base64 string) (string, error) {
	message, err := base64.StdEncoding.DecodeString(_base64)
	if err != nil {
		return "", err
	}

	return BytesToString(message), nil
}

func Base64Encode(str string) string {
	return base64.StdEncoding.EncodeToString(StringToBytes(str))
}
