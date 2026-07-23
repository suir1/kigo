package mux

import (
	"fmt"
	"strings"

	"github.com/suir1/kigo/internal/protocol"
)

type Plan struct {
	bindings []protocol.StreamBinding
	byItem   map[int]int
	byStream map[int]int
}

type StreamState struct {
	Item   int
	Opened bool
	Ended  bool
}

type Tracker struct {
	plan   *Plan
	states map[int]StreamState
}

func NewPlan(itemCount int) *Plan {
	bindings := make([]protocol.StreamBinding, itemCount)
	for item := range itemCount {
		bindings[item] = protocol.StreamBinding{ID: item + 1, Item: item}
	}
	return newPlan(bindings)
}

func PlanFromManifest(manifest *protocol.Manifest) (*Plan, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest is missing")
	}
	if len(manifest.Items) == 0 {
		return nil, fmt.Errorf("manifest has no items")
	}
	if len(manifest.Streams) == 0 {
		bindings := make([]protocol.StreamBinding, len(manifest.Items))
		for item := range manifest.Items {
			bindings[item] = protocol.StreamBinding{ID: item, Item: item}
		}
		return newPlan(bindings), nil
	}
	if len(manifest.Streams) != len(manifest.Items) {
		return nil, fmt.Errorf("manifest stream count %d does not match item count %d", len(manifest.Streams), len(manifest.Items))
	}
	plan := newPlan(nil)
	plan.bindings = make([]protocol.StreamBinding, 0, len(manifest.Streams))
	for _, binding := range manifest.Streams {
		if binding.ID < 0 {
			return nil, fmt.Errorf("negative stream id: %d", binding.ID)
		}
		if binding.Item < 0 || binding.Item >= len(manifest.Items) {
			return nil, fmt.Errorf("stream %d item index out of range: %d", binding.ID, binding.Item)
		}
		if _, exists := plan.byStream[binding.ID]; exists {
			return nil, fmt.Errorf("duplicate stream id in manifest: %d", binding.ID)
		}
		if _, exists := plan.byItem[binding.Item]; exists {
			return nil, fmt.Errorf("duplicate stream binding for item: %d", binding.Item)
		}
		plan.bindings = append(plan.bindings, binding)
		plan.byStream[binding.ID] = binding.Item
		plan.byItem[binding.Item] = binding.ID
	}
	for item := range manifest.Items {
		if _, ok := plan.byItem[item]; !ok {
			return nil, fmt.Errorf("manifest item %d has no stream binding", item)
		}
	}
	return plan, nil
}

func newPlan(bindings []protocol.StreamBinding) *Plan {
	plan := &Plan{
		bindings: append([]protocol.StreamBinding(nil), bindings...),
		byItem:   make(map[int]int, len(bindings)),
		byStream: make(map[int]int, len(bindings)),
	}
	for _, binding := range bindings {
		plan.byItem[binding.Item] = binding.ID
		plan.byStream[binding.ID] = binding.Item
	}
	return plan
}

func (p *Plan) Apply(manifest *protocol.Manifest) {
	if p == nil || manifest == nil {
		return
	}
	manifest.Streams = p.Bindings()
}

func (p *Plan) Bindings() []protocol.StreamBinding {
	if p == nil {
		return nil
	}
	return append([]protocol.StreamBinding(nil), p.bindings...)
}

func (p *Plan) StreamForItem(item int) (int, bool) {
	if p == nil {
		return 0, false
	}
	stream, ok := p.byItem[item]
	return stream, ok
}

func (p *Plan) ItemForStream(stream int) (int, bool) {
	if p == nil {
		return 0, false
	}
	item, ok := p.byStream[stream]
	return item, ok
}

func (p *Plan) ResolveFrame(manifest *protocol.Manifest, msg protocol.Message, requireStream bool) (protocol.StreamBinding, error) {
	if manifest == nil || msg.Item < 0 || msg.Item >= len(manifest.Items) {
		return protocol.StreamBinding{}, fmt.Errorf("chunk item index out of range: %d", msg.Item)
	}
	if requireStream && msg.Stream == nil {
		return protocol.StreamBinding{}, fmt.Errorf("%s missing stream", msg.Type)
	}
	stream, ok := p.StreamForItem(msg.Item)
	if !ok {
		return protocol.StreamBinding{}, fmt.Errorf("manifest item %d has no stream binding", msg.Item)
	}
	if msg.Stream != nil && *msg.Stream != stream {
		return protocol.StreamBinding{}, fmt.Errorf("stream %d does not match item %d binding %d", *msg.Stream, msg.Item, stream)
	}
	return protocol.StreamBinding{ID: stream, Item: msg.Item}, nil
}

