package scoring

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScoreString(t *testing.T) {
	as := assert.New(t)
	s := Score{
		Pos:     1,
		Mask:    2,
		Geo:     3456000, // metres
		ISP:     7,
		Delta:   8,
		Label:   "foo",
		Resolve: "example.com",
		Repo:    "/xzsyw",
	}
	as.Equal(s.String(), "{1, /2, 3460km, 7, +8, foo:example.com, /xzsyw}")

	s.Geo = math.Inf(1)
	as.Equal(s.String(), "{1, /2, +Inf, 7, +8, foo:example.com, /xzsyw}")

	s.Geo = math.Inf(-1)
	as.Equal(s.String(), "{1, /2, -Inf, 7, +8, foo:example.com, /xzsyw}")

	// not testing NaN for now
}

func TestScoresJSON(t *testing.T) {
	as := assert.New(t)
	s := Score{
		Pos:     1,
		Mask:    2,
		Geo:     3456000, // metres
		ISP:     7,
		Delta:   8,
		Label:   "foo",
		Resolve: "example.com",
		Repo:    "/xzsyw",
	}

	b := new(bytes.Buffer)
	err := json.NewEncoder(b).Encode(s)
	as.NotZero(b.Len())
	as.Nil(err)

	b.Reset()
	s.Geo = math.Inf(0)
	err = json.NewEncoder(b).Encode(s)
	as.NotZero(b.Len())
	as.Nil(err)
}
