package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/constants"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/dto"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/model"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/repository"
	"gorm.io/gorm"
)

// ExecutionPermitValidator is the narrow gate the directive service uses
// before allowing an approved directive to enter execution. It lives in the
// service package to avoid a circular dependency between the two services.
type ExecutionPermitValidator interface {
	// RequireEffectivePermit performs the non-mutating safety check (valid,
	// not revoked, same gate) before the directive transaction opens.
	RequireEffectivePermit(ctx context.Context, directiveID uint, gateCode string) (model.DispatchPermit, error)
	// ConsumePermit marks the permit as used inside the directive transaction
	// and re-checks the row lock so concurrent execution/revocation cannot
	// both succeed.
	ConsumePermit(ctx context.Context, permit model.DispatchPermit) error
}

type DispatchPermitService interface {
	List(context.Context, dto.PageQuery) (repository.Page[dto.DispatchPermitView], error)
	ListForDirectives(ctx context.Context, directives []model.OperationDirective) map[uint]dto.DispatchPermitView
	Get(context.Context, uint) (dto.DispatchPermitView, error)
	GetByCode(context.Context, string) (dto.DispatchPermitView, error)
	Apply(context.Context, dto.CreateDispatchPermit, string, string) (dto.DispatchPermitView, error)
	Issue(context.Context, uint, dto.IssueDispatchPermit, string, string, string) (dto.DispatchPermitView, error)
	Revoke(context.Context, uint, dto.RevokeDispatchPermit, string, string, string) (dto.DispatchPermitView, error)
	SweepExpired(context.Context, string) ([]dto.DispatchPermitView, error)
	ExecutionPermitValidator
}

type dispatchPermitService struct {
	repository repository.DispatchPermitRepository
	directives repository.OperationDirectiveRepository
	gates      repository.GateUnitRepository
	security   SecurityService
}

func NewDispatchPermitService(repo repository.DispatchPermitRepository, directives repository.OperationDirectiveRepository, gates repository.GateUnitRepository, security SecurityService) DispatchPermitService {
	return &dispatchPermitService{repository: repo, directives: directives, gates: gates, security: security}
}

func toPermitView(permit model.DispatchPermit, now time.Time) dto.DispatchPermitView {
	expired := permit.Status == "expired" ||
		(permit.Status == "active" && permit.ValidUntil != nil && permit.ValidUntil.Before(now))
	effective := permit.Status == "active" && !expired && permit.UsedAt == nil
	return dto.DispatchPermitView{
		ID: permit.ID, Code: permit.Code, Status: permit.Status, Version: permit.Version,
		DirectiveID: permit.DirectiveID, DirectiveCode: permit.DirectiveCode,
		GateID: permit.GateID, GateCode: permit.GateCode,
		AppliedBy: permit.AppliedBy, AppliedAt: permit.AppliedAt,
		ValidFrom: permit.ValidFrom, ValidUntil: permit.ValidUntil,
		IssuedBy: permit.IssuedBy, IssuedAt: permit.IssuedAt, IssueRequestID: permit.IssueRequestID, IssueReason: permit.IssueReason,
		RevokedBy: permit.RevokedBy, RevokedAt: permit.RevokedAt, RevokeReason: permit.RevokeReason,
		InvalidatedBy: permit.InvalidatedBy, InvalidatedAt: permit.InvalidatedAt, InvalidateCause: permit.InvalidateCause,
		UsedAt: permit.UsedAt, ExpiredAt: permit.ExpiredAt, Expired: expired, Effective: effective,
		CreatedAt: permit.CreatedAt, UpdatedAt: permit.UpdatedAt,
	}
}

func (s *dispatchPermitService) List(ctx context.Context, query dto.PageQuery) (repository.Page[dto.DispatchPermitView], error) {
	if err := s.sweepExpired(ctx, "system", "sweep-on-list"); err != nil {
		return repository.Page[dto.DispatchPermitView]{}, err
	}
	page, err := s.repository.List(ctx, query)
	if err != nil {
		return repository.Page[dto.DispatchPermitView]{}, err
	}
	now := time.Now().UTC()
	views := make([]dto.DispatchPermitView, 0, len(page.Items))
	for _, item := range page.Items {
		views = append(views, toPermitView(item, now))
	}
	return repository.Page[dto.DispatchPermitView]{Items: views, Total: page.Total, Page: page.Page, PageSize: page.PageSize}, nil
}

