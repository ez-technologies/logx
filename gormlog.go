package logx

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Gorm is a database logger that says what is slow or broken and nothing else: a
// query over the threshold, or an error (a missing row is normal and is not one).
// The SQL is logged with placeholders, never with the bound values.
type Gorm struct{ Slow time.Duration }

func (g Gorm) LogMode(logger.LogLevel) logger.Interface      { return g }
func (g Gorm) Info(context.Context, string, ...interface{})  {}
func (g Gorm) Warn(context.Context, string, ...interface{})  {}
func (g Gorm) Error(context.Context, string, ...interface{}) {}

// ParamsFilter drops the values before the statement is rendered, so the log shows
// $1, $2 and never the data bound to them.
func (g Gorm) ParamsFilter(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
	return sql, nil
}

// Trace implements gorm's logger.Interface.
func (g Gorm) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	elapsed := time.Since(begin)
	if t := timingFrom(ctx); t != nil {
		t.add(elapsed)
		if LogSQL {
			sql, _ := fc()
			t.note(sql)
		}
	}
	slow := g.Slow > 0 && elapsed > g.Slow
	failed := err != nil && !errors.Is(err, gorm.ErrRecordNotFound)
	if !slow && !failed {
		return
	}
	sql, rows := fc()
	attrs := []any{"sql", truncate(sql, 400), "rows", rows, "duration_ms", elapsed.Milliseconds()}
	l := From(ctx)
	if failed {
		l.Error("database error", append(attrs, "error", err.Error())...)
		return
	}
	l.Warn("slow query", attrs...)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
