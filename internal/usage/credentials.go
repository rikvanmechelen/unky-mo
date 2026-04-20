package usage

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type credentialsFile struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

const keychainService = "Claude Code-credentials"

func readAccessToken() (string, error) {
	// Try the JSON file first (works on all platforms, older Claude Code versions).
	if token, err := readAccessTokenFromFile(); err == nil {
		return token, nil
	}

	// On macOS, Claude Code stores credentials in the Keychain.
	if runtime.GOOS == "darwin" {
		return readAccessTokenFromKeychain()
	}

	return "", errors.New("usage: no credentials found (checked ~/.claude/.credentials.json and macOS Keychain)")
}

func readAccessTokenFromFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return parseAccessToken(b)
}

func readAccessTokenFromKeychain() (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", keychainService, "-w").Output()
	if err != nil {
		return "", errors.New("usage: keychain entry not found for " + keychainService)
	}
	return parseAccessToken(out)
}

func parseAccessToken(b []byte) (string, error) {
	var f credentialsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return "", err
	}
	if f.ClaudeAiOauth.AccessToken == "" {
		return "", errors.New("usage: no accessToken in credentials")
	}
	return f.ClaudeAiOauth.AccessToken, nil
}
