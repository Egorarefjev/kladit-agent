package worker

import (
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
)

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
	config.Env = append(config.Env, "NVIDIA_VISIBLE_DEVICES="+visible, "NVIDIA_DRIVER_CAPABILITIES=compute,utility", "CUDA_VISIBLE_DEVICES="+cuda, "NVIDIA_MIG_CONFIG_DEVICES=", "NVIDIA_MIG_MONITOR_DEVICES=")
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
	h.LogConfig.Config = map[string]string{"max-size": "10m", "max-file": "1", "compress": "false", "mode": "non-blocking", "max-buffer-size": "1m"}
	return config
}

func matchesProfile(c *containerInfo, want containerConfig) bool {
	h, w := c.HostConfig, want.HostConfig
	if h.Privileged != w.Privileged || h.AutoRemove != w.AutoRemove || h.RestartPolicy.Name != w.RestartPolicy.Name || h.ReadonlyRootfs != w.ReadonlyRootfs || h.PidMode != w.PidMode || h.IpcMode != w.IpcMode || h.NetworkMode != w.NetworkMode || h.CPUPeriod != w.CPUPeriod || h.CPUQuota != w.CPUQuota || h.Memory != w.Memory || h.MemorySwap != w.MemorySwap || h.PidsLimit != w.PidsLimit || h.ShmSize != w.ShmSize {
		return false
	}
	if len(h.CapAdd) != 0 || len(h.DeviceCgroupRules) != 0 || len(h.Devices) != 0 || len(h.Binds) != 0 || len(h.PortBindings) != 0 || h.PublishAllPorts || len(h.VolumesFrom) != 0 {
		return false
	}
	if len(h.Tmpfs) != 1 || h.Tmpfs["/tmp"] != w.Tmpfs["/tmp"] || h.LogConfig.Type != w.LogConfig.Type {
		return false
	}
	for key, value := range w.LogConfig.Config {
		if h.LogConfig.Config[key] != value {
			return false
		}
	}
	if len(h.SecurityOpt) != 1 {
		return false
	}
	if !slices.Contains(h.CapDrop, "ALL") || (!slices.Contains(h.SecurityOpt, "no-new-privileges") && !slices.Contains(h.SecurityOpt, "no-new-privileges:true")) {
		return false
	}
	if len(h.Mounts) != 1 || h.Mounts[0].Type != "bind" || h.Mounts[0].Source != w.Mounts[0].Source || h.Mounts[0].Target != w.Mounts[0].Target || h.Mounts[0].ReadOnly != w.Mounts[0].ReadOnly {
		return false
	}
	if len(h.DeviceRequests) != len(w.DeviceRequests) {
		return false
	}
	if len(w.DeviceRequests) > 0 {
		d := h.DeviceRequests[0]
		if d.Driver != w.DeviceRequests[0].Driver || d.Count != w.DeviceRequests[0].Count || !slices.Equal(d.DeviceIDs, w.DeviceRequests[0].DeviceIDs) || !slices.EqualFunc(d.Capabilities, w.DeviceRequests[0].Capabilities, slices.Equal[[]string]) {
			return false
		}
	}
	if c.Config.User != want.User || c.Config.WorkingDir != want.WorkingDir || !slices.Equal(c.Config.Entrypoint, want.Entrypoint) || !slices.Equal(c.Config.Cmd, want.Cmd) {
		return false
	}
	expected := make(map[string]string, len(want.Env))
	for _, entry := range want.Env {
		key, value, _ := strings.Cut(entry, "=")
		expected[key] = value
	}
	seen := make(map[string]bool, len(expected))
	for _, entry := range c.Config.Env {
		key, value, _ := strings.Cut(entry, "=")
		if wantValue, controlled := expected[key]; controlled {
			if seen[key] || value != wantValue {
				return false
			}
			seen[key] = true
		}
	}
	if len(seen) != len(expected) {
		return false
	}
	return true
}
