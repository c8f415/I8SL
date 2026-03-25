package code

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

type Generator struct {
	length int
}

func NewGenerator(length int) Generator {
	return Generator{length: length}
}

func (g Generator) Generate() (string, error) {
	if g.length <= 0 {
		return "", fmt.Errorf("invalid code length: %d", g.length)
	}

	result := make([]byte, g.length)
	limit := big.NewInt(int64(len(alphabet)))

	for i := range result {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("read random value: %w", err)
		}

		result[i] = alphabet[n.Int64()]
	}

	return string(result), nil
}
