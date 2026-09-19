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

type GateDispatchPermitService interface {
	List(context.Context, dto.GateDispatchPermitQuery) (repository.Page[model.GateDispatchPermit], error)
	Get(context.Context, uint) (model.GateDispatchPermit, error)
	Apply(context.Context, dto.ApplyGateDispatchPermit, string, string) (model.GateDispatchPermit, error)
	Issue(context.Context, uint, dto.IssueGateDispatchPermit, string, string) (model.GateDispatchPermit, error)
	Revoke(context.Context, uint, dto.RevokeGateDispatchPermit, string, string) (model.GateDispatchPermit, error)
	// ValidateForExecution enforces "valid, not revoked, same gate" immediately
	// before an approved directive enters executing.
	ValidateForExecution(ctx context.Context, directiveID uint, gateCode string, actor, requestID string) error
	// AssertExecutable is the read-only re-check used inside the execution
	// transaction to close the revoke/execute race window; it never writes.
	AssertExecutable(ctx context.Context, directiveID uint, gateCode string) error
	// CloseForDirective terminates permits when a directive aborts so the gate is
	// not held forever; executed directives are never rolled back.
	CloseForDirective(ctx context.Context, directiveID uint, reason, actor, requestID string) error
}

type gateDispatchPermitService struct {
	repository repository.GateDispatchPermitRepository
	directives repository.OperationDirectiveRepository
	gates      repository.GateUnitRepository
	security   SecurityService
}

func NewGateDispatchPermitService(repo repository.GateDispatchPermitRepository, directives repository.OperationDirectiveRepository, gates repository.GateUnitRepository, security SecurityService) GateDispatchPermitService {
	return &gateDispatchPermitService{repository: repo, directives: directives, gates: gates, security: security}
}

func (s *gateDispatchPermitService) List(ctx context.Context, query dto.GateDispatchPermitQuery) (repository.Page[model.GateDispatchPermit], error) {
	return s.repository.List(ctx, query)
}

func (s *gateDispatchPermitService) Get(ctx context.Context, id uint) (model.GateDispatchPermit, error) {
	return s.repository.Get(ctx, id)
}

func (s *gateDispatchPermitService) Apply(ctx context.Context, input dto.ApplyGateDispatchPermit, actor, requestID string) (model.GateDispatchPermit, error) {
	directive, err := s.directives.Get(ctx, input.DirectiveID)
	if err != nil {
		return model.GateDispatchPermit{}, fmt.Errorf("linked directive: %w", err)
	}
	if directive.Status != string(constants.DirectiveStateApproved) {
		return model.GateDispatchPermit{}, fmt.Errorf("%w: permit application requires an approved directive", ErrInvalidInput)
	}
	requestedGate := model.NormalizeGateCode(input.GateCode)
	if requestedGate != model.NormalizeGateCode(directive.RelatedCode) {
		return model.GateDispatchPermit{}, fmt.Errorf("%w: application gate %s does not match directive gate %s", ErrInvalidInput, requestedGate, directive.RelatedCode)
	}
	if _, err := s.gates.GetByCode(ctx, requestedGate); err != nil {
		return model.GateDispatchPermit{}, fmt.Errorf("linked gate %q: %w", requestedGate, err)
	}
	if _, err := s.repository.PendingForDirective(ctx, directive.ID); err == nil {
		return model.GateDispatchPermit{}, fmt.Errorf("%w: directive already has a pending permit application", ErrPermitConflict)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.GateDispatchPermit{}, err
	}
	now := time.Now().UTC()
	if err := s.expireGatePermitIfDue(ctx, requestedGate, actor, requestID, now); err != nil {
		return model.GateDispatchPermit{}, err
	}
	if active, err := s.repository.ActiveForGate(ctx, requestedGate); err == nil {
		// Another effective permit occupies the gate: the application is rejected.
		return model.GateDispatchPermit{}, fmt.Errorf("%w: gate %s already has effective permit %s", ErrPermitConflict, requestedGate, active.Code)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.GateDispatchPermit{}, err
	}
	item := model.GateDispatchPermit{
		Code:          strings.ToUpper(strings.TrimSpace(input.Code)),
		DirectiveID:   directive.ID,
		DirectiveCode: directive.Code,
		GateCode:      requestedGate,
		Status:        string(constants.PermitStatePending),
		RequestedBy:   actor,
		RequestedAt:   now,
		RequestReason: strings.TrimSpace(input.RequestReason),
		ValidFrom:     now,
		ValidUntil:    now.Add(time.Duration(input.ValidMinutes) * time.Minute),
		Version:       1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.Create(txCtx, &item); err != nil {
			return err
		}
		return s.security.Audit(txCtx, actor, requestID, "permit_apply", "GateDispatchPermit", item.ID, "", item.Status, item.RequestReason)
	}); err != nil {
		return model.GateDispatchPermit{}, mapPermitPersistenceError(err, "apply")
	}
	return s.repository.Get(ctx, item.ID)
}

