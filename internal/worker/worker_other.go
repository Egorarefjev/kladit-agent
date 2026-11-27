//go:build !linux

package worker

import (
	"context"
	"fmt"
)

// Run is Linux-only; other CLI commands remain available on supported platforms.
func Run(context.Context, Options) error { return fmt.Errorf("worker requires Linux") }
