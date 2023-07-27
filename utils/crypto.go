package utils

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"golang.org/x/crypto/pbkdf2"
	"math/big"
	"strconv"
	"strings"
)

type RSAKeySize int

const (
	EncryptRSA2048Bits RSAKeySize = iota
	EncryptRSA4096Bits
)

var rsaKeySizeMapper = map[RSAKeySize]int{
	EncryptRSA2048Bits: 2048,
	EncryptRSA4096Bits: 4096,
}

type ECDSASignature struct {
	R, S *big.Int
}

func hash(b []byte) []byte {
	h := sha256.New()
	// hash the body bytes
	h.Write(b)
	// compute the SHA256 hash
	return h.Sum(nil)
}

func hash384(b []byte) []byte {
	h := sha512.New384()
	// hash the body bytes
	h.Write(b)
	// compute the SHA256 hash
	return h.Sum(nil)
}

func hash512(b []byte) []byte {
	h := sha512.New()
	// hash the body bytes
	h.Write(b)
	// compute the SHA256 hash
	return h.Sum(nil)
}

func NewSha256(str string) string {
	hash := sha256.Sum256([]byte(str))
	return fmt.Sprintf("%x", hash[:])
}

func NewSha384(str string) string {
	hash := sha512.Sum384([]byte(str))
	return fmt.Sprintf("%x", hash[:])
}

func NewSha512(str string) string {
	hash := sha512.Sum512([]byte(str))
	return fmt.Sprintf("%x", hash[:])
}

type KeyPair struct {
	PublicKeyPem  string
	PrivateKeyPem string
	PrivateKey    *ecdsa.PrivateKey
}

type RSAKeyPair struct {
	PublicKeyPem  string
	PrivateKeyPem string
	PrivateKey    *rsa.PrivateKey
}

func EncodeKeyPair(privateKey *ecdsa.PrivateKey, publicKey *ecdsa.PublicKey) (string, string) {
	x509Encoded, _ := x509.MarshalECPrivateKey(privateKey)
	pemEncoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509Encoded})

	x509EncodedPub, _ := x509.MarshalPKIXPublicKey(publicKey)
	pemEncodedPub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: x509EncodedPub})

	return string(pemEncoded), string(pemEncodedPub)
}

func EncodeRSAKeyPair(privateKey *rsa.PrivateKey, publicKey *rsa.PublicKey) (string, string) {
	x509Encoded := x509.MarshalPKCS1PrivateKey(privateKey)
	pemEncoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509Encoded})

	x509EncodedPub := x509.MarshalPKCS1PublicKey(publicKey)
	pemEncodedPub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: x509EncodedPub})

	return string(pemEncoded), string(pemEncodedPub)
}

func generateECDSAKeyPair(curve elliptic.Curve) (*KeyPair, error) {
	akey, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		return nil, err
	}

	pri, pub := EncodeKeyPair(akey, &akey.PublicKey)

	return &KeyPair{
		PublicKeyPem:  pub,
		PrivateKeyPem: pri,
		PrivateKey:    akey,
	}, nil
}

func generateRSAKeyPair(size RSAKeySize) (*RSAKeyPair, error) {
	keySize, ok := rsaKeySizeMapper[size]
	if !ok {
		keySize = rsaKeySizeMapper[EncryptRSA2048Bits]
	}
	akey, err := rsa.GenerateKey(rand.Reader, keySize)
	if err != nil {
		return nil, err
	}

	pri, pub := EncodeRSAKeyPair(akey, &akey.PublicKey)

	return &RSAKeyPair{
		PublicKeyPem:  pub,
		PrivateKeyPem: pri,
		PrivateKey:    akey,
	}, nil
}

func GenerateKeyPair() (*KeyPair, error) {
	return generateECDSAKeyPair(elliptic.P256())
}

type GenerateKeyPairOption struct {
	Algorithm x509.SignatureAlgorithm
	KeySize   RSAKeySize
}

func CreateECDSAPublicKey(curve elliptic.Curve, x *big.Int, y *big.Int) (*ecdsa.PublicKey, error) {
	pub := &ecdsa.PublicKey{}
	pub.Curve = curve
	pub.X = x
	pub.Y = y

	if !pub.IsOnCurve(x, y) {
		return nil, fmt.Errorf("coordiate (x = %s, y = %s) is not on curve %s", pub.X.String(), pub.Y.String(), curve)
	}

	return pub, nil
}

