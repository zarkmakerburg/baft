package session

import (
	"context"
	"errors"
	"io"
	"sync"
)

// receiveRing is the concrete receive-memory backing for the TWRL design.
// Bytes remain in the ring until the target socket has actually accepted them.
type receiveRing struct {
	mu     sync.Mutex
	buf    []byte
	head   int
	tail   int
	size   int
	closed bool
	wake   chan struct{}
}

func newReceiveRing(capacity int) (*receiveRing, error) {
	if capacity <= 0 {
		return nil, errors.New("receive ring capacity must be positive")
	}
	return &receiveRing{buf: make([]byte, capacity), wake: make(chan struct{})}, nil
}

func (r *receiveRing) Capacity() int { return len(r.buf) }

func (r *receiveRing) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

func (r *receiveRing) Write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return io.ErrClosedPipe
	}
	if len(p) > len(r.buf)-r.size {
		return errors.New("receive ring capacity exceeded")
	}
	first := len(p)
	if remain := len(r.buf)-r.tail; first > remain {
		first = remain
	}
	copy(r.buf[r.tail:r.tail+first], p[:first])
	second := len(p)-first
	if second > 0 {
		copy(r.buf[:second], p[first:])
	}
	r.tail = (r.tail + len(p)) % len(r.buf)
	r.size += len(p)
	r.signalLocked()
	return nil
}

// Peek returns a stable slice into the ring. The writer is the sole consumer;
// the bytes stay accounted and cannot be overwritten until Consume is called.
func (r *receiveRing) Peek(ctx context.Context, max int) ([]byte, error) {
	if max <= 0 {
		return nil, errors.New("peek size must be positive")
	}
	for {
		r.mu.Lock()
		if r.size > 0 {
			n := r.size
			if n > max {
				n = max
			}
			if contiguous := len(r.buf)-r.head; n > contiguous {
				n = contiguous
			}
			out := r.buf[r.head : r.head+n]
			r.mu.Unlock()
			return out, nil
		}
		if r.closed {
			r.mu.Unlock()
			return nil, io.EOF
		}
		wake := r.wake
		r.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func (r *receiveRing) Consume(n int) error {
	if n <= 0 {
		return errors.New("consume size must be positive")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > r.size {
		return errors.New("consume exceeds buffered bytes")
	}
	r.head = (r.head + n) % len(r.buf)
	r.size -= n
	if r.size == 0 {
		r.head = r.tail
	}
	return nil
}

func (r *receiveRing) Close() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.wake)
	}
	r.mu.Unlock()
}

func (r *receiveRing) signalLocked() {
	if r.closed {
		return
	}
	close(r.wake)
	r.wake = make(chan struct{})
}
