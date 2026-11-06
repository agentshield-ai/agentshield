package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Read responses are built in memory from the rows a request loads (a list
// of spans, objects, table rows, feedback), so every read request is metered
// (store.ReadMeter) against a server-wide response budget, as request
// bodies are against the body budget (otlp/limits.go):
//
//   - Each row a read loads reserves respMemFactor times its stored size
//     (decoded values plus the response being written) until the request
//     ends; a request waits for budget held by others (then 503).
//   - List and query routes refuse a response over maxListResponseBytes
//     stored (400, "lower limit"), before loading it where the page's size
//     can be read first (spans, objects).
//   - Streams and exports release what a page held once it is written
//     (readPageDone), so they run in bounded memory however long they are.
const (
	// DefaultResponseBudget is the default server-wide response budget.
	DefaultResponseBudget = 512 << 20
	// respMemFactor: memory a loaded row costs per byte stored.
	respMemFactor = 2
	// defaultMaxListResponseBytes bounds the stored size of the rows one
	// list or query response holds (maxListResponseBytes; tests lower it).
	defaultMaxListResponseBytes = 64 << 20
)

var maxListResponseBytes int64 = defaultMaxListResponseBytes

// respMeter meters one read request (store.ReadMeter).
type respMeter struct {
	mu     sync.Mutex
	budget *otlp.BodyBudget
	hold   func(*otlp.Reservation) // keeps a reservation until the request ends
	res    *otlp.Reservation
	held   int64 // bytes reserved since the last page
	credit int64 // reserved ahead (reserve) and not yet read
	read   int64 // stored bytes read since the last page
	max    int64 // stored-byte cap (0: what the budget holds)
	hint   string
}

func (s *Server) newRespMeter(hold func(*otlp.Reservation)) *respMeter {
	m := &respMeter{budget: s.respBudget, hold: hold}
	m.res = m.budget.NewReservation()
	hold(m.res)
	return m
}

// Read implements store.ReadMeter.
func (m *respMeter) Read(ctx context.Context, n int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.read += n
	if m.max > 0 && m.read > m.max {
		return tooLargeResponse(m.max, m.hint)
	}
	cost := n * respMemFactor
	if m.credit >= cost {
		m.credit -= cost
		return nil
	}
	cost -= m.credit
	m.credit = 0
	return m.grow(ctx, cost)
}

func (m *respMeter) grow(ctx context.Context, cost int64) error {
	if limit := m.budget.Limit(); m.held+cost > limit {
		return &overBudgetError{store.Invalidf("the response would need more than the server's response budget (%d MiB): lower limit", limit>>20)}
	}
	if err := m.res.Grow(ctx, cost); err != nil {
		var be *otlp.BodyError
		if errors.As(err, &be) {
			return &requestError{code: http.StatusServiceUnavailable, msg: "server busy answering other large queries; retry later"}
		}
		return err
	}
	m.held += cost
	return nil
}

// reserve reserves the memory of n stored bytes the request is about to
// read, all at once (a request does not hold part of the budget while
// waiting for the rest).
func (m *respMeter) reserve(ctx context.Context, n int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.max > 0 && n > m.max {
		return tooLargeResponse(m.max, m.hint)
	}
	cost := n * respMemFactor
	if err := m.grow(ctx, cost); err != nil {
		return err
	}
	m.credit += cost
	return nil
}

// pageDone releases what the request read so far (it was written).
func (m *respMeter) pageDone() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.res.Release()
	m.res = m.budget.NewReservation()
	m.hold(m.res)
	m.held, m.credit, m.read = 0, 0, 0
}

// overBudgetError: one request (or one page of a stream) needs more than
// the whole response budget. Streams retry with smaller pages.
type overBudgetError struct{ err error }

func (e *overBudgetError) Error() string { return e.err.Error() }
func (e *overBudgetError) Unwrap() error { return e.err }

func isOverBudget(err error) bool {
	var ob *overBudgetError
	return errors.As(err, &ob)
}

func tooLargeResponse(max int64, hint string) error {
	msg := fmt.Sprintf("the response would be larger than %d MiB: lower limit", max>>20)
	if hint != "" {
		msg += ", or " + hint
	}
	return store.Invalidf("%s", msg)
}

func meterOf(ctx context.Context) *respMeter {
	m, _ := store.ReadMeterFrom(ctx).(*respMeter)
	return m
}

// limitResponse caps the stored bytes a list or query response may hold
// (hint says what to use instead) and, when size is known (>= 0), checks
// and reserves it before the rows are loaded.
func limitResponse(ctx context.Context, max int64, hint string, size int64) error {
	m := meterOf(ctx)
	if m == nil {
		if size > max {
			return tooLargeResponse(max, hint)
		}
		return nil
	}
	m.mu.Lock()
	m.max, m.hint = max, hint
	m.mu.Unlock()
	if size < 0 {
		return nil
	}
	return m.reserve(ctx, size)
}

// readPageDone tells a stream's meter that a page was written.
func readPageDone(ctx context.Context) {
	if m := meterOf(ctx); m != nil {
		m.pageDone()
	}
}
