package db

import (
	"database/sql/driver"
	"fmt"
	"math"
	"math/big"
	"slices"
	"sync"

	"modernc.org/sqlite"
)

var registerOnce sync.Once

// registerFunctions installs the application-defined SQLite aggregates used by
// the Dialect helpers (once per process, before the first connection).
func registerFunctions() {
	registerOnce.Do(func() {
		sqlite.MustRegisterFunction("og_percentile_cont", &sqlite.FunctionImpl{
			NArgs: 2, Deterministic: true,
			MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) { return &percentileAgg{}, nil },
		})
		sqlite.MustRegisterFunction("og_sum_text", &sqlite.FunctionImpl{
			NArgs: 1, Deterministic: true,
			MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) { return &sumTextAgg{}, nil },
		})
	})
}

// percentileAgg implements PostgreSQL's percentile_cont(fraction) WITHIN GROUP
// (ORDER BY x): NULL inputs are ignored, the result is NULL without input and
// otherwise interpolates linearly between the two nearest ranks.
type percentileAgg struct {
	vals     []float64
	fraction float64
}

func (a *percentileAgg) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	f, err := asFloat(args[1])
	if err != nil || f < 0 || f > 1 || math.IsNaN(f) {
		return fmt.Errorf("og_percentile_cont: fraction must be between 0 and 1")
	}
	a.fraction = f
	if args[0] == nil {
		return nil
	}
	v, err := asFloat(args[0])
	if err != nil {
		return fmt.Errorf("og_percentile_cont: %w", err)
	}
	a.vals = append(a.vals, v)
	return nil
}

func (a *percentileAgg) WindowInverse(*sqlite.FunctionContext, []driver.Value) error {
	return fmt.Errorf("og_percentile_cont: not supported as a window function")
}

func (a *percentileAgg) WindowValue(*sqlite.FunctionContext) (driver.Value, error) {
	if len(a.vals) == 0 {
		return nil, nil
	}
	v := slices.Clone(a.vals)
	slices.Sort(v)
	pos := a.fraction * float64(len(v)-1)
	lo := math.Floor(pos)
	hi := math.Ceil(pos)
	if lo == hi {
		return v[int(lo)], nil
	}
	return v[int(lo)] + (pos-lo)*(v[int(hi)]-v[int(lo)]), nil
}

func (a *percentileAgg) Final(*sqlite.FunctionContext) {}

// sumTextAgg sums integers exactly (no int64 overflow, unlike SQLite's sum)
// and returns decimal text, NULL without input, like sum(bigint)::text.
type sumTextAgg struct {
	sum  big.Int
	seen bool
}

func (a *sumTextAgg) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if args[0] == nil {
		return nil
	}
	n, err := asInt(args[0])
	if err != nil {
		return fmt.Errorf("og_sum_text: %w", err)
	}
	a.sum.Add(&a.sum, big.NewInt(n))
	a.seen = true
	return nil
}

func (a *sumTextAgg) WindowInverse(*sqlite.FunctionContext, []driver.Value) error {
	return fmt.Errorf("og_sum_text: not supported as a window function")
}

func (a *sumTextAgg) WindowValue(*sqlite.FunctionContext) (driver.Value, error) {
	if !a.seen {
		return nil, nil
	}
	return a.sum.String(), nil
}

func (a *sumTextAgg) Final(*sqlite.FunctionContext) {}
