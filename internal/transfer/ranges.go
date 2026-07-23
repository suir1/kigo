package transfer

import "fmt"

type byteRange struct {
	start int64
	end   int64
}

type byteRanges struct {
	ranges []byteRange
}

func newByteRanges(prefix int64) *byteRanges {
	ranges := &byteRanges{}
	if prefix > 0 {
		ranges.ranges = []byteRange{{start: 0, end: prefix}}
	}
	return ranges
}

func (r *byteRanges) Add(start, end int64) error {
	if start < 0 || end < start {
		return fmt.Errorf("invalid byte range [%d,%d)", start, end)
	}
	if start == end {
		return nil
	}
	insert := 0
	for insert < len(r.ranges) && r.ranges[insert].end <= start {
		insert++
	}
	if insert < len(r.ranges) && r.ranges[insert].start < end && start < r.ranges[insert].end {
		return fmt.Errorf("byte range [%d,%d) overlaps [%d,%d)", start, end, r.ranges[insert].start, r.ranges[insert].end)
	}
	merged := byteRange{start: start, end: end}
	if insert > 0 && r.ranges[insert-1].end == merged.start {
		insert--
		merged.start = r.ranges[insert].start
		r.ranges = append(r.ranges[:insert], r.ranges[insert+1:]...)
	}
	if insert < len(r.ranges) && merged.end == r.ranges[insert].start {
		merged.end = r.ranges[insert].end
		r.ranges = append(r.ranges[:insert], r.ranges[insert+1:]...)
	}
	r.ranges = append(r.ranges, byteRange{})
	copy(r.ranges[insert+1:], r.ranges[insert:])
	r.ranges[insert] = merged
	return nil
}

func (r *byteRanges) Prefix() int64 {
	if r == nil || len(r.ranges) == 0 || r.ranges[0].start != 0 {
		return 0
	}
	return r.ranges[0].end
}

func (r *byteRanges) Complete(size int64) bool {
	return r != nil && r.Prefix() == size
}
