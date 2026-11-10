package protocol

type SDKInfo struct {
	Language string `json:"language"`
	Version  string `json:"version"`
}

// AuthRequest WS 1.1：HMAC 验签，secret 不进帧。
// method 预留扩展（默认 hmac）。
type AuthRequest struct {
	Method           string   `json:"method,omitempty"` // hmac（默认）| mtls_token（后置）
	Code             string   `json:"code"`
	CredentialID     string   `json:"credential_id"`
	InstanceKey      string   `json:"instance_key"`
	Timestamp        string   `json:"timestamp"` // unix seconds string
	Nonce            string   `json:"nonce"`
	Signature        string   `json:"signature"`
	ProtocolVersions []string `json:"protocol_versions"`
	SDK              SDKInfo  `json:"sdk"`
}

type AuthResponse struct {
	OK                       bool       `json:"ok"`
	SessionID                string     `json:"session_id,omitempty"`
	SessionEpoch             int64      `json:"session_epoch,omitempty"`
	InstanceID               string     `json:"instance_id,omitempty"`
	ProtocolVersion          string     `json:"protocol_version,omitempty"`
	ServerTime               string     `json:"server_time,omitempty"`
	HeartbeatIntervalSeconds int        `json:"heartbeat_interval_seconds,omitempty"`
	LeaseTTLSeconds          int        `json:"lease_ttl_seconds,omitempty"`
	MaxMessageBytes          int64      `json:"max_message_bytes,omitempty"`
	Error                    *ErrorBody `json:"error,omitempty"`
}
