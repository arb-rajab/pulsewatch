package scheduler

import "time"

// CheckJob is what a scheduler tick pushes onto the worker pool's channel.
// Per ADR-0004, a channel send only means "this target looked due when the
// tick ran" — the worker still has to win the ADR-0001 lease claim before
// this job represents real, exclusive work.
type CheckJob struct {
	TargetID         string
	Type             string // "http" | "tcp"
	URLOrHost        string
	Port             *int32
	BodyMatchPattern *string
	TimeoutSeconds   int
	IntervalSeconds  int
}

// checkOutcome is the result of actually executing a claimed check — the
// input to the check_results INSERT in the release transaction.
type checkOutcome struct {
	success                    bool
	latency                    time.Duration
	statusCode                 *int32
	failureReason              *string // timeout | refused | status_mismatch | body_mismatch
	bodyMatchFragment          *string
	bodyMatchFragmentTruncated bool
}