func (s *gateDispatchPermitService) Issue(ctx context.Context, id uint, input dto.IssueGateDispatchPermit, actor, requestID string) (model.GateDispatchPermit, error) {
	permit, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.GateDispatchPermit{}, err
	}
	if permit.Status != string(constants.PermitStatePending) {
		return model.GateDispatchPermit{}, fmt.Errorf("%w: only pending applications can be issued", ErrPermitStateChange)
	}
	directive, err := s.directives.Get(ctx, permit.DirectiveID)
	if err != nil {
		return model.GateDispatchPermit{}, fmt.Errorf("linked directive: %w", err)
	}
	if directive.Status != string(constants.DirectiveStateApproved) {
		return model.GateDispatchPermit{}, fmt.Errorf("%w: permit can only be issued for an approved directive", ErrInvalidInput)
	}
	if model.NormalizeGateCode(permit.GateCode) != model.NormalizeGateCode(directive.RelatedCode) {
		return model.GateDispatchPermit{}, fmt.Errorf("%w: permit gate does not match the directive gate", ErrInvalidInput)
	}
	// Two-person rule for the permit itself: the reviewer signing the permit
	// must not be the operator who requested it.
	if strings.EqualFold(strings.TrimSpace(permit.RequestedBy), strings.TrimSpace(actor)) {
		return model.GateDispatchPermit{}, ErrTwoPersonRequired
	}
	now := time.Now().UTC()
	supersedeReason := fmt.Sprintf("许可 %s 经 %s 签发，同闸门待审申请失效", permit.Code, actor)
	auditBefore := permit.Status
	err = s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		// Lock the application row first so concurrent issuance of the same
		// application serializes; the loser hits the status/version guard.
		locked, lockErr := s.repository.GetForUpdate(txCtx, id)
		if lockErr != nil {
			return lockErr
		}
		if locked.Status != string(constants.PermitStatePending) {
			return fmt.Errorf("%w: application was already decided", ErrPermitStateChange)
		}
		// Re-check inside the transaction that no effective permit occupies the
		// gate. A permit past its validity is expired and recorded as such so
		// the unique gate slot is released; the unique index is the final
		// guarantee against preemption.
		active, findErr := s.repository.ActiveForGate(txCtx, permit.GateCode)
		if findErr == nil {
			if active.ValidUntil.After(now) {
				return fmt.Errorf("%w: gate %s already has effective permit %s", ErrPermitConflict, permit.GateCode, active.Code)
			}
			if err := s.persistExpiry(txCtx, active, actor, requestID, now); err != nil {
				return mapPermitPersistenceError(err, "expire")
			}
		} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
		supersededIDs, err := s.repository.SupersedePendingForGate(txCtx, permit.GateCode, permit.DirectiveID, supersedeReason)
		if err != nil {
			return err
		}
		fields := map[string]any{
			"status":          string(constants.PermitStateIssued),
			"active_gate_key": permit.GateCode,
			"issued_by":       actor,
			"issued_at":       now,
			"issue_reason":    strings.TrimSpace(input.IssueReason),
			"version":         input.ExpectedVersion + 1,
			"updated_at":      now,
		}
		if err := s.repository.UpdateStatusGuarded(txCtx, id, input.ExpectedVersion, string(constants.PermitStatePending), fields); err != nil {
			return err
		}
		for _, supersededID := range supersededIDs {
			if err := s.security.Audit(txCtx, actor, requestID, "permit_supersede", "GateDispatchPermit", supersededID, string(constants.PermitStatePending), string(constants.PermitStateSuperseded), supersedeReason); err != nil {
				return err
			}
		}
		return s.security.Audit(txCtx, actor, requestID, "permit_issue", "GateDispatchPermit", id, auditBefore, string(constants.PermitStateIssued), strings.TrimSpace(input.IssueReason))
	})
	if err != nil {
		return model.GateDispatchPermit{}, mapPermitPersistenceError(err, "issue")
	}
	return s.repository.Get(ctx, id)
}

