package main

import "testing"

func TestOrderSeparatesSenderAndConversation(t *testing.T) {
	var o observation
	o.add(0, 0, 1, 100, 1)
	o.add(1, 1, 1, 99, 2) // Global DB IDs regress, but authoritative conversation seq does not.
	o.add(2, 0, 3, 103, 4)
	o.add(3, 0, 2, 102, 3)
	if o.SequenceRegressions != 1 || o.IDRegressions != 2 || o.ConversationSeqRegressions != 1 {
		t.Fatalf("unexpected counters: %+v", o)
	}
	if o.add(1, 1, 1, 99, 2) {
		t.Fatal("duplicate accepted")
	}
	if o.Duplicates != 1 || o.SequenceRegressions != 1 || o.IDRegressions != 2 || o.ConversationSeqRegressions != 1 {
		t.Fatalf("duplicate polluted ordering: %+v", o)
	}
}
func TestLatencyNearestRankAndEmpty(t *testing.T) {
	if summarize(nil).Count != 0 {
		t.Fatal("empty sample")
	}
	s := summarize([]float64{100, 1, 3, 2, 4})
	if s.P50 != 3 || s.P95 != 100 || s.P99 != 100 || s.Max != 100 || s.Count != 5 {
		t.Fatalf("unexpected percentiles: %+v", s)
	}
}
