package logx

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiddleware_LogsDatabaseTimeAndQueryCountAndFeedsMetrics(t *testing.T) {
	g := Gorm{Slow: time.Hour}
	_, lines := captured(t, func(r *gin.Engine) {
		r.GET("/t/:id", func(c *gin.Context) {
			for i := 0; i < 3; i++ {
				g.Trace(c.Request.Context(), time.Now().Add(-20*time.Millisecond), func() (string, int64) { return "select 1", 1 }, nil)
			}
			c.JSON(200, gin.H{})
		})
	}, httptest.NewRequest(http.MethodGet, "/t/9", nil))
	require.Len(t, lines, 1)
	assert.EqualValues(t, 3, lines[0]["db_queries"])
	assert.GreaterOrEqual(t, lines[0]["db_ms"].(float64), 60.0)

	DefaultMetrics.SetPrefix("app_")
	t.Cleanup(func() { DefaultMetrics.SetPrefix("") })
	var out bytes.Buffer
	DefaultMetrics.Render(&out)
	assert.Contains(t, out.String(), `app_request_db_queries_total{method="GET",route="/t/:id"} 3`)
	assert.Contains(t, out.String(), `app_request_duration_seconds_count{method="GET",route="/t/:id"} 1`)
}

func TestMetricsHandler_IsOffWithoutTokenAndNeedsItWithOne(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		token, header string
		want          int
	}{{"", "Bearer ", 404}, {"s", "", 404}, {"s", "Bearer x", 404}, {"s", "Bearer s", 200}} {
		r := gin.New()
		r.GET("/m", MetricsHandler(tc.token))
		req := httptest.NewRequest(http.MethodGet, "/m", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, tc.want, rec.Code, tc)
	}
}

func BenchmarkMiddleware(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)
	setup(io.Discard, "info", "json", "")
	r := gin.New()
	r.Use(Middleware())
	r.GET("/x", func(c *gin.Context) { c.Status(200) })
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	for i := 0; i < b.N; i++ {
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
}
