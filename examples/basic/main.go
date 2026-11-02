// Command basic is a small Gin server that shows what logx does: one structured line
// per request with a request id, fields added with Annotate, a logged panic, and
// per-route metrics behind a token.
//
//	METRICS_TOKEN=secret go run ./examples/basic
//	curl -i localhost:8080/orders/42
//	curl -H "Authorization: Bearer secret" localhost:8080/metrics
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/ez-technologies/logx"
	"github.com/gin-gonic/gin"
)

func main() {
	logx.Setup(logx.Options{
		Service:      "shop-api",
		LevelEnv:     "LOG_LEVEL",  // debug, info (default), warn, error
		FormatEnv:    "LOG_FORMAT", // json (default), text, gcp
		MetricPrefix: "shop_",
	})

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(logx.Middleware())

	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.GET("/metrics", logx.MetricsHandler(os.Getenv("METRICS_TOKEN")))

	r.GET("/orders/:id", func(c *gin.Context) {
		// Every later line of this request carries the customer id.
		logx.Annotate(c, "customer_id", 1001)
		if c.Param("id") != "42" {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		logx.From(c.Request.Context()).Info("order loaded", "order_id", 42)
		time.Sleep(15 * time.Millisecond) // pretend to do some work
		c.JSON(http.StatusOK, gin.H{"id": 42, "status": "shipped"})
	})

	r.GET("/panic", func(c *gin.Context) {
		var m map[string]int
		m["boom"] = 1 // a bug: the middleware logs it and answers 500
	})

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
