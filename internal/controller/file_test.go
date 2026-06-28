package controller

import "testing"

func TestParseRangeHeaderSupportsOpenEndedAndSuffixRanges(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		size      int64
		wantStart int64
		wantEnd   int64
	}{
		{name: "open ended", header: "bytes=5-", size: 10, wantStart: 5, wantEnd: 9},
		{name: "suffix", header: "bytes=-4", size: 10, wantStart: 6, wantEnd: 9},
		{name: "clips end", header: "bytes=7-99", size: 10, wantStart: 7, wantEnd: 9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := parseRangeHeader(tt.header, tt.size)
			if err != nil {
				t.Fatalf("parseRangeHeader returned error: %v", err)
			}
			if !ok {
				t.Fatal("expected range header to be parsed")
			}
			if got.start != tt.wantStart || got.end != tt.wantEnd {
				t.Fatalf("expected range %d-%d, got %d-%d", tt.wantStart, tt.wantEnd, got.start, got.end)
			}
		})
	}
}

func TestParseRangeHeaderRejectsUnsatisfiedRange(t *testing.T) {
	_, ok, err := parseRangeHeader("bytes=20-30", 10)
	if err == nil {
		t.Fatal("expected unsatisfied range to return error")
	}
	if ok {
		t.Fatal("expected unsatisfied range not to be parsed")
	}
}
