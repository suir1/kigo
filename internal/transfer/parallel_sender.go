package transfer

import (
	"context"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/suir1/kigo/internal/protocol"
	"github.com/suir1/kigo/internal/transport"
)

const chunkPathQueueDepth = 8

type chunkMessageSender interface {
	SendChunk(context.Context, int, int64, []byte) error
}

type chunkPathStats struct {
	Connection int
	Bytes      int64
	Chunks     int64
	SendTime   time.Duration
}

type chunkSendJob struct {
	message protocol.Message
	bytes   int64
}

type chunkPathWorker struct {
	pipe           *securePipe
	jobs           chan chunkSendJob
	historyWeight  float64
	pendingBytes   atomic.Int64
	scheduledBytes atomic.Int64
	sentBytes      atomic.Int64
	sentChunks     atomic.Int64
	sendNanos      atomic.Int64
}

type parallelChunkSender struct {
	ctx       context.Context
	cancel    context.CancelFunc
	session   *TransferSession
	workers   []*chunkPathWorker
	cursor    int
	workerWG  sync.WaitGroup
	available chan struct{}
	errMu     sync.Mutex
	firstErr  error
	finish    sync.Once
	finishErr error
}

func newParallelChunkSender(ctx context.Context, session *TransferSession) *parallelChunkSender {
	workerCtx, cancel := context.WithCancel(ctx)
	sender := &parallelChunkSender{
		ctx:       workerCtx,
		cancel:    cancel,
		session:   session,
		available: make(chan struct{}, 1),
	}
	for _, pipe := range session.pipes[1:] {
		worker := &chunkPathWorker{
			pipe:          pipe,
			jobs:          make(chan chunkSendJob, chunkPathQueueDepth),
			historyWeight: clampPathWeight(session.PhysicalPathWeight(pipe.index)),
		}
		sender.workers = append(sender.workers, worker)
		sender.workerWG.Add(1)
		go sender.runWorker(worker)
	}
	return sender
}

func (s *parallelChunkSender) SendChunk(ctx context.Context, itemID int, offset int64, data []byte) error {
	if err := s.error(); err != nil {
		return err
	}
	message, err := s.session.buildChunkMessage(itemID, offset, data)
	if err != nil {
		return err
	}
	job := chunkSendJob{message: message, bytes: int64(len(data))}
	for {
		for _, worker := range s.orderedWorkers() {
			worker.pendingBytes.Add(job.bytes)
			select {
			case worker.jobs <- job:
				worker.scheduledBytes.Add(job.bytes)
				s.cursor = worker.pipe.index % len(s.workers)
				return nil
			default:
				worker.pendingBytes.Add(-job.bytes)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ctx.Done():
			if err := s.error(); err != nil {
				return err
			}
			return s.ctx.Err()
		case <-s.available:
		}
	}
}

func (s *parallelChunkSender) orderedWorkers() []*chunkPathWorker {
	workers := append([]*chunkPathWorker(nil), s.workers...)
	weights := livePathWeights(workers)
	start := s.cursor
	sort.SliceStable(workers, func(i, j int) bool {
		leftWeight := weights[workers[i]]
		rightWeight := weights[workers[j]]
		leftScore := workerSchedulingScore(workers[i], leftWeight)
		rightScore := workerSchedulingScore(workers[j], rightWeight)
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		leftRank := (workers[i].pipe.index - 1 - start + len(s.workers)) % len(s.workers)
		rightRank := (workers[j].pipe.index - 1 - start + len(s.workers)) % len(s.workers)
		return leftRank < rightRank
	})
	return workers
}

func workerSchedulingScore(worker *chunkPathWorker, weight float64) float64 {
	weight = clampPathWeight(weight)
	scheduled := worker.scheduledBytes.Load()
	pending := worker.pendingBytes.Load()
	return float64(scheduled+pending*2) / weight
}

func livePathWeights(workers []*chunkPathWorker) map[*chunkPathWorker]float64 {
	weights := make(map[*chunkPathWorker]float64, len(workers))
	var rateTotal float64
	var rateCount int
	rates := make(map[*chunkPathWorker]float64, len(workers))
	for _, worker := range workers {
		nanos := worker.sendNanos.Load()
		bytes := worker.sentBytes.Load()
		if nanos <= 0 || bytes <= 0 {
			continue
		}
		rate := float64(bytes) / (float64(nanos) / float64(time.Second))
		rates[worker] = rate
		rateTotal += rate
		rateCount++
	}
	averageRate := 0.0
	if rateCount > 0 {
		averageRate = rateTotal / float64(rateCount)
	}
	for _, worker := range workers {
		history := clampPathWeight(worker.historyWeight)
		rate := rates[worker]
		if averageRate <= 0 || rate <= 0 {
			weights[worker] = history
			continue
		}
		live := clampPathWeight(rate / averageRate)
		alpha := math.Min(float64(worker.sentChunks.Load())/16, 0.75)
		weights[worker] = clampPathWeight(history*(1-alpha) + live*alpha)
	}
	return weights
}

func clampPathWeight(weight float64) float64 {
	if weight < 0.5 {
		return 0.5
	}
	if weight > 2 {
		return 2
	}
	return weight
}

func (s *parallelChunkSender) runWorker(worker *chunkPathWorker) {
	defer s.workerWG.Done()
	for job := range worker.jobs {
		select {
		case s.available <- struct{}{}:
		default:
		}
		started := time.Now()
		err := s.ctx.Err()
		if err == nil {
			err = worker.pipe.sendMessage(s.ctx, job.message)
		}
		elapsed := time.Since(started)
		worker.pendingBytes.Add(-job.bytes)
		if err != nil {
			s.recordError(err)
			continue
		}
		worker.sentBytes.Add(job.bytes)
		worker.sentChunks.Add(1)
		worker.sendNanos.Add(int64(elapsed))
	}
}

func (s *parallelChunkSender) recordError(err error) {
	if err == nil {
		return
	}
	s.errMu.Lock()
	if s.firstErr == nil {
		s.firstErr = err
		s.cancel()
	}
	s.errMu.Unlock()
}

func (s *parallelChunkSender) error() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.firstErr
}

func (s *parallelChunkSender) Close() error {
	return s.finishWorkers(false)
}

func (s *parallelChunkSender) Abort() {
	_ = s.finishWorkers(true)
}

func (s *parallelChunkSender) finishWorkers(abort bool) error {
	s.finish.Do(func() {
		if abort {
			s.cancel()
		}
		for _, worker := range s.workers {
			close(worker.jobs)
		}
		s.workerWG.Wait()
		s.cancel()
		s.finishErr = s.error()
	})
	return s.finishErr
}

func (s *parallelChunkSender) Stats() []chunkPathStats {
	stats := make([]chunkPathStats, 0, len(s.workers))
	for _, worker := range s.workers {
		stats = append(stats, chunkPathStats{
			Connection: worker.pipe.index,
			Bytes:      worker.sentBytes.Load(),
			Chunks:     worker.sentChunks.Load(),
			SendTime:   time.Duration(worker.sendNanos.Load()),
		})
	}
	return stats
}

func physicalPathStats(stats []chunkPathStats) []transport.PhysicalPathStats {
	out := make([]transport.PhysicalPathStats, 0, len(stats))
	for _, stat := range stats {
		out = append(out, transport.PhysicalPathStats{
			Connection: stat.Connection,
			SentBytes:  stat.Bytes,
			SentChunks: stat.Chunks,
			SendNanos:  int64(stat.SendTime),
		})
	}
	return out
}
