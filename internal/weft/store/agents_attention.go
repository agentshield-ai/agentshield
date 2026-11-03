package store

// Attention cards on the Sessions home (docs/weft/DESIGN.md §7.1 rule 1,
// GET /api/v1/sessions/attention): how many sessions in the range failed,
// went on after a failed step, were blocked or approval-heavy, scored low or
// cost far more than usual. The counts are computed here, over every session
// that matches the request's filters, so they are never cut at a page of
// sessions; each card carries the /api/v1/sessions parameters that list
// exactly the sessions it counts.

import (
	"context"
	"math"
	"sort"
	"strconv"
)

const (
	// LowScore: a session whose lowest session-scope score mean is below
	// this has a low score.
	LowScore = 0.5
	// CostSpikeFactor: a session costing at least this many times the median
	// session cost is a cost spike.
	CostSpikeFactor = 3
	// costSpikeMinSessions is the fewest sessions with a known cost that give
	// a median worth comparing against.
	costSpikeMinSessions = 4
)

// AttentionSession names one session a card counts.
type AttentionSession struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
}

// AttentionCard is one attention card.
type AttentionCard struct {
	// ID is failed, failed_step, security, low_score or cost.
	ID    string `json:"id"`
	Count int    `json:"count"`
	// Filter holds the /api/v1/sessions parameters that, added to the
	// request's own, list exactly the sessions counted (empty when the card
	// has no list, e.g. cost without a threshold).
	Filter map[string]string `json:"filter"`
	// Latest is the counted session with the most recent activity.
	Latest *AttentionSession `json:"latest"`
}

// SessionAttention is the response of GET /api/v1/sessions/attention.
type SessionAttention struct {
	Items []AttentionCard `json:"items"`
	// Sessions is the number of sessions matching the request's filters.
	Sessions int `json:"sessions"`
	// Blocked and ApprovalHeavy split the security card.
	Blocked       int `json:"blocked"`
	ApprovalHeavy int `json:"approval_heavy"`
	// LowestScore is the lowest score among low-score sessions.
	LowestScore *float64 `json:"lowest_score"`
	// CostSpikeThreshold is null with fewer than four sessions of known cost.
	CostSpikeThreshold *float64 `json:"cost_spike_threshold"`
	// The thresholds used, for explaining the numbers.
	ApprovalHeavyThreshold int     `json:"approval_heavy_threshold"`
	LowScoreThreshold      float64 `json:"low_score_threshold"`
	CostSpikeFactor        float64 `json:"cost_spike_factor"`
}

// roundUp2 rounds x up to two significant digits (0.0048, not
// 0.0048000000000000004).
func roundUp2(x float64) float64 {
	if x <= 0 {
		return 0
	}
	e := math.Floor(math.Log10(x)) - 1
	p := math.Pow(10, e)
	n := math.Ceil(clean12(x / p))
	return clean12(n * p)
}

func clean12(v float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 12, 64), 64)
	return f
}

// CostSpikeThreshold is CostSpikeFactor times the median known cost,
// rounded up, or false with fewer than four sessions of known cost.
func CostSpikeThreshold(costs []float64) (float64, bool) {
	known := make([]float64, 0, len(costs))
	for _, c := range costs {
		if c > 0 {
			known = append(known, c)
		}
	}
	if len(known) < costSpikeMinSessions {
		return 0, false
	}
	sort.Float64s(known)
	m := len(known) / 2
	med := known[m]
	if len(known)%2 == 0 {
		med = (known[m-1] + known[m]) / 2
	}
	return roundUp2(med * CostSpikeFactor), true
}

// SessionsAttention computes the attention cards over every session that
// matches q (paging and sorting fields are ignored).
func (s *Store) SessionsAttention(ctx context.Context, q SessionsQuery) (*SessionAttention, error) {
	q.Limit, q.Offset, q.SortBy = 0, 0, ""
	sessions, pid, _, err := s.matchingSessions(ctx, q) // newest activity first
	if err != nil {
		return nil, err
	}
	out := &SessionAttention{Sessions: len(sessions), ApprovalHeavyThreshold: ApprovalHeavy,
		LowScoreThreshold: LowScore, CostSpikeFactor: CostSpikeFactor}
	cards := []*AttentionCard{
		{ID: "failed", Filter: map[string]string{"status": "error"}},
		{ID: "failed_step", Filter: map[string]string{"status": SessionStatusFailedStep}},
		{ID: "security", Filter: map[string]string{"has_detection": HasDetectionFlagged}},
		{ID: "low_score", Filter: map[string]string{"score_below": strconv.FormatFloat(LowScore, 'f', -1, 64)}},
		{ID: "cost", Filter: map[string]string{}},
	}
	firsts := map[string]*SessionSummary{}
	count := func(c *AttentionCard, ss *SessionSummary) {
		c.Count++
		if firsts[c.ID] == nil {
			firsts[c.ID] = ss
		}
	}
	costs := make([]float64, 0, len(sessions))
	for _, ss := range sessions {
		if ss.CostUSD != nil {
			costs = append(costs, *ss.CostUSD)
		}
	}
	threshold, hasThreshold := CostSpikeThreshold(costs)
	if hasThreshold {
		out.CostSpikeThreshold = fptr(threshold)
		cards[4].Filter["cost_min"] = strconv.FormatFloat(threshold, 'f', -1, 64)
	}
	for _, ss := range sessions {
		if ss.Status == "error" {
			count(cards[0], ss)
		} else if ss.ErrorCount > 0 {
			count(cards[1], ss)
		}
		if ss.DetectionCount[DetectionBlock] > 0 {
			out.Blocked++
		}
		if ss.DetectionCount[DetectionRequireApproval] >= ApprovalHeavy {
			out.ApprovalHeavy++
		}
		if IsFlaggedSession(ss.DetectionCount) {
			count(cards[2], ss)
		}
		if low, ok := lowestScore(ss); ok && low < LowScore {
			count(cards[3], ss)
			if out.LowestScore == nil || low < *out.LowestScore {
				out.LowestScore = fptr(low)
			}
		}
		if hasThreshold && ss.CostUSD != nil && *ss.CostUSD >= threshold {
			count(cards[4], ss)
		}
	}
	var named []*SessionSummary
	for _, ss := range firsts {
		named = append(named, ss)
	}
	if err := s.fillTitles(ctx, pid, named); err != nil {
		return nil, err
	}
	for _, c := range cards {
		if ss := firsts[c.ID]; ss != nil {
			c.Latest = &AttentionSession{SessionID: ss.SessionID, Title: ss.Title}
		}
		out.Items = append(out.Items, *c)
	}
	return out, nil
}
