package adb

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrCommandFailed = errors.New("adb command failed")
)

type Error struct {
	Op     string
	Serial string
	Args   []string
	Result CommandResult
	Err    error
}

func (e *Error) Error() string {
	parts := []string{"adb"}
	if e.Op != "" {
		parts = append(parts, "op="+e.Op)
	}
	if e.Serial != "" {
		parts = append(parts, "serial="+e.Serial)
	}
	if len(e.Args) > 0 {
		parts = append(parts, "args="+strings.Join(e.Args, " "))
	}

	message := strings.Join(parts, " ")
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}

	output := strings.TrimSpace(e.Result.Stdout + e.Result.Stderr)
	if output != "" {
		message += "\nOutput:\n" + output
	}

	return message
}

func (e *Error) Unwrap() error {
	return e.Err
}

func commandError(op, serial string, args []string, result CommandResult, err error) error {
	return &Error{
		Op:     op,
		Serial: serial,
		Args:   append([]string(nil), args...),
		Result: result,
		Err:    fmt.Errorf("%w: %v", ErrCommandFailed, err),
	}
}