func (p *Plan) ValidateResumeEntry(items []protocol.Item, entry protocol.ResumeEntry) (int64, error) {
	if entry.Item < 0 || entry.Item >= len(items) {
		return 0, fmt.Errorf("resume item index out of range: %d", entry.Item)
	}
	stream, ok := p.StreamForItem(entry.Item)
	if !ok {
		return 0, fmt.Errorf("manifest item %d has no stream binding", entry.Item)
	}
	if entry.Stream != nil && *entry.Stream != stream {
		return 0, fmt.Errorf("resume stream %d does not match item %d binding %d", *entry.Stream, entry.Item, stream)
	}
	if entry.Offset < 0 {
		return 0, fmt.Errorf("negative resume offset for %s: %d", items[entry.Item].Name, entry.Offset)
	}
	if entry.PrefixSHA256 != "" && !validSHA256(entry.PrefixSHA256) {
		return 0, fmt.Errorf("invalid resume prefix sha256 for %s", items[entry.Item].Name)
	}
	if entry.SHA256 != "" && !validSHA256(entry.SHA256) {
		return 0, fmt.Errorf("invalid resume sha256 for %s", items[entry.Item].Name)
	}
	if entry.Complete && !entry.Skip {
		return 0, fmt.Errorf("completed resume for %s must be a skip", items[entry.Item].Name)
	}
	if entry.Skip {
		if items[entry.Item].Kind != protocol.ItemFile {
			return 0, fmt.Errorf("skip requested for non-file item %s", items[entry.Item].Name)
		}
		if entry.Offset != items[entry.Item].Size {
			return 0, fmt.Errorf("skip offset for %s must equal file size", items[entry.Item].Name)
		}
		if entry.PrefixSHA256 != "" {
			return 0, fmt.Errorf("skip request for %s must not include prefix sha256", items[entry.Item].Name)
		}
	}
	return clampInt64(entry.Offset, 0, items[entry.Item].Size), nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range strings.ToLower(value) {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func NewTracker(plan *Plan) *Tracker {
	return &Tracker{plan: plan, states: map[int]StreamState{}}
}

func (t *Tracker) AcceptOpen(manifest *protocol.Manifest, msg protocol.Message) (protocol.StreamBinding, error) {
	binding, err := t.plan.ResolveFrame(manifest, msg, true)
	if err != nil {
		return protocol.StreamBinding{}, err
	}
	state := t.state(binding.ID)
	if state.Opened {
		return protocol.StreamBinding{}, fmt.Errorf("stream %d opened more than once", binding.ID)
	}
	t.states[binding.ID] = StreamState{Item: binding.Item, Opened: true}
	return binding, nil
}

func (t *Tracker) AcceptEnd(manifest *protocol.Manifest, msg protocol.Message) (protocol.StreamBinding, error) {
	binding, err := t.plan.ResolveFrame(manifest, msg, true)
	if err != nil {
		return protocol.StreamBinding{}, err
	}
	state := t.state(binding.ID)
	if !state.Opened {
		return protocol.StreamBinding{}, fmt.Errorf("stream %d ended before open", binding.ID)
	}
	if state.Ended {
		return protocol.StreamBinding{}, fmt.Errorf("stream %d ended more than once", binding.ID)
	}
	state.Ended = true
	t.states[binding.ID] = state
	return binding, nil
}

func (t *Tracker) AcceptChunk(manifest *protocol.Manifest, msg protocol.Message) (protocol.StreamBinding, error) {
	return t.acceptChunk(manifest, msg, false)
}

func (t *Tracker) AcceptChunkAfterEnd(manifest *protocol.Manifest, msg protocol.Message) (protocol.StreamBinding, error) {
	return t.acceptChunk(manifest, msg, true)
}

func (t *Tracker) acceptChunk(manifest *protocol.Manifest, msg protocol.Message, allowAfterEnd bool) (protocol.StreamBinding, error) {
	binding, err := t.plan.ResolveFrame(manifest, msg, false)
	if err != nil {
		return protocol.StreamBinding{}, err
	}
	if t.state(binding.ID).Ended && !allowAfterEnd {
		return protocol.StreamBinding{}, fmt.Errorf("chunk arrived after stream %d ended", binding.ID)
	}
	return binding, nil
}

func (t *Tracker) state(streamID int) StreamState {
	if t == nil || t.states == nil {
		return StreamState{}
	}
	return t.states[streamID]
}

func clampInt64(value, minValue, maxValue int64) int64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
