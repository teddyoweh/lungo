//go:build !darwin

package proc

import "context"

// List reads every process with ps.
func List(ctx context.Context) *Table { return psList(ctx) }