func CreateECDSAPrivateKey(D *big.Int, curve elliptic.Curve) (*ecdsa.PrivateKey, error) {
	privateKey := &ecdsa.PrivateKey{}
	privateKey.D = D
	privateKey.PublicKey.Curve = curve
	privateKey.PublicKey.X, privateKey.PublicKey.Y = privateKey.PublicKey.Curve.ScalarBaseMult(privateKey.D.Bytes())
	if !privateKey.IsOnCurve(privateKey.X, privateKey.Y) {
		return nil, fmt.Errorf("coordiate (x = %s, y = %s) is not on curve %s", privateKey.X.String(), privateKey.Y.String(), curve)
	}
	return privateKey, nil
}

func GenerateKeyPairWithOption(option *GenerateKeyPairOption) (interface{}, error) {
	if option == nil {
		option = &GenerateKeyPairOption{Algorithm: x509.ECDSAWithSHA256}
	}

	switch option.Algorithm {
	case x509.ECDSAWithSHA256, x509.ECDSAWithSHA384, x509.ECDSAWithSHA512:
		return GenerateKeyPair()
	case x509.SHA256WithRSA, x509.SHA384WithRSA, x509.SHA512WithRSA, x509.SHA256WithRSAPSS, x509.SHA384WithRSAPSS, x509.SHA512WithRSAPSS:
		return generateRSAKeyPair(option.KeySize)
	default:
		return nil, errors.New("unsupported algorithm")
	}
}

func LoadPublicKey(publicKey string) (*ecdsa.PublicKey, error) {
	// decode the key, assuming it's in PEM format
	block, _ := pem.Decode([]byte(publicKey))
	if block == nil {
		return nil, errors.New("Failed to decode PEM public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("Failed to parse ECDSA public key")
	}
	switch pub := pub.(type) {
	case *ecdsa.PublicKey:
		return pub, nil
	}
	return nil, errors.New("Unsupported public key type")
}

func LoadRSAPublicKey(publicKey string) (*rsa.PublicKey, error) {
	// decode the key, assuming it's in PEM format
	block, _ := pem.Decode([]byte(publicKey))
	if block == nil {
		return nil, errors.New("failed to decode PEM public key")
	}
	pub, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("failed to parse RSA public key")
	}

	return pub, nil
}

func LoadPrivateKey(privateKey string) (*ecdsa.PrivateKey, error) {
	// decode the key, assuming it's in PEM format
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return nil, errors.New("Failed to decode PEM private key")
	}
	pri, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("Failed to parse ECDSA private key")
	}

	return pri, nil
}

func LoadRSAPrivateKey(privateKey string) (*rsa.PrivateKey, error) {
	// decode the key, assuming it's in PEM format
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return nil, errors.New("failed to decode PEM private key")
	}
	pri, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("failed to parse RSA private key")
	}

	return pri, nil
}

func SignMessage(privateKey *ecdsa.PrivateKey, message string) (string, error) {
	sig, err := privateKey.Sign(rand.Reader, hash(StringToBytes(message)), nil)
	sigValue := &ECDSASignature{}
	_, err = asn1.Unmarshal(sig, sigValue)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(sig), nil
}

type SignMessageOption struct {
	Algorithm x509.SignatureAlgorithm
}

func SignMessageWithOption(privateKey interface{}, message string, option *SignMessageOption) (string, error) {
	if option == nil {
		option = &SignMessageOption{Algorithm: x509.ECDSAWithSHA256}
	}

	switch option.Algorithm {
	case x509.ECDSAWithSHA256:
		ecdsaPrivateKey, ok := privateKey.(*ecdsa.PrivateKey)
		if !ok {
			return "", errors.New("incorrect private key type")
		}
		return SignMessage(ecdsaPrivateKey, message)
	case x509.ECDSAWithSHA384:
		ecdsaPrivateKey, ok := privateKey.(*ecdsa.PrivateKey)
		if !ok {
			return "", errors.New("incorrect private key type")
		}
		sig, err := ecdsaPrivateKey.Sign(rand.Reader, hash384(StringToBytes(message)), nil)
		sigValue := &ECDSASignature{}
		_, err = asn1.Unmarshal(sig, sigValue)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	case x509.ECDSAWithSHA512:
		ecdsaPrivateKey, ok := privateKey.(*ecdsa.PrivateKey)
		if !ok {
			return "", errors.New("incorrect private key type")
		}
		sig, err := ecdsaPrivateKey.Sign(rand.Reader, hash512(StringToBytes(message)), nil)
		sigValue := &ECDSASignature{}
		_, err = asn1.Unmarshal(sig, sigValue)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	case x509.SHA256WithRSA:
		rsaPrivateKey, ok := privateKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("incorrect private key type")
		}
		sig, err := rsaPrivateKey.Sign(rand.Reader, hash(StringToBytes(message)), crypto.SHA256)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	case x509.SHA384WithRSA:
		rsaPrivateKey, ok := privateKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("incorrect private key type")
		}
		sig, err := rsaPrivateKey.Sign(rand.Reader, hash384(StringToBytes(message)), crypto.SHA384)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	case x509.SHA512WithRSA:
		rsaPrivateKey, ok := privateKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("incorrect private key type")
		}
		sig, err := rsaPrivateKey.Sign(rand.Reader, hash512(StringToBytes(message)), crypto.SHA512)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	case x509.SHA256WithRSAPSS:
		rsaPrivateKey, ok := privateKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("incorrect private key type")
		}
		sig, err := rsaPrivateKey.Sign(rand.Reader, hash(StringToBytes(message)), &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	case x509.SHA384WithRSAPSS:
		rsaPrivateKey, ok := privateKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("incorrect private key type")
		}
		sig, err := rsaPrivateKey.Sign(rand.Reader, hash384(StringToBytes(message)), &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	case x509.SHA512WithRSAPSS:
		rsaPrivateKey, ok := privateKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("incorrect private key type")
		}
		sig, err := rsaPrivateKey.Sign(rand.Reader, hash512(StringToBytes(message)), &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	default:
		return "", errors.New("unsupported algorithm")
	}
}

