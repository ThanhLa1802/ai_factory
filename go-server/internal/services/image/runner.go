package image

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Runner defaults. The interval bounds how long a queued job waits before it is
// picked up; the lease bounds how long a running job may go without progress
// before another node reclaims it (crash recovery).
const (
	runnerInterval = 5 * time.Second
	jobLease       = 10 * time.Minute
	runnerBatch    = 4
)

// Runner executes queued async image jobs in the background. It is started only
// on API nodes (services.api + services.image), mirroring the usage roller.
//
// Claim is atomic (FOR UPDATE SKIP LOCKED), so several API replicas may run a
// Runner concurrently without processing the same job twice. A job abandoned by
// a crashed node is re-claimed once its lease lapses; progress_step is
// preserved, so the resumed work continues from where it stopped.
type Runner struct {
	svc      *Service
	log      *slog.Logger
	interval time.Duration
	lease    time.Duration
	batch    int

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewRunner builds a job runner over the image service.
func NewRunner(svc *Service, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{svc: svc, log: log, interval: runnerInterval, lease: jobLease, batch: runnerBatch}
}

// Start begins polling in the background until ctx is cancelled or Close is
// called. Safe to call once.
func (r *Runner) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			if n, err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
				r.log.Error("image job runner", "err", err)
			} else if n > 0 {
				r.log.Info("image jobs processed", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// RunOnce claims and processes one batch of jobs. Exported so tests can run it
// deterministically. It returns the number of jobs it finished.
func (r *Runner) RunOnce(ctx context.Context) (int, error) {
	jobs, err := r.svc.repos.Jobs.Claim(ctx, r.batch, time.Now().UTC().Add(-r.lease))
	if err != nil {
		return 0, err
	}
	finished := 0
	for i := range jobs {
		if ctx.Err() != nil {
			return finished, ctx.Err()
		}
		job := jobs[i]
		if err := r.svc.ExecuteJob(ctx, job); err != nil {
			if ctx.Err() != nil {
				// Shutdown interrupted generation: leave the job running so a
				// restart re-claims it after the lease instead of failing it.
				r.log.Warn("image job interrupted; will resume after lease", "job", job.ID)
				return finished, ctx.Err()
			}
			r.log.Error("image job failed", "job", job.ID, "err", err)
			if ferr := r.svc.repos.Jobs.Finish(ctx, job.ID, JobFailed, err.Error()); ferr != nil {
				r.log.Error("mark image job failed", "job", job.ID, "err", ferr)
			}
			finished++
			continue
		}
		if err := r.svc.repos.Jobs.Finish(ctx, job.ID, JobSucceeded, ""); err != nil {
			r.log.Error("mark image job succeeded", "job", job.ID, "err", err)
		}
		finished++
	}
	return finished, nil
}

// Close stops the background loop (DI Closer lifecycle).
func (r *Runner) Close() error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}

// isTerminalJob reports whether a job status is final (no further progress).
func isTerminalJob(status string) bool {
	switch status {
	case JobSucceeded, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}
