package mevshare

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/flashbots/mev-share-node/simqueue"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

var consumeSimulationTimeout = 5 * time.Second

type SimQueue struct {
	log            *zap.Logger
	queue          simqueue.Queue
	eth            EthClient
	workers        []SimulationWorker
	workersPerNode int
}

func NewQueue(log *zap.Logger, queue simqueue.Queue, eth EthClient, sim []SimulationBackend, simRes SimulationResult, workersPerNode int, backgroundWg *sync.WaitGroup) *SimQueue {
	log = log.Named("queue")
	q := &SimQueue{
		log:            log,
		queue:          queue,
		eth:            eth,
		workers:        make([]SimulationWorker, 0, len(sim)),
		workersPerNode: workersPerNode,
	}

	for i, s := range sim {
		worker := SimulationWorker{
			log:               log.Named("worker").With(zap.Int("worker-id", i)),
			simulationBackend: s,
			simRes:            simRes,
			backgroundWg:      backgroundWg,
		}
		q.workers = append(q.workers, worker)
	}
	return q
}

func (q *SimQueue) Start(ctx context.Context) *sync.WaitGroup {
	process := make([]simqueue.ProcessFunc, 0, len(q.workers)*q.workersPerNode)
	for _, w := range q.workers {
		if q.workersPerNode > 1 {
			workers := simqueue.MultipleWorkers(w.Process, q.workersPerNode, rate.Inf, 1)[0]
			process = append(process, workers)
		} else {
			process = append(process, w.Process)
		}
	}
	blockNumber, err := q.eth.BlockNumber(ctx)
	if err != nil {
		q.log.Warn("Failed to get block number", zap.Error(err))
	} else {
		_ = q.queue.UpdateBlock(blockNumber)
	}

	wg := q.queue.StartProcessLoop(ctx, process)

	wg.Add(1)
	go func() {
		defer wg.Done()

		back := backoff.NewExponentialBackOff()
		back.MaxInterval = 3 * time.Second
		back.MaxElapsedTime = 12 * time.Second

		ticker := time.NewTicker(100 * time.Millisecond)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := backoff.Retry(func() error {
					blockNumber, err := q.eth.BlockNumber(ctx)
					if err != nil {
						return err
					}
					return q.queue.UpdateBlock(blockNumber)
				}, back)
				if err != nil {
					q.log.Error("Failed to update block number", zap.Error(err))
				}
			}
		}
	}()
	return wg
}

func (q *SimQueue) ScheduleBundleSimulation(ctx context.Context, bundle *SendMevBundleArgs, highPriority bool) error {
	data, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	return q.queue.Push(ctx, data, highPriority, uint64(bundle.Inclusion.BlockNumber), uint64(bundle.Inclusion.MaxBlock))
}

type SimulationWorker struct {
	log               *zap.Logger
	simulationBackend SimulationBackend
	simRes            SimulationResult
	backgroundWg      *sync.WaitGroup
}

func (w *SimulationWorker) Process(ctx context.Context, data []byte) error {
	var bundle SendMevBundleArgs
	err := json.Unmarshal(data, &bundle)
	if err != nil {
		w.log.Error("Failed to unmarshal bundle simulation data", zap.Error(err))
		// we don't want to retry after such error
		return nil //nolint:nilerr
	}

	result, err := w.simulationBackend.SimulateBundle(ctx, &bundle, nil)
	if err != nil {
		w.log.Error("Failed to simulate matched bundle", zap.Error(err))
		return err
	}

	w.backgroundWg.Add(1)
	go func() {
		defer w.backgroundWg.Done()
		resCtx, cancel := context.WithTimeout(context.Background(), consumeSimulationTimeout)
		defer cancel()
		err = w.simRes.SimulatedBundle(resCtx, &bundle, result)
		if err != nil {
			w.log.Error("Failed to consume matched share bundle", zap.Error(err))
		}
	}()
	return nil
}