func (s *dispatchPermitService) ListForDirectives(ctx context.Context, directives []model.OperationDirective) map[uint]dto.DispatchPermitView {
	result := make(map[uint]dto.DispatchPermitView)
	ids := make([]uint, 0, len(directives))
	for _, directive := range directives {
		ids = append(ids, directive.ID)
	}
	permits, err := s.repository.ListByDirectiveIDs(ctx, ids)
	if err != nil {
		return result
	}
	now := time.Now().UTC()
	for _, permit := range permits {
		view := toPermitView(permit, now)
		// The most recent non-terminal permit is the one the directive page
		// surfaces; terminal history remains available in the full list.
		existing, occupied := result[permit.DirectiveID]
		if !occupied || permitRank(view.Status) > permitRank(existing.Status) {
			result[permit.DirectiveID] = view
		}
	}
	return result
}

func permitRank(status string) int {
	switch status {
	case "active":
		return 5
	case "pending":
		return 4
	case "expired":
		return 3
	case "revoked":
		return 2
	default:
		return 1
	}
}

func (s *dispatchPermitService) Get(ctx context.Context, id uint) (dto.DispatchPermitView, error) {
	permit, err := s.repository.Get(ctx, id)
	if err != nil {
		return dto.DispatchPermitView{}, err
	}
	return toPermitView(permit, time.Now().UTC()), nil
}

func (s *dispatchPermitService) GetByCode(ctx context.Context, code string) (dto.DispatchPermitView, error) {
	permit, err := s.repository.GetByCode(ctx, code)
	if err != nil {
		return dto.DispatchPermitView{}, err
	}
	return toPermitView(permit, time.Now().UTC()), nil
}

// Apply records an operator's time-limited permit application. The directive
// must already be approved, the asserted gate must be the directive's own
// gate (cross-gate applications are rejected), and neither the directive nor
// the gate may already hold an open application or signed permit.
func (s *dispatchPermitService) Apply(ctx context.Context, input dto.CreateDispatchPermit, actor, requestID string) (dto.DispatchPermitView, error) {
	directive, err := s.directives.GetByCode(ctx, input.DirectiveCode)
	if err != nil {
		return dto.DispatchPermitView{}, fmt.Errorf("linked directive %q: %w", input.DirectiveCode, err)
	}
	if directive.Status != string(constants.DirectiveStateApproved) {
		return dto.DispatchPermitView{}, fmt.Errorf("%w: permit application requires an approved directive", ErrInvalidInput)
	}
	gate, err := s.gates.GetByCode(ctx, directive.RelatedCode)
	if err != nil {
		return dto.DispatchPermitView{}, fmt.Errorf("linked gate %q: %w", directive.RelatedCode, err)
	}
	// Defensive cross-gate check: the application must name the directive's
	// own gate. A mismatched gateCode is a rejected cross-gate request.
	if claimed := strings.TrimSpace(input.GateCode); claimed != "" && !strings.EqualFold(claimed, gate.Code) {
		return dto.DispatchPermitView{}, fmt.Errorf("%w: application gate %s does not match directive gate %s", ErrInvalidInput, claimed, gate.Code)
	}
	if gate.Status == string(constants.GateStateLocked) {
		return dto.DispatchPermitView{}, fmt.Errorf("%w: locked gate cannot receive a dispatch permit", ErrInvalidInput)
	}
	if _, err := s.repository.FindOpenForDirective(ctx, directive.ID); err == nil {
		return dto.DispatchPermitView{}, fmt.Errorf("%w: directive %s already has a pending or active permit", ErrPermitConflict, directive.Code)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return dto.DispatchPermitView{}, err
	}
	// A gate with an effective permit cannot accept new applications; pending
	// applications may still queue and are resolved when the reviewer signs.
	if active, err := s.repository.FindActiveForGate(ctx, gate.Code); err == nil {
		return dto.DispatchPermitView{}, fmt.Errorf("%w: gate %s already has effective permit %s", ErrPermitConflict, gate.Code, active.Code)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return dto.DispatchPermitView{}, err
	}
	if input.DurationMinutes < 5 || input.DurationMinutes > 1440 {
		return dto.DispatchPermitView{}, fmt.Errorf("%w: permit duration must be between 5 and 1440 minutes", ErrInvalidInput)
	}
	code := strings.ToUpper(strings.TrimSpace(input.Code))
	if code == "" {
		code = fmt.Sprintf("DP-%d", time.Now().UTC().UnixNano()%1_000_000_000)
	}
	now := time.Now().UTC()
	permit := model.DispatchPermit{
		Code: code, Status: string(constants.PermitStatePending), Version: 1,
		DirectiveID: directive.ID, DirectiveCode: directive.Code, GateID: gate.ID, GateCode: gate.Code,
		AppliedBy: actor, AppliedAt: now, RequestedWindow: input.DurationMinutes,
	}
	if err := s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.Create(txCtx, &permit); err != nil {
			if repository.IsUniqueViolation(err) {
				return ErrPermitConflict
			}
			return err
		}
		return s.security.Audit(txCtx, actor, requestID, "permit_apply", "DispatchPermit", permit.ID, "", permit.Status, input.Reason)
	}); err != nil {
		return dto.DispatchPermitView{}, err
	}
	created, getErr := s.repository.Get(ctx, permit.ID)
	if getErr != nil {
		return dto.DispatchPermitView{}, getErr
	}
	return toPermitView(created, now), nil
}

