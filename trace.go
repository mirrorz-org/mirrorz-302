package main

import (
	"fmt"
	"io"
	"strings"
)

type TraceFunc func(format string, args ...any)

type tracerKey struct{}

// For use in context.WithValue
var TracerKey = tracerKey{}

type Tracer interface {
	io.WriterTo
	fmt.Stringer
	Printf(format string, args ...any)
}

type bufTracer struct {
	b strings.Builder
}

func NewTracer(enable bool) Tracer {
	if enable {
		return new(bufTracer)
	}
	return new(nopTracer)
}

// Tracef implements the Tracer interface.
func (t *bufTracer) Printf(format string, args ...any) {
	fmt.Fprintf(&t.b, format, args...)
}

// String implements the fmt.Stringer interface.
func (t *bufTracer) String() string {
	return t.b.String()
}

// WriteTo implements the io.WriterTo interface.
func (t *bufTracer) WriteTo(w io.Writer) (n int64, err error) {
	N, err := io.WriteString(w, t.b.String())
	return int64(N), err
}

type nopTracer struct{}

// Tracef implements the Tracer interface.
func (t *nopTracer) Printf(format string, args ...any) {}

// String implements the fmt.Stringer interface.
func (t *nopTracer) String() string {
	return ""
}

// WriteTo implements the io.WriterTo interface.
func (t *nopTracer) WriteTo(w io.Writer) (n int64, err error) {
	return 0, nil
}
