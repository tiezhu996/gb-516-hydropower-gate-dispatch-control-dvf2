package repository

import (
	"context"
	"strings"
	"time"

	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/dto"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DispatchPermitRepository owns persistence for 闸门调度许可.
type DispatchPermitRepository interface {
	List(context.Context, dto.PageQuery) (Page[model.DispatchPermit], error)
	ListByDirectiveIDs(ctx context.Context, ids []uint) ([]model.DispatchPermit, error)
	Get(context.Context, uint) (model.DispatchPermit, error)
	GetByCode(context.Context, string) (model.DispatchPermit, error)
	Create(context.Context, *model.DispatchPermit) error
	Save(context.Context, *model.DispatchPermit) error
	// FindActiveForGate returns the single effective permit of a gate.
	FindActiveForGate(ctx context.Context, gateCode string) (model.DispatchPermit, error)
	// ListPendingForGate returns queued pending applications for a gate.
	ListPendingForGate(ctx context.Context, gateCode string) ([]model.DispatchPermit, error)
	// FindOpenForDirective returns the pending/active permit of a directive.
	FindOpenForDirective(ctx context.Context, directiveID uint) (model.DispatchPermit, error)
	// FindActiveForExecution locks the gate-scoped active permit row inside a
	// transaction so execution cannot race with revocation or expiry.
	FindActiveForExecution(ctx context.Context, directiveID uint, gateCode string) (model.DispatchPermit, error)
	// ExpireDueActive marks active permits past their valid-until time as
	// expired (releasing the gate slot) and returns the affected codes.
	ExpireDueActive(ctx context.Context, now time.Time) ([]model.DispatchPermit, error)
}

type dispatchPermitRepository struct {
	store *Store[model.DispatchPermit]
	db    *gorm.DB
}

func NewDispatchPermitRepository(db *gorm.DB) DispatchPermitRepository {
	return &dispatchPermitRepository{store: NewStore[model.DispatchPermit](db), db: db}
}

func (r *dispatchPermitRepository) List(ctx context.Context, q dto.PageQuery) (Page[model.DispatchPermit], error) {
	page, pageSize := normalizePage(q.Page, q.PageSize)
	db := databaseForContext(ctx, r.db).Model(&model.DispatchPermit{})
	if search := strings.TrimSpace(strings.ToLower(q.Search)); search != "" {
		wildcard := "%" + search + "%"
		db = db.Where("LOWER(code) LIKE ? OR LOWER(directive_code) LIKE ? OR LOWER(gate_code) LIKE ?", wildcard, wildcard, wildcard)
	}
	if status := strings.TrimSpace(q.Status); status != "" {
		db = db.Where("status = ?", status)
	}
	if directive := strings.TrimSpace(q.DirectiveCode); directive != "" {
		db = db.Where("directive_code = ?", strings.ToUpper(directive))
	}
	if gate := strings.TrimSpace(q.GateCode); gate != "" {
		db = db.Where("gate_code = ?", strings.ToUpper(gate))
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return Page[model.DispatchPermit]{}, err
	}
	items := make([]model.DispatchPermit, 0)
	err := db.Order("updated_at DESC, id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error
	return Page[model.DispatchPermit]{Items: items, Total: total, Page: page, PageSize: pageSize}, err
}

func (r *dispatchPermitRepository) ListByDirectiveIDs(ctx context.Context, ids []uint) ([]model.DispatchPermit, error) {
	items := make([]model.DispatchPermit, 0)
	if len(ids) == 0 {
		return items, nil
	}
	err := databaseForContext(ctx, r.db).Where("directive_id IN ?", ids).
		Order("created_at DESC, id DESC").Find(&items).Error
	return items, err
}

func (r *dispatchPermitRepository) Get(ctx context.Context, id uint) (model.DispatchPermit, error) {
	return r.store.Get(ctx, id)
}

func (r *dispatchPermitRepository) GetByCode(ctx context.Context, code string) (model.DispatchPermit, error) {
	var item model.DispatchPermit
	err := databaseForContext(ctx, r.db).Where("code = ?", strings.ToUpper(strings.TrimSpace(code))).First(&item).Error
	return item, err
}

func (r *dispatchPermitRepository) Create(ctx context.Context, item *model.DispatchPermit) error {
	item.PrepareForInsert()
	return databaseForContext(ctx, r.db).Create(item).Error
}

func (r *dispatchPermitRepository) Save(ctx context.Context, item *model.DispatchPermit) error {
	item.PrepareForUpdate()
	result := databaseForContext(ctx, r.db).Model(&model.DispatchPermit{}).
		Where("id = ? AND version = ?", item.ID, item.Version-1).
		Select("*").Omit("id", "code", "created_at").Save(item)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrVersionConflict
	}
	return nil
}

func (r *dispatchPermitRepository) FindActiveForGate(ctx context.Context, gateCode string) (model.DispatchPermit, error) {
	var item model.DispatchPermit
	err := databaseForContext(ctx, r.db).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("gate_code = ? AND status = ?", strings.ToUpper(strings.TrimSpace(gateCode)), "active").
		First(&item).Error
	return item, err
}

func (r *dispatchPermitRepository) ListPendingForGate(ctx context.Context, gateCode string) ([]model.DispatchPermit, error) {
	items := make([]model.DispatchPermit, 0)
	err := databaseForContext(ctx, r.db).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("gate_code = ? AND status = ?", strings.ToUpper(strings.TrimSpace(gateCode)), "pending").
		Order("created_at ASC, id ASC").Find(&items).Error
	return items, err
}

func (r *dispatchPermitRepository) FindOpenForDirective(ctx context.Context, directiveID uint) (model.DispatchPermit, error) {
	var item model.DispatchPermit
	err := databaseForContext(ctx, r.db).
		Where("directive_id = ? AND status IN ?", directiveID, []string{"pending", "active"}).
		Order("created_at DESC, id DESC").First(&item).Error
	return item, err
}

func (r *dispatchPermitRepository) FindActiveForExecution(ctx context.Context, directiveID uint, gateCode string) (model.DispatchPermit, error) {
	var item model.DispatchPermit
	err := databaseForContext(ctx, r.db).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("directive_id = ? AND gate_code = ? AND status = ?", directiveID, strings.ToUpper(strings.TrimSpace(gateCode)), "active").
		First(&item).Error
	return item, err
}

func (r *dispatchPermitRepository) ExpireDueActive(ctx context.Context, now time.Time) ([]model.DispatchPermit, error) {
	db := databaseForContext(ctx, r.db)
	var due []model.DispatchPermit
	if err := db.Where("status = ? AND valid_until IS NOT NULL AND valid_until <= ?", "active", now.UTC()).
		Find(&due).Error; err != nil {
		return nil, err
	}
	for index := range due {
		expiredAt := now.UTC()
		due[index].Status = "expired"
		due[index].Version++
		due[index].UpdatedAt = expiredAt
		due[index].ExpiredAt = &expiredAt
		due[index].PrepareForUpdate()
		if err := db.Model(&model.DispatchPermit{}).
			Where("id = ? AND version = ?", due[index].ID, due[index].Version-1).
			Select("*").Omit("id", "code", "created_at").Save(&due[index]).Error; err != nil {
			return nil, err
		}
	}
	return due, nil
}
