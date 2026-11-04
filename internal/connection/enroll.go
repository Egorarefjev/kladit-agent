package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Enroll exchanges a one-time credential from stdin. Neither credential is printed.
// directory must be a private staging directory owned by the installer.
func Enroll(ctx context.Context, endpoint string, input io.Reader, directory string) error {
	data, err := io.ReadAll(io.LimitReader(input, 45))
	if err != nil {
		return fmt.Errorf("read enrollment token failed")
	}
	token := strings.TrimSpace(string(data))
	if len(token) != 43 || strings.IndexFunc(token, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-", r)
	}) >= 0 {
		return fmt.Errorf("invalid enrollment token")
	}
	client, err := NewClient(Config{Endpoint: endpoint}, token, "0.2.0")
	if err != nil {
		return err
	}
	// Reserve both files before consuming the one-time token; never overwrite credentials.
	secret, err := os.OpenFile(filepath.Join(directory, "token"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create machine token file")
	}
	defer secret.Close()
	complete := false
	defer func() {
		if !complete {
			os.Remove(secret.Name())
		}
	}()
	config, err := os.OpenFile(filepath.Join(directory, "connection.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create connection config")
	}
	defer config.Close()
	defer func() {
		if !complete {
			os.Remove(config.Name())
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"version":1}`))
	if err != nil {
		return fmt.Errorf("invalid enrollment endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.http.Do(req)
	if err != nil {
		return fmt.Errorf("enrollment failed: check network and TLS; create a new enrollment before retrying")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return fmt.Errorf("enrollment rejected: HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4097))
	if err != nil || len(body) > 4096 {
		return fmt.Errorf("invalid enrollment response")
	}
	var result struct {
		Version    int    `json:"version"`
		Token      string `json:"machine_token"`
		MetricsURL string `json:"metrics_url"`
	}
	if json.Unmarshal(body, &result) != nil || result.Version != 1 || len(result.Token) != 43 || validateToken(result.Token) != nil {
		return fmt.Errorf("invalid enrollment response")
	}
	source, _ := url.Parse(endpoint)
	target, err := url.Parse(result.MetricsURL)
	if err != nil || (Config{Endpoint: result.MetricsURL}).validate() != nil || target.Host != source.Host || target.Path != "/api/v1/agent/metrics" {
		return fmt.Errorf("invalid metrics endpoint in enrollment response")
	}
	if _, err = secret.WriteString(result.Token + "\n"); err != nil {
		return fmt.Errorf("save machine token failed; create a new enrollment")
	}
	if err = json.NewEncoder(config).Encode(Config{Endpoint: result.MetricsURL}); err != nil {
		return fmt.Errorf("save connection config failed; create a new enrollment")
	}
	if secret.Sync() != nil || config.Sync() != nil {
		return fmt.Errorf("persist credentials failed; create a new enrollment")
	}
	complete = true
	return nil
}
