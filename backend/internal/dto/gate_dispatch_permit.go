package dto

// ApplyGateDispatchPermit is submitted by the operator for an already approved
// directive. The gate code is validated against the directive's linked gate so
// cross-gate applications can never be issued.
type ApplyGateDispatchPermit struct {
	Code          string `json:"code" binding:"required,min=2,max=64"`
	DirectiveID   uint   `json:"directiveId" binding:"required"`
	GateCode      string `json:"gateCode" binding:"required,min=2,max=64"`
	ValidMinutes  int    `json:"validMinutes" binding:"required,min=1,max=720"`
	RequestReason string `json:"requestReason" binding:"required,min=3,max=500"`
}

// IssueGateDispatchPermit is the reviewer decision. The status guard plus the
// unique active-gate index guarantee the reviewer only signs when no other
// permit is effective for the gate.
type IssueGateDispatchPermit struct {
	ExpectedVersion uint   `json:"expectedVersion" binding:"required"`
	IssueReason     string `json:"issueReason" binding:"required,min=3,max=500"`
}

// RevokeGateDispatchPermit stops an issued permit before it expires. Revocation
// is final and blocks every later execution attempt.
type RevokeGateDispatchPermit struct {
	ExpectedVersion uint   `json:"expectedVersion" binding:"required"`
	RevokeReason    string `json:"revokeReason" binding:"required,min=3,max=500"`
}

// GateDispatchPermitQuery supports filtering the permit ledger by directive,
// gate and lifecycle state.
type GateDispatchPermitQuery struct {
	Page          int    `form:"page"`
	PageSize      int    `form:"pageSize"`
	DirectiveCode string `form:"directiveCode"`
	GateCode      string `form:"gateCode"`
	Status        string `form:"status"`
}
