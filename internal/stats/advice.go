package stats

import "fmt"

// Step heuristics: when a step's numbers suggest a change to the pipeline.
const (
	// A deciding step that passes at least this often, over at least
	// AlwaysPassMinVisits visits, may not be earning its keep.
	AlwaysPassRate      = 0.95
	AlwaysPassMinVisits = 10
	// A step failing more often than this (over MostlyFailMinVisits)
	// probably gets poor input from the step before it.
	MostlyFailRate      = 0.60
	MostlyFailMinVisits = 5
	// A step taking at least this share of a run's time or cost may be
	// doing too much.
	LongShare = 0.50
	// An agent step under this share of both time and cost may be worth
	// merging into a neighbour.
	TinyShare = 0.03
	// Fewest runs before shares mean anything.
	ShareMinRuns = 3
	// A step needing a person in at least this share of runs.
	NeedsPersonShare = 0.50
)

// StepFacts are a step's numbers across a set of runs.
type StepFacts struct {
	Step    string
	Agent   bool // an agent or split step (merging only makes sense for these)
	Decides bool // has more than one way out, so a pass rate means something
	Visits  int
	Passes  int // visits that moved the run on
	Fails   int // visits that sent it back, stopped it, or errored
	Runs    int // runs in the set
	// The step's share of the runs' total time and cost.
	DurationShare float64
	CostShare     float64
	// The share of runs where it needed a person.
	NeedsPerson float64
}

// Advice is one suggestion about a step.
type Advice struct {
	Kind string `json:"kind"` // always-passes | mostly-fails | long | tiny | hands-on
	Text string `json:"text"`
}

// Advise suggests changes to a step from its numbers.
func Advise(f StepFacts) []Advice {
	var out []Advice
	if f.Decides && f.Visits >= AlwaysPassMinVisits && float64(f.Passes) >= AlwaysPassRate*float64(f.Visits) {
		out = append(out, Advice{"always-passes", fmt.Sprintf("Passed %d of %d times. If it never catches anything, it may not be needed.", f.Passes, f.Visits)})
	}
	if f.Visits >= MostlyFailMinVisits && float64(f.Fails) > MostlyFailRate*float64(f.Visits) {
		out = append(out, Advice{"mostly-fails", fmt.Sprintf("Failed or sent work back %d of %d times. Look at the step before it: it may need clearer instructions or a check of its own.", f.Fails, f.Visits)})
	}
	if f.Runs >= ShareMinRuns {
		switch {
		case f.DurationShare >= LongShare || f.CostShare >= LongShare:
			out = append(out, Advice{"long", fmt.Sprintf("Takes %.0f%% of the time and %.0f%% of the cost. It may be doing too much; consider splitting it.", f.DurationShare*100, f.CostShare*100)})
		case f.Agent && f.DurationShare < TinyShare && f.CostShare < TinyShare:
			out = append(out, Advice{"tiny", fmt.Sprintf("Only %.0f%% of the time and %.0f%% of the cost. Consider merging it into a neighbouring step.", f.DurationShare*100, f.CostShare*100)})
		}
		if f.NeedsPerson >= NeedsPersonShare {
			out = append(out, Advice{"hands-on", fmt.Sprintf("Needed a person in %.0f%% of runs. Clearer instructions, or an agent check before it, may let it run on its own.", f.NeedsPerson*100)})
		}
	}
	return out
}
