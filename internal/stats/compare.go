package stats

import (
	"fmt"
	"math"
	"sort"
)

// Verdict is a comparison's conclusion about B relative to A.
type Verdict string

// Verdicts.
const (
	TooFew         Verdict = "too few runs"
	LikelyBetter   Verdict = "likely better"
	ProbablyBetter Verdict = "probably better"
	NoDifference   Verdict = "no clear difference"
	ProbablyWorse  Verdict = "probably worse"
	LikelyWorse    Verdict = "likely worse"
)

// Thresholds on P(B is better than A).
const (
	LikelyAt   = 0.95
	ProbablyAt = 0.80
	// MinRuns is the default fewest samples per version for a verdict.
	MinRuns = 3
)

// Good reports whether v favours B; Bad whether it favours A.
func (v Verdict) Good() bool { return v == LikelyBetter || v == ProbablyBetter }

// Bad reports whether v says B is worse.
func (v Verdict) Bad() bool { return v == LikelyWorse || v == ProbablyWorse }

// Judge turns P(B better) into a verdict.
func Judge(pBetter float64, nA, nB, minN int) Verdict {
	if minN < 2 {
		minN = 2
	}
	switch {
	case nA < minN || nB < minN:
		return TooFew
	case pBetter >= LikelyAt:
		return LikelyBetter
	case pBetter >= ProbablyAt:
		return ProbablyBetter
	case pBetter <= 1-LikelyAt:
		return LikelyWorse
	case pBetter <= 1-ProbablyAt:
		return ProbablyWorse
	}
	return NoDifference
}

// Kinds of metric.
const (
	KindRate       = "rate"       // a share of trials (Change is a difference in points)
	KindCount      = "count"      // events per run (Change is a ratio)
	KindContinuous = "continuous" // time, cost, tokens (Change is a ratio)
)

// Comparison compares one metric between version A (older) and B.
type Comparison struct {
	Metric        string  `json:"metric"`
	Kind          string  `json:"kind"`
	LowerIsBetter bool    `json:"lower_is_better"`
	NA            int     `json:"n_a"`
	NB            int     `json:"n_b"`
	A             float64 `json:"a"` // rate, mean count, or median
	B             float64 `json:"b"`
	// Change is B relative to A: a difference (rates) or a ratio.
	Change     float64 `json:"change"`
	Lo         float64 `json:"lo"` // 95% credible interval of Change
	Hi         float64 `json:"hi"`
	ProbBetter float64 `json:"prob_better"` // P(B is better than A)
	Verdict    Verdict `json:"verdict"`
}

// better turns draws of B-relative-to-A into P(B better), counting ties
// as half.
func better(diffs []float64, lowerIsBetter bool) float64 {
	n := 0.0
	for _, d := range diffs {
		switch {
		case d == 0:
			n += 0.5
		case (d < 0) == lowerIsBetter:
			n++
		}
	}
	return n / float64(len(diffs))
}

// CompareRates compares successes out of trials (Beta-Binomial, Beta(1,1)
// prior). Change is B's rate minus A's.
func CompareRates(metric string, sA, nA, sB, nB int, lowerIsBetter bool, minN int) Comparison {
	c := Comparison{Metric: metric, Kind: KindRate, LowerIsBetter: lowerIsBetter, NA: nA, NB: nB}
	if nA > 0 {
		c.A = float64(sA) / float64(nA)
	}
	if nB > 0 {
		c.B = float64(sB) / float64(nB)
	}
	r := newRand()
	diffs := make([]float64, Draws)
	for i := range diffs {
		diffs[i] = beta(r, float64(sB)+1, float64(nB-sB)+1) - beta(r, float64(sA)+1, float64(nA-sA)+1)
	}
	c.ProbBetter = better(diffs, lowerIsBetter)
	sort.Float64s(diffs)
	c.Change = Quantile(diffs, 0.5)
	c.Lo, c.Hi = interval(diffs)
	c.Verdict = Judge(c.ProbBetter, nA, nB, minN)
	return c
}

// RateInterval is the 95% credible interval of a rate (Beta(1,1) prior).
func RateInterval(s, n int) (lo, hi float64) {
	r := newRand()
	d := make([]float64, Draws/4)
	for i := range d {
		d[i] = beta(r, float64(s)+1, float64(n-s)+1)
	}
	sort.Float64s(d)
	return interval(d)
}

// Gamma-Poisson prior for counts per run.
const (
	countShape = 0.5
	countRate  = 0.01
)

