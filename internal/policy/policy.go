package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type Policy struct {
	Version      int             `json:"version"`
	Capabilities map[string]bool `json:"capabilities"`
}

func Load(path string) (Policy, error) {
	f, err := os.Open(path)
	if err != nil {
		return Policy{}, fmt.Errorf("read policy: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return Policy{}, err
	}
	if len(data) > 65536 {
		return Policy{}, fmt.Errorf("policy exceeds 64 KiB")
	}
	return parse(data)
}

func parse(data []byte) (Policy, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := checkObject(d); err != nil {
		return Policy{}, err
	}
	if _, err := d.Token(); err != io.EOF {
		return Policy{}, fmt.Errorf("unexpected data after policy")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Policy{}, err
	}
	for key := range fields {
		if key != "version" && key != "capabilities" {
			return Policy{}, fmt.Errorf("unknown policy field: %s", key)
		}
	}
	var p Policy
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("invalid policy: %w", err)
	}
	if p.Version != 1 {
		return Policy{}, fmt.Errorf("unsupported policy version")
	}
	for capability := range p.Capabilities {
		if capability != "metrics.read" {
			return Policy{}, fmt.Errorf("unsupported capability: %s", capability)
		}
	}
	return p, nil
}

// Reject ambiguous duplicate keys and null values before decoding the schema.
func checkObject(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("policy must contain JSON objects")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return fmt.Errorf("duplicate or invalid policy key")
		}
		seen[key] = true
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return err
		}
		if bytes.Equal(raw, []byte("null")) {
			return fmt.Errorf("null policy values are not allowed")
		}
		if len(raw) > 0 && raw[0] == '{' {
			if err := checkObject(json.NewDecoder(bytes.NewReader(raw))); err != nil {
				return err
			}
		}
	}
	_, err = d.Token()
	return err
}

func (p Policy) Require(capability string) error {
	if !p.Capabilities[capability] {
		return fmt.Errorf("capability denied: %s", capability)
	}
	return nil
}
