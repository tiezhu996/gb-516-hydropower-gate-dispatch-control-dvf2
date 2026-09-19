package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/constants"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/dto"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrPermitUniqueConflict is returned when the active-gate unique index rejects
// an insert or update. The service turns it into a 409 so concurrent reviewers
// learn the gate was already claimed.
var ErrPermitUniqueConflict = errors.New("gate dispatch permit unique constraint conflict")

// GateDispatchPermitRepository owns persistence for 闸门调度许可.
type GateDispatchPermitRepository interface {
	List(context.Context, dto.GateDispatchPermitQuery) (Page[model.GateDispatchPermit], error)
	Get(context.Context, uint) (model.GateDispatchPermit, error)
	GetByCode(context.Context, string) (model.GateDispatchPermit, error)
	GetForUpdate(context.Context, uint) (model.GateDispatchPermit, error)
	Create(context.Context, *model.GateDispatchPermit) error
	ActiveForGate(context.Context, string) (model.GateDispatchPermit, error)
	ActiveForDirective(context.Context, uint) (model.GateDispatchPermit, error)
	PendingForDirective(context.Context, uint) (model.GateDispatchPermit, error)
	// UpdateStatusGuarded applies fields only while the row still has
	// expectedVersion and expectStatus, providing optimistic locking plus the
	// lifecycle guard for issue, revoke, expiry and termination.
	UpdateStatusGuarded(context.Context, uint, uint, string, map[string]any) error
	// SupersedePendingForGate invalidates pending applications of the same gate
	// except the one being issued, returning the affected permit IDs so the
	// service can write audit evidence for each invalidation.
	SupersedePendingForGate(context.Context, string, uint, string) ([]uint, error)
	// SupersedePendingForDirective invalidates waiting applications when their
	// directive is aborted before issuance.
	SupersedePendingForDirective(context.Context, uint, string) error
}

type gateDispatchPermitRepository struct {
	db *gorm.DB
}

func NewGateDispatchPermitRepository(db *gorm.DB) GateDispatchPermitRepository {
	return &gateDispatchPermitRepository{db: db}
}