// CompareCounts compares events per run (Gamma-Poisson). Change is the
// ratio of B's rate to A's.
func CompareCounts(metric string, a, b []float64, lowerIsBetter bool, minN int) Comparison {
	c := Comparison{Metric: metric, Kind: KindCount, LowerIsBetter: lowerIsBetter, NA: len(a), NB: len(b)}
	sum := func(xs []float64) float64 {
		t := 0.0
		for _, x := range xs {
			t += x
		}
		return t
	}
	sa, sb := sum(a), sum(b)
	if len(a) > 0 {
		c.A = sa / float64(len(a))
	}
	if len(b) > 0 {
		c.B = sb / float64(len(b))
	}
	r := newRand()
	logRatio := make([]float64, Draws)
	for i := range logRatio {
		la := gamma(r, countShape+sa) / (countRate + float64(len(a)))
		lb := gamma(r, countShape+sb) / (countRate + float64(len(b)))
		logRatio[i] = math.Log(lb) - math.Log(la)
	}
	c.ProbBetter = better(logRatio, lowerIsBetter)
	sort.Float64s(logRatio)
	lo, hi := interval(logRatio)
	c.Change, c.Lo, c.Hi = math.Exp(Quantile(logRatio, 0.5)), math.Exp(lo), math.Exp(hi)
	c.Verdict = Judge(c.ProbBetter, len(a), len(b), minN)
	return c
}

// logs maps xs to log(x + offset), where offset keeps zeros finite: half
// the smallest positive value across both samples.
func logs(a, b []float64) (la, lb []float64) {
	minPos := math.Inf(1)
	for _, xs := range [][]float64{a, b} {
		for _, x := range xs {
			if x > 0 && x < minPos {
				minPos = x
			}
		}
	}
	off := 0.0
	if math.IsInf(minPos, 1) {
		off = 1
	} else {
		for _, xs := range [][]float64{a, b} {
			for _, x := range xs {
				if x <= 0 {
					off = minPos / 2
				}
			}
		}
	}
	f := func(xs []float64) []float64 {
		out := make([]float64, len(xs))
		for i, x := range xs {
			out[i] = math.Log(math.Max(x, 0) + off)
		}
		return out
	}
	return f(a), f(b)
}

// CompareContinuous compares positive measurements (time, cost, tokens)
// on a log scale. With the reference prior the posterior of each log-mean
// is m + s/sqrt(n)·t(n-1). Change is the ratio of B's typical (geometric
// mean) value to A's; A and B are the medians.
func CompareContinuous(metric string, a, b []float64, lowerIsBetter bool, minN int) Comparison {
	c := Comparison{Metric: metric, Kind: KindContinuous, LowerIsBetter: lowerIsBetter, NA: len(a), NB: len(b)}
	c.A, c.B = Describe(a).Median, Describe(b).Median
	if len(a) < 2 || len(b) < 2 {
		c.ProbBetter, c.Change, c.Lo, c.Hi = 0.5, 1, 0, math.Inf(1)
		if len(a) > 0 && len(b) > 0 && c.A > 0 {
			c.Change = c.B / c.A
		}
		c.Verdict = TooFew
		return c
	}
	la, lb := logs(a, b)
	da, db := Describe(la), Describe(lb)
	r := newRand()
	diffs := make([]float64, Draws)
	for i := range diffs {
		ma := da.Mean + da.StdDev/math.Sqrt(float64(da.N))*studentT(r, float64(da.N-1))
		mb := db.Mean + db.StdDev/math.Sqrt(float64(db.N))*studentT(r, float64(db.N-1))
		diffs[i] = mb - ma
	}
	c.ProbBetter = better(diffs, lowerIsBetter)
	sort.Float64s(diffs)
	lo, hi := interval(diffs)
	c.Change, c.Lo, c.Hi = math.Exp(Quantile(diffs, 0.5)), math.Exp(lo), math.Exp(hi)
	c.Verdict = Judge(c.ProbBetter, len(a), len(b), minN)
	return c
}

// Explain says what a comparison found, in plain words, naming the
// versions a and b.
func (c Comparison) Explain(a, b string) string {
	if c.Verdict == TooFew {
		return fmt.Sprintf("Too few to tell yet (%d on %s, %d on %s).", c.NA, a, c.NB, b)
	}
	var change string
	switch c.Kind {
	case KindRate:
		change = fmt.Sprintf("%s is %s on %s, %s on %s: a change of %+.0f points (95%% interval %+.0f to %+.0f)",
			c.Metric, pct(c.A), a, pct(c.B), b, c.Change*100, c.Lo*100, c.Hi*100)
	default:
		change = fmt.Sprintf("%s on %s is %s %s's (95%% interval %s to %s)",
			c.Metric, b, ratio(c.Change), a, ratio(c.Lo), ratio(c.Hi))
	}
	return fmt.Sprintf("%s. There's a %.0f%% chance %s is better here.", change, c.ProbBetter*100, b)
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

// ratio is "0.72×" as words: "28% below" / "15% above" / "about the same as".
func ratio(r float64) string {
	switch {
	case math.IsInf(r, 0) || math.IsNaN(r):
		return "unknown"
	case math.Abs(r-1) < 0.005:
		return "about the same as"
	case r < 1:
		return fmt.Sprintf("%.0f%% below", (1-r)*100)
	}
	return fmt.Sprintf("%.0f%% above", (r-1)*100)
}