// Issue signs a pending permit. The reviewer may only sign when the gate slot
// is free of any other active permit; competing pending applications for the
// same gate are invalidated in the same transaction. The unique index on the
// active gate slot makes concurrent sign attempts for the same gate (or
// different directives on that gate) succeed exactly once.
func (s *dispatchPermitService) Issue(ctx context.Context, id uint, input dto.IssueDispatchPermit, actor, role, requestID string) (dto.DispatchPermitView, error) {
	if role != model.RoleReviewer && role != model.RoleAdmin {
		return dto.DispatchPermitView{}, ErrForbidden
	}
	permit, err := s.repository.Get(ctx, id)
	if err != nil {
		return dto.DispatchPermitView{}, err
	}
	if permit.Status != string(constants.PermitStatePending) {
		return dto.DispatchPermitView{}, fmt.Errorf("%w: only pending permits can be issued, current state %s", ErrPermitConflict, permit.Status)
	}
	if permit.Version != input.ExpectedVersion {
		return dto.DispatchPermitView{}, repository.ErrVersionConflict
	}
	directive, err := s.directives.Get(ctx, permit.DirectiveID)
	if err != nil {
		return dto.DispatchPermitView{}, err
	}
	if directive.Status != string(constants.DirectiveStateApproved) {
		return dto.DispatchPermitView{}, fmt.Errorf("%w: directive must remain approved while the permit is signed", ErrInvalidInput)
	}
	if !strings.EqualFold(directive.RelatedCode, permit.GateCode) {
		return dto.DispatchPermitView{}, fmt.Errorf("%w: permit gate %s no longer matches directive gate %s", ErrInvalidInput, permit.GateCode, directive.RelatedCode)
	}
	if strings.EqualFold(directive.SubmittedBy, actor) || strings.EqualFold(permit.AppliedBy, actor) {
		return dto.DispatchPermitView{}, ErrTwoPersonRequired
	}
	var result dto.DispatchPermitView
	err = s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		// The reviewer signs only when the gate has no other effective
		// permit. A competing active permit (including one signed by a
		// concurrent request) blocks the issue outright.
		if active, activeErr := s.repository.FindActiveForGate(txCtx, permit.GateCode); activeErr == nil {
			if active.ID != permit.ID {
				return fmt.Errorf("%w: gate %s already has effective permit %s", ErrPermitConflict, permit.GateCode, active.Code)
			}
		} else if !errors.Is(activeErr, gorm.ErrRecordNotFound) {
			return activeErr
		}
		// Every other pending application for the same gate dies as
		// invalidated in the same commit; only this signed permit survives.
		competitors, listErr := s.repository.ListPendingForGate(txCtx, permit.GateCode)
		if listErr != nil {
			return listErr
		}
		invalidatedAt := time.Now().UTC()
		for index := range competitors {
			other := competitors[index]
			if other.ID == permit.ID {
				continue
			}
			other.Status = "invalidated"
			other.Version++
			other.UpdatedAt = invalidatedAt
			other.InvalidatedBy = actor
			other.InvalidatedAt = &invalidatedAt
			cause := fmt.Sprintf("同闸门待审申请因许可 %s 签发而失效", permit.Code)
			other.InvalidateCause = cause
			if err := s.repository.Save(txCtx, &other); err != nil {
				return err
			}
			if err := s.security.Audit(txCtx, actor, requestID, "permit_invalidate", "DispatchPermit", other.ID, "pending", "invalidated", cause); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		validFrom := now
		window := permit.RequestedWindow
		if window < 5 || window > 1440 {
			window = defaultPermitMinutes
		}
		validUntil := now.Add(time.Duration(window) * time.Minute)
		permit.Status = "active"
		permit.Version++
		permit.ValidFrom = &validFrom
		permit.ValidUntil = &validUntil
		permit.IssuedBy = actor
		permit.IssuedAt = &now
		permit.IssueRequestID = requestID
		permit.IssueReason = strings.TrimSpace(input.Reason)
		permit.UpdatedAt = now
		if err := s.repository.Save(txCtx, &permit); err != nil {
			if repository.IsUniqueViolation(err) {
				return ErrPermitConflict
			}
			return err
		}
		if err := s.security.Audit(txCtx, actor, requestID, "permit_issue", "DispatchPermit", permit.ID, "pending", "active", input.Reason); err != nil {
			return err
		}
		stored, getErr := s.repository.Get(txCtx, permit.ID)
		if getErr != nil {
			return getErr
		}
		result = toPermitView(stored, now)
		return nil
	})
	return result, err
}

