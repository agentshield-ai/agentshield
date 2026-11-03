package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Security insights over engine detections (GET /api/v1/shield/insights):
// approval fatigue, override escalation, and cross-agent correlation within
// a team.

// InsightsQuery selects the window and thresholds.
type InsightsQuery struct {
	ProjectID string
	From, To  int64  // unix micros; 0 = open
	Bucket    string // hour or day (default)
	// FatigueThreshold flags a session with at least this many
	// require_approval verdicts (default 5).
	FatigueThreshold int
	// EscalationThreshold flags a session with at least this many overrides
	// (default 3), or two overridden blocks.
	EscalationThreshold int
	// Window groups detections of different agents of a team into one
	// incident when they are at most this far apart (default 10 minutes).
	Window time.Duration
	// Limit bounds the per-session and per-team lists (default 50).
	Limit int
}

// InsightBucket is one time bucket of a series.
type InsightBucket struct {
	Bucket           string `json:"bucket"`
	RequireApproval  int    `json:"require_approval"`
	Overridden       int    `json:"overridden"`
	Overrides        int    `json:"overrides"`
	OverriddenBlocks int    `json:"overridden_blocks"`
}

// FatigueSession is a session's approval load.
type FatigueSession struct {
	SessionID       string   `json:"session_id"`
	SessionTitle    string   `json:"session_title,omitempty"`
	Agents          []string `json:"agents"`
	RequireApproval int      `json:"require_approval"`
	Approved        int      `json:"approved"` // overridden require_approval verdicts
	FirstAt         string   `json:"first_at"`
	LastAt          string   `json:"last_at"`
	PerHour         float64  `json:"per_hour"`
	Fatigued        bool     `json:"fatigued"`
}

// FatigueAgent is an agent's approval load across sessions.
type FatigueAgent struct {
	Agent           string  `json:"agent"`
	RequireApproval int     `json:"require_approval"`
	Approved        int     `json:"approved"`
	Sessions        int     `json:"sessions"`
	ApprovalRate    float64 `json:"approval_rate"`
}

// ApprovalFatigue is the require_approval panel.
type ApprovalFatigue struct {
	Threshold int              `json:"threshold"`
	Series    []InsightBucket  `json:"series"`
	BySession []FatigueSession `json:"by_session"`
	ByAgent   []FatigueAgent   `json:"by_agent"`
}

// EscalationSession is a session's override history.
type EscalationSession struct {
	SessionID           string   `json:"session_id"`
	SessionTitle        string   `json:"session_title,omitempty"`
	Agents              []string `json:"agents"`
	Overrides           int      `json:"overrides"`
	OverriddenBlocks    int      `json:"overridden_blocks"`
	OverriddenApprovals int      `json:"overridden_approvals"`
	RuleIDs             []string `json:"rule_ids"`
	FirstAt             string   `json:"first_at"`
	LastAt              string   `json:"last_at"`
	Escalating          bool     `json:"escalating"`
}

// OverrideEscalation is the overrides panel.
type OverrideEscalation struct {
	Threshold int                 `json:"threshold"`
	Series    []InsightBucket     `json:"series"`
	BySession []EscalationSession `json:"by_session"`
}

// SharedRule is a rule that fired for more than one agent of a team.
type SharedRule struct {
	RuleID string   `json:"rule_id"`
	Title  string   `json:"title"`
	Level  string   `json:"level"`
	Agents []string `json:"agents"`
	Count  int      `json:"count"`
}

// TeamIncident groups detections of several agents of a team close in time.
type TeamIncident struct {
	Start      string   `json:"start"`
	End        string   `json:"end"`
	Agents     []string `json:"agents"`
	RuleIDs    []string `json:"rule_ids"`
	SessionIDs []string `json:"session_ids"`
	Detections int      `json:"detections"`
	MaxAction  string   `json:"max_action"`
}

// TeamCorrelation is cross-agent correlation within one team.
type TeamCorrelation struct {
	TeamID               string         `json:"team_id"`
	Agents               []string       `json:"agents"`
	Sessions             int            `json:"sessions"`
	Detections           int            `json:"detections"`
	AgentsWithDetections []string       `json:"agents_with_detections"`
	SharedRules          []SharedRule   `json:"shared_rules"`
	Incidents            []TeamIncident `json:"incidents"`
	Correlated           bool           `json:"correlated"`
}

// Insights is the response of ShieldInsights.
type Insights struct {
	From               *string            `json:"from"`
	To                 *string            `json:"to"`
	Bucket             string             `json:"bucket"`
	ApprovalFatigue    ApprovalFatigue    `json:"approval_fatigue"`
	OverrideEscalation OverrideEscalation `json:"override_escalation"`
	CrossAgent         []TeamCorrelation  `json:"cross_agent"`
}

