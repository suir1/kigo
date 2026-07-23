package mux

import "testing"

func TestWeightedSchedulerHonorsDeficitWeights(t *testing.T) {
	scheduler, err := NewWeightedScheduler([]WeightedStream{
		{ID: 10, Weight: 2},
		{ID: 20, Weight: 1},
	}, 100)
	if err != nil {
		t.Fatal(err)
	}

	var got []int
	for range 6 {
		turn, err := scheduler.Next(100)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, turn.StreamID)
		if err := scheduler.Commit(turn.StreamID, turn.Budget, false); err != nil {
			t.Fatal(err)
		}
	}
	want := []int{10, 10, 20, 10, 10, 20}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("turn order = %v, want %v", got, want)
		}
	}
}

func TestWeightedSchedulerRemovesCompletedStreams(t *testing.T) {
	scheduler, err := NewWeightedScheduler([]WeightedStream{
		{ID: 1, Weight: 1},
		{ID: 2, Weight: 1},
	}, 64)
	if err != nil {
		t.Fatal(err)
	}
	first, err := scheduler.Next(64)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Commit(first.StreamID, 10, true); err != nil {
		t.Fatal(err)
	}
	second, err := scheduler.Next(64)
	if err != nil {
		t.Fatal(err)
	}
	if second.StreamID == first.StreamID {
		t.Fatalf("completed stream %d was scheduled again", first.StreamID)
	}
	if err := scheduler.Commit(second.StreamID, second.Budget, true); err != nil {
		t.Fatal(err)
	}
	done, err := scheduler.Next(64)
	if err != nil {
		t.Fatal(err)
	}
	if done.OK {
		t.Fatalf("scheduler returned turn after all streams completed: %#v", done)
	}
}

func TestWeightedSchedulerRequiresCommitAndProgress(t *testing.T) {
	scheduler, err := NewWeightedScheduler([]WeightedStream{{ID: 1, Weight: 1}}, 64)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := scheduler.Next(64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Next(64); err == nil {
		t.Fatal("uncommitted turn was ignored")
	}
	if err := scheduler.Commit(turn.StreamID, 0, false); err == nil {
		t.Fatal("zero-progress commit was accepted")
	}
}
