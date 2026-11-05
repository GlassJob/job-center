package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/GlassJob/job-center/internal/infrastructure/security"
)

type ctxKey int

const opKey ctxKey = 1

func OperatorFrom(ctx context.Context) (*security.Operator, bool) {
	o, ok := ctx.Value(opKey).(*security.Operator)
	return o, ok
}

// AdminAuth 校验 HMAC 签名、时间窗、nonce，并把 Operator 放入 context。
func (s *Server) AdminAuth(requirePerm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientID := r.Header.Get(security.HeaderClient)
			ts := r.Header.Get(security.HeaderTimestamp)
			nonce := r.Header.Get(security.HeaderNonce)
			opHdr := r.Header.Get(security.HeaderOperator)
			sig := r.Header.Get(security.HeaderSignature)
			if clientID == "" || ts == "" || nonce == "" || sig == "" {
				writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "missing signature headers", nil)
				return
			}
			ac, ok := s.cfg.Security.AdminClients[clientID]
			if !ok || ac.Secret == "" {
				writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "unknown client", nil)
				return
			}
			sec, err := strconv.ParseInt(ts, 10, 64)
			if err != nil {
				writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "bad timestamp", nil)
				return
			}
			skew := s.cfg.Security.TimestampSkew.D()
			if skew <= 0 {
				skew = 5 * time.Minute
			}
			now := time.Now().Unix()
			diff := now - sec
			if diff < 0 {
				diff = -diff
			}
			if diff > int64(skew/time.Second) {
				writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "timestamp skew", nil)
				return
			}
			if !s.nonces.Use(clientID, nonce) {
				writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "nonce replay", nil)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBody()))
			if err != nil {
				writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "read body failed", nil)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))

			canon, err := security.Canonical(r.Method, r.URL.Path, r.URL.RawQuery, body, ts, nonce, opHdr)
			if err != nil || !security.VerifySignature(ac.Secret, canon, sig) {
				writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "bad signature", nil)
				return
			}
			op, err := security.DecodeOperator(opHdr)
			if err != nil {
				writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "bad operator", nil)
				return
			}
			if !permsWithin(op.Perms, ac.MaxPerms) {
				writeAPIError(w, r, http.StatusForbidden, "PERMISSION_DENIED", "operator perms exceed client max", nil)
				return
			}
			if requirePerm != "" && !op.Has(requirePerm) {
				writeAPIError(w, r, http.StatusForbidden, "PERMISSION_DENIED", "missing permission "+requirePerm, nil)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), opKey, op)))
		})
	}
}

func permsWithin(have, max []string) bool {
	for _, m := range max {
		if m == "*" {
			return true
		}
	}
	allow := map[string]bool{}
	for _, m := range max {
		allow[m] = true
	}
	for _, p := range have {
		if p == "*" || !allow[p] {
			return false
		}
	}
	return true
}

func (s *Server) maxBody() int64 {
	if s.cfg != nil && s.cfg.HTTP.MaxBodyBytes > 0 {
		return s.cfg.HTTP.MaxBodyBytes
	}
	return 1 << 20
}
