package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type Policy struct {
	Version      int
	Capabilities map[string]bool
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
	fields, err := object(data)
	if err != nil {
		return Policy{}, err
	}
	var p Policy
	for key, value := range fields {
		switch key {
		case "version":
			err = json.Unmarshal(value, &p.Version)
		case "capabilities":
			var capabilities map[string]json.RawMessage
			capabilities, err = object(value)
			p.Capabilities = make(map[string]bool)
			for name, raw := range capabilities {
				if name != "metrics.read" || (string(raw) != "true" && string(raw) != "false") {
					return Policy{}, fmt.Errorf("unsupported capability or value: %s", name)
				}
				p.Capabilities[name] = string(raw) == "true"
			}
		default:
			return Policy{}, fmt.Errorf("unknown policy field: %s", key)
		}
		if err != nil {
			return Policy{}, fmt.Errorf("invalid policy: %w", err)
		}
	}
	if p.Version != 1 {
		return Policy{}, fmt.Errorf("unsupported policy version")
	}
	return p, nil
}

// Decode one object without silently accepting duplicate keys.
func object(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("expected a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		key := token.(string)
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate policy key: %s", key)
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return nil, err
		}
		fields[key] = raw
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after JSON object")
	}
	return fields, nil
}

func (p Policy) Require(capability string) error {
	if !p.Capabilities[capability] {
		return fmt.Errorf("capability denied: %s", capability)
	}
	return nil
}
