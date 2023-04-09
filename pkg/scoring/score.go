package scoring

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"

	"github.com/mirrorz-org/mirrorz-302/pkg/geo"
)

const JSONInfReplacement = 1e100

type Score struct {
	Pos   int     `json:"pos"`   // pos of label, bigger = better
	Mask  int     `json:"mask"`  // longest mask
	Geo   float64 `json:"geo"`   // geographical distance
	ISP   int     `json:"isp"`   // matching ISP
	Delta int     `json:"delta"` // often negative

	// payload
	Abbr    string `json:"abbr"`
	Label   string `json:"label"`
	Resolve string `json:"resolve"`
	Repo    string `json:"repo"`
}

var zeroScore Score

// Less determines whether l is better than r
//
// In a list of best scores, Less determines if l should go before r.
func (l Score) Less(r Score) bool {
	if l.Pos != r.Pos {
		return l.Pos > r.Pos
	}
	if l.Mask != r.Mask {
		return l.Mask > r.Mask
	}
	// Favor ISP over raw geo distance
	lGeo, rGeo := l.Geo, r.Geo
	if l.ISP > 0 {
		lGeo /= 2
	}
	if r.ISP > 0 {
		rGeo /= 2
	}
	if math.Abs(lGeo-rGeo) > geo.GeoDistanceEpsilon {
		return lGeo < rGeo
	} else if l.ISP > r.ISP {
		// Same "effective" geo distance, prefer matching ISP
		return true
	}
	if l.Delta == 0 {
		return false
	} else if r.Delta == 0 {
		return true
	} else if l.Delta < 0 && r.Delta > 0 {
		return true
	} else if r.Delta < 0 && l.Delta > 0 {
		return false
	} else if r.Delta > 0 && l.Delta > 0 {
		return l.Delta <= r.Delta
	} else {
		return r.Delta <= l.Delta
	}
}

func (l Score) Zero() bool {
	return l == zeroScore
}

func (l Score) String() string {
	if l.Zero() {
		return "<empty>"
	}
	geo := math.Round(l.Geo/1e4) * 10
	geoString := fmt.Sprintf("%.fkm", geo)
	if math.IsNaN(l.Geo) || math.IsInf(l.Geo, 0) {
		geoString = fmt.Sprintf("%+v", l.Geo)
	}
	return fmt.Sprintf("{%d, /%d, %s, %d, %+d, %s:%s, %s}",
		l.Pos, l.Mask, geoString, l.ISP, l.Delta,
		l.Label, l.Resolve, l.Repo)
}

// MarshalJSON helper types.
// http://choly.ca/post/go-json-marshalling/
type scoreA Score
type scoreJSON struct{ scoreA }

// MarshalJSON implements the json.Marshaler interface.
func (l Score) MarshalJSON() ([]byte, error) {
	if math.IsInf(l.Geo, 0) {
		l.Geo = JSONInfReplacement
	} else {
		l.Geo = math.Round(l.Geo/1e4) * 10
	}
	return json.Marshal(scoreJSON{scoreA(l)})
}

type Scores []Score

// Len, Less, Swap implement the sort.Interface interface.
func (s Scores) Len() int {
	return len(s)
}

// Less determines whether l should be sorted before r
func (s Scores) Less(l, r int) bool {
	return s[l].Less(s[r])
}

// Swap swaps the elements with indexes l and r.
func (s Scores) Swap(l, r int) { s[l], s[r] = s[r], s[l] }

// Sort sorts the scores in place.
func (s Scores) Sort() {
	sort.Sort(s)
}

func (scores Scores) RandomRange(r int) Score {
	i := rand.Intn(r)
	return scores[i]
}

func (scores Scores) RandomHalf() Score {
	return scores.RandomRange((len(scores) + 1) / 2)
}

func (scores Scores) Random() Score {
	return scores.RandomRange(len(scores))
}
