package mux

import "fmt"

type WeightedStream struct {
	ID     int
	Weight int
}

type Turn struct {
	StreamID int
	Budget   int
	OK       bool
}

type WeightedScheduler struct {
	streams []*weightedState
	byID    map[int]*weightedState
	quantum int
	cursor  int
	active  int
	current *weightedState
	budget  int
}

type weightedState struct {
	id      int
	weight  int
	deficit int
	active  bool
}

func NewWeightedScheduler(streams []WeightedStream, quantum int) (*WeightedScheduler, error) {
	if quantum <= 0 {
		return nil, fmt.Errorf("scheduler quantum must be positive: %d", quantum)
	}
	scheduler := &WeightedScheduler{
		streams: make([]*weightedState, 0, len(streams)),
		byID:    make(map[int]*weightedState, len(streams)),
		quantum: quantum,
	}
	for _, stream := range streams {
		if stream.ID < 0 {
			return nil, fmt.Errorf("negative scheduler stream id: %d", stream.ID)
		}
		if stream.Weight <= 0 {
			return nil, fmt.Errorf("stream %d has invalid weight %d", stream.ID, stream.Weight)
		}
		if _, exists := scheduler.byID[stream.ID]; exists {
			return nil, fmt.Errorf("duplicate scheduler stream id: %d", stream.ID)
		}
		state := &weightedState{id: stream.ID, weight: stream.Weight, active: true}
		scheduler.streams = append(scheduler.streams, state)
		scheduler.byID[stream.ID] = state
		scheduler.active++
	}
	return scheduler, nil
}

func (s *WeightedScheduler) Next(maxBytes int) (Turn, error) {
	if s == nil {
		return Turn{}, fmt.Errorf("scheduler is nil")
	}
	if maxBytes <= 0 {
		return Turn{}, fmt.Errorf("maximum turn size must be positive: %d", maxBytes)
	}
	if s.current != nil {
		return Turn{}, fmt.Errorf("stream %d turn has not been committed", s.current.id)
	}
	if s.active == 0 {
		return Turn{}, nil
	}
	for {
		state := s.streams[s.cursor]
		if !state.active {
			s.advance()
			continue
		}
		if state.deficit <= 0 {
			state.deficit += s.quantum * state.weight
		}
		budget := min(maxBytes, state.deficit)
		s.current = state
		s.budget = budget
		return Turn{StreamID: state.id, Budget: budget, OK: true}, nil
	}
}

func (s *WeightedScheduler) Commit(streamID, usedBytes int, done bool) error {
	if s == nil || s.current == nil {
		return fmt.Errorf("scheduler has no active turn")
	}
	if s.current.id != streamID {
		return fmt.Errorf("committed stream %d does not match active stream %d", streamID, s.current.id)
	}
	if usedBytes < 0 || usedBytes > s.budget {
		return fmt.Errorf("stream %d used %d bytes outside turn budget %d", streamID, usedBytes, s.budget)
	}
	if usedBytes == 0 && !done {
		return fmt.Errorf("stream %d made no progress", streamID)
	}
	state := s.current
	state.deficit -= usedBytes
	if done {
		state.active = false
		state.deficit = 0
		s.active--
	}
	advance := done || state.deficit <= 0 || usedBytes < s.budget
	s.current = nil
	s.budget = 0
	if advance && s.active > 0 {
		s.advance()
	}
	return nil
}

func (s *WeightedScheduler) advance() {
	if len(s.streams) == 0 {
		return
	}
	s.cursor = (s.cursor + 1) % len(s.streams)
}
