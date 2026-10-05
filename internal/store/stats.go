package store

// StepStat sums one step's finished visits.
type StepStat struct {
	Step       string     `json:"step"`
	Type       string     `json:"type"`
	Visits     int        `json:"visits"`
	DurationMS int64      `json:"duration_ms"`
	CostUSD    float64    `json:"cost_usd,omitempty"`
	Tokens     int64      `json:"tokens,omitempty"`
	Usage      TokenUsage `json:"usage"`
}

// StepStats sums the finished visits of a run by step, in the order the
// steps first ran.
func StepStats(visits []VisitSummary) []StepStat {
	var out []StepStat
	idx := map[string]int{}
	for _, v := range visits {
		if v.Finished == nil {
			continue
		}
		i, ok := idx[v.Step]
		if !ok {
			i = len(out)
			idx[v.Step] = i
			out = append(out, StepStat{Step: v.Step, Type: v.Type})
		}
		st := &out[i]
		st.Visits++
		st.DurationMS += v.DurationMS
		st.CostUSD += v.CostUSD
		st.Tokens += v.Tokens
		if v.Usage != nil {
			st.Usage.Add(*v.Usage)
		}
	}
	return out
}

// StepAverage is a step's average per run across several runs.
type StepAverage struct {
	Step string `json:"step"`
	Type string `json:"type"`
	// Runs is how many of the runs visited the step.
	Runs int `json:"runs"`
	// Per run, over all the runs (a step a run skipped counts as zero).
	Visits     float64    `json:"visits"`
	DurationMS int64      `json:"duration_ms"`
	CostUSD    float64    `json:"cost_usd,omitempty"`
	Tokens     int64      `json:"tokens,omitempty"`
	Usage      TokenUsage `json:"usage"`
}

// AverageStepStats averages the step stats of runs, per run. Steps come in
// the order they first ran.
func AverageStepStats(runs []*RunSnapshot) []StepAverage {
	var out []StepAverage
	idx := map[string]int{}
	for _, s := range runs {
		for _, st := range StepStats(s.Visits) {
			i, ok := idx[st.Step]
			if !ok {
				i = len(out)
				idx[st.Step] = i
				out = append(out, StepAverage{Step: st.Step, Type: st.Type})
			}
			a := &out[i]
			a.Runs++
			a.Visits += float64(st.Visits)
			a.DurationMS += st.DurationMS
			a.CostUSD += st.CostUSD
			a.Tokens += st.Tokens
			a.Usage.Add(st.Usage)
		}
	}
	n := int64(len(runs))
	if n == 0 {
		return out
	}
	for i := range out {
		a := &out[i]
		a.Visits /= float64(n)
		a.DurationMS /= n
		a.CostUSD /= float64(n)
		a.Tokens /= n
		a.Usage = TokenUsage{Input: a.Usage.Input / n, Output: a.Usage.Output / n, CacheWrite: a.Usage.CacheWrite / n, CacheRead: a.Usage.CacheRead / n}
	}
	return out
}
