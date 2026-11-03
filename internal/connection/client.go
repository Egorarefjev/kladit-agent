package connection

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/metrics"
)

type Client struct {
	endpoint string
	token    string
	version  string
	http     *http.Client
}

func NewClient(config Config, token, version string) (*Client, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if err := validateToken(token); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &Client{
		endpoint: config.Endpoint,
		token:    token,
		version:  version,
		http: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Send(ctx context.Context, snapshot metrics.Snapshot) error {
	body, err := json.Marshal(struct {
		Version      int              `json:"version"`
		AgentVersion string           `json:"agent_version"`
		Metrics      metrics.Snapshot `json:"metrics"`
	}{1, c.version, snapshot})
	if err != nil {
		return fmt.Errorf("encode metrics: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid metrics request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kladit-agent/"+c.version)
	response, err := c.http.Do(req)
	if err != nil {
		// URL errors may include local configuration; responses may reflect credentials.
		return fmt.Errorf("metrics delivery failed: check network, TLS and endpoint configuration")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("metrics delivery rejected: HTTP %d", response.StatusCode)
	}
	return nil
}
