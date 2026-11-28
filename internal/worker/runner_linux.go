package worker

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
	"github.com/Egorarefjev/kladit-agent/internal/metrics"
	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

var imageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type execution struct {
	Phase          string `json:"phase"`
	TimeoutSeconds uint64 `json:"timeout_seconds,omitempty"`
	UnknownReason  string `json:"unknown_reason,omitempty"`
	ContainerID    string `json:"container_id,omitempty"`
	ImageID        string `json:"image_id,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	Deadline       string `json:"deadline,omitempty"`
}

func containerName(r *record) string { return "kladit-" + r.Assignment.AttemptID }
func containerLabels(r *record) map[string]string {
	return map[string]string{"kladit.attempt": r.Assignment.AttemptID, "kladit.job": r.Assignment.JobID, "kladit.spec": r.Assignment.SpecHash, "kladit.generation": r.Assignment.WorkerGeneration}
}
func ownsContainer(r *record, c *containerInfo) bool {
	if !containerID.MatchString(c.ID) || c.Image != r.Runtime.ImageID || (r.Runtime.ContainerID != "" && r.Runtime.ContainerID != c.ID) {
		return false
	}
	for k, v := range containerLabels(r) {
		if c.Config.Labels[k] != v {
			return false
		}
	}
	return true
}
func buildContainer(r *record, s jobSpec, output string) containerConfig {
	config := containerConfig{Image: r.Runtime.ImageID, Entrypoint: []string{s.Argv[0]}, Cmd: append([]string{}, s.Argv[1:]...), User: strconv.Itoa(os.Geteuid()) + ":" + strconv.Itoa(os.Getegid()), WorkingDir: "/output", Labels: containerLabels(r)}
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		config.Env = append(config.Env, k+"="+s.Env[k])
	}
	visible := "void"
	cuda := ""
	if s.GPUUUID != nil {
		visible = *s.GPUUUID
		cuda = "0"
	}
	config.Env = append(config.Env, "NVIDIA_VISIBLE_DEVICES="+visible, "NVIDIA_DRIVER_CAPABILITIES=compute,utility", "CUDA_VISIBLE_DEVICES="+cuda)
	config.Healthcheck.Test = []string{"NONE"}
	h := &config.HostConfig
	h.RestartPolicy.Name = "no"
	h.NetworkMode = s.Network
	h.IpcMode = "private"
	h.ReadonlyRootfs = true
	h.CapDrop = []string{"ALL"}
	h.SecurityOpt = []string{"no-new-privileges:true"}
	h.CPUPeriod = 100000
	h.CPUQuota = int64(s.CPUMillis) * 100
	h.Memory = int64(s.MemoryBytes)
	h.MemorySwap = h.Memory
	h.PidsLimit = 128
	h.ShmSize = 16 << 20
	h.Tmpfs = map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=67108864,mode=1777"}
	h.Mounts = []mount{{Type: "bind", Source: output, Target: "/output", BindOptions: map[string]string{"Propagation": "rprivate"}}}
	if s.GPUUUID != nil {
		h.DeviceRequests = []deviceRequest{{Driver: "nvidia", DeviceIDs: []string{*s.GPUUUID}, Capabilities: [][]string{{"gpu"}}}}
	}
	h.LogConfig.Type = "local"
	h.LogConfig.Config = map[string]string{"max-size": "10m", "max-file": "1", "mode": "non-blocking", "max-buffer-size": "1m"}
	return config
}

func (j *journal) emit(state, reason string, c *containerInfo) error {
	r := j.state.Current
	event := connection.JobEvent{Seq: r.Report.Event.Seq + 1, State: state, Reason: reason}
	if r.Runtime != nil && r.Runtime.StartedAt != "" {
		event.ContainerID = r.Runtime.ContainerID
		event.StartedAt = r.Runtime.StartedAt
	}
	if c != nil && state == "finalizing" {
		event.ExitCode = &c.State.ExitCode
		event.OOMKilled = &c.State.OOMKilled
	}
	if terminalState(state) {
		event.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	r.Report.Event = event
	r.Acked = false
	return j.save()
}

// tick reconciles local evidence first. An unacknowledged report is replayed exactly.
// start_intent is a one-way barrier: neither created nor absent containers cross it twice.
func (j *journal) tick(ctx context.Context, d *dockerClient, options Options, control *connection.JobAssignment, enabled bool) error {
	r := j.state.Current
	if r == nil || r.Runtime == nil {
		return nil
	}
	run := r.Runtime
	if run.Phase == "unknown" {
		if r.Acked && r.Report.Event.State != "unknown" {
			return j.emit("unknown", run.UnknownReason, nil)
		}
		return nil
	}
	if run.Phase == "exited" {
		return nil
	}
	c, err := d.inspect(ctx, containerName(r))
	if err == nil && !ownsContainer(r, c) {
		return j.markUnknown("CONTAINER_IDENTITY_MISMATCH")
	}
	missing := false
	if e, ok := err.(*dockerError); ok && e.status == 404 {
		missing = true
	} else if err != nil {
		return nil
	}
	if run.Phase != "preparing" {
		if missing || c.State.Status == "created" {
			return j.markUnknown("START_OUTCOME_UNKNOWN")
		}
		if c.State.Status != "running" && c.State.Status != "exited" {
			return j.markUnknown("CONTAINER_STATE_UNKNOWN")
		}
		started, parseErr := time.Parse(time.RFC3339Nano, c.State.StartedAt)
		if parseErr != nil || started.IsZero() {
			return j.markUnknown("START_OUTCOME_UNKNOWN")
		}
		if run.StartedAt != "" && run.StartedAt != c.State.StartedAt {
			return j.markUnknown("CONTAINER_START_CHANGED")
		}
		if run.ContainerID == "" {
			run.ContainerID = c.ID
		}
		if !r.Acked {
			return nil
		}
		if run.StartedAt == "" {
			run.StartedAt = c.State.StartedAt
			run.Deadline = started.Add(time.Duration(run.TimeoutSeconds) * time.Second).UTC().Format(time.RFC3339Nano)
			run.Phase = "running"
			return j.emit("running", "", nil)
		}
		if c.State.Status == "exited" {
			run.Phase = "exited"
			return j.emit("finalizing", "", c)
		}
		return nil
	}
	if !r.Acked {
		return nil
	}
	if control == nil || control.State != "preparing" || control.AcknowledgedSeq != r.Report.Event.Seq || !sameAssignment(r.Assignment, *control) || !enabled || control.CancelRequested || control.StopReason != nil || control.StartPermitExpiresAt == nil || time.Now().After(*control.StartPermitExpiresAt) || time.Now().After(control.PrepareDeadlineAt) {
		return nil
	}
	p, err := policy.LoadWorker(options.PolicyPath)
	if err != nil || !p.ExecuteAllowed() {
		return nil
	}
	spec, err := parseSpec(r.Assignment, p)
	if err != nil {
		return j.emit("failed", err.Error(), nil)
	}
	run.TimeoutSeconds = spec.TimeoutSeconds
	if err = jobPreflight(ctx, options, p, spec); err != nil {
		return j.emit("failed", err.Error(), nil)
	}
	if missing {
		image, err := d.image(ctx, spec.Image)
		if err != nil {
			return nil
		}
		run.ImageID = image.ID
		relative := filepath.Join("jobs", r.Assignment.JobID, r.Assignment.AttemptID, "output")
		if err = j.root.MkdirAll(relative, 0700); err != nil {
			return j.emit("failed", "OUTPUT_UNAVAILABLE", nil)
		}
		output := filepath.Join(options.StatePath, relative)
		if err = privateOutput(j, relative); err != nil {
			return j.emit("failed", "OUTPUT_UNAVAILABLE", nil)
		}
		if err = j.save(); err != nil {
			return err
		}
		config := buildContainer(r, spec, output)
		var created struct {
			ID string `json:"Id"`
		}
		if err = d.request(ctx, http.MethodPost, "/containers/create?name="+containerName(r), config, &created); err != nil {
			return nil
		}
		if !containerID.MatchString(created.ID) {
			return j.markUnknown("CONTAINER_IDENTITY_MISMATCH")
		}
		run.ContainerID = created.ID
		if err = j.save(); err != nil {
			return err
		}
		c, err = d.inspect(ctx, containerName(r))
		if err != nil {
			return nil
		}
		if !ownsContainer(r, c) {
			return j.markUnknown("CONTAINER_IDENTITY_MISMATCH")
		}
	}
	if c.State.Status != "created" {
		return j.markUnknown("UNEXPECTED_CONTAINER_START")
	}
	// Refresh permission/preflight after pulling; never spend an expired start permit.
	p, err = policy.LoadWorker(options.PolicyPath)
	if err != nil || !p.ExecuteAllowed() || time.Now().After(*control.StartPermitExpiresAt) {
		return nil
	}
	if _, err = parseSpec(r.Assignment, p); err != nil {
		return nil
	}
	if err = jobPreflight(ctx, options, p, spec); err != nil {
		return j.emit("failed", err.Error(), nil)
	}
	if time.Now().After(*control.StartPermitExpiresAt) || time.Now().After(control.PrepareDeadlineAt) {
		return nil
	}
	run.ContainerID = c.ID
	run.Phase = "start_intent"
	run.Deadline = time.Now().Add(time.Duration(spec.TimeoutSeconds) * time.Second).UTC().Format(time.RFC3339Nano)
	if err = j.save(); err != nil {
		return err
	}
	// No retry, including ambiguous transport failure. Next tick can only inspect.
	_ = d.request(ctx, http.MethodPost, "/containers/"+run.ContainerID+"/start", nil, nil)
	return nil
}

func (j *journal) markUnknown(reason string) error {
	r := j.state.Current
	r.Runtime.Phase = "unknown"
	r.Runtime.UnknownReason = reason
	if !r.Acked {
		return j.save()
	}
	return j.emit("unknown", reason, nil)
}
func privateOutput(j *journal, path string) error {
	// Every component was created below the anchored private worker directory.
	for current := path; current != "."; current = filepath.Dir(current) {
		info, err := j.root.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe output")
		}
	}
	return nil
}

func jobPreflight(ctx context.Context, options Options, p policy.Worker, s jobSpec) error {
	if err := preflight(ctx, p, options.StatePath, options.DockerSocket); err != nil {
		return err
	}
	_, available, err := metrics.ReadMemory()
	if err != nil || available < s.MemoryBytes {
		return fmt.Errorf("MEMORY_UNAVAILABLE")
	}
	return gpuPreflight(ctx, s, p)
}
