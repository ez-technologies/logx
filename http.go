package logx

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestIDHeader carries the request id between the client, any proxy and the server.
const RequestIDHeader = "X-Request-ID"

const (
	keyID     = "logx.request_id"
	keyFields = "logx.log_fields"
	// Enough of an error answer to say what went wrong; never a whole body.
	maxErrorText = 300
)

// RequestID returns the id of the request, "" outside one.
func RequestID(c *gin.Context) string { id, _ := c.Get(keyID); s, _ := id.(string); return s }

// Annotate adds fields to every later log line of this request (for example the user).
func Annotate(c *gin.Context, kv ...any) {
	existing, _ := c.Get(keyFields)
	fields, _ := existing.([]any)
	fields = append(fields, kv...)
	c.Set(keyFields, fields)
	c.Request = c.Request.WithContext(Into(c.Request.Context(), From(c.Request.Context()).With(kv...)))
}

// validID accepts an id supplied by a caller only when it is plain and short, so a
// caller cannot inject anything into the logs through it.
func validID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// recorder watches what is sent: the status and size always, and the text of an
// error answer so a failure can be explained without logging success bodies.
type recorder struct {
	gin.ResponseWriter
	status int
	bytes  int
	errBuf bytes.Buffer
}

func (r *recorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status >= 400 && r.errBuf.Len() < 2048 {
		r.errBuf.Write(p)
	}
	r.bytes += len(p)
	return r.ResponseWriter.Write(p)
}
func (r *recorder) WriteString(s string) (int, error) { return r.Write([]byte(s)) }

// errorText pulls the "error" message out of a JSON error answer.
func errorText(raw []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		if len(body.Error) > maxErrorText {
			return body.Error[:maxErrorText]
		}
		return body.Error
	}
	return ""
}

// Middleware gives every request an id (kept if the caller sent a sane one), puts a
// logger carrying it on the request's context, logs one line when the request ends,
// and turns a panic into a logged 500 with the stack instead of a dropped connection.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		id := c.GetHeader(RequestIDHeader)
		if !validID(id) {
			id = newID()
		}
		c.Set(keyID, id)
		c.Header(RequestIDHeader, id)
		logger := slog.Default().With("request_id", id)
		ctx, timing := withTiming(Into(c.Request.Context(), logger))
		c.Request = c.Request.WithContext(ctx)

		rec := &recorder{ResponseWriter: c.Writer}
		c.Writer = rec

		defer func() {
			if p := recover(); p != nil {
				From(c.Request.Context()).Error("panic",
					"panic", fmt.Sprint(p), "stack", string(debug.Stack()), "method", c.Request.Method, "path", c.Request.URL.Path)
				if !c.Writer.Written() {
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error", "request_id": id})
				}
				rec.status = http.StatusInternalServerError
			}
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			attrs := []any{
				"method", c.Request.Method,
				// The route pattern groups requests; the path says which record.
				"route", c.FullPath(),
				"path", c.Request.URL.Path,
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
				"db_ms", timing.nanos.Load() / int64(time.Millisecond),
				"db_queries", timing.queries.Load(),
				"bytes", rec.bytes,
				"client_ip", c.ClientIP(),
			}
			// The names of the query parameters, never their values, which can be personal data.
			if q := c.Request.URL.Query(); len(q) > 0 {
				keys := make([]string, 0, len(q))
				for k := range q {
					keys = append(keys, k)
				}
				attrs = append(attrs, "query_keys", strings.Join(keys, ","))
			}
			if LogSQL {
				timing.mu.Lock()
				attrs = append(attrs, "sql", timing.stmts)
				timing.mu.Unlock()
			}
			if ua := c.Request.UserAgent(); ua != "" {
				if len(ua) > 120 {
					ua = ua[:120]
				}
				attrs = append(attrs, "user_agent", ua)
			}
			if status >= 400 {
				if msg := errorText(rec.errBuf.Bytes()); msg != "" {
					attrs = append(attrs, "error", msg)
				}
			}
			if c.FullPath() != "" {
				DefaultMetrics.Observe(c.Request.Method, c.FullPath(), status, time.Since(start),
					time.Duration(timing.nanos.Load()), timing.queries.Load())
			}
			level := slog.LevelInfo
			switch {
			case status >= 500:
				level = slog.LevelError
			case status >= 400:
				level = slog.LevelWarn
			}
			// A successful health probe is not logged.
			if c.FullPath() == "/healthz" && status < 400 {
				return
			}
			From(c.Request.Context()).Log(c.Request.Context(), level, "request", attrs...)
		}()
		c.Next()
	}
}
