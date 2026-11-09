package sqlite

import (
	"context"
	"sync"
	"time"
)

const (
	maxWriteJobs  = 64
	maxWriteBytes = 16 << 20
	maxPerSource  = 32
	admitWait     = 100 * time.Millisecond

	classIngest = 0
	classMaint  = 1
)

// writeSched is the single writer. Handlers never start their own transactions.
// The queue admits at most 64 jobs and 16MiB of payload, and only waits
// admitWait for a slot. A source that already holds maxPerSource jobs is not
// admitted while another source is queued. A pending maintenance job runs
// after at most 10 ingest jobs.
type writeSched struct {
	mu          sync.Mutex
	cond        *sync.Cond
	ingest      map[string][]*writeJob
	rr          []string
	rrPos       int
	maint       []*writeJob
	perSource   map[string]int
	sinceMaint  int
	queued      int
	queuedBytes int
	closed      bool
	done        chan struct{}
}

type writeJob struct {
	source string
	nbytes int
	class  int
	ctx    context.Context
	fn     func(context.Context) error
	err    error
	done   chan struct{}
}

func (s *Store) startSched() {
	sc := &writeSched{
		ingest:    map[string][]*writeJob{},
		perSource: map[string]int{},
		done:      make(chan struct{}),
	}
	sc.cond = sync.NewCond(&sc.mu)
	s.sched = sc
	go s.writerLoop()
}

func (sc *writeSched) stop() {
	sc.mu.Lock()
	sc.closed = true
	sc.cond.Broadcast()
	sc.mu.Unlock()
	<-sc.done
}

func (s *Store) writerLoop() {
	defer close(s.sched.done)
	for {
		s.sched.mu.Lock()
		for s.sched.queued == 0 && !s.sched.closed {
			s.sched.cond.Wait()
		}
		if s.sched.queued == 0 && s.sched.closed {
			s.sched.mu.Unlock()
			return
		}
		job := s.sched.popLocked()
		s.sched.cond.Broadcast()
		s.sched.mu.Unlock()
		if job == nil {
			continue
		}
		if job.ctx.Err() != nil {
			job.err = job.ctx.Err()
		} else {
			job.err = job.fn(job.ctx)
		}
		close(job.done)
	}
}

func (s *Store) submit(ctx context.Context, source string, nbytes, class int, fn func(context.Context) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if nbytes < 1 {
		nbytes = 1
	}
	job := &writeJob{
		source: source,
		nbytes: nbytes,
		class:  class,
		ctx:    ctx,
		fn:     fn,
		done:   make(chan struct{}),
	}
	waitCtx, cancel := context.WithTimeout(ctx, admitWait)
	defer cancel()
	go func() {
		<-waitCtx.Done()
		s.sched.mu.Lock()
		s.sched.cond.Broadcast()
		s.sched.mu.Unlock()
	}()

	s.sched.mu.Lock()
	for {
		if s.sched.closed {
			s.sched.mu.Unlock()
			return ErrStorage
		}
		if s.sched.canAdmitLocked(job) {
			s.sched.enqueueLocked(job)
			s.sched.cond.Broadcast()
			s.sched.mu.Unlock()
			select {
			case <-job.done:
				return job.err
			case <-ctx.Done():
			}
			select {
			case <-job.done:
				return job.err
			default:
				return ctx.Err()
			}
		}
		if waitCtx.Err() != nil {
			s.sched.mu.Unlock()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrBusy
		}
		s.sched.cond.Wait()
	}
}

func (sc *writeSched) canAdmitLocked(job *writeJob) bool {
	if sc.queued >= maxWriteJobs {
		return false
	}
	if job.nbytes > maxWriteBytes || sc.queuedBytes+job.nbytes > maxWriteBytes {
		return false
	}
	if job.class == classIngest {
		others := 0
		for src, n := range sc.perSource {
			if src != job.source {
				others += n
			}
		}
		if others > 0 && sc.perSource[job.source] >= maxPerSource {
			return false
		}
	}
	return true
}

func (sc *writeSched) enqueueLocked(job *writeJob) {
	sc.queued++
	sc.queuedBytes += job.nbytes
	if job.class == classMaint {
		sc.maint = append(sc.maint, job)
		return
	}
	sc.perSource[job.source]++
	if _, ok := sc.ingest[job.source]; !ok {
		sc.rr = append(sc.rr, job.source)
	}
	sc.ingest[job.source] = append(sc.ingest[job.source], job)
}

func (sc *writeSched) popLocked() *writeJob {
	if len(sc.maint) > 0 && (sc.sinceMaint >= 10 || !sc.hasIngestLocked()) {
		job := sc.maint[0]
		sc.maint = sc.maint[1:]
		sc.sinceMaint = 0
		sc.accountPop(job)
		return job
	}
	job := sc.popIngestLocked()
	if job == nil {
		return nil
	}
	if sc.sinceMaint < 10 {
		sc.sinceMaint++
	}
	sc.accountPop(job)
	return job
}

func (sc *writeSched) hasIngestLocked() bool {
	return len(sc.rr) > 0
}

func (sc *writeSched) popIngestLocked() *writeJob {
	if len(sc.rr) == 0 {
		return nil
	}
	n := len(sc.rr)
	for i := 0; i < n; i++ {
		idx := sc.rrPos % len(sc.rr)
		src := sc.rr[idx]
		q := sc.ingest[src]
		if len(q) == 0 {
			sc.rrPos++
			continue
		}
		job := q[0]
		q = q[1:]
		if len(q) == 0 {
			delete(sc.ingest, src)
			sc.rr = append(sc.rr[:idx], sc.rr[idx+1:]...)
			if len(sc.rr) == 0 {
				sc.rrPos = 0
			} else {
				sc.rrPos = idx % len(sc.rr)
			}
		} else {
			sc.ingest[src] = q
			sc.rrPos = (idx + 1) % len(sc.rr)
		}
		return job
	}
	return nil
}

func (sc *writeSched) accountPop(job *writeJob) {
	sc.queued--
	sc.queuedBytes -= job.nbytes
	if sc.queuedBytes < 0 {
		sc.queuedBytes = 0
	}
	if job.class != classIngest {
		return
	}
	sc.perSource[job.source]--
	if sc.perSource[job.source] <= 0 {
		delete(sc.perSource, job.source)
	}
}
