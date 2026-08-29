package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dbOpTimeout bounds the claim/release Postgres round-trips themselves —
// independent of a target's own configured check timeout, which governs
// only check execution (ADR-0004: per-check timeout is a distinct config
// value from anything else).
const dbOpTimeout = 5 * time.Second

// Scheduler runs ADR-0001's claim/release lease cycle on ADR-0004's bounded
// worker pool.
type Scheduler struct {
	pool    *pgxpool.Pool
	cfg     Config
	ownerID string
	jobs    chan CheckJob
	wg      sync.WaitGroup
	logger  *slog.Logger
}

// New constructs a Scheduler against the given pool. It starts no
// goroutines — call Run for that.
func New(pool *pgxpool.Pool, cfg Config, logger *slog.Logger) (*Scheduler, error) {
	owner, err := newOwnerID()
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		pool:    pool,
		cfg:     cfg,
		ownerID: owner,
		jobs:    make(chan CheckJob, cfg.WorkerPoolSize),
		logger:  logger,
	}, nil
}

// OwnerID exposes the process's lease-owner identifier — e.g. for the
// self-observability labeling ADR-0001's Consequences section names (a
// gauge of currently-held leases per process).
func (s *Scheduler) OwnerID() string { return s.ownerID }

// Run starts the worker pool and the tick loop, and blocks until ctx is
// canceled. Shutdown then proceeds exactly as ADR-0004 specifies: the tick
// loop stops issuing new ticks/claims immediately; already-claimed workers
// keep running under their own per-check timeout (never ctx) up to
// cfg.HardShutdownDeadline, past which they are force-canceled.
func (s *Scheduler) Run(ctx context.Context) error {
	// Deliberately detached from ctx: ADR-0004 requires an in-flight check
	// to outlive rootCtx's cancellation, governed only by its own per-check
	// timeout and this hard-deadline force-cancel — never by shutdown
	// itself starting.
	drainCtx, drainCancel := context.WithCancel(context.Background()) //nolint:contextcheck // intentional detach from rootCtx, see ADR-0004
	defer drainCancel()

	s.wg.Add(s.cfg.WorkerPoolSize)
	for range s.cfg.WorkerPoolSize {
		go s.worker(drainCtx) //nolint:contextcheck // drainCtx is the intentional detach from rootCtx above, see ADR-0004
	}

	ticker := time.NewTicker(s.cfg.TickInterval)
	defer ticker.Stop()

tickLoop:
	for {
		select {
		case <-ctx.Done():
			break tickLoop
		case <-ticker.C:
			s.tick(ctx)
		}
	}

	close(s.jobs) // no more dispatch; workers still drain any already-buffered jobs safely (each is just a claim attempt)

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(s.cfg.HardShutdownDeadline):
		s.logger.Warn("hard shutdown deadline reached; force-canceling in-flight checks", "owner_id", s.ownerID)
		drainCancel()
		<-done
		return nil
	}
}

// tick is the single scheduling pass this ADR-0001 property depends on
// being the ONLY code path: Run's ticker loop calls it on every ordinary
// tick, and the restart-recovery test calls it directly to prove the first
// post-restart tick needs no special-cased recovery logic — there is no
// separate "recover" function anywhere in this package.
func (s *Scheduler) tick(ctx context.Context) {
	jobs, err := s.dueTargets(ctx)
	if err != nil {
		s.logger.Error("due-target scan failed", "error", err)
		return
	}

	for _, job := range jobs {
		select {
		case s.jobs <- job:
		case <-ctx.Done():
			// Shutdown began mid-dispatch: any remaining due targets this
			// tick found are simply picked up by a later tick or process —
			// no claim was ever attempted for them, so there is nothing to
			// undo.
			return
		}
	}
}

// dueTargets runs the due-set scan ADR-0001 and ADR-0004 both depend on: the
// same indexed query on every tick, including the first tick after a
// restart — next_due_at <= now() already includes targets whose lease
// expired while an old process held it, with no separate query needed.
func (s *Scheduler) dueTargets(ctx context.Context) ([]CheckJob, error) {
	const stmt = `
SELECT ts.target_id::text, t.type, t.url_or_host, t.port, t.body_match_pattern, t.timeout_seconds, t.interval_seconds
FROM target_schedule ts
JOIN targets t ON t.id = ts.target_id
WHERE ts.next_due_at <= now()
  AND t.deleted_at IS NULL
  AND t.agent_id IS NULL
ORDER BY ts.next_due_at`

	rows, err := s.pool.Query(ctx, stmt)
	if err != nil {
		return nil, fmt.Errorf("due-target scan: %w", err)
	}
	defer rows.Close()

	var jobs []CheckJob
	for rows.Next() {
		var job CheckJob
		if err := rows.Scan(
			&job.TargetID, &job.Type, &job.URLOrHost, &job.Port,
			&job.BodyMatchPattern, &job.TimeoutSeconds, &job.IntervalSeconds,
		); err != nil {
			return nil, fmt.Errorf("scan due target row: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("due-target scan: %w", err)
	}
	return jobs, nil
}

// worker is one of the pool's long-lived goroutines (ADR-0004: "N goroutines
// started once at process startup"). It ranges over the jobs channel until
// Run closes it, so wg.Wait() blocks until every worker has finished
// whatever job it was on when shutdown began.
func (s *Scheduler) worker(drainCtx context.Context) {
	defer s.wg.Done()
	for job := range s.jobs {
		s.handleJob(drainCtx, job)
	}
}

// handleJob is the claim -> execute -> release cycle a worker runs for one
// dispatched job. A failed claim (zero rows) is the normal, silent skip
// ADR-0001/ADR-0004 both name — never logged as an error, since it is the
// expected outcome of the exact race NFR-007 exists to make harmless.
func (s *Scheduler) handleJob(drainCtx context.Context, job CheckJob) {
	leaseDuration := time.Duration(job.TimeoutSeconds)*time.Second + s.cfg.LeaseSafetyMargin

	claimCtx, claimCancel := context.WithTimeout(drainCtx, dbOpTimeout)
	defer claimCancel()
	claimed, err := claimLease(claimCtx, s.pool, job.TargetID, s.ownerID, leaseDuration)
	if err != nil {
		s.logger.Error("claim failed", "target_id", job.TargetID, "error", err)
		return
	}
	if !claimed {
		return
	}

	checkedAt := time.Now()
	checkCtx, checkCancel := context.WithTimeout(drainCtx, time.Duration(job.TimeoutSeconds)*time.Second)
	outcome := executeCheck(checkCtx, job)
	checkCancel()

	releaseCtx, releaseCancel := context.WithTimeout(drainCtx, dbOpTimeout)
	defer releaseCancel()
	if err := releaseAndRecord(releaseCtx, s.pool, job, checkedAt, outcome); err != nil {
		s.logger.Error("release failed", "target_id", job.TargetID, "error", err)
	}
}
