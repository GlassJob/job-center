package auth

import (
	"errors"
	"regexp"
	"strings"
)

const CredentialPrefix = "jcs_"

var (
	ErrMalformedCredential = errors.New("malformed credential")
	credIDRe               = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	credSecretRe           = regexp.MustCompile(`^[0-9A-Za-z]{32,128}$`)
)

// FormatCredentialToken 组装 Executor 凭据令牌：jcs_<credential_id>.<secret>
func FormatCredentialToken(credentialID, secret string) string {
	return CredentialPrefix + credentialID + "." + secret
}

// ParseCredentialToken 拆分 jcs_<credential_id>.<secret>（id 为 UUID）。
func ParseCredentialToken(token string) (credentialID, secret string, err error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), CredentialPrefix)
	if !ok {
		return "", "", ErrMalformedCredential
	}
	credentialID, secret, ok = strings.Cut(rest, ".")
	if !ok || !credIDRe.MatchString(credentialID) || !credSecretRe.MatchString(secret) {
		return "", "", ErrMalformedCredential
	}
	return credentialID, secret, nil
}
