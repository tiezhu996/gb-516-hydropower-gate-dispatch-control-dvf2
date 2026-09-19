package model

import "time"

// DispatchPermit is the time-limited execution permit for an already approved
// 操作指令. Operators apply for a permit; the independent reviewer signs it
// only when the target gate has no other effective permit. Several pending
// applications may queue for a gate, but at most one can ever be active: the
// nullable active slot column is populated only while a permit is active and
// its unique index makes concurrent sign attempts or competing directives on
// the same gate succeed exactly once. NULL does not collide in the unique
// index on both SQLite and PostgreSQL, so revoking or expiring a permit
// releases the gate slot naturally.
type DispatchPermit struct {
	ID              uint       `json:"id" gorm:"primaryKey"`
	Code            string     `json:"code" gorm:"size:64;uniqueIndex;not null"`
	Status          string     `json:"status" gorm:"size:32;index;not null"`
	Version         uint       `json:"version" gorm:"not null;default:1"`
	DirectiveID     uint       `json:"directiveId" gorm:"not null;index"`
	DirectiveCode   string     `json:"directiveCode" gorm:"size:64;index;not null"`
	GateID          uint       `json:"gateId" gorm:"not null;index"`
	GateCode        string     `json:"gateCode" gorm:"size:64;index;not null"`
	ActiveGateCode  *string    `json:"-" gorm:"size:64;uniqueIndex:idx_permit_gate_active"`
	AppliedBy       string     `json:"appliedBy" gorm:"size:80;index;not null"`
	AppliedAt       time.Time  `json:"appliedAt" gorm:"index;not null"`
	RequestedWindow int        `json:"requestedWindow" gorm:"not null;default:30"`
	ValidFrom       *time.Time `json:"validFrom"`
	ValidUntil      *time.Time `json:"validUntil" gorm:"index"`
	IssuedBy        string     `json:"issuedBy" gorm:"size:80;index"`
	IssuedAt        *time.Time `json:"issuedAt"`
	IssueRequestID  string     `json:"issueRequestId" gorm:"size:64;index"`
	IssueReason     string     `json:"issueReason" gorm:"size:500"`
	RevokedBy       string     `json:"revokedBy" gorm:"size:80;index"`
	RevokedAt       *time.Time `json:"revokedAt"`
	RevokeReason    string     `json:"revokeReason" gorm:"size:500"`
	InvalidatedBy   string     `json:"invalidatedBy" gorm:"size:80"`
	InvalidatedAt   *time.Time `json:"invalidateAt"`
	InvalidateCause string     `json:"invalidateCause" gorm:"size:500"`
	UsedAt          *time.Time `json:"usedAt"`
	ExpiredAt       *time.Time `json:"expiredAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (item DispatchPermit) TableName() string { return "dispatch_permits" }

// occupySlot marks the single active-gate slot only while the permit is
// active. Pending applications do not occupy the unique slot, so several may
// queue; moving to any terminal state clears the pointer and frees the gate.
func (p *DispatchPermit) occupySlot() {
	p.ActiveGateCode = nil
	if p.Status == "active" {
		gate := p.GateCode
		p.ActiveGateCode = &gate
	}
}

// PrepareForInsert/PrepareForUpdate centralise slot bookkeeping so callers
// never persist a permit without the correct unique-index columns.
func (p *DispatchPermit) PrepareForInsert() {
	if p.Version == 0 {
		p.Version = 1
	}
	p.occupySlot()
}

func (p *DispatchPermit) PrepareForUpdate() { p.occupySlot() }
