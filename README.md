# logx

Structured logging for Go services built on [Gin](https://github.com/gin-gonic/gin) and [GORM](https://gorm.io), on top of the standard `log/slog`.

- `Setup` installs the process logger, reading the level and format (`json`, `text` or `gcp`) from environment variables you name.
- `Middleware` gives every request an id (`X-Request-ID`), logs one line when it ends (route, status, duration, database time and query count, query parameter names but never their values), and turns a panic into a logged 500.
- `Gorm` is a GORM logger that reports only slow or failed statements, never the bound values.
- `MetricsHandler` serves per-route metrics in Prometheus text format, behind a bearer token.

Request and response bodies are never logged.

## Install

```sh
go get github.com/ez-technologies/logx
```

## Usage

```go
logx.Setup(logx.Options{
	Service:      "my-service", // added to every line
	LevelEnv:     "LOG_LEVEL",  // debug, info, warn, error (default info)
	FormatEnv:    "LOG_FORMAT", // json (default), text, gcp
	SQLEnv:       "LOG_SQL",    // "1" lists each request's statements
	MetricPrefix: "my_service_",
})

db, _ := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logx.Gorm{Slow: 200 * time.Millisecond}})

r := gin.New()
r.Use(logx.Middleware())
r.GET("/metrics", logx.MetricsHandler(os.Getenv("METRICS_TOKEN")))
r.GET("/things/:id", func(c *gin.Context) {
	logx.Annotate(c, "user_id", 7) // on every later line of this request
	logx.From(c.Request.Context()).Info("loaded thing")
	db.WithContext(c.Request.Context()).First(&thing, c.Param("id"))
})
```

All `Options` fields are optional; the env names above are the defaults, the service is omitted and the metric prefix is empty when not set.

## Licence

MIT, see [LICENSE](LICENSE).