func (s *gateDispatchPermitService) Revoke(ctx context.Context, id uint, input dto.RevokeGateDispatchPermit, actor, requestID string) (model.GateDispatchPermit, error) {
	permit, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.GateDispatchPermit{}, err
	}
	if permit.Status != string(constants.PermitStateIssued) {
		return model.GateDispatchPermit{}, fmt.Errorf("%w: only an issued permit can be revoked", ErrPermitStateChange)
	}
	now := time.Now().UTC()
	if !permit.ValidUntil.After(now) {
		// Already past validity: record the expiry instead of a revocation.
		if err := s.persistExpiry(ctx, permit, actor, requestID, now); err != nil {
			return model.GateDispatchPermit{}, mapPermitPersistenceError(err, "revoke")
		}
		return s.repository.Get(ctx, id)
	}
	reason := strings.TrimSpace(input.RevokeReason)
	fields := map[string]any{
		"status":          string(constants.PermitStateRevoked),
		"active_gate_key": nil,
		"revoked_by":      actor,
		"revoked_at":      now,
		"revoke_reason":   reason,
		"version":         input.ExpectedVersion + 1,
		"updated_at":      now,
	}
	err = s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.UpdateStatusGuarded(txCtx, id, input.ExpectedVersion, string(constants.PermitStateIssued), fields); err != nil {
			return err
		}
		return s.security.Audit(txCtx, actor, requestID, "permit_revoke", "GateDispatchPermit", id, string(constants.PermitStateIssued), string(constants.PermitStateRevoked), reason)
	})
	if err != nil {
		return model.GateDispatchPermit{}, mapPermitPersistenceError(err, "revoke")
	}
	return s.repository.Get(ctx, id)
}

func (s *gateDispatchPermitService) ValidateForExecution(ctx context.Context, directiveID uint, gateCode string, actor, requestID string) error {
	_, err := s.requireExecutablePermit(ctx, directiveID, gateCode, time.Now().UTC(), actor, requestID)
	return err
}

func (s *gateDispatchPermitService) AssertExecutable(ctx context.Context, directiveID uint, gateCode string) error {
	_, err := s.requireExecutablePermit(ctx, directiveID, gateCode, time.Now().UTC(), "system", "execution-recheck")
	return err
}

// requireExecutablePermit enforces "exists, issued, same gate, within window".
// When the only problem is an elapsed validity window, the expiry is recorded
// immediately (independent transaction) so the gate is released and the reason
// is visible; revocation and absence never mutate anything.
func (s *gateDispatchPermitService) requireExecutablePermit(ctx context.Context, directiveID uint, gateCode string, now time.Time, actor, requestID string) (model.GateDispatchPermit, error) {
	permit, err := s.repository.ActiveForDirective(ctx, directiveID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.GateDispatchPermit{}, fmt.Errorf("%w: no effective permit for this directive", ErrPermitRequired)
		}
		return model.GateDispatchPermit{}, err
	}
	if model.NormalizeGateCode(permit.GateCode) != model.NormalizeGateCode(gateCode) {
		return model.GateDispatchPermit{}, fmt.Errorf("%w: permit gate %s does not match execution gate %s", ErrPermitRequired, permit.GateCode, gateCode)
	}
	if !permit.ValidFrom.Before(now) {
		return model.GateDispatchPermit{}, fmt.Errorf("%w: permit %s is not valid before %s", ErrPermitRequired, permit.Code, permit.ValidFrom.Format(time.RFC3339))
	}
	if !permit.ValidUntil.After(now) {
		if persistErr := s.persistExpiry(ctx, permit, actor, requestID, now); persistErr != nil {
			return model.GateDispatchPermit{}, mapPermitPersistenceError(persistErr, "expire")
		}
		return model.GateDispatchPermit{}, fmt.Errorf("%w: permit %s expired at %s", ErrPermitRequired, permit.Code, permit.ValidUntil.Format(time.RFC3339))
	}
	return permit, nil
}

