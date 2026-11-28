package worker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

type jobSpec struct {
	ServerID          string   `json:"server_id"`
	Image             string   `json:"image"`
	Argv              []string `json:"argv"`
	CPUMillis         uint64   `json:"cpu_millis"`
	MemoryBytes       uint64   `json:"memory_bytes"`
	GPUUUID           *string  `json:"gpu_uuid"`
	MinVRAMBytes      *uint64  `json:"min_vram_bytes"`
	TimeoutSeconds    uint64   `json:"timeout_seconds"`
	InputID           *string  `json:"input_id"`
	ResultDestination struct {
		ServerID  string `json:"server_id"`
		StorageID string `json:"storage_id"`
	} `json:"result_destination"`
	DeleteSource bool              `json:"delete_source_after_transfer"`
	Env          map[string]string `json:"env"`
	SecretRefs   []string          `json:"secret_refs"`
	Network      string            `json:"network"`
}

var imageDigest = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]{1,5})?/(?:[a-z0-9]+(?:[._-][a-z0-9]+)*/)*[a-z0-9]+(?:[._-][a-z0-9]+)*@sha256:[a-f0-9]{64}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var gpuUUID = regexp.MustCompile(`^GPU-[A-Za-z0-9-]{1,92}$`)

func parseSpec(a connection.JobAssignment, p policy.Worker) (jobSpec, error) {
	var s jobSpec
	d := json.NewDecoder(bytes.NewReader(a.Spec))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
		return s, fmt.Errorf("INVALID_JOB_SPEC")
	}
	canonical, err := canonicalSpec(a.Spec)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(canonical)) != a.SpecHash {
		return s, fmt.Errorf("SPEC_HASH_MISMATCH")
	}
	if !connection.ValidJobIdentity(s.ServerID) || !imageDigest.MatchString(s.Image) || len(s.Image) > 512 || len(s.Argv) == 0 || len(s.Argv) > 128 || s.Argv[0] == "" || s.CPUMillis == 0 || s.CPUMillis > p.Limits.CPUMillis || s.CPUMillis > 9007199254740991 || s.MemoryBytes == 0 || s.MemoryBytes > p.Limits.MemoryBytes || s.MemoryBytes > 9007199254740991 || s.TimeoutSeconds == 0 || s.TimeoutSeconds > p.Limits.TimeoutSeconds || s.TimeoutSeconds > 86400 || (s.Network != "none" && s.Network != "bridge") {
		return s, fmt.Errorf("JOB_LIMITS_DENIED")
	}
	if s.ResultDestination.ServerID != s.ServerID || s.ResultDestination.StorageID != "default" || s.DeleteSource || s.InputID != nil || len(s.SecretRefs) != 0 {
		return s, fmt.Errorf("LOCAL_RESOURCE_NOT_ALLOWED")
	}
	if len(s.Env) > 32 || (s.GPUUUID == nil) != (s.MinVRAMBytes == nil) {
		return s, fmt.Errorf("INVALID_JOB_SPEC")
	}
	for _, arg := range s.Argv {
		if !safeText(arg, 4096) {
			return s, fmt.Errorf("INVALID_JOB_SPEC")
		}
	}
	for k, v := range s.Env {
		if len(k) > 128 || !envName.MatchString(k) || strings.HasPrefix(k, "NVIDIA_") || k == "CUDA_VISIBLE_DEVICES" || !safeText(v, 2048) {
			return s, fmt.Errorf("RESERVED_JOB_ENV")
		}
	}
	if s.GPUUUID != nil {
		allowed := false
		for _, id := range p.GPUs {
			allowed = allowed || id == *s.GPUUUID
		}
		if !allowed || !gpuUUID.MatchString(*s.GPUUUID) || *s.MinVRAMBytes == 0 || *s.MinVRAMBytes > 9007199254740991 {
			return s, fmt.Errorf("GPU_NOT_ALLOWED")
		}
	}
	return s, nil
}

func safeText(s string, limit int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= limit && !strings.ContainsRune(s, 0)
}

// Matches backend canonicalJson for this bounded JSON contract: ASCII object keys,
// safe integers, ordinary strings. Never convert integers through float64.
func canonicalSpec(spec json.RawMessage) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(spec))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	var out bytes.Buffer
	var emit func(any) error
	emit = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			out.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					out.WriteByte(',')
				}
				quoteJSON(&out, k)
				out.WriteByte(':')
				if err := emit(x[k]); err != nil {
					return err
				}
			}
			out.WriteByte('}')
		case []any:
			out.WriteByte('[')
			for i, item := range x {
				if i > 0 {
					out.WriteByte(',')
				}
				if err := emit(item); err != nil {
					return err
				}
			}
			out.WriteByte(']')
		case string:
			quoteJSON(&out, x)
		case json.Number:
			// Normalized backend integers have no decimal/exponent notation.
			if strings.ContainsAny(string(x), ".eE") {
				return fmt.Errorf("noncanonical number")
			}
			out.WriteString(string(x))
		case nil:
			out.WriteString("null")
		case bool:
			if x {
				out.WriteString("true")
			} else {
				out.WriteString("false")
			}
		default:
			return fmt.Errorf("invalid JSON value")
		}
		return nil
	}
	if err := emit(value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func quoteJSON(out *bytes.Buffer, s string) {
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 32 {
				fmt.Fprintf(out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}
