// Package stats describes samples (summaries, histograms) and compares two
// pipeline versions the Bayesian way: for each metric, how likely the newer
// version is better, with a 95% credible interval, in plain words.
//
// Rates (success, a step's pass rate) use a Beta-Binomial model with a
// uniform Beta(1,1) prior. Counts per run (interventions, PR rounds) use a
// Gamma-Poisson model with a weak Gamma(0.5, 0.01) prior. Continuous
// metrics (time, cost, tokens) are compared on a log scale, where they're
// roughly normal: the posterior of the mean difference is a Student-t
// (the reference prior), reported as a ratio. Probabilities come from
// Monte Carlo draws with a fixed seed, so the same data always gives the
// same numbers.
package stats

import (
	"math"
	"math/rand"
	"sort"
)

// Summary describes a sample.
type Summary struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P10    float64 `json:"p10"`
	P25    float64 `json:"p25"`
	P75    float64 `json:"p75"`
	P90    float64 `json:"p90"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	StdDev float64 `json:"stddev"` // sample standard deviation (0 for n < 2)
}

// Describe summarizes xs.
func Describe(xs []float64) Summary {
	s := Summary{N: len(xs)}
	if len(xs) == 0 {
		return s
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, x := range sorted {
		sum += x
	}
	s.Mean = sum / float64(len(xs))
	s.Min, s.Max = sorted[0], sorted[len(sorted)-1]
	s.Median = Quantile(sorted, 0.5)
	s.P10, s.P25, s.P75, s.P90 = Quantile(sorted, 0.1), Quantile(sorted, 0.25), Quantile(sorted, 0.75), Quantile(sorted, 0.9)
	if len(xs) > 1 {
		ss := 0.0
		for _, x := range sorted {
			ss += (x - s.Mean) * (x - s.Mean)
		}
		s.StdDev = math.Sqrt(ss / float64(len(xs)-1))
	}
	return s
}

// Quantile is the q-quantile of sorted xs, interpolating linearly.
func Quantile(sorted []float64, q float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	pos := q * float64(n-1)
	lo := int(math.Floor(pos))
	if lo >= n-1 {
		return sorted[n-1]
	}
	frac := pos - float64(lo)
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}

// Bin is one histogram bar: Lo <= x < Hi (the last bin includes Hi).
type Bin struct {
	Lo    float64 `json:"lo"`
	Hi    float64 `json:"hi"`
	Count int     `json:"count"`
}

// Range is the span of several samples, for histograms that share bins.
func Range(samples ...[]float64) (lo, hi float64) {
	first := true
	for _, xs := range samples {
		for _, x := range xs {
			if first || x < lo {
				lo = x
			}
			if first || x > hi {
				hi = x
			}
			first = false
		}
	}
	return lo, hi
}

// Histogram counts xs into n bins over [lo, hi], log-spaced when log is
// set and lo > 0 (good for durations and costs, which span orders of
// magnitude). Values outside the range go in the end bins.
func Histogram(xs []float64, lo, hi float64, n int, log bool) []Bin {
	if n < 1 {
		n = 1
	}
	if hi <= lo {
		hi = lo + 1
		if lo > 0 {
			hi = lo * 2
		}
	}
	log = log && lo > 0
	edge := func(i int) float64 {
		t := float64(i) / float64(n)
		if log {
			return math.Exp(math.Log(lo) + t*(math.Log(hi)-math.Log(lo)))
		}
		return lo + t*(hi-lo)
	}
	bins := make([]Bin, n)
	for i := range bins {
		bins[i] = Bin{Lo: edge(i), Hi: edge(i + 1)}
	}
	for _, x := range xs {
		var t float64
		if log {
			if x <= 0 {
				t = 0
			} else {
				t = (math.Log(x) - math.Log(lo)) / (math.Log(hi) - math.Log(lo))
			}
		} else {
			t = (x - lo) / (hi - lo)
		}
		i := int(math.Floor(t * float64(n)))
		if i < 0 {
			i = 0
		}
		if i >= n {
			i = n - 1
		}
		bins[i].Count++
	}
	return bins
}

// WantsLog reports whether a sample spans enough orders of magnitude for
// log-spaced bins to read better.
func WantsLog(lo, hi float64) bool { return lo > 0 && hi/lo >= 20 }

// --- sampling --------------------------------------------------------------------

// Draws is how many Monte Carlo draws each comparison uses.
const Draws = 20000

// seed keeps results reproducible.
const seed = 7

func newRand() *rand.Rand { return rand.New(rand.NewSource(seed)) }

// gamma draws from Gamma(shape, 1) (Marsaglia and Tsang).
func gamma(r *rand.Rand, shape float64) float64 {
	if shape < 1 {
		u := r.Float64()
		return gamma(r, shape+1) * math.Pow(u, 1/shape)
	}
	d := shape - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := r.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := r.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

func beta(r *rand.Rand, a, b float64) float64 {
	x, y := gamma(r, a), gamma(r, b)
	return x / (x + y)
}

// studentT draws from a Student-t with df degrees of freedom.
func studentT(r *rand.Rand, df float64) float64 {
	return r.NormFloat64() / math.Sqrt(2*gamma(r, df/2)/df)
}

// interval is the central 95% of sorted draws.
func interval(sorted []float64) (lo, hi float64) {
	return Quantile(sorted, 0.025), Quantile(sorted, 0.975)
}
