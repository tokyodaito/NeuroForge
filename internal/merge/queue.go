package merge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"neuroforge/internal/adapter/vcs"
	"neuroforge/internal/audit"
	"neuroforge/internal/policy"
)

// Queue is the deterministic merge queue (spec §28, M11-5/M11-6). It serialises
// merge operations so that branch-currency checks (the §28 branch_current gate)
// hold at execution time, not just at decision time: between a Governor
// ALLOW_MERGE decision and the actual merge, the target may have advanced. The
// queue processes items in FIFO order and, for each item:
//
//  1. re-validates branch currency via the provider (GetChecks / a head probe);
//     if the branch fell behind, the item is returned as REQUIRE_REBASE;
//  2. performs the merge through the Authority (the only merge-authority path);
//  3. on a remote-merge failure (e.g. checks went red, or the platform refuses),
//     optionally falls back to a LOCAL merge via the configured local-git
//     provider when policy permits the §5.1 R5 local-merge mode.
//
// Determinism (rule §36.6): same items in ⇒ same order out ⇒ same outcomes.
// The queue is single-threaded by construction (a serial processing loop).
type Queue struct {
	auth   *Authority
	local  vcs.ChangeRequestProvider // local-git provider for the fallback
	logger *slog.Logger

	mu     sync.Mutex
	items  []QueueItem
	notify chan struct{}
	closed bool
}

// QueueItem is one queued merge.
type QueueItem struct {
	TaskID   string
	Decision Result
	Policy   policy.Resolved
	Provider vcs.ChangeRequestProvider
	Request  vcs.MergeRequest
	// AllowLocalFallback permits a local merge if the remote merge fails AND
	// policy allows the local-merge mode (§5.1 R5: merge=true while
	// change_request.create=false). The queue consults [CanLocalMergeMode].
	AllowLocalFallback bool
}

// QueueOutcome is the per-item result of processing.
type QueueOutcome struct {
	Item      QueueItem
	Merged    bool
	Method    vcs.MergeMethod
	CommitSHA string
	// FellBack reports whether the local merge fallback was used.
	FellBack bool
	// Err is non-nil when the merge could not be completed.
	Err error
}

// NewQueue creates a merge queue. localProvider may be nil when local fallback
// is not desired.
func NewQueue(auth *Authority, localProvider vcs.ChangeRequestProvider, logger *slog.Logger) *Queue {
	if logger == nil {
		logger = slog.New(discardHandler())
	}
	return &Queue{
		auth:   auth,
		local:  localProvider,
		logger: logger,
		notify: make(chan struct{}, 1),
	}
}

// Enqueue appends an item. It refuses items whose Decision is not ALLOW_MERGE
// (the Governor must have authorised merge before queuing). It is safe for
// concurrent use.
func (q *Queue) Enqueue(item QueueItem) error {
	if q.auth == nil {
		return errors.New("merge: queue has no authority")
	}
	if item.Decision.Decision != DecisionAllowMerge {
		return fmt.Errorf("merge: cannot enqueue decision %q; only ALLOW_MERGE is queueable", item.Decision.Decision)
	}
	if item.Provider == nil {
		return errors.New("merge: queue item has no provider")
	}

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return errors.New("merge: queue is closed")
	}
	q.items = append(q.items, item)
	q.mu.Unlock()

	select {
	case q.notify <- struct{}{}:
	default:
	}
	return nil
}

// Len returns the number of pending items.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Process drains the queue serially in FIFO order, returning one outcome per
// item. It blocks until all currently-enqueued items are processed or ctx is
// cancelled. This is the deterministic processing path.
func (q *Queue) Process(ctx context.Context) ([]QueueOutcome, error) {
	var out []QueueOutcome
	for {
		item, ok := q.dequeue()
		if !ok {
			return out, nil
		}
		oc, err := q.processOne(ctx, item)
		if err != nil {
			return out, err
		}
		out = append(out, oc)
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
	}
}

// ProcessOne processes exactly the first item, if any. Useful for tests and for
// step-driven queues.
func (q *Queue) ProcessOne(ctx context.Context) (QueueOutcome, bool, error) {
	item, ok := q.dequeue()
	if !ok {
		return QueueOutcome{}, false, nil
	}
	oc, err := q.processOne(ctx, item)
	return oc, true, err
}

func (q *Queue) dequeue() (QueueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return QueueItem{}, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	return item, true
}

// processOne merges a single item deterministically.
func (q *Queue) processOne(ctx context.Context, item QueueItem) (QueueOutcome, error) {
	oc := QueueOutcome{Item: item}

	// The Authority re-checks the Governor decision + policy + network lock.
	res, err := q.auth.Merge(ctx, item.Decision, item.Policy, item.Provider, item.Request)
	if err == nil {
		oc.Merged = res.Merged
		oc.Method = res.Method
		oc.CommitSHA = res.CommitSHA
		return oc, nil
	}
	oc.Err = err

	// Branch-not-current → do not fall back; surface for rebase.
	if errors.Is(err, vcs.ErrBranchNotCurrent) {
		return oc, nil
	}

	// Local fallback: only when the remote provider is a network provider, the
	// item allows fallback, policy permits local-merge mode, and a local
	// provider is configured.
	if item.AllowLocalFallback && q.local != nil && item.Provider.Capabilities().IsNetwork {
		if CanLocalMergeMode(item.Policy) {
			localReq := item.Request
			localReq.Number = 0 // local merge has no PR/MR number
			q.logger.Info("merge queue: remote merge failed, falling back to local merge",
				"task", item.TaskID, "remote_err", err)
			lres, lerr := q.auth.Merge(ctx, item.Decision, item.Policy, q.local, localReq)
			if lerr == nil {
				oc.Merged = lres.Merged
				oc.Method = lres.Method
				oc.CommitSHA = lres.CommitSHA
				oc.FellBack = true
				oc.Err = nil
				return oc, nil
			}
			oc.Err = fmt.Errorf("remote merge: %v; local fallback: %v", err, lerr)
		}
	}
	return oc, nil
}

// Close stops accepting new items. Pending items remain processable.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
}

// CanLocalMergeMode reports whether the resolved policy permits the §5.1 R5
// "local merge" mode: merge=true while change_request.create=false. In this mode
// the merge queue may merge locally into the user's checkout instead of via a
// remote PR/MR. This is the ONLY configuration where a merge proceeds without a
// remote change request.
func CanLocalMergeMode(res policy.Resolved) bool {
	p := res.Pipeline
	return p.Merge && !p.ChangeRequest.Create
}

// recordQueueAudit is a hook for the queue to record its own outcomes. The
// Authority already audits each provider call, so this is intentionally minimal
// (records the fallback decision).
func (q *Queue) recordQueueAudit(ctx context.Context, rec *audit.Recorder, taskID string, oc QueueOutcome) {
	if rec == nil || !oc.FellBack {
		return
	}
	_, _ = rec.Record(ctx, audit.Event{
		Type:    "merge.queue.fallback",
		Scope:   audit.ScopeTask,
		ScopeID: taskID,
		Actor:   audit.ActorDaemon,
		Payload: audit.Payload("local_merge", true, "task", taskID, "time", time.Now().UTC().Format(time.RFC3339)),
	})
}
