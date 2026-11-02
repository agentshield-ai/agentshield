package evals

import "testing"

func TestJudgeNormalisation(t *testing.T) {
	for _, c := range []struct {
		in   any
		want float64
	}{{0.4, 0.4}, {5.0, 1}, {3.0, 0.5}, {8.0, 0.8}, {75.0, 0.75}, {true, 1}, {"no", 0}, {-2.0, 0}} {
		if got, ok := JudgeNumber(c.in); !ok || got != c.want {
			t.Fatalf("JudgeNumber(%v) = %v, %v", c.in, got, ok)
		}
	}
	m, err := ParseJudge("Sure!\n```json\n{\"matched\": \"yes\", \"confidence\": 90, \"reason\": \"x\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	sc, res, err := SignalScores(SignalSpec{ID: "nsfw", Kind: "tag"}, m)
	if err != nil || !*res.Matched || sc.ScorerTags[0] != "nsfw" || sc.ScorerTagConfidences["nsfw"] != 0.9 {
		t.Fatalf("tag scores = %v %v %v", sc, res, err)
	}
	if _, _, err := SignalScores(SignalSpec{ID: "q", Kind: "rating"}, map[string]any{"reason": "no score"}); err == nil {
		t.Fatal("missing score accepted")
	}
	if _, err := ParseJudge("no json here"); err == nil {
		t.Fatal("non-JSON accepted")
	}
}
