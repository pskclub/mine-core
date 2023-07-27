package utils

import (
	"crypto"
	"crypto/elliptic"
	"crypto/x509"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerifySignature(t *testing.T) {
	pub := `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEyxdC77ywDgIXL9LDaULrWsKh0LOm
mHU3Ndrxuj7xibtxvDF2h1UZi5Ms5dMo1MClTLy02yU9+xk1OcHe6jzwug==
-----END PUBLIC KEY-----`

	sig := "MEYCIQC1GFLsITdSOKni+HOuxvYrq0he42tZvlxv9IQUkWqt3AIhAJ2kTr3Jlq+fTBwy+TwmoiaTNxspgsN+w6pH7DLWdTW/"
	msg := "eyJwdWJsaWNfa2V5IjoiLS0tLS1CRUdJTiBQVUJMSUMgS0VZLS0tLS1cbk1Ga3dFd1lIS29aSXpqMENBUVlJS29aSXpqMERBUWNEUWdBRXl4ZEM3N3l3RGdJWEw5TERhVUxyV3NLaDBMT21cbm1IVTNOZHJ4dWo3eGlidHh2REYyaDFVWmk1TXM1ZE1vMU1DbFRMeTAyeVU5K3hrMU9jSGU2anp3dWc9PVxuLS0tLS1FTkQgUFVCTElDIEtFWS0tLS0tXG4iLCJub25jZSI6IjIwMjAtMDctMjMgMTY6MDQ6MDMiLCJvcGVyYXRpb24iOiJSRUdJU1RFUiJ9"
	valid, err := VerifySignature(pub, sig, msg)
	assert.NoError(t, err)
	assert.Equal(t, true, valid)
}

func TestConvertNewKey(t *testing.T) {
	keyPem := "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEyxdC77ywDgIXL9LDaULrWsKh0LOm\nmHU3Ndrxuj7xibtxvDF2h1UZi5Ms5dMo1MClTLy02yU9+xk1OcHe6jzwug==\n-----END PUBLIC KEY-----"
	keyHash := NewSha256(keyPem)
	signature := "MEUCIFrzzR4iLd4cRS/QOIYLtJ7nYGTLj1xZd6a6gcq3d8XdAiEA/ju77y0t2iCBfovjYtjOVipwZK05QtGTjS+1v/uSt9Q="
	json := map[string]interface{}{
		"key_pem":   keyPem,
		"key_hash":  keyHash,
		"key_type":  "Secp256r1VerificationKey2018",
		"signature": signature,
	}
	hex := StructToHexString(json)
	newKeyModel, err := ConvertNewKey(hex)
	assert.NoError(t, err)
	assert.Equal(t, newKeyModel.Signature, signature)
	assert.Equal(t, newKeyModel.KeyPem, keyPem)
	assert.Equal(t, newKeyModel.KeyHash, keyHash)
	assert.Equal(t, newKeyModel.KeyType, "Secp256r1VerificationKey2018")

}

func TestGenerateKeyPair(t *testing.T) {
	keypair, err := GenerateKeyPair()
	assert.NoError(t, err)
	assert.NotEmpty(t, keypair.PrivateKeyPem)
	assert.NotEmpty(t, keypair.PublicKeyPem)

	pri, err := LoadPrivateKey(keypair.PrivateKeyPem)
	assert.NoError(t, err)
	assert.NotEmpty(t, pri)

	msg := "pskclub"
	sig, err := SignMessage(pri, msg)
	assert.NotEmpty(t, sig)
	assert.NoError(t, err)

	isValid, err := VerifySignature(keypair.PublicKeyPem, sig, msg)
	assert.True(t, isValid)
	assert.NoError(t, err)

	priPem, _ := EncodeKeyPair(pri, &pri.PublicKey)
	assert.NotEmpty(t, priPem)
}

func TestLoadPrivateKey(t *testing.T) {
	pri1, err := LoadPrivateKey(`-----BEGIN PRIVATE KEY-----
MHcCAQEEIPuvnds8cfWZMqmNYmm6bPmA9Fona93l3s44l8KdvIE5oAoGCCqGSM49
AwEHoUQDQgAEDou3WDa71lvfM9J8hwz/odyntwfafZURJOAaLaIjiPHkCRaPcaS+
74WJEHha5vaCce62+uR8RZMBzw4q/eQu5g==
-----END PRIVATE KEY-----`)
	assert.NoError(t, err)
	assert.NotEmpty(t, pri1)

	pri, err := LoadPrivateKey(`-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgWgm0s3GyUk18alJN
rMLxFIuAWnAlKweC/EcLiRn50tKhRANCAAS5mdSlwlYcjuJLWVn0oPMTbvsD4fRL
kWxKj5T7mMxG3R3Lw1Ro9qT/aXJgeT9SD66fRa8UUiLivE84IF1547jv
-----END PRIVATE KEY-----
`)
	assert.Error(t, err)
	assert.Empty(t, pri)
}

func TestLoadRSAPrivateKey(t *testing.T) {
	pri1, err := LoadRSAPrivateKey(`-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAzAQTWaPkCqL0+Qwjt8HZIYDydmr0OfEprl69NmdRmMjqaOuZ
TZjP7An7C5pPpr4SGbao/oCA7JT31blz/SuReJgHIgPDZxvWA74d5kpPTXNc8nZe
thnn7GPx96J44lasHeIcN2nsD+5gBmIxP+iUNVWOmA1IV4YKUxoCrGVbCr3bTdb4
EMDJStpNUSxl1ZZeGZM/HJ8xypdIhq9nJKybSgprp6Dmxje0x1Ga/4qEd62QkvdC
lzjSuvCnUtaPcAJqB1/TTZZrgSWE+1AUsspbO/QwENh/k1jXeWOxslU6I13Zdzwu
ZtOgx4FkDRB+Gpo8gFl7VYk9Exh0Pw6zxPCYyQIDAQABAoIBAC1wtvKfS1qHIzMZ
Xhc+qOMKenafqdgB+/unhFNKveTe0z8dQp8C60SykGTsMNN9wEBT694Ltyt6npzO
2qPIAXWvvt73oZ0kmQ1qWfSCFpm0mT4z2TKMIJkJRcqXOH+JOHrLcudwwzBlMqQZ
WMjYP7KFZOY/Bx7fbwtFXtURGi8Q5h1Y3Ll1VcBKIacG28IBV8QmibNIuwzYT0aX
VJBeS6HiZW/O7Md/AdW5g9TzRN1A2Cv+SX4k10sqKxYj2hnLNUPo7DbDmzM3Qq5n
copsxRn9gkAoacma5oL9IQvps7A6gFauGbENJeHeD3DtE3simctjw9yKP4bN94sv
4vneYFECgYEA/Z8qkRH0CyRxolhZ7IVxH23VTPvTMc3Xz9myLHURWMGar/KDKy3Y
aor0lSmUDJ41ftSpQlxcJlbPW1kQvTSlreiaJjT7EXUqTXFAGFQyjA4NMKQk1HHN
SRay78xRpdV9zrEVgaz0BI4H70hpn3TdZeb8K33F9KtQCz6N/VfePe0CgYEAze3T
4Pcm+KmzAwbX7pLSl8ohNnDv8lmsuKfSihXI0r5Y6S32j3rpkfPoi07m6lPRc8hm
AG7NosNDyhFBysRYUHQJtAvuHQ2jq3hqx72bjZwHgjpNz0I5P79rXAm1OikuwnHb
QWRKpRkwMfL+zB+tSI9pJUFtK7rnUsLigDQMys0CgYA+iKM3N8UDhk3aaIxrjA9z
X2JrY+AT9JwUrKmI2qiXSM06OsJqwBgPVQmvSZwubEfmaMr/CMTT0y23aUN+U1/S
fHqdlwycplXy2EykcwYvkDtiVeaa1yB1t/oQtEEhcX1enD0gRyO3h8mfDyyz213H
hWeB1bYceKz4yMi7wZGTlQKBgQCaep2mNmGiylLCo4CatMOMIJJ3r5Mgf4rlXue+
tIbZKPezvMooajELTyiUUJVDFaubKqryCizyu35/+CAdxtrlR5b73LM8Uj4EZKnd
uuwU+AZd9/Tk1K3zl1onShUMU1aDgTvUOzMP1Oxlm/7uC5lHRGXBD+qgkm3zlMSr
HeC2xQKBgAIu0GJm0bx51Og1C+Q9dBmfq5dI8HJCZ42YgM1pbO3ZgYuwPe44+Xa9
Fvbl7UTyW+o5zXGKpgV5KQ4PAhkHQf4lfBysFBnx61QVbhvWeoV3+y3TaD/X+WAk
cRmuHwPv6GGOD0woD5RL7rur6t+wB4ktMpj3OGthiH20WsqLu1Ok
-----END RSA PRIVATE KEY-----`)
	assert.NoError(t, err)
	assert.NotEmpty(t, pri1)

	pri, err := LoadRSAPrivateKey(`-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEAzAQTWaPkCqL0+Qwjt8HZIYDydmr0OfEprl69NmdRmMjqaOuZ
TZjP7An7C5pPpr4SGbao/oCA7JT31blz/SuReJgHIgPDZxvWA74d5kpPTXNc8nZe
thnn7GPx96J44lasHeIcN2nsD+5gBmIxP+iUNVWOmA1IV4YKUxoCrGVbCr3bTdb4
EMDJStpNUSxl1ZZeGZM/HJ8xypdIhq9nJKybSgprp6Dmxje0x1Ga/4qEd62QkvdC
lzjSuvCnUtaPcAJqB1/TTZZrgSWE+1AUsspbO/QwENh/k1jXeWOxslU6I13Zdzwu
ZtOgx4FkDRB+Gpo8gFl7VYk9Exh0Pw6zxPCYyQIDAQABAoIBAC1wtvKfS1qHIzMZ
Xhc+qOMKenafqdgB+/unhFNKveTe0z8dQp8C60SykGTsMNN9wEBT694Ltyt6npzO
2qPIAXWvvt73oZ0kmQ1qWfSCFpm0mT4z2TKMIJkJRcqXOH+JOHrLcudwwzBlMqQZ
WMjYP7KFZOY/Bx7fbwtFXtURGi8Q5h1Y3Ll1VcBKIacG28IBV8QmibNIuwzYT0aX
VJBeS6HiZW/O7Md/AdW5g9TzRN1A2Cv+SX4k10sqKxYj2hnLNUPo7DbDmzM3Qq5n
copsxRn9gkAoacma5oL9IQvps7A6gFauGbENJeHeD3DtE3simctjw9yKP4bN94sv
4vneYFECgYEA/Z8qkRH0CyRxolhZ7IVxH23VTPvTMc3Xz9myLHURWMGar/KDKy3Y
aor0lSmUDJ41ftSpQlxcJlbPW1kQvTSlreiaJjT7EXUqTXFAGFQyjA4NMKQk1HHN
SRay78xRpdV9zrEVgaz0BI4H70hpn3TdZeb8K33F9KtQCz6N/VfePe0CgYEAze3T
4Pcm+KmzAwbX7pLSl8ohNnDv8lmsuKfSihXI0r5Y6S32j3rpkfPoi07m6lPRc8hm
AG7NosNDyhFBysRYUHQJtAvuHQ2jq3hqx72bjZwHgjpNz0I5P79rXAm1OikuwnHb
QWRKpRkwMfL+zB+tSI9pJUFtK7rnUsLigDQMys0CgYA+iKM3N8UDhk3aaIxrjA9z
X2JrY+AT9JwUrKmI2qiXSM06OsJqwBgPVQmvSZwubEfmaMr/CMTT0y23aUN+U1/S
fHqdlwycplXy2EykcwYvkDtiVeaa1yB1t/oQtEEhcX1enD0gRyO3h8mfDyyz213H
hWeB1bYceKz4yMi7wZGTlQKBgQCaep2mNmGiylLCo4CatMOMIJJ3r5Mgf4rlXue+
tIbZKPezvMooajELTyiUUJVDFaubKqryCizyu35/+CAdxtrlR5b73LM8Uj4EZKnd
uuwU+AZd9/Tk1K3zl1onShUMU1aDgTvUOzMP1xlm/7uC5lHRGXBD+qgkm3zlMSr
HeC2xQKBgAIu0GJm0bx51Og1C+Q9dBmfq5dI8HJCZ42YgM1pbO3ZgYuwPe44+Xa9
Fvbl7UTyW+o5zXGKpgV5KQ4PAhkHQf4lfBysFBnx61QVbhvWeoV3+y3TaD/X+WAk
cRmuHwPv6GGOD0woD5RL7rur6t+wB4ktMpj3OGthiH20WsqLu1Ok
-----END RSA PRIVATE KEY-----`)
	assert.Error(t, err)
	assert.Empty(t, pri)
}

func TestVerifySignatureFallback(t *testing.T) {
	pub := `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEyxdC77ywDgIXL9LDaULrWsKh0LOm
mHU3Ndrxuj7xibtxvDF2h1UZi5Ms5dMo1MClTLy02yU9+xk1OcHe6jzwug==
-----END PUBLIC KEY-----`

	sig := "MEYCIQC1GFLsITdSOKni+HOuxvYrq0he42tZvlxv9IQUkWqt3AIhAJ2kTr3Jlq+fTBwy+TwmoiaTNxspgsN+w6pH7DLWdTW/"
	msg := "eyJwdWJsaWNfa2V5IjoiLS0tLS1CRUdJTiBQVUJMSUMgS0VZLS0tLS1cbk1Ga3dFd1lIS29aSXpqMENBUVlJS29aSXpqMERBUWNEUWdBRXl4ZEM3N3l3RGdJWEw5TERhVUxyV3NLaDBMT21cbm1IVTNOZHJ4dWo3eGlidHh2REYyaDFVWmk1TXM1ZE1vMU1DbFRMeTAyeVU5K3hrMU9jSGU2anp3dWc9PVxuLS0tLS1FTkQgUFVCTElDIEtFWS0tLS0tXG4iLCJub25jZSI6IjIwMjAtMDctMjMgMTY6MDQ6MDMiLCJvcGVyYXRpb24iOiJSRUdJU1RFUiJ9"
	valid, err := VerifySignatureWithOption(pub, sig, msg, nil)
	assert.NoError(t, err)
	assert.Equal(t, true, valid)
	valid, err = VerifySignatureWithOption(pub, sig, msg, &VerifySignatureOption{Algorithm: x509.ECDSAWithSHA256})
	assert.NoError(t, err)
	assert.Equal(t, true, valid)
}

func TestGenerateKeyPairFallback(t *testing.T) {
	keypair, err := GenerateKeyPairWithOption(nil)
	assert.NoError(t, err)
	_, ok := keypair.(*KeyPair)
	assert.True(t, ok)
	assert.NotEmpty(t, keypair.(*KeyPair).PrivateKeyPem)
	assert.NotEmpty(t, keypair.(*KeyPair).PublicKeyPem)

	pri, err := LoadPrivateKey(keypair.(*KeyPair).PrivateKeyPem)
	assert.NoError(t, err)
	assert.NotEmpty(t, pri)

	msg := "foobar"
	sig, err := SignMessage(pri, msg)
	assert.NotEmpty(t, sig)
	assert.NoError(t, err)

	isValid, err := VerifySignature(keypair.(*KeyPair).PublicKeyPem, sig, msg)
	assert.True(t, isValid)
	assert.NoError(t, err)

	sig, err = SignMessageWithOption(pri, msg, &SignMessageOption{Algorithm: x509.ECDSAWithSHA256})
	assert.NotEmpty(t, sig)
	assert.NoError(t, err)

	isValid, err = VerifySignatureWithOption(keypair.(*KeyPair).PublicKeyPem, sig, msg, &VerifySignatureOption{Algorithm: x509.ECDSAWithSHA256})
	assert.True(t, isValid)
	assert.NoError(t, err)

	priPem, _ := EncodeKeyPair(pri, &pri.PublicKey)
	assert.NotEmpty(t, priPem)
}

func TestGenerateKeyPairRSAWithSHA256(t *testing.T) {
	keypair, err := GenerateKeyPairWithOption(&GenerateKeyPairOption{Algorithm: x509.SHA256WithRSA})
	assert.NoError(t, err)
	_, ok := keypair.(*RSAKeyPair)
	assert.True(t, ok)
	assert.NotEmpty(t, keypair.(*RSAKeyPair).PrivateKeyPem)
	assert.NotEmpty(t, keypair.(*RSAKeyPair).PublicKeyPem)

	pri, err := LoadRSAPrivateKey(keypair.(*RSAKeyPair).PrivateKeyPem)
	assert.NoError(t, err)
	assert.NotEmpty(t, pri)

	msg := "foobar"
	sig, err := SignMessageWithOption(pri, msg, &SignMessageOption{Algorithm: x509.SHA256WithRSA})
	assert.NotEmpty(t, sig)
	assert.NoError(t, err)

	isValid, err := VerifySignatureWithOption(keypair.(*RSAKeyPair).PublicKeyPem, sig, msg, &VerifySignatureOption{Algorithm: x509.SHA256WithRSA})
	assert.True(t, isValid)
	assert.NoError(t, err)

	sig, err = SignMessageWithOption(pri, msg, &SignMessageOption{Algorithm: x509.SHA256WithRSA})
	assert.NotEmpty(t, sig)
	assert.NoError(t, err)

	isValid, err = VerifySignatureWithOption(keypair.(*RSAKeyPair).PublicKeyPem, sig, msg, &VerifySignatureOption{Algorithm: x509.SHA256WithRSA})
	assert.True(t, isValid)
	assert.NoError(t, err)

	priPem, _ := EncodeRSAKeyPair(pri, &pri.PublicKey)
	assert.NotEmpty(t, priPem)
}

func TestEncryptAndDecryptMessageSuccess(t *testing.T) {
	keypair, err := GenerateKeyPairWithOption(&GenerateKeyPairOption{Algorithm: x509.SHA512WithRSA})
	assert.NoError(t, err)
	_, ok := keypair.(*RSAKeyPair)
	assert.True(t, ok)
	assert.NotEmpty(t, keypair.(*RSAKeyPair).PrivateKeyPem)
	assert.NotEmpty(t, keypair.(*RSAKeyPair).PublicKeyPem)

	pri, err := LoadRSAPrivateKey(keypair.(*RSAKeyPair).PrivateKeyPem)
	assert.NoError(t, err)
	assert.NotEmpty(t, pri)

	message := "foobar"
	cipherText, err := EncryptMessage([]byte(message), &pri.PublicKey, &MessageEncryptionOptions{HashingAlgorithm: crypto.SHA512})
	assert.NotEmpty(t, cipherText)
	assert.NoError(t, err)
	assert.NotEqual(t, message, string(cipherText))

	decryptedMessage, err := DecryptCipherText(cipherText, pri, &MessageEncryptionOptions{HashingAlgorithm: crypto.SHA512})
	assert.Equal(t, message, string(decryptedMessage))
	assert.NoError(t, err)
}

func TestEncryptAndDecryptMessageWrongPrivateKey(t *testing.T) {
	keypair, err := GenerateKeyPairWithOption(&GenerateKeyPairOption{Algorithm: x509.SHA512WithRSA})
	assert.NoError(t, err)
	_, ok := keypair.(*RSAKeyPair)
	assert.True(t, ok)
	assert.NotEmpty(t, keypair.(*RSAKeyPair).PrivateKeyPem)
	assert.NotEmpty(t, keypair.(*RSAKeyPair).PublicKeyPem)

	pri, err := LoadRSAPrivateKey(keypair.(*RSAKeyPair).PrivateKeyPem)
	assert.NoError(t, err)
	assert.NotEmpty(t, pri)

	message := "foobar"
	cipherText, err := EncryptMessage([]byte(message), &pri.PublicKey, &MessageEncryptionOptions{HashingAlgorithm: crypto.SHA512})
	assert.NotEmpty(t, cipherText)
	assert.NoError(t, err)
	assert.NotEqual(t, message, string(cipherText))

	newKeypair, err := GenerateKeyPairWithOption(&GenerateKeyPairOption{Algorithm: x509.SHA512WithRSA})
	assert.NoError(t, err)
	_, ok = newKeypair.(*RSAKeyPair)
	assert.True(t, ok)
	assert.NotEmpty(t, newKeypair.(*RSAKeyPair).PrivateKeyPem)
	assert.NotEmpty(t, newKeypair.(*RSAKeyPair).PublicKeyPem)

	newPri, err := LoadRSAPrivateKey(newKeypair.(*RSAKeyPair).PrivateKeyPem)
	assert.NoError(t, err)
	assert.NotEmpty(t, newPri)

	decryptedMessage, err := DecryptCipherText(cipherText, newPri, &MessageEncryptionOptions{HashingAlgorithm: crypto.SHA512})
	assert.Empty(t, decryptedMessage)
	assert.Error(t, err)
}

func TestEncryptMessageUnsupportedAlgorithm(t *testing.T) {
	keypair, err := GenerateKeyPairWithOption(&GenerateKeyPairOption{Algorithm: x509.SHA384WithRSA})
	assert.NoError(t, err)
	_, ok := keypair.(*RSAKeyPair)
	assert.True(t, ok)
	assert.NotEmpty(t, keypair.(*RSAKeyPair).PrivateKeyPem)
	assert.NotEmpty(t, keypair.(*RSAKeyPair).PublicKeyPem)

	pri, err := LoadRSAPrivateKey(keypair.(*RSAKeyPair).PrivateKeyPem)
	assert.NoError(t, err)
	assert.NotEmpty(t, pri)

	message := "foobar"
	cipherText, err := EncryptMessage([]byte(message), &pri.PublicKey, &MessageEncryptionOptions{HashingAlgorithm: crypto.SHA384})
	assert.Empty(t, cipherText)
	assert.Error(t, err)
}

func TestCreateECDSAPublicKeySuccess(t *testing.T) {
	x, _ := new(big.Int).SetString("9C3035331E4D16A7099F7D5B31D01E82681163E7DE2044E0DAA1004398E7E809", 16)
	y, _ := new(big.Int).SetString("6E7AD5E8C4C188192D3DD2FEC66F933BDAAFC105DF1366048E33E6F0D94AFE45", 16)
	pub, err := CreateECDSAPublicKey(elliptic.P256(), x, y)
	assert.NotNil(t, pub)
	assert.NoError(t, err)
}

func TestCreateECDSAPublicKeyNotOnCurve(t *testing.T) {
	x, _ := new(big.Int).SetString("9C3F35331E4D16A7099F7D5B31D01E82681163E7DE2044E0DAA1004398E7E809", 16)
	y, _ := new(big.Int).SetString("6E7AD5E8C4C188192D3DD2FEC66F933BDAAFC105DF1366048E33E6F0D94AFE45", 16)
	pub, err := CreateECDSAPublicKey(elliptic.P256(), x, y)
	assert.Nil(t, pub)
	assert.Error(t, err)
}

func TestCreateECDSAPrivateKeySuccess(t *testing.T) {
	D, valid := GetBigNumber("0d108075042943316490911572303885890032968976721168446735305857462087742624235540")
	assert.True(t, valid)
	X, valid := GetBigNumber("0d15480682461565412640681234001009019896662077234523728604211785737309528786925")
	assert.True(t, valid)
	Y, valid := GetBigNumber("0d10935117971294716762606342076334883851566038346846479346072690385799108834595")
	assert.True(t, valid)
	privateKey, err := CreateECDSAPrivateKey(D, elliptic.P256())
	assert.NotEmpty(t, privateKey)
	assert.NoError(t, err)
	assert.Equal(t, X, privateKey.PublicKey.X)
	assert.Equal(t, Y, privateKey.PublicKey.Y)
}
