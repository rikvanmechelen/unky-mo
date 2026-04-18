package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type credentialsFile struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

func readAccessToken() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var f credentialsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return "", err
	}
	if f.ClaudeAiOauth.AccessToken == "" {
		return "", errors.New("usage: no accessToken in ~/.claude/.credentials.json")
	}
	return f.ClaudeAiOauth.AccessToken, nil
}