func (s *gateDispatchPermitService) CloseForDirective(ctx context.Context, directiveID uint, reason, actor, requestID string) error {
	active, err := s.repository.ActiveForDirective(ctx, directiveID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// No issued permit: a still-waiting application is invalidated so it
		// cannot be signed against an aborted directive.
		return s.supersedePendingForDirective(ctx, directiveID, reason, actor, requestID)
	}
	now := time.Now().UTC()
	if !active.ValidUntil.After(now) {
		return s.persistExpiry(ctx, active, actor, requestID, now)
	}
	fields := map[string]any{
		"status":          string(constants.PermitStateTerminated),
		"active_gate_key": nil,
		"close_reason":    reason,
		"version":         active.Version + 1,
		"updated_at":      now,
	}
	return s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.UpdateStatusGuarded(txCtx, active.ID, active.Version, string(constants.PermitStateIssued), fields); err != nil {
			return err
		}
		return s.security.Audit(txCtx, actor, requestID, "permit_terminate", "GateDispatchPermit", active.ID, string(constants.PermitStateIssued), string(constants.PermitStateTerminated), reason)
	})
}

func (s *gateDispatchPermitService) supersedePendingForDirective(ctx context.Context, directiveID uint, reason, actor, requestID string) error {
	pending, err := s.repository.PendingForDirective(ctx, directiveID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	return s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.SupersedePendingForDirective(txCtx, directiveID, reason); err != nil {
			return err
		}
		return s.security.Audit(txCtx, actor, requestID, "permit_supersede", "GateDispatchPermit", pending.ID, string(constants.PermitStatePending), string(constants.PermitStateSuperseded), reason)
	})
}

// expireGatePermitIfDue lazily records expiry of an issued gate permit whose
// validity window has already elapsed, releasing the gate for new applications.
func (s *gateDispatchPermitService) expireGatePermitIfDue(ctx context.Context, gateCode, actor, requestID string, now time.Time) error {
	permit, err := s.repository.ActiveForGate(ctx, gateCode)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if permit.ValidUntil.After(now) {
		return nil
	}
	return s.persistExpiry(ctx, permit, actor, requestID, now)
}

func (s *gateDispatchPermitService) persistExpiry(ctx context.Context, permit model.GateDispatchPermit, actor, requestID string, now time.Time) error {
	reason := fmt.Sprintf("许可有效期截止 %s，系统记录过期", permit.ValidUntil.Format(time.RFC3339))
	fields := map[string]any{
		"status":          string(constants.PermitStateExpired),
		"active_gate_key": nil,
		"close_reason":    reason,
		"version":         permit.Version + 1,
		"updated_at":      now,
	}
	return s.security.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.UpdateStatusGuarded(txCtx, permit.ID, permit.Version, string(constants.PermitStateIssued), fields); err != nil {
			return err
		}
		return s.security.Audit(txCtx, actor, requestID, "permit_expire", "GateDispatchPermit", permit.ID, string(constants.PermitStateIssued), string(constants.PermitStateExpired), reason)
	})
}

func mapPermitPersistenceError(err error, action string) error {
	switch {
	case errors.Is(err, repository.ErrPermitUniqueConflict):
		return fmt.Errorf("%w: duplicate issuance or concurrent preemption rejected", ErrPermitConflict)
	case errors.Is(err, repository.ErrPermitStateChanged):
		return fmt.Errorf("%w: permit was already decided", ErrPermitStateChange)
	case errors.Is(err, repository.ErrVersionConflict):
		return fmt.Errorf("%w: permit version conflict during %s", ErrPermitStateChange, action)
	default:
		return fmt.Errorf("%s gate dispatch permit: %w", action, err)
	}
}