// Revoke withdraws an active permit before execution. Already executed
// directives are not rolled back: the permit simply records who revoked it
// and why, and the gate slot is released.
func (s *dispatchPermitService) Revoke(ctx context.Context, id uint, input dto.RevokeDispatchPermit, actor, role, requestID string) (dto.DispatchPermitView, error) {
	if role != model.RoleReviewer && role != model.RoleAdmin {
		return dto.DispatchPermitView{}, ErrForbidden
	}
	permit, err := s.repository.Get(ctx, id)
	if err != nil {
		return dto.DispatchPermitView{}, err
	}
	if permit.Status != "active" {
		return dto.DispatchPermitView{}, fmt.Errorf("%w: only effective permits can be revoked, current state %s", ErrPermitNotActive, permit.Status)
	}
	var result dto.DispatchPermitView
	err = s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		now := time.Now().UTC()
		permit.Status = "revoked"
		permit.Version++
		permit.RevokedBy = actor
		permit.RevokedAt = &now
		permit.RevokeReason = strings.TrimSpace(input.Reason)
		permit.UpdatedAt = now
		if err := s.repository.Save(txCtx, &permit); err != nil {
			return err
		}
		// Revocation never rewinds a directive that already consumed the
		// permit; we only record the reason for the audit trail.
		detail := input.Reason
		if permit.UsedAt != nil {
			detail = fmt.Sprintf("%s（指令已执行，状态不倒退）", detail)
		}
		if err := s.security.Audit(txCtx, actor, requestID, "permit_revoke", "DispatchPermit", permit.ID, "active", "revoked", detail); err != nil {
			return err
		}
		stored, getErr := s.repository.Get(txCtx, permit.ID)
		if getErr != nil {
			return getErr
		}
		result = toPermitView(stored, now)
		return nil
	})
	return result, err
}

