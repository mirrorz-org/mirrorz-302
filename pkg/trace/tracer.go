package trace

import (
	"fmt"
	"io"
	"strings"
)

type contextKey int

// Key can be used as a key in context.WithValue
const Key contextKey = iota

func (contextKey) String() string {
	return "context key for trace"
}

// A Tracer is a convenient string builder for accumulating debug output. It is intended to be passed with a context.Context.
type Tracer interface {
	io.WriterTo
	fmt.Stringer
	Printf(format string, args ...any)
}

// NewTracer returns a new Tracer.
// If enabled, the returned Tracer will record trace data.
// Otherwise, it will be a no-op.
//
// A Tracer cannot be toggled after it is created.
func NewTracer(enable bool) Tracer {
	if enable {
		return new(bufTracer)
	}
	return new(nopTracer)
}

// A bufTracer is a Tracer that records traces in a buffer.
type bufTracer struct {
	b strings.Builder
}

// Printf implements the Tracer interface.
func (t *bufTracer) Printf(format string, args ...any) {
	if len(args) == 0 {
		t.b.WriteString(format)
		return
	}
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

// A nopTracer is a Tracer that discards all traces.
type nopTracer struct{}

// Printf implements the Tracer interface.
func (t *nopTracer) Printf(format string, args ...any) {}

// String implements the fmt.Stringer interface.
func (t *nopTracer) String() string {
	return ""
}

// WriteTo implements the io.WriterTo interface.
func (t *nopTracer) WriteTo(w io.Writer) (n int64, err error) {
	return 0, nil
}
