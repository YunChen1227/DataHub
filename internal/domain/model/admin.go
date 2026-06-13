package model

import "time"

// AdminUser is an internal operator account for the admin console (DESIGN §16.1).
type AdminUser struct {
	ID           int64
	Username     string
	PasswordHash string // 加盐哈希; 生产应换 bcrypt/argon2
	Role         string // ADMIN（本期单一角色）
	CreatedAt    time.Time
}

// UserDetail is the admin-facing aggregate view of a普通用户 (license + 配额 +
// IP 白名单), used by the user management screens (DESIGN §16.2).
type UserDetail struct {
	LicenseID         string    `json:"licenseId"`
	AppKey            string    `json:"appKey"`
	Name              string    `json:"name"`
	Status            string    `json:"status"`
	ClientUUID        string    `json:"clientUuid"`
	ServiceTotal      int64     `json:"serviceTotal"`
	ServiceUsed       int64     `json:"serviceUsed"`
	UpstreamTotal     int64     `json:"upstreamTotal"`
	UpstreamCommitted int64     `json:"upstreamCommitted"`
	UpstreamReserved  int64     `json:"upstreamReserved"`
	IPWhitelist       []string  `json:"ipWhitelist"`
	CreatedAt         time.Time `json:"createdAt"`
}

// AuditRecord is the rich per-request audit log (DESIGN §16.3 / §16.5). It is
// append-only and keyed by requestId for cross-referencing the billing ledger
// and the [requestId]-prefixed logs (§9).
type AuditRecord struct {
	ID             int64     `json:"id"`
	RequestID      string    `json:"requestId"`
	AppKey         string    `json:"appKey"`
	TradeNo        string    `json:"tradeNo"`
	Reqid          string    `json:"reqid"`
	ClientIP       string    `json:"clientIp"`
	CalledUpstream bool      `json:"calledUpstream"` // 是否成功调用上游
	FoundData      bool      `json:"foundData"`      // 是否查得数据 (busiCode 10)
	BusiCode       int       `json:"busiCode"`
	BusiMsg        string    `json:"busiMsg"`
	UpstreamCode   string    `json:"upstreamCode"`
	UpstreamUID    string    `json:"upstreamUid"`
	UpstreamLogID  string    `json:"upstreamLogId"`
	Billed         bool      `json:"billed"` // 是否计维度①（对用户计费）
	LatencyMs      int64     `json:"latencyMs"`
	NameMask       string    `json:"nameMask"`
	IDCardMask     string    `json:"idCardMask"`
	MobileMask     string    `json:"mobileMask"`
	ErrMsg         string    `json:"errMsg"`
	CreatedAt      time.Time `json:"createdAt"`
}

// AuditFilter narrows an audit query (DESIGN §16.3).
type AuditFilter struct {
	AppKey   string
	BusiCode *int
	Limit    int
	Offset   int
}
