package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"time"
)

type dockerClient struct{ http *http.Client }
type dockerError struct{ status int }

func (e *dockerError) Error() string { return fmt.Sprintf("DOCKER_HTTP_%d", e.status) }

func newDocker(socket string) *dockerClient {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &dockerClient{http: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (d *dockerClient) close() { d.http.CloseIdleConnections() }
func (d *dockerClient) request(ctx context.Context, method, path string, body any, result any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("DOCKER_REQUEST_INVALID")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("DOCKER_REQUEST_INVALID")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("DOCKER_UNAVAILABLE")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &dockerError{status: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1048577))
	if err != nil || len(data) > 1048576 {
		return fmt.Errorf("DOCKER_RESPONSE_INVALID")
	}
	if result != nil && json.Unmarshal(data, result) != nil {
		return fmt.Errorf("DOCKER_RESPONSE_INVALID")
	}
	return nil
}

type imageInfo struct {
	ID           string `json:"Id"`
	OS           string `json:"Os"`
	Architecture string
	Config       struct{ Volumes map[string]json.RawMessage }
}
type containerInfo struct {
	ID     string `json:"Id"`
	Image  string
	Config struct{ Labels map[string]string }
	State  struct {
		Status     string
		Running    bool
		StartedAt  string
		FinishedAt string
		ExitCode   int
		OOMKilled  bool
		Restarting bool
		Dead       bool
	}
}

func (d *dockerClient) inspect(ctx context.Context, name string) (*containerInfo, error) {
	var info containerInfo
	err := d.request(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, &info)
	if err != nil {
		return nil, err
	}
	return &info, nil
}
func (d *dockerClient) image(ctx context.Context, digest string) (imageInfo, error) {
	var info imageInfo
	path := "/images/" + url.PathEscape(digest) + "/json"
	err := d.request(ctx, http.MethodGet, path, nil, &info)
	if rejected, ok := err.(*dockerError); ok && rejected.status == 404 {
		// A pull can be repeated before start intent; no host shell/registry credentials.
		err = d.request(ctx, http.MethodPost, "/images/create?fromImage="+url.QueryEscape(digest), nil, nil)
		if err == nil {
			err = d.request(ctx, http.MethodGet, path, nil, &info)
		}
	}
	if err != nil {
		return info, err
	}
	if len(info.Config.Volumes) != 0 {
		return info, fmt.Errorf("IMAGE_VOLUMES_UNSUPPORTED")
	}
	if !imageID.MatchString(info.ID) || info.OS != "linux" || info.Architecture != runtime.GOARCH {
		return info, fmt.Errorf("IMAGE_PLATFORM_UNSUPPORTED")
	}
	return info, nil
}

type mount struct {
	Type        string
	Source      string
	Target      string
	ReadOnly    bool
	BindOptions map[string]string
}
type deviceRequest struct {
	Driver       string
	DeviceIDs    []string
	Capabilities [][]string
}
type containerConfig struct {
	Image       string
	Entrypoint  []string
	Cmd         []string
	Env         []string
	User        string
	WorkingDir  string
	Labels      map[string]string
	Healthcheck struct{ Test []string }
	Tty         bool
	HostConfig  hostConfig
}
type hostConfig struct {
	RestartPolicy  struct{ Name string }
	AutoRemove     bool
	Privileged     bool
	NetworkMode    string
	PidMode        string
	IpcMode        string
	ReadonlyRootfs bool
	CapDrop        []string
	SecurityOpt    []string
	CPUPeriod      int64 `json:"CpuPeriod"`
	CPUQuota       int64 `json:"CpuQuota"`
	Memory         int64
	MemorySwap     int64
	PidsLimit      int64
	ShmSize        int64
	Tmpfs          map[string]string
	Mounts         []mount
	DeviceRequests []deviceRequest
	LogConfig      struct {
		Type   string
		Config map[string]string
	}
}
