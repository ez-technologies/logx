package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// captured runs a request through the middleware and returns the log lines it wrote.
func captured(t *testing.T, register func(r *gin.Engine), req *http.Request) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var out bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	setup(&out, "debug", "json", "example-service")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Middleware())
	register(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &m), raw)
		lines = append(lines, m)
	}
	return rec, lines
}

func TestMiddleware_EveryRequestGetsAnIdAndOneStructuredLine(t *testing.T) {
	rec, lines := captured(t, func(r *gin.Engine) {
		r.GET("/things/:id", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	}, httptest.NewRequest(http.MethodGet, "/things/123?search=Jane+Doe&limit=5", nil))

	id := rec.Header().Get(RequestIDHeader)
	require.NotEmpty(t, id, "the id goes back to the caller")
	require.Len(t, lines, 1)
	l := lines[0]
	assert.Equal(t, "request", l["msg"])
	assert.Equal(t, id, l["request_id"])
	assert.Equal(t, "/things/:id", l["route"])
	assert.Equal(t, "/things/123", l["path"])
	assert.EqualValues(t, 200, l["status"])
	assert.Equal(t, "INFO", l["level"])
	assert.Equal(t, "example-service", l["service"])
	assert.Contains(t, l, "duration_ms")
}

func TestMiddleware_QueryValuesAreNeverLoggedOnlyTheirNames(t *testing.T) {
	_, lines := captured(t, func(r *gin.Engine) {
		r.GET("/x", func(c *gin.Context) { c.Status(204) })
	}, httptest.NewRequest(http.MethodGet, "/x?search=Jane+Doe&user=7654321", nil))

	raw, _ := json.Marshal(lines)
	assert.NotContains(t, string(raw), "Jane")
	assert.NotContains(t, string(raw), "7654321")
	assert.Equal(t, "search,user", sorted(lines[0]["query_keys"].(string)))
}

func sorted(csv string) string {
	parts := strings.Split(csv, ",")
	if len(parts) == 2 && parts[0] > parts[1] {
		parts[0], parts[1] = parts[1], parts[0]
	}
	return strings.Join(parts, ",")
}

func TestMiddleware_KeepsASaneCallerIdAndReplacesAnUnsafeOne(t *testing.T) {
	good := httptest.NewRequest(http.MethodGet, "/x", nil)
	good.Header.Set(RequestIDHeader, "web-9f2c1a")
	rec, lines := captured(t, func(r *gin.Engine) { r.GET("/x", func(c *gin.Context) { c.Status(204) }) }, good)
	assert.Equal(t, "web-9f2c1a", rec.Header().Get(RequestIDHeader))
	assert.Equal(t, "web-9f2c1a", lines[0]["request_id"])

	bad := httptest.NewRequest(http.MethodGet, "/x", nil)
	bad.Header.Set(RequestIDHeader, "x\n{\"level\":\"ERROR\",\"msg\":\"forged\"}")
	rec, lines = captured(t, func(r *gin.Engine) { r.GET("/x", func(c *gin.Context) { c.Status(204) }) }, bad)
	assert.NotContains(t, rec.Header().Get(RequestIDHeader), "forged")
	assert.Len(t, lines, 1, "nothing could be injected into the log")
}

func TestMiddleware_AFailureIsLoggedWithItsMessageAtTheRightLevel(t *testing.T) {
	_, lines := captured(t, func(r *gin.Engine) {
		r.GET("/missing", func(c *gin.Context) { c.JSON(404, gin.H{"error": "no such item"}) })
		r.GET("/boom", func(c *gin.Context) { c.JSON(500, gin.H{"error": "database is down"}) })
	}, httptest.NewRequest(http.MethodGet, "/missing", nil))
	assert.Equal(t, "WARN", lines[0]["level"])
	assert.Equal(t, "no such item", lines[0]["error"])

	_, lines = captured(t, func(r *gin.Engine) {
		r.GET("/boom", func(c *gin.Context) { c.JSON(500, gin.H{"error": "database is down"}) })
	}, httptest.NewRequest(http.MethodGet, "/boom", nil))
	assert.Equal(t, "ERROR", lines[0]["level"])
}

func TestMiddleware_SuccessBodiesAreNeverLogged(t *testing.T) {
	_, lines := captured(t, func(r *gin.Engine) {
		r.GET("/me", func(c *gin.Context) {
			c.JSON(200, gin.H{"full_name": "Jane Doe", "avatar": "data:image/png;base64,AAAA"})
		})
	}, httptest.NewRequest(http.MethodGet, "/me", nil))
	raw, _ := json.Marshal(lines)
	assert.NotContains(t, string(raw), "Jane")
	assert.NotContains(t, string(raw), "base64")
}

func TestMiddleware_APanicBecomesALoggedStackAndAnAnswerCarryingTheId(t *testing.T) {
	rec, lines := captured(t, func(r *gin.Engine) {
		r.GET("/panic", func(c *gin.Context) { panic("something exploded") })
	}, httptest.NewRequest(http.MethodGet, "/panic", nil))

	assert.Equal(t, 500, rec.Code)
	assert.Contains(t, rec.Body.String(), rec.Header().Get(RequestIDHeader), "the person can quote it")
	require.GreaterOrEqual(t, len(lines), 2)
	assert.Equal(t, "panic", lines[0]["msg"])
	assert.Contains(t, lines[0]["stack"], "logx")
	assert.Equal(t, "ERROR", lines[len(lines)-1]["level"])
}

func TestMiddleware_TheHealthProbeIsQuietUnlessItFails(t *testing.T) {
	_, lines := captured(t, func(r *gin.Engine) { r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{}) }) },
		httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Empty(t, lines)
}

