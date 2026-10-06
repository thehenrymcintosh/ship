package stats

import (
	"math"
	"strings"
	"testing"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestDescribe(t *testing.T) {
	s := Describe([]float64{5, 1, 3, 2, 4})
	if s.N != 5 || s.Mean != 3 || s.Median != 3 || s.Min != 1 || s.Max != 5 || !near(s.StdDev, math.Sqrt(2.5), 1e-9) {
		t.Fatalf("%+v", s)
	}
	if !near(s.P10, 1.4, 1e-9) || !near(s.P90, 4.6, 1e-9) {
		t.Fatalf("%+v", s)
	}
	if e := Describe(nil); e.N != 0 || e.Mean != 0 {
		t.Fatal(e)
	}
	if one := Describe([]float64{7}); one.Median != 7 || one.StdDev != 0 {
		t.Fatal(one)
	}
}

func TestHistogram(t *testing.T) {
	bins := Histogram([]float64{0, 1, 2, 3, 9.99, 10, 50}, 0, 10, 5, false)
	got := []int{}
	for _, b := range bins {
		got = append(got, b.Count)
	}
	if len(bins) != 5 || bins[0].Lo != 0 || bins[4].Hi != 10 || got[0] != 2 || got[1] != 2 || got[4] != 3 {
		t.Fatalf("%v %+v", got, bins)
	}
	lb := Histogram([]float64{1, 10, 100, 1000}, 1, 1000, 3, true)
	for i, b := range lb {
		if b.Count < 1 || (i < 2 && b.Count != 1) {
			t.Fatalf("log bins %+v", lb)
		}
	}
	if !near(lb[1].Lo, 10, 1e-6) {
		t.Fatalf("log edges %+v", lb)
	}
	if lo, hi := Range([]float64{3, 1}, []float64{9}); lo != 1 || hi != 9 {
		t.Fatal(lo, hi)
	}
	if !WantsLog(1, 100) || WantsLog(0, 100) || WantsLog(10, 50) {
		t.Fatal("WantsLog")
	}
}

func TestJudge(t *testing.T) {
	for _, c := range []struct {
		p    float64
		n    int
		want Verdict
	}{{0.99, 5, LikelyBetter}, {0.85, 5, ProbablyBetter}, {0.5, 5, NoDifference}, {0.15, 5, ProbablyWorse}, {0.01, 5, LikelyWorse}, {0.99, 2, TooFew}} {
		if got := Judge(c.p, c.n, c.n, 3); got != c.want {
			t.Errorf("%v: got %s want %s", c, got, c.want)
		}
	}
}

func TestCompareRates(t *testing.T) {
	// 4/10 vs 9/10 succeeding: clearly better.
	c := CompareRates("success", 4, 10, 9, 10, false, 3)
	if c.Verdict != LikelyBetter || c.ProbBetter < 0.97 || c.Lo <= 0 || !near(c.A, 0.4, 1e-9) || !near(c.B, 0.9, 1e-9) {
		t.Fatalf("%+v", c)
	}
	// The exact answer for Beta(5,7) vs Beta(10,2) is about 0.99.
	if !near(c.ProbBetter, 0.99, 0.01) {
		t.Fatalf("P = %v", c.ProbBetter)
	}
	// Same rates: no difference, symmetric.
	c = CompareRates("success", 5, 10, 5, 10, false, 3)
	if c.Verdict != NoDifference || !near(c.ProbBetter, 0.5, 0.02) || !near(c.Change, 0, 0.02) {
		t.Fatalf("%+v", c)
	}
	// Lower is better flips it.
	if c := CompareRates("errors", 4, 10, 9, 10, true, 3); c.Verdict != LikelyWorse {
		t.Fatalf("%+v", c)
	}
	// Deterministic.
	a, b := CompareRates("x", 3, 7, 5, 8, false, 3), CompareRates("x", 3, 7, 5, 8, false, 3)
	if a != b {
		t.Fatal("not deterministic")
	}
	if lo, hi := RateInterval(50, 100); !near(lo, 0.40, 0.02) || !near(hi, 0.60, 0.02) {
		t.Fatal(lo, hi)
	}
}

func TestCompareCounts(t *testing.T) {
	a := []float64{3, 2, 4, 3, 5, 2, 3, 4}
	b := []float64{0, 1, 0, 0, 1, 0, 0, 0}
	c := CompareCounts("interventions", a, b, true, 3)
	if c.Verdict != LikelyBetter || c.Change >= 0.3 || c.Hi >= 1 {
		t.Fatalf("%+v", c)
	}
	// All zeros both sides: no difference.
	z := []float64{0, 0, 0, 0}
	if c := CompareCounts("x", z, z, true, 3); c.Verdict != NoDifference {
		t.Fatalf("%+v", c)
	}
}

func TestCompareContinuous(t *testing.T) {
	a := []float64{100, 120, 90, 110, 105, 130, 95}
	b := []float64{60, 70, 55, 65, 75, 58, 62}
	c := CompareContinuous("time", a, b, true, 3)
	if c.Verdict != LikelyBetter || c.Change > 0.7 || c.Hi >= 1 || c.A != 105 || c.B != 62 {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(c.Explain("v1", "v2"), "below v1's") {
		t.Fatal(c.Explain("v1", "v2"))
	}
	// Noisy and overlapping: no clear difference.
	n1 := []float64{10, 200, 50, 5, 120}
	n2 := []float64{80, 15, 150, 30, 60}
	if c := CompareContinuous("time", n1, n2, true, 3); c.Verdict != NoDifference {
		t.Fatalf("%+v", c)
	}
	// Zeros (free script steps) stay finite.
	if c := CompareContinuous("cost", []float64{0, 0, 0.1}, []float64{0, 0.2, 0.3}, true, 3); math.IsNaN(c.ProbBetter) || math.IsInf(c.Change, 0) {
		t.Fatalf("%+v", c)
	}
	// One sample: too few.
	if c := CompareContinuous("time", []float64{1}, b, true, 3); c.Verdict != TooFew || !strings.Contains(c.Explain("v1", "v2"), "Too few") {
		t.Fatalf("%+v", c)
	}
	// Identical constants: a tie.
	if c := CompareContinuous("time", []float64{5, 5, 5}, []float64{5, 5, 5}, true, 3); c.ProbBetter != 0.5 {
		t.Fatalf("%+v", c)
	}
}

func TestAdvise(t *testing.T) {
	kinds := func(f StepFacts) string {
		var k []string
		for _, a := range Advise(f) {
			k = append(k, a.Kind)
		}
		return strings.Join(k, ",")
	}
	if k := kinds(StepFacts{Decides: true, Visits: 20, Passes: 20, Runs: 10, DurationShare: 0.2, CostShare: 0.2}); k != "always-passes" {
		t.Fatal(k)
	}
	if k := kinds(StepFacts{Visits: 10, Fails: 7, Runs: 10, DurationShare: 0.6, CostShare: 0.1}); k != "mostly-fails,long" {
		t.Fatal(k)
	}
	if k := kinds(StepFacts{Agent: true, Visits: 5, Passes: 5, Runs: 5, DurationShare: 0.01, CostShare: 0.01, NeedsPerson: 0.6}); k != "tiny,hands-on" {
		t.Fatal(k)
	}
	// Too few to say anything; a non-deciding step can't "always pass".
	if k := kinds(StepFacts{Visits: 30, Passes: 30, Runs: 2, DurationShare: 0.9}); k != "" {
		t.Fatal(k)
	}
}
