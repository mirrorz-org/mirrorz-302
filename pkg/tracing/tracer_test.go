package tracing

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTracer(t *testing.T) {
	as := assert.New(t)
	tr := NewTracer(true)

	tr.Printf("ta%sky", "o")
	as.Equal(tr.String(), "taoky")
	tr.Printf(" str%sng", "o")
	as.Equal(tr.String(), "taoky strong")
	as.Equal(tr.String(), "taoky strong") // Repeated calls should not change the result

	b := new(bytes.Buffer)
	tr.WriteTo(b)
	as.Equal(b.String(), "taoky strong")
	b.Reset()
	tr.WriteTo(b)
	as.Equal(b.String(), "taoky strong") // Repeated WriteTos should not change the result
}

func TestNopTracer(t *testing.T) {
	as := assert.New(t)
	tr := NewTracer(false)

	tr.Printf("ta%sky", "o")
	as.Equal(tr.String(), "")
	tr.Printf(" str%sng", "o")
	as.Equal(tr.String(), "")

	b := new(bytes.Buffer)
	tr.WriteTo(b)
	as.Zero(b.Len())
	b.Reset()
	tr.WriteTo(b)
	as.Zero(b.Len())
}