func TestAnnotate_LaterLinesOfTheRequestCarryTheAnnotations(t *testing.T) {
	_, lines := captured(t, func(r *gin.Engine) {
		r.GET("/x", func(c *gin.Context) {
			Annotate(c, "tenant", "alpha", "user_id", 7)
			From(c.Request.Context()).Info("did a thing", "count", 3)
			c.Status(204)
		})
	}, httptest.NewRequest(http.MethodGet, "/x", nil))

	var thing map[string]any
	for _, l := range lines {
		if l["msg"] == "did a thing" {
			thing = l
		}
	}
	require.NotNil(t, thing)
	assert.Equal(t, "alpha", thing["tenant"])
	assert.EqualValues(t, 7, thing["user_id"])
	assert.NotEmpty(t, thing["request_id"])
}

func TestGorm_LogsOnlySlowAndFailedStatementsWithoutTheirValues(t *testing.T) {
	var out bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	setup(&out, "debug", "json", "")
	g := Gorm{Slow: 100 * time.Millisecond}
	ctx := context.Background()
	sql := func() (string, int64) { return "SELECT * FROM users WHERE email = $1", 1 }

	g.Trace(ctx, time.Now(), sql, nil)                                    // fast and fine
	g.Trace(ctx, time.Now(), sql, gorm.ErrRecordNotFound)                 // normal
	g.Trace(ctx, time.Now().Add(-time.Second), sql, nil)                  // slow
	g.Trace(ctx, time.Now(), sql, errors.New("connection reset by peer")) // broken

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], "slow query")
	assert.Contains(t, lines[1], "database error")
	assert.Contains(t, lines[1], "connection reset")

	// ParamsFilter is what keeps bound values out of the statement text.
	filtered, vars := g.ParamsFilter(ctx, "SELECT $1", "7654321")
	assert.Equal(t, "SELECT $1", filtered)
	assert.Nil(t, vars)
}

func TestSetup_GCPFormatUsesTheFieldNamesCloudLoggingReads(t *testing.T) {
	var out bytes.Buffer
	l := setup(&out, "info", "gcp", "")
	l.Warn("something odd", "item", "i1")
	l.Error("it broke")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 2)
	var warn, bad map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &warn))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &bad))
	assert.Equal(t, "WARNING", warn["severity"])
	assert.Equal(t, "something odd", warn["message"])
	assert.Equal(t, "i1", warn["item"])
	assert.Equal(t, "ERROR", bad["severity"])
	assert.NotContains(t, warn, "level")
	assert.NotContains(t, warn, "msg")
}

func TestSetup_ReadsTheNamedVariablesAndOmitsAnEmptyService(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev); LogSQL = false; DefaultMetrics.SetPrefix("") })
	t.Setenv("APP_LEVEL", "warn")
	t.Setenv("APP_FORMAT", "text")
	t.Setenv("APP_SQL", "1")
	var out bytes.Buffer
	l := Setup(Options{LevelEnv: "APP_LEVEL", FormatEnv: "APP_FORMAT", SQLEnv: "APP_SQL", Output: &out})
	l.Info("hidden")
	l.Warn("shown")
	assert.NotContains(t, out.String(), "hidden")
	assert.Contains(t, out.String(), "msg=shown")
	assert.NotContains(t, out.String(), "service=")
	assert.True(t, LogSQL)
}
