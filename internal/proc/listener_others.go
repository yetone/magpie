//go:build !darwin && !linux && !windows

package proc

import (
	"context"
	"errors"
)

var errUnsupported = errors.New("not supported on this system")

func listeningOn(context.Context, int) ([]int, error) { return nil, errUnsupported }

func executable(int) (string, error) { return "", errUnsupported }

func processName(int) string { return "" }