func VerifySignature(publicKey string, signature string, msg string) (bool, error) {
	der, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false, err
	}

	sig := &ECDSASignature{}
	_, err = asn1.Unmarshal(der, sig)
	if err != nil {
		return false, err
	}

	pub, err := LoadPublicKey(publicKey)
	if err != nil {
		return false, err
	}
	h := hash([]byte(msg))

	valid := ecdsa.Verify(
		pub,
		h,
		sig.R,
		sig.S,
	)

	if !valid {
		return false, nil
	}

	return true, nil
}

type VerifySignatureOption struct {
	Algorithm x509.SignatureAlgorithm
}

func VerifySignatureWithOption(publicKey string, signature string, msg string, option *VerifySignatureOption) (bool, error) {
	if option == nil || option.Algorithm == x509.ECDSAWithSHA256 {
		return VerifySignature(publicKey, signature, msg)
	}
	der, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false, err
	}

	switch option.Algorithm {
	case x509.ECDSAWithSHA256:
		return VerifySignature(publicKey, signature, msg)
	case x509.ECDSAWithSHA384:
		sig := &ECDSASignature{}
		_, err = asn1.Unmarshal(der, sig)
		if err != nil {
			return false, err
		}

		pub, err := LoadPublicKey(publicKey)
		if err != nil {
			return false, err
		}
		h := hash384([]byte(msg))

		valid := ecdsa.Verify(
			pub,
			h,
			sig.R,
			sig.S,
		)

		if !valid {
			return false, nil
		}
	case x509.ECDSAWithSHA512:
		sig := &ECDSASignature{}
		_, err = asn1.Unmarshal(der, sig)
		if err != nil {
			return false, err
		}

		pub, err := LoadPublicKey(publicKey)
		if err != nil {
			return false, err
		}
		h := hash512([]byte(msg))

		valid := ecdsa.Verify(
			pub,
			h,
			sig.R,
			sig.S,
		)

		if !valid {
			return false, nil
		}
	case x509.SHA256WithRSA:
		pub, err := LoadRSAPublicKey(publicKey)
		if err != nil {
			return false, err
		}
		h := hash([]byte(msg))
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, h, der)
		if err != nil {
			return false, nil
		}
	case x509.SHA384WithRSA:
		pub, err := LoadRSAPublicKey(publicKey)
		if err != nil {
			return false, err
		}
		h := hash384([]byte(msg))
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA384, h, der)
		if err != nil {
			return false, nil
		}
	case x509.SHA512WithRSA:
		pub, err := LoadRSAPublicKey(publicKey)
		if err != nil {
			return false, err
		}
		h := hash512([]byte(msg))
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA512, h, der)
		if err != nil {
			return false, nil
		}
	case x509.SHA256WithRSAPSS:
		pub, err := LoadRSAPublicKey(publicKey)
		if err != nil {
			return false, err
		}
		h := hash([]byte(msg))
		err = rsa.VerifyPSS(pub, crypto.SHA256, h, der, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		if err != nil {
			return false, nil
		}
	case x509.SHA384WithRSAPSS:
		pub, err := LoadRSAPublicKey(publicKey)
		if err != nil {
			return false, err
		}
		h := hash384([]byte(msg))
		err = rsa.VerifyPSS(pub, crypto.SHA384, h, der, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		if err != nil {
			return false, nil
		}
	case x509.SHA512WithRSAPSS:
		pub, err := LoadRSAPublicKey(publicKey)
		if err != nil {
			return false, err
		}
		h := hash512([]byte(msg))
		err = rsa.VerifyPSS(pub, crypto.SHA512, h, der, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		if err != nil {
			return false, nil
		}
	default:
		return false, errors.New("unsupported algorithm")
	}

	return true, nil
}

func GetMD5Hash(text string) string {
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}

type DIDDetail struct {
	Method string `json:"method"`
	ID     string `json:"id"`
}

func ExtractDID(didAddress string) *DIDDetail {
	arr := strings.Split(didAddress, ":")
	if len(arr) != 3 {
		return nil
	}
	return &DIDDetail{Method: arr[1], ID: arr[2]}
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

func GenerateDID(did string, method string) string {
	return fmt.Sprintf("did:%s:%s", method, did)
}

type Consensus struct {
	Method     string
	ApproveDID string
	DIDAddress string
}

func ConvertConsensus(consensus *string) *Consensus {
	consensusMessage := strings.Split(*consensus, "|")
	if len(consensusMessage) < 3 {
		return &Consensus{}
	}
	return &Consensus{
		Method:     consensusMessage[0],
		ApproveDID: consensusMessage[1],
		DIDAddress: consensusMessage[2],
	}
}

type NewKey struct {
	Controller *string `json:"controller"`
	KeyPem     string  `json:"key_pem"`
	KeyHash    string  `json:"key_hash"`
	KeyType    string  `json:"key_type"`
	Signature  string  `json:"signature"`
}

func ConvertNewKey(newKey string) (*NewKey, error) {

	newKeyString, err := HexToBytesString(newKey)
	if err != nil {
		return nil, err
	}
	newKeyModel := &NewKey{}
	err = JSONParse(newKeyString, newKeyModel)
	if err != nil {
		return nil, err
	}
	return newKeyModel, nil
}

type MessageEncryptionOptions struct {
	HashingAlgorithm crypto.Hash
	Label            []byte
}

// The label parameter may contain arbitrary data that will not be encrypted,
// but which gives important context to the message. For example, if a given
// public key is used to decrypt two types of messages then distinct label
// values could be used to ensure that a ciphertext for one purpose cannot be
// used for another by an attacker. If not required it can be empty.

func EncryptMessage(message []byte, publicKey *rsa.PublicKey, options *MessageEncryptionOptions) ([]byte, error) {
	if options == nil {
		options = &MessageEncryptionOptions{HashingAlgorithm: crypto.SHA512}
	}

	if options.HashingAlgorithm != crypto.SHA512 && options.HashingAlgorithm != crypto.SHA256 {
		return nil, errors.New("unsupported algorithm")
	}

	cipherText, err := rsa.EncryptOAEP(options.HashingAlgorithm.New(), rand.Reader, publicKey, message, options.Label)
	if err != nil {
		return nil, err
	}
	return cipherText, nil
}

func DecryptCipherText(cipherText []byte, privateKey *rsa.PrivateKey, options *MessageEncryptionOptions) ([]byte, error) {
	if options == nil {
		options = &MessageEncryptionOptions{HashingAlgorithm: crypto.SHA512}
	}

	if options.HashingAlgorithm != crypto.SHA512 && options.HashingAlgorithm != crypto.SHA256 {
		return nil, errors.New("unsupported algorithm")
	}

	message, err := rsa.DecryptOAEP(options.HashingAlgorithm.New(), rand.Reader, privateKey, cipherText, options.Label)
	if err != nil {
		return nil, err
	}
	return message, nil
}

func HashPassword(password string) (*string, error) {
	randByte := make([]byte, 8)

	_, err := rand.Read(randByte)
	if err != nil {
		return nil, err
	}

	base64RandByte := base64.StdEncoding.EncodeToString(randByte)
	salt := []byte(base64RandByte)

	iter := 180000

	dk := pbkdf2.Key([]byte(password), salt, iter, 32, sha256.New)

	hashedPassword := fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", iter, string(salt), base64.StdEncoding.EncodeToString(dk))

	return &hashedPassword, nil
}

func ComparePassword(userPassword string, password string) bool {
	if userPassword == "" || password == "" {
		return false
	}

	splitted := strings.Split(userPassword, "$")

	salt := []byte(splitted[2])

	// saved password iteration value should be converted to int
	iter, _ := strconv.Atoi(splitted[1])

	dk := pbkdf2.Key([]byte(password), salt, iter, 32, sha256.New)

	hashedPassword := fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", iter, splitted[2], base64.StdEncoding.EncodeToString(dk))

	if subtle.ConstantTimeCompare([]byte(userPassword), []byte(hashedPassword)) == 0 {
		return false
	}

	return true
}
