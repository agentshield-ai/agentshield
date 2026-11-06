package store

import (
	"context"
	"strconv"
	"strings"
)

// ReadMeter bounds what one request reads from the database into memory:
// list and query reads report the stored size of each row they load (its
// JSON text) before decoding the next, and stop with the meter's error
// (a response too large, or the server's response budget exhausted).
type ReadMeter interface {
	Read(ctx context.Context, bytes int64) error
}

type readMeterKey struct{}

// WithReadMeter returns ctx whose list and query reads report to m.
func WithReadMeter(ctx context.Context, m ReadMeter) context.Context {
	return context.WithValue(ctx, readMeterKey{}, m)
}

// ReadMeterFrom returns ctx's meter, or nil.
func ReadMeterFrom(ctx context.Context) ReadMeter {
	m, _ := ctx.Value(readMeterKey{}).(ReadMeter)
	return m
}

// rowOverhead is the memory a decoded row costs besides its JSON text.
const rowOverhead = 256

func meterRead(ctx context.Context, n int64) error {
	if m := ReadMeterFrom(ctx); m != nil {
		return m.Read(ctx, n+rowOverhead)
	}
	return nil
}

// CallsPageSize is the stored size of the calls a CallsQuery's page holds
// (its limit and offset), read without loading them: a list can be
// refused before it is built.
func (s *Store) CallsPageSize(ctx context.Context, q CallsQuery) (int64, error) {
	pid, err := normalizeProjectID(q.ProjectID)
	if err != nil {
		return 0, err
	}
	where, args, err := whereCalls(pid, q.Filter, q.Query, q.Search, q.IncludeDeleted)
	if err != nil {
		return 0, err
	}
	order, oargs, err := orderCalls(q.SortBy)
	if err != nil {
		return 0, err
	}
	limit := -1
	if q.Limit != nil {
		limit = *q.Limit
	}
	args = append(append(args, oargs...), limit, q.Offset)
	var n int64
	err = s.r.QueryRowContext(ctx, `SELECT COALESCE(SUM(sz), 0) FROM (SELECT c.storage_size + `+strconv.Itoa(rowOverhead)+` AS sz FROM calls c WHERE `+
		where+" ORDER BY "+order+" LIMIT ? OFFSET ?)", args...).Scan(&n)
	return n, err
}

// ObjectsPageSize is the stored size of the object versions an ObjsQuery's
// page holds (values included unless MetadataOnly).
func (s *Store) ObjectsPageSize(ctx context.Context, q ObjsQuery) (int64, error) {
	_, conds, args, _, err := objsWhere(q)
	if err != nil {
		return 0, err
	}
	order, err := objsOrder(q)
	if err != nil {
		return 0, err
	}
	size := "length(o.val)"
	if q.MetadataOnly {
		size = "0"
	}
	limit := q.Limit
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	args = append(args, limit, q.Offset)
	var n int64
	err = s.r.QueryRowContext(ctx, `SELECT COALESCE(SUM(sz), 0) FROM (SELECT `+size+` + `+strconv.Itoa(rowOverhead)+` AS sz FROM objects o WHERE `+
		strings.Join(conds, " AND ")+" ORDER BY "+order+" LIMIT ? OFFSET ?)", args...).Scan(&n)
	return n, err
}