type insightRow struct {
	id, session, agent, action, sev string
	overridden                      bool
	overriddenAt, created           int64
	rules                           []DetectionRule
}

// ShieldInsights computes the insights panels over engine detections.
func (s *Store) ShieldInsights(ctx context.Context, q InsightsQuery) (*Insights, error) {
	if q.Bucket == "" {
		q.Bucket = "day"
	}
	if q.Bucket != "day" && q.Bucket != "hour" {
		return nil, Invalidf("bucket must be hour or day")
	}
	if q.FatigueThreshold <= 0 {
		q.FatigueThreshold = 5
	}
	if q.EscalationThreshold <= 0 {
		q.EscalationThreshold = 3
	}
	if q.Window <= 0 {
		q.Window = 10 * time.Minute
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 50
	}
	dq := DetectionQuery{ProjectID: q.ProjectID, Source: DetectionSourceEngine, From: q.From, To: q.To}
	where, args := dq.where()
	rows, err := s.r.QueryContext(ctx, `SELECT d.id, d.session_id, d.agent_name, d.action, d.severity, d.overridden, COALESCE(d.overridden_at, 0), d.created_at, d.rules
		FROM detections d WHERE `+where+` AND d.action <> 'allow' ORDER BY d.created_at LIMIT 200000`, args...)
	if err != nil {
		return nil, err
	}
	var all []insightRow
	for rows.Next() {
		var r insightRow
		var rules string
		if err := rows.Scan(&r.id, &r.session, &r.agent, &r.action, &r.sev, &r.overridden, &r.overriddenAt, &r.created, &rules); err != nil {
			rows.Close()
			return nil, err
		}
		r.rules = detDecodeRules(rules)
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := &Insights{Bucket: q.Bucket, From: microsPtr(q.From), To: microsPtr(q.To)}
	bucketOf := func(us int64) string {
		t := time.UnixMicro(us).UTC()
		if q.Bucket == "hour" {
			return t.Format("2006-01-02T15:00:00Z")
		}
		return t.Format("2006-01-02")
	}

	// Approval fatigue.
	fat := ApprovalFatigue{Threshold: q.FatigueThreshold, Series: []InsightBucket{}, BySession: []FatigueSession{}, ByAgent: []FatigueAgent{}}
	series := map[string]*InsightBucket{}
	sessions := map[string]*FatigueSession{}
	sessFirst := map[string][2]int64{}
	agents := map[string]*FatigueAgent{}
	agentSessions := map[string]map[string]bool{}
	sessAgents := map[string]map[string]bool{}
	for _, r := range all {
		if sessAgents[r.session] == nil {
			sessAgents[r.session] = map[string]bool{}
		}
		if r.agent != "" {
			sessAgents[r.session][r.agent] = true
		}
		if r.action != DetectionRequireApproval {
			continue
		}
		b := detBucket(series, bucketOf(r.created))
		b.RequireApproval++
		if r.overridden {
			b.Overridden++
		}
		if r.session != "" {
			fs := sessions[r.session]
			if fs == nil {
				fs = &FatigueSession{SessionID: r.session}
				sessions[r.session] = fs
				sessFirst[r.session] = [2]int64{r.created, r.created}
			}
			fs.RequireApproval++
			if r.overridden {
				fs.Approved++
			}
			fl := sessFirst[r.session]
			fl[1] = r.created
			sessFirst[r.session] = fl
		}
		a := agents[r.agent]
		if a == nil {
			a = &FatigueAgent{Agent: r.agent}
			agents[r.agent] = a
			agentSessions[r.agent] = map[string]bool{}
		}
		a.RequireApproval++
		if r.overridden {
			a.Approved++
		}
		agentSessions[r.agent][r.session] = true
	}
	for sid, fs := range sessions {
		fl := sessFirst[sid]
		fs.FirstAt, fs.LastAt = FormatMicros(fl[0]), FormatMicros(fl[1])
		hours := float64(fl[1]-fl[0]) / float64(time.Hour.Microseconds())
		if hours < 1.0/60 {
			hours = 1.0 / 60
		}
		fs.PerHour = float64(fs.RequireApproval) / hours
		fs.Fatigued = fs.RequireApproval >= q.FatigueThreshold
		fs.Agents = detKeys(sessAgents[sid])
		fat.BySession = append(fat.BySession, *fs)
	}
	sort.Slice(fat.BySession, func(i, j int) bool {
		if fat.BySession[i].RequireApproval != fat.BySession[j].RequireApproval {
			return fat.BySession[i].RequireApproval > fat.BySession[j].RequireApproval
		}
		return fat.BySession[i].SessionID < fat.BySession[j].SessionID
	})
	fat.BySession = fat.BySession[:min(len(fat.BySession), q.Limit)]
	for name, a := range agents {
		a.Sessions = len(agentSessions[name])
		a.ApprovalRate = float64(a.Approved) / float64(a.RequireApproval)
		fat.ByAgent = append(fat.ByAgent, *a)
	}
	sort.Slice(fat.ByAgent, func(i, j int) bool {
		if fat.ByAgent[i].RequireApproval != fat.ByAgent[j].RequireApproval {
			return fat.ByAgent[i].RequireApproval > fat.ByAgent[j].RequireApproval
		}
		return fat.ByAgent[i].Agent < fat.ByAgent[j].Agent
	})
	fat.Series = detBuckets(series)
	out.ApprovalFatigue = fat

	// Override escalation.
	esc := OverrideEscalation{Threshold: q.EscalationThreshold, Series: []InsightBucket{}, BySession: []EscalationSession{}}
	oseries := map[string]*InsightBucket{}
	osess := map[string]*EscalationSession{}
	oSpan := map[string][2]int64{}
	oRules := map[string]map[string]bool{}
	for _, r := range all {
		if !r.overridden {
			continue
		}
		at := r.overriddenAt
		if at == 0 {
			at = r.created
		}
		b := detBucket(oseries, bucketOf(at))
		b.Overrides++
		if r.action == DetectionBlock {
			b.OverriddenBlocks++
		}
		if r.session == "" {
			continue
		}
		es := osess[r.session]
		if es == nil {
			es = &EscalationSession{SessionID: r.session}
			osess[r.session] = es
			oSpan[r.session] = [2]int64{at, at}
			oRules[r.session] = map[string]bool{}
		}
		es.Overrides++
		switch r.action {
		case DetectionBlock:
			es.OverriddenBlocks++
		case DetectionRequireApproval:
			es.OverriddenApprovals++
		}
		sp := oSpan[r.session]
		sp[0], sp[1] = min(sp[0], at), max(sp[1], at)
		oSpan[r.session] = sp
		for _, rr := range r.rules {
			oRules[r.session][rr.ID] = true
		}
	}
	for sid, es := range osess {
		sp := oSpan[sid]
		es.FirstAt, es.LastAt = FormatMicros(sp[0]), FormatMicros(sp[1])
		es.RuleIDs = detKeys(oRules[sid])
		es.Agents = detKeys(sessAgents[sid])
		es.Escalating = es.Overrides >= q.EscalationThreshold || es.OverriddenBlocks >= 2
		esc.BySession = append(esc.BySession, *es)
	}
	sort.Slice(esc.BySession, func(i, j int) bool {
		if esc.BySession[i].Overrides != esc.BySession[j].Overrides {
			return esc.BySession[i].Overrides > esc.BySession[j].Overrides
		}
		return esc.BySession[i].SessionID < esc.BySession[j].SessionID
	})
	esc.BySession = esc.BySession[:min(len(esc.BySession), q.Limit)]
	esc.Series = detBuckets(oseries)
	out.OverrideEscalation = esc

	cross, err := s.crossAgent(ctx, q, all)
	if err != nil {
		return nil, err
	}
	out.CrossAgent = cross
	return out, nil
}

func detBucket(m map[string]*InsightBucket, k string) *InsightBucket {
	b := m[k]
	if b == nil {
		b = &InsightBucket{Bucket: k}
		m[k] = b
	}
	return b
}

func detBuckets(m map[string]*InsightBucket) []InsightBucket {
	out := make([]InsightBucket, 0, len(m))
	for _, b := range m {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out
}

func detKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func detDecodeRules(s string) []DetectionRule {
	var rules []DetectionRule
	_ = json.Unmarshal([]byte(s), &rules)
	return rules
}

// SessionTeam is the team a session belongs to and its agents: weft.team.id
// when a span of the session sets it, otherwise "auto:" plus the sorted agent
// names joined by "+" (DESIGN.md §2).
func (s *Store) SessionTeam(ctx context.Context, projectID, sessionID string) (teamID string, agents []string, err error) {
	set := map[string]bool{}
	rows, err := s.r.QueryContext(ctx, `SELECT DISTINCT agent_name FROM agent_spans WHERE project_id = ? AND conversation_id = ? AND agent_name <> ''`,
		projectID, sessionID)
	if err != nil {
		return "", nil, err
	}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return "", nil, err
		}
		set[a] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	var attrs string
	err = s.r.QueryRowContext(ctx, `SELECT c.attributes FROM agent_spans a JOIN calls c ON c.project_id = a.project_id AND c.id = a.id
		WHERE a.project_id = ? AND a.conversation_id = ? AND instr(c.attributes, 'weft.team.id') > 0 LIMIT 1`, projectID, sessionID).Scan(&attrs)
	if err == nil {
		teamID = newSpanAttrs(decodeMap(attrs)).str("weft.team.id")
	}
	return teamID, detKeys(set), nil
}

func (s *Store) crossAgent(ctx context.Context, q InsightsQuery, all []insightRow) ([]TeamCorrelation, error) {
	bySession := map[string][]insightRow{}
	for _, r := range all {
		if r.session != "" {
			bySession[r.session] = append(bySession[r.session], r)
		}
	}
	type team struct {
		id       string
		members  map[string]bool
		sessions map[string]bool
		rows     []insightRow
	}
	teams := map[string]*team{}
	for sid, rows := range bySession {
		teamID, members, err := s.SessionTeam(ctx, q.ProjectID, sid)
		if err != nil {
			return nil, err
		}
		set := map[string]bool{}
		for _, m := range members {
			set[m] = true
		}
		for _, r := range rows {
			if r.agent != "" {
				set[r.agent] = true
			}
		}
		if teamID == "" {
			teamID = "auto:" + strings.Join(detKeys(set), "+")
		}
		t := teams[teamID]
		if t == nil {
			t = &team{id: teamID, members: map[string]bool{}, sessions: map[string]bool{}}
			teams[teamID] = t
		}
		for m := range set {
			t.members[m] = true
		}
		t.sessions[sid] = true
		t.rows = append(t.rows, rows...)
	}
	out := []TeamCorrelation{}
	for _, t := range teams {
		if len(t.members) < 2 {
			continue
		}
		sort.Slice(t.rows, func(i, j int) bool { return t.rows[i].created < t.rows[j].created })
		tc := TeamCorrelation{TeamID: t.id, Agents: detKeys(t.members), Sessions: len(t.sessions), Detections: len(t.rows),
			SharedRules: []SharedRule{}, Incidents: []TeamIncident{}}
		withDet := map[string]bool{}
		type ruleAgg struct {
			r      DetectionRule
			agents map[string]bool
			n      int
		}
		rules := map[string]*ruleAgg{}
		for _, r := range t.rows {
			withDet[r.agent] = true
			for _, rr := range r.rules {
				a := rules[rr.ID]
				if a == nil {
					a = &ruleAgg{r: rr, agents: map[string]bool{}}
					rules[rr.ID] = a
				}
				a.n++
				if r.agent != "" {
					a.agents[r.agent] = true
				}
			}
		}
		tc.AgentsWithDetections = detKeys(withDet)
		for _, a := range rules {
			if len(a.agents) >= 2 {
				tc.SharedRules = append(tc.SharedRules, SharedRule{RuleID: a.r.ID, Title: a.r.Title, Level: a.r.Level, Agents: detKeys(a.agents), Count: a.n})
			}
		}
		sort.Slice(tc.SharedRules, func(i, j int) bool {
			if tc.SharedRules[i].Count != tc.SharedRules[j].Count {
				return tc.SharedRules[i].Count > tc.SharedRules[j].Count
			}
			return tc.SharedRules[i].RuleID < tc.SharedRules[j].RuleID
		})
		// Incidents: runs of detections no more than Window apart that
		// involve at least two agents.
		w := q.Window.Microseconds()
		flush := func(run []insightRow) {
			ag, rl, ss := map[string]bool{}, map[string]bool{}, map[string]bool{}
			maxAct := ""
			for _, r := range run {
				ag[r.agent] = true
				ss[r.session] = true
				for _, rr := range r.rules {
					rl[rr.ID] = true
				}
				if detActionRank(r.action) > detActionRank(maxAct) {
					maxAct = r.action
				}
			}
			if len(detKeys(ag)) < 2 {
				return
			}
			tc.Incidents = append(tc.Incidents, TeamIncident{Start: FormatMicros(run[0].created), End: FormatMicros(run[len(run)-1].created),
				Agents: detKeys(ag), RuleIDs: detKeys(rl), SessionIDs: detKeys(ss), Detections: len(run), MaxAction: maxAct})
		}
		var run []insightRow
		for _, r := range t.rows {
			if len(run) > 0 && r.created-run[len(run)-1].created > w {
				flush(run)
				run = nil
			}
			run = append(run, r)
		}
		if len(run) > 0 {
			flush(run)
		}
		tc.Correlated = len(tc.Incidents) > 0 || len(tc.SharedRules) > 0
		out = append(out, tc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Correlated != out[j].Correlated {
			return out[i].Correlated
		}
		if out[i].Detections != out[j].Detections {
			return out[i].Detections > out[j].Detections
		}
		return out[i].TeamID < out[j].TeamID
	})
	return out[:min(len(out), q.Limit)], nil
}

func detActionRank(a string) int {
	switch a {
	case DetectionBlock:
		return 3
	case DetectionRequireApproval:
		return 2
	case DetectionLog:
		return 1
	}
	return 0
}
