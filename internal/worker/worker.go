// Package worker implements the opt-in worker foundation, with no container execution.
package worker

import "io"

// Options holds local paths only. None can be supplied by an assignment.
type Options struct {
	PolicyPath   string
	ConfigPath   string
	TokenPath    string
	StatePath    string
	DockerSocket string
	Version      string
	Check        bool
	Log          io.Writer
}
