package main

import (
	"fmt"
	"strings"
)

type TraceFunc func(format string, args ...any)

// For use in context.WithValue
type TracerKey struct{}

type Tracer struct {
	b strings.Builder
}

func NewTracer() *Tracer {
	return &Tracer{}
}

func (t *Tracer) Trace(format string, args ...any) {
	fmt.Fprintf(&t.b, format, args...)
}
