package vproc

import (
	"sync"
	"time"
)

// coalescer delivers at most one value per interval, and always the newest.
//
// A proc reporting its progress is describing a state, not recording an
// event: two scores a millisecond apart do not both need to arrive, and the
// older one is worthless the moment the newer exists. So a value offered
// during a wait replaces the one before it rather than queueing behind it,
// which is what lets a caller in a tight inner loop cost the same as one in a
// slow outer loop and frees applications from rate-limiting by hand.
type coalescer struct {
	interval time.Duration
	send     func(scoreVal)

	mu     sync.Mutex
	latest scoreVal
	fresh  bool

	wake chan struct{}
	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

func newCoalescer(interval time.Duration, send func(scoreVal)) *coalescer {
	c := &coalescer{
		interval: interval,
		send:     send,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	c.wg.Go(c.run)
	return c
}

// offer records a value to be sent. It never blocks.
func (c *coalescer) offer(v scoreVal) {
	c.mu.Lock()
	c.latest, c.fresh = v, true
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default: // a send is already pending and will pick this up
	}
}

// close stops delivery and waits for any send in flight.
func (c *coalescer) close() {
	c.once.Do(func() { close(c.done) })
	c.wg.Wait()
}

// run sends immediately, then holds off for the interval. The first value is
// not delayed -- a proc that reports once and works for a minute should not
// look silent for the first hundred milliseconds of it.
func (c *coalescer) run() {
	for {
		select {
		case <-c.wake:
		case <-c.done:
			return
		}

		c.mu.Lock()
		v, ok := c.latest, c.fresh
		c.fresh = false
		c.mu.Unlock()
		if ok {
			c.send(v)
		}

		t := time.NewTimer(c.interval)
		select {
		case <-t.C:
		case <-c.done:
			t.Stop()
			return
		}
	}
}
