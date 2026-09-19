package dto

import "time"

// CreateOperationDirective is the public write contract for 操作指令. Status is deliberately
// omitted so callers cannot bypass the service state machine.
type CreateOperationDirective struct {
	Code        string    `json:"code" binding:"required,min=2,max=64"`
	Name        string    `json:"name" binding:"required,min=2,max=160"`
	Description string    `json:"description" binding:"max=1000"`
	Facility    string    `json:"facility" binding:"required,max=120"`
	Owner       string    `json:"owner" binding:"required,max=120"`
	Category    string    `json:"category" binding:"required,max=80"`
	RiskLevel   string    `json:"riskLevel" binding:"required,oneof=low medium high critical"`
	MetricValue float64   `json:"metricValue"`
	MetricUnit  string    `json:"metricUnit" binding:"max=24"`
	EffectiveAt time.Time `json:"effectiveAt" binding:"required"`
	Evidence    string    `json:"evidence" binding:"max=2000"`
	RelatedCode string    `json:"relatedCode" binding:"required,min=2,max=64"`
	GateState   string    `json:"gateState" binding:"omitempty,oneof=open closed locked"`
}

type UpdateOperationDirective struct {
	ExpectedVersion uint      `json:"expectedVersion" binding:"required"`
	Name            string    `json:"name" binding:"required,min=2,max=160"`
	Description     string    `json:"description" binding:"max=1000"`
	Facility        string    `json:"facility" binding:"required,max=120"`
	Owner           string    `json:"owner" binding:"required,max=120"`
	Category        string    `json:"category" binding:"required,max=80"`
	RiskLevel       string    `json:"riskLevel" binding:"required,oneof=low medium high critical"`
	MetricValue     float64   `json:"metricValue"`
	MetricUnit      string    `json:"metricUnit" binding:"max=24"`
	EffectiveAt     time.Time `json:"effectiveAt" binding:"required"`
	Evidence        string    `json:"evidence" binding:"max=2000"`
	RelatedCode     string    `json:"relatedCode" binding:"required,min=2,max=64"`
	GateState       string    `json:"gateState" binding:"omitempty,oneof=open closed locked"`
}

// CreateDispatchPermit is the operator application contract for the
// time-limited 闸门调度许可. The permit is always anchored server-side to the
// directive's own gate, so callers cannot forge a cross-gate application;
// gateCode, when supplied, must match the directive's gate and is used purely
// as a defensive cross-gate assertion.
type CreateDispatchPermit struct {
	Code            string `json:"code" binding:"omitempty,min=2,max=64"`
	DirectiveCode   string `json:"directiveCode" binding:"required,min=2,max=64"`
	GateCode        string `json:"gateCode" binding:"omitempty,min=2,max=64"`
	DurationMinutes int    `json:"durationMinutes" binding:"required,min=5,max=1440"`
	Reason          string `json:"reason" binding:"required,min=3,max=500"`
}

// IssueDispatchPermit is the reviewer signing contract. A permit is only
// signed while pending and only when its gate has no other active permit.
type IssueDispatchPermit struct {
	ExpectedVersion uint   `json:"expectedVersion" binding:"required"`
	Reason          string `json:"reason" binding:"required,min=3,max=500"`
}

// RevokeDispatchPermit is the revocation contract. Only active permits may be
// revoked; the reason is retained for the directive page and audit trail.
type RevokeDispatchPermit struct {
	Reason string `json:"reason" binding:"required,min=3,max=500"`
}

// DispatchPermitView is the read model served to the workbench. Expired is
// derived lazily: a still-active permit whose validity window has elapsed is
// reported as expired even before the background sweep persists that state.
type DispatchPermitView struct {
	ID              uint       `json:"id"`
	Code            string     `json:"code"`
	Status          string     `json:"status"`
	Version         uint       `json:"version"`
	DirectiveID     uint       `json:"directiveId"`
	DirectiveCode   string     `json:"directiveCode"`
	GateID          uint       `json:"gateId"`
	GateCode        string     `json:"gateCode"`
	AppliedBy       string     `json:"appliedBy"`
	AppliedAt       time.Time  `json:"appliedAt"`
	ValidFrom       *time.Time `json:"validFrom"`
	ValidUntil      *time.Time `json:"validUntil"`
	IssuedBy        string     `json:"issuedBy"`
	IssuedAt        *time.Time `json:"issuedAt"`
	IssueRequestID  string     `json:"issueRequestId"`
	IssueReason     string     `json:"issueReason"`
	RevokedBy       string     `json:"revokedBy"`
	RevokedAt       *time.Time `json:"revokedAt"`
	RevokeReason    string     `json:"revokeReason"`
	InvalidatedBy   string     `json:"invalidatedBy"`
	InvalidatedAt   *time.Time `json:"invalidatedAt"`
	InvalidateCause string     `json:"invalidateCause"`
	UsedAt          *time.Time `json:"usedAt"`
	ExpiredAt       *time.Time `json:"expiredAt"`
	Expired         bool       `json:"expired"`
	Effective       bool       `json:"effective"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}
