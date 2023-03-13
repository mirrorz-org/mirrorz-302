package main

import "math/rand"

type Score struct {
	pos   int // pos of label, bigger the better
	mask  int // maximum mask
	as    int // is in
	delta int // often negative

	// payload
	resolve string
	repo    string
}

func (l Score) Less(r Score) bool {
	// ret > 0 means r > l
	if l.pos != r.pos {
		return r.pos-l.pos < 0
	}
	if l.mask != r.mask {
		return r.mask-l.mask < 0
	}
	if l.as != r.as {
		if l.as == 1 {
			return true
		} else {
			return false
		}
	}
	if l.delta == 0 {
		return false
	} else if r.delta == 0 {
		return true
	} else if l.delta < 0 && r.delta > 0 {
		return true
	} else if r.delta < 0 && l.delta > 0 {
		return false
	} else if r.delta > 0 && l.delta > 0 {
		return l.delta-r.delta <= 0
	} else {
		return r.delta-l.delta <= 0
	}
}

func (l Score) DominateExceptDelta(r Score) bool {
	rangeDominate := false
	if l.mask > r.mask || (l.mask == r.mask && l.as >= r.as && r.as != 1) {
		rangeDominate = true
	}
	return l.pos >= r.pos && rangeDominate
}

func (l Score) Dominate(r Score) bool {
	deltaDominate := false
	if l.delta == 0 && r.delta == 0 {
		deltaDominate = true
	} else if l.delta < 0 && r.delta < 0 && l.delta > r.delta {
		deltaDominate = true
	} else if l.delta > 0 && r.delta > 0 && l.delta < r.delta {
		deltaDominate = true
	}
	return l.DominateExceptDelta(r) && deltaDominate
}

func (l Score) DeltaOnly() bool {
	return l.pos == 0 && l.mask == 0 && l.as == 0
}

func (l Score) EqualExceptDelta(r Score) bool {
	return l.pos == r.pos && l.mask == r.mask && l.as == r.as
}

type Scores []Score

func (s Scores) Len() int { return len(s) }

func (s Scores) Less(l, r int) bool {
	return s[l].Less(s[r])
}

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

func (scores Scores) AllDelta() (allDelta bool) {
	allDelta = true
	for _, s := range scores {
		if !s.DeltaOnly() {
			allDelta = false
		}
	}
	return
}

func (scores Scores) AllEqualExceptDelta() (allEqualExceptDelta bool) {
	allEqualExceptDelta = true
	if len(scores) == 0 {
		return
	}
	for _, l := range scores {
		if !l.EqualExceptDelta(scores[0]) { // [0] valid ensured by previous if
			allEqualExceptDelta = false
		}
	}
	return
}

func (scores Scores) RandomRange(r int) (score Score) {
	i := rand.Intn(r)
	score = scores[i]
	return
}

func (scores Scores) RandomHalf() (score Score) {
	score = scores.RandomRange((len(scores) + 1) / 2)
	return
}

func (scores Scores) Random() (score Score) {
	score = scores.RandomRange(len(scores))
	return
}
