package roadmaprun

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

// The broker accepts at most 4096 bytes per argument; base64 expands 3:4.
const MaxGoalBytes = 3072

func EncodeGoal(goal string) (string, error) {
	if len(goal) == 0 || len(goal) > MaxGoalBytes || !utf8.ValidString(goal) || strings.TrimSpace(goal) == "" || strings.ContainsRune(goal, 0) {
		return "", errors.New("goal requires 1..3072 UTF-8 bytes without NUL")
	}
	return base64.RawURLEncoding.EncodeToString([]byte(goal)), nil
}
func DecodeGoal(encoded string) (string, error) {
	if len(encoded) > 4096 {
		return "", errors.New("encoded goal exceeds broker argument limit")
	}
	goal, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	if _, err := EncodeGoal(string(goal)); err != nil {
		return "", err
	}
	return string(goal), nil
}
