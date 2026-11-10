package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/GlassJob/job-center/internal/pkg/auth"
	"github.com/GlassJob/job-center/internal/repository/mysql"
	"github.com/gin-gonic/gin"
)

type ctxKey int

const (
	ctxClientID ctxKey = iota + 1
	ctxOperatorID
	ctxOperatorName
	ctxScopes
)

type AuthConfig struct {
	Store  *mysql.Store
	Pepper string
	Skew   time.Duration
	Nonces *auth.NonceCache
}

func Auth(cfg AuthConfig, requireScope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientID := c.GetHeader(auth.HeaderClientID)
		ts := c.GetHeader(auth.HeaderTimestamp)
		nonce := c.GetHeader(auth.HeaderNonce)
		opID := c.GetHeader(auth.HeaderOperatorID)
		opName := c.GetHeader(auth.HeaderOperatorName)
		sig := c.GetHeader(auth.HeaderSignature)
		if clientID == "" || ts == "" || nonce == "" || sig == "" {
			abortJSON(c, http.StatusUnauthorized, 40100, "missing signature headers")
			return
		}
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			abortJSON(c, http.StatusUnauthorized, 40100, "bad timestamp")
			return
		}
		skew := cfg.Skew
		if skew <= 0 {
			skew = 5 * time.Minute
		}
		diff := time.Now().Unix() - sec
		if diff < 0 {
			diff = -diff
		}
		if diff > int64(skew/time.Second) {
			abortJSON(c, http.StatusUnauthorized, 40100, "timestamp skew")
			return
		}
		if !cfg.Nonces.Use(clientID, nonce) {
			abortJSON(c, http.StatusUnauthorized, 40100, "nonce replay")
			return
		}

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		if err != nil {
			abortJSON(c, http.StatusBadRequest, 40000, "read body failed")
			return
		}
		_ = c.Request.Body.Close()
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		cl, err := cfg.Store.GetClientByID(c.Request.Context(), clientID)
		if err != nil || cl.Status != "ACTIVE" {
			abortJSON(c, http.StatusUnauthorized, 40100, "unknown client")
			return
		}
		secret, err := auth.DecryptSecret(cfg.Pepper, cl.SecretEnc)
		if err != nil {
			abortJSON(c, http.StatusUnauthorized, 40100, "client secret unavailable")
			return
		}
		canon, err := auth.Canonical(
			c.Request.Method, c.Request.URL.Path, c.Request.URL.RawQuery,
			clientID, ts, nonce, opID, opName, body,
		)
		if err != nil || !auth.VerifySignature(secret, canon, sig) {
			abortJSON(c, http.StatusUnauthorized, 40100, "bad signature")
			return
		}
		if requireScope != "" && !hasScope(cl.Scopes, requireScope) {
			abortJSON(c, http.StatusForbidden, 40300, "missing scope "+requireScope)
			return
		}

		cfg.Store.TouchClientUsed(c.Request.Context(), clientID, c.ClientIP())
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, ctxClientID, clientID)
		ctx = context.WithValue(ctx, ctxOperatorID, opID)
		ctx = context.WithValue(ctx, ctxOperatorName, opName)
		ctx = context.WithValue(ctx, ctxScopes, cl.Scopes)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == "*" || s == want {
			return true
		}
	}
	return false
}

func ClientID(ctx context.Context) string {
	v, _ := ctx.Value(ctxClientID).(string)
	return v
}

func OperatorID(ctx context.Context) string {
	v, _ := ctx.Value(ctxOperatorID).(string)
	return v
}

func OperatorName(ctx context.Context) string {
	v, _ := ctx.Value(ctxOperatorName).(string)
	return v
}

func abortJSON(c *gin.Context, httpStatus, code int, msg string) {
	c.AbortWithStatusJSON(httpStatus, gin.H{"code": code, "message": msg})
}