func (s *dispatchPermitService) SweepExpired(ctx context.Context, actor string) ([]dto.DispatchPermitView, error) {
	now := time.Now().UTC()
	views := make([]dto.DispatchPermitView, 0)
	err := s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		due, dueErr := s.repository.ExpireDueActive(txCtx, now)
		if dueErr != nil {
			return dueErr
		}
		for _, permit := range due {
			cause := fmt.Sprintf("许可 %s 已超过有效期，自动失效", permit.Code)
			if auditErr := s.security.Audit(txCtx, actor, "permit-expiry-sweep", "permit_expire", "DispatchPermit", permit.ID, "active", "expired", cause); auditErr != nil {
				return auditErr
			}
			views = append(views, toPermitView(permit, now))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return views, nil
}

// sweepExpired lazily retires active permits whose window elapsed. It is
// invoked before reads and before execution so an expired permit can never
// gate an operation. Invalidating the row releases the active gate slot.
func (s *dispatchPermitService) sweepExpired(ctx context.Context, actor, requestID string) error {
	now := time.Now().UTC()
	due, err := s.repository.ExpireDueActive(ctx, now)
	if err != nil {
		return err
	}
	for _, permit := range due {
		cause := fmt.Sprintf("许可 %s 已超过有效期，自动失效", permit.Code)
		if auditErr := s.security.Audit(ctx, actor, requestID, "permit_expire", "DispatchPermit", permit.ID, "active", "expired", cause); auditErr != nil {
			return auditErr
		}
	}
	return nil
}

// RequireEffectivePermit is the pre-execution guard. It expires due permits
// first, then requires a single active, unrevoked, unexpired permit that
// names both the directive and its gate. A mismatch or missing permit blocks
// execution before any gate moves.
func (s *dispatchPermitService) RequireEffectivePermit(ctx context.Context, directiveID uint, gateCode string) (model.DispatchPermit, error) {
	if err := s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		return s.sweepExpired(txCtx, "system", "permit-expiry-on-execute")
	}); err != nil {
		return model.DispatchPermit{}, err
	}
	permit, err := s.repository.FindActiveForExecution(ctx, directiveID, gateCode)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.DispatchPermit{}, fmt.Errorf("%w: no effective dispatch permit for gate %s", ErrPermitNotActive, gateCode)
		}
		return model.DispatchPermit{}, err
	}
	now := time.Now().UTC()
	if permit.Status != "active" {
		return model.DispatchPermit{}, fmt.Errorf("%w: permit %s is %s", ErrPermitNotActive, permit.Code, permit.Status)
	}
	if permit.ValidUntil == nil || permit.ValidUntil.Before(now) {
		return model.DispatchPermit{}, fmt.Errorf("%w: permit %s has expired", ErrPermitNotActive, permit.Code)
	}
	if permit.UsedAt != nil {
		return model.DispatchPermit{}, fmt.Errorf("%w: permit %s was already consumed", ErrPermitNotActive, permit.Code)
	}
	if !strings.EqualFold(permit.GateCode, gateCode) {
		return model.DispatchPermit{}, fmt.Errorf("%w: permit %s is for gate %s, not %s", ErrPermitNotActive, permit.Code, permit.GateCode, gateCode)
	}
	return permit, nil
}

// ConsumePermit runs inside the directive execution transaction: it re-locks
// the permit row, verifies it remains active and unexpired, then stamps the
// usage time. Revocation or expiry winning the race aborts the whole
// directive transition.
func (s *dispatchPermitService) ConsumePermit(ctx context.Context, permit model.DispatchPermit) error {
	locked, err := s.repository.FindActiveForExecution(ctx, permit.DirectiveID, permit.GateCode)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: permit %s is no longer effective", ErrPermitNotActive, permit.Code)
		}
		return err
	}
	if locked.ID != permit.ID {
		return fmt.Errorf("%w: gate permit changed since validation", ErrPermitNotActive)
	}
	now := time.Now().UTC()
	if locked.ValidUntil == nil || locked.ValidUntil.Before(now) || locked.UsedAt != nil {
		return fmt.Errorf("%w: permit %s expired or consumed before execution", ErrPermitNotActive, locked.Code)
	}
	locked.UsedAt = &now
	locked.Version++
	locked.UpdatedAt = now
	return s.repository.Save(ctx, &locked)
}

const defaultPermitMinutes = 30