func (r *gateDispatchPermitRepository) List(ctx context.Context, q dto.GateDispatchPermitQuery) (Page[model.GateDispatchPermit], error) {
	page, pageSize := normalizePage(q.Page, q.PageSize)
	db := databaseForContext(ctx, r.db).Model(&model.GateDispatchPermit{})
	if code := strings.TrimSpace(q.DirectiveCode); code != "" {
		db = db.Where("directive_code = ?", strings.ToUpper(code))
	}
	if code := strings.TrimSpace(q.GateCode); code != "" {
		db = db.Where("gate_code = ?", strings.ToUpper(code))
	}
	if status := strings.TrimSpace(q.Status); status != "" {
		db = db.Where("status = ?", status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return Page[model.GateDispatchPermit]{}, err
	}
	items := make([]model.GateDispatchPermit, 0)
	if err := db.Order("updated_at DESC, id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		return Page[model.GateDispatchPermit]{}, err
	}
	now := time.Now().UTC()
	for index := range items {
		items[index].Decorate(now)
	}
	return Page[model.GateDispatchPermit]{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (r *gateDispatchPermitRepository) Get(ctx context.Context, id uint) (model.GateDispatchPermit, error) {
	var item model.GateDispatchPermit
	if err := databaseForContext(ctx, r.db).First(&item, id).Error; err != nil {
		return model.GateDispatchPermit{}, err
	}
	item.Decorate(time.Now().UTC())
	return item, nil
}

func (r *gateDispatchPermitRepository) GetByCode(ctx context.Context, code string) (model.GateDispatchPermit, error) {
	var item model.GateDispatchPermit
	err := databaseForContext(ctx, r.db).Where("code = ?", strings.ToUpper(strings.TrimSpace(code))).First(&item).Error
	if err != nil {
		return model.GateDispatchPermit{}, err
	}
	item.Decorate(time.Now().UTC())
	return item, nil
}

func (r *gateDispatchPermitRepository) GetForUpdate(ctx context.Context, id uint) (model.GateDispatchPermit, error) {
	var item model.GateDispatchPermit
	db := databaseForContext(ctx, r.db)
	// SQLite serializes writers and does not understand FOR UPDATE.
	if name := db.Dialector.Name(); name == "postgres" || name == "mysql" {
		db = db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := db.First(&item, id).Error; err != nil {
		return model.GateDispatchPermit{}, err
	}
	item.Decorate(time.Now().UTC())
	return item, nil
}

func (r *gateDispatchPermitRepository) Create(ctx context.Context, item *model.GateDispatchPermit) error {
	err := databaseForContext(ctx, r.db).Create(item).Error
	if err != nil && isPermitUniqueViolation(err) {
		return ErrPermitUniqueConflict
	}
	return err
}

func (r *gateDispatchPermitRepository) ActiveForGate(ctx context.Context, gateCode string) (model.GateDispatchPermit, error) {
	var item model.GateDispatchPermit
	err := databaseForContext(ctx, r.db).
		Where("gate_code = ? AND status = ?", model.NormalizeGateCode(gateCode), constants.PermitActiveState).
		First(&item).Error
	if err != nil {
		return model.GateDispatchPermit{}, err
	}
	item.Decorate(time.Now().UTC())
	return item, nil
}

func (r *gateDispatchPermitRepository) ActiveForDirective(ctx context.Context, directiveID uint) (model.GateDispatchPermit, error) {
	var item model.GateDispatchPermit
	err := databaseForContext(ctx, r.db).
		Where("directive_id = ? AND status = ?", directiveID, constants.PermitActiveState).
		First(&item).Error
	if err != nil {
		return model.GateDispatchPermit{}, err
	}
	item.Decorate(time.Now().UTC())
	return item, nil
}

func (r *gateDispatchPermitRepository) PendingForDirective(ctx context.Context, directiveID uint) (model.GateDispatchPermit, error) {
	var item model.GateDispatchPermit
	err := databaseForContext(ctx, r.db).
		Where("directive_id = ? AND status = ?", directiveID, string(constants.PermitStatePending)).
		Order("id ASC").First(&item).Error
	return item, err
}

func (r *gateDispatchPermitRepository) UpdateStatusGuarded(ctx context.Context, id, expectedVersion uint, expectStatus string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	if _, exists := fields["updated_at"]; !exists {
		fields["updated_at"] = time.Now().UTC()
	}
	result := databaseForContext(ctx, r.db).Model(&model.GateDispatchPermit{}).
		Where("id = ? AND version = ? AND status = ?", id, expectedVersion, expectStatus).
		Updates(fields)
	if result.Error != nil {
		if isPermitUniqueViolation(result.Error) {
			return ErrPermitUniqueConflict
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		// Distinguish a lifecycle/status change from a stale version by re-reading.
		var current model.GateDispatchPermit
		if err := databaseForContext(ctx, r.db).Select("status").First(&current, id).Error; err != nil {
			return err
		}
		if current.Status != expectStatus {
			return ErrPermitStateChanged
		}
		return ErrVersionConflict
	}
	return nil
}

func (r *gateDispatchPermitRepository) SupersedePendingForGate(ctx context.Context, gateCode string, keepDirectiveID uint, reason string) ([]uint, error) {
	db := databaseForContext(ctx, r.db)
	var ids []uint
	if err := db.Model(&model.GateDispatchPermit{}).
		Where("gate_code = ? AND status = ? AND directive_id <> ?", model.NormalizeGateCode(gateCode), string(constants.PermitStatePending), keepDirectiveID).
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := db.Model(&model.GateDispatchPermit{}).
		Where("id IN ?", ids).
		Updates(map[string]any{
			"status":       string(constants.PermitStateSuperseded),
			"close_reason": reason,
			"updated_at":   time.Now().UTC(),
		}).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *gateDispatchPermitRepository) SupersedePendingForDirective(ctx context.Context, directiveID uint, reason string) error {
	return databaseForContext(ctx, r.db).Model(&model.GateDispatchPermit{}).
		Where("directive_id = ? AND status = ?", directiveID, string(constants.PermitStatePending)).
		Updates(map[string]any{
			"status":       string(constants.PermitStateSuperseded),
			"close_reason": reason,
			"updated_at":   time.Now().UTC(),
		}).Error
}

// ErrPermitStateChanged reports that a guarded update targeted a row that had
// already left the expected lifecycle state.
var ErrPermitStateChanged = errors.New("gate dispatch permit state changed by another request")

func isPermitUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "duplicate key") ||
		strings.Contains(message, "1062")
}
