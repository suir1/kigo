package transfer

import "testing"

func TestByteRangesMergeOutOfOrderCoverage(t *testing.T) {
	ranges := newByteRanges(4)
	for _, span := range []byteRange{
		{start: 8, end: 12},
		{start: 4, end: 8},
	} {
		if err := ranges.Add(span.start, span.end); err != nil {
			t.Fatal(err)
		}
	}
	if ranges.Prefix() != 12 || !ranges.Complete(12) {
		t.Fatalf("prefix=%d ranges=%#v", ranges.Prefix(), ranges.ranges)
	}
}

func TestByteRangesRejectOverlap(t *testing.T) {
	ranges := newByteRanges(4)
	if err := ranges.Add(8, 12); err != nil {
		t.Fatal(err)
	}
	if err := ranges.Add(2, 9); err == nil {
		t.Fatal("overlapping range was accepted")
	}
}
