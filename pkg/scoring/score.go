package scoring

import (
	"fmt"
	"math/rand"
)

type Score struct {
	Pos   int // pos of label, bigger = better
	Mask  int // longest mask
	AS    int // is in AS
	Delta int // often negative

	// payload
	Resolve string
	Repo    string
}

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
	if l.AS != r.AS {
		return l.AS == 1
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

func (l Score) DominateExceptDelta(r Score) bool {
	rangeDominate := false
	if l.Mask > r.Mask || (l.Mask == r.Mask && l.AS >= r.AS && r.AS != 1) {
		rangeDominate = true
	}
	return l.Pos >= r.Pos && rangeDominate
}

func (l Score) Dominate(r Score) bool {
	deltaDominate := false
	if l.Delta == 0 && r.Delta == 0 {
		deltaDominate = true
	} else if l.Delta < 0 && r.Delta < l.Delta {
		deltaDominate = true
	} else if l.Delta > 0 && r.Delta > l.Delta {
		deltaDominate = true
	}
	return l.DominateExceptDelta(r) && deltaDominate
}

func (l Score) DeltaOnly() bool {
	return l.Pos == 0 && l.Mask == 0 && l.AS == 0
}

func (l Score) EqualExceptDelta(r Score) bool {
	return l.Pos == r.Pos && l.Mask == r.Mask && l.AS == r.AS
}

func (l Score) LogString() string {
	return fmt.Sprintf("%d %d %d %d", l.Pos, l.Mask, l.AS, l.Delta)
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

func (scores Scores) OptimalsExceptDelta() (optimalScores Scores) {
	for i, l := range scores {
		dominated := false
		for j, r := range scores {
			if i != j && r.DominateExceptDelta(l) {
				dominated = true
			}
		}
		if !dominated {
			optimalScores = append(optimalScores, l)
		}
	}
	return
}

func (scores Scores) Optimals() (optimalScores Scores) {
	for i, l := range scores {
		dominated := false
		for j, r := range scores {
			if i != j && r.Dominate(l) {
				dominated = true
			}
		}
		if !dominated {
			optimalScores = append(optimalScores, l)
		}
	}
	return
}

func (scores Scores) AllDelta() bool {
	for _, s := range scores {
		if !s.DeltaOnly() {
			return false
		}
	}
	return true
}

func (scores Scores) AllEqualExceptDelta() bool {
	if len(scores) == 0 {
		return true
	}
	for _, l := range scores {
		if !l.EqualExceptDelta(scores[0]) { // [0] valid ensured by previous if
			return false
		}
	}
	return true
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
