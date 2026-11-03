package connection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Endpoint string `json:"endpoint"`
}

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("read connection config: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return Config{}, err
	}
	if len(data) > 65536 {
		return Config{}, fmt.Errorf("connection config exceeds 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var config Config
	if err := d.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("invalid connection config")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Config{}, fmt.Errorf("unexpected data after connection config")
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("endpoint must be an HTTPS URL without credentials, query or fragment")
	}
	return nil
}

func LoadToken(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read agent token: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || (info.Mode().Perm() != 0600 && info.Mode().Perm() != 0400) {
		return "", fmt.Errorf("agent token must be a private regular file (mode 0600 or 0400)")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", err
	}
	if len(data) > 4096 {
		return "", fmt.Errorf("agent token file exceeds 4 KiB")
	}
	token := strings.TrimSpace(string(data))
	if err := validateToken(token); err != nil {
		return "", err
	}
	return token, nil
}

func validateToken(token string) error {
	if len(token) < 32 || len(token) > 4096 {
		return fmt.Errorf("invalid agent token length")
	}
	for _, c := range token {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~+/=", c) {
			return fmt.Errorf("invalid agent token format")
		}
	}
	return nil
}
