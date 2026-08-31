package scoring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScoreString(t *testing.T) {
	as := assert.New(t)
	s := Score{
		Pos:     1,
		Mask:    2,
		Geo:     3456, // kilometres
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
		Geo:     3456, // kilometres
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

func TestUnknownScoreIsFallback(t *testing.T) {
	preferredByLocation := Score{Geo: 1, Unknown: true}
	normal := Score{Geo: 1000}

	assert.True(t, normal.Less(preferredByLocation))
	assert.False(t, preferredByLocation.Less(normal))
}

func TestLessEqualScores(t *testing.T) {
	as := assert.New(t)
	synced := Score{Geo: 900, Delta: -70000}
	as.False(synced.Less(synced))
	proxy := Score{Geo: 900, Delta: 1000}
	as.False(proxy.Less(proxy))
}

func TestSortKeepsEqualScoreOrder(t *testing.T) {
	as := assert.New(t)
	for _, n := range []int{6, 20} {
		scores := make(Scores, 0, n)
		for i := 0; i < n; i++ {
			scores = append(scores, Score{Geo: 900, Delta: -70000, Label: fmt.Sprintf("e%02d", i)})
		}
		scores.Sort()
		for i, score := range scores {
			as.Equal(fmt.Sprintf("e%02d", i), score.Label)
		}
	}
}
