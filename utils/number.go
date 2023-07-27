package utils

import (
	"math/big"
	"strings"
)

func GetBigNumber(n string) (*big.Int, bool) {
	if strings.HasPrefix(n, "0d") {
		return new(big.Int).SetString(strings.TrimPrefix(n, "0d"), 10)
	}
	if strings.HasPrefix(n, "0x") {
		return new(big.Int).SetString(strings.TrimPrefix(n, "0x"), 16)
	}
	if strings.HasPrefix(n, "0b") {
		return new(big.Int).SetString(strings.TrimPrefix(n, "0b"), 2)
	}
	return nil, false
}
