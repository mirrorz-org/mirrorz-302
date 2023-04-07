package scoring

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScoreString(t *testing.T) {
	as := assert.New(t)
	s := Score{
		Pos:   1,
		Mask:  2,
		Geo:   3456000, // metres
		ISP:   7,
		Delta: 8,
	}
	as.Equal(s.String(), "{1, /2, 3460km, 7, +8}")
}
