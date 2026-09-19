package model

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

// GateDispatchPermit models a time-boxed 闸门调度许可 requested by an operator
// for an approved 操作指令 and issued independently by a reviewer. At most one
// issued permit may exist per gate: ActiveGateKey is set to the gate code while
// the permit is issued and cleared when it leaves the issued state, backed by a
// composite unique index so duplicate issuance and concurrent preemption can
// only succeed once at the database level.
type GateDispatchPermit struct {
	ID            uint           `json:"id" gorm:"primaryKey"`
	Code          string         `json:"code" gorm:"size:64;uniqueIndex;not null"`
	DirectiveID   uint           `json:"directiveId" gorm:"not null;index:idx_permit_directive"`
	DirectiveCode string         `json:"directiveCode" gorm:"size:64;not null;index"`
	GateCode      string         `json:"gateCode" gorm:"size:64;not null;index:idx_permit_active,priority:1"`
	// ActiveGateKey points at the gate only while Status == issued. It is NULL
	// for every other state, and the composite unique index idx_permit_active
	// enforces the single-active-permit-per-gate rule (SQL treats NULLs as
	// distinct, so pending applications never collide).
	ActiveGateKey *string        `json:"-" gorm:"size:64;uniqueIndex:idx_permit_active,priority:2"`
	Status        string         `json:"status" gorm:"size:32;not null;index;default:pending"`
	RequestedBy   string         `json:"requestedBy" gorm:"size:80;not null;index"`
	RequestedAt   time.Time      `json:"requestedAt" gorm:"not null;index"`
	RequestReason string         `json:"requestReason" gorm:"size:500;not null"`
	ValidFrom     time.Time      `json:"validFrom" gorm:"not null"`
	ValidUntil    time.Time      `json:"validUntil" gorm:"not null;index"`
	IssuedBy      string         `json:"issuedBy" gorm:"size:80;index"`
	IssuedAt      *time.Time     `json:"issuedAt"`
	IssueReason   string         `json:"issueReason" gorm:"size:500"`
	RevokedBy     string         `json:"revokedBy" gorm:"size:80;index"`
	RevokedAt     *time.Time     `json:"revokedAt"`
	RevokeReason  string         `json:"revokeReason" gorm:"size:500"`
	CloseReason   string         `json:"closeReason" gorm:"size:500"`
	Version       uint           `json:"version" gorm:"not null;default:1"`
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	DeletedAt     gorm.DeletedAt `json:"-" gorm:"index"`

	// Expired/DisplayStatus are derived lazily at read time. A permit that is
	// issued past ValidUntil is presented as expired without mutating state;
	// issuance and execution persist the expiry when they observe it.
	Expired       bool   `json:"expired" gorm:"-"`
	DisplayStatus string `json:"displayStatus" gorm:"-"`
}

func (item GateDispatchPermit) TableName() string { return "gate_dispatch_permits" }

// Decorate fills the lazy derived fields used by the directive workbench.
func (item *GateDispatchPermit) Decorate(now time.Time) {
	item.Expired = item.Status == "issued" && item.ValidUntil.Before(now)
	item.DisplayStatus = item.Status
	if item.Expired {
		item.DisplayStatus = "expired"
	}
}

// PermitRequestCodePrefix keeps application codes visually distinct.
const PermitRequestCodePrefix = "GDP-"

func NormalizeGateCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}
