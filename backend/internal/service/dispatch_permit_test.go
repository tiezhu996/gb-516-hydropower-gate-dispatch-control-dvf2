package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/config"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/dto"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/model"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/repository"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type permitWorkflow struct {
	permits    DispatchPermitService
	directives OperationDirectiveService
	permitRepo repository.DispatchPermitRepository
	gateRepo   repository.GateUnitRepository
	db         *gorm.DB
}

func newPermitWorkflow(t *testing.T) permitWorkflow {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.GateUnit{}, &model.OperationDirective{}, &model.DirectiveApproval{}, &model.ExecutionConfirmation{}, &model.DispatchPermit{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	gateRepo := repository.NewGateUnitRepository(db)
	directiveRepo := repository.NewOperationDirectiveRepository(db)
	permitRepo := repository.NewDispatchPermitRepository(db)
	security := NewSecurityService(repository.NewSecurityRepository(db), config.Config{})
	permits := NewDispatchPermitService(permitRepo, directiveRepo, gateRepo, security)
	directives := NewOperationDirectiveService(directiveRepo, gateRepo, security, permits)
	for _, spec := range []struct{ code, status string }{
		{"GU-P1", "closed"}, {"GU-P2", "closed"},
	} {
		gate := model.GateUnit{BaseModel: model.BaseModel{Code: spec.code, Name: spec.code + " 闸门", Status: spec.status, Version: 1}, Facility: "主坝", Owner: "运行一组"}
		if err := gateRepo.Create(context.Background(), &gate); err != nil {
			t.Fatalf("create gate %s: %v", spec.code, err)
		}
	}
	return permitWorkflow{permits: permits, directives: directives, permitRepo: permitRepo, gateRepo: gateRepo, db: db}
}

func approveDirective(t *testing.T, wf permitWorkflow, code, gateCode string, targetGateState string) model.OperationDirective {
	t.Helper()
	ctx := context.Background()
	created, err := wf.directives.Create(ctx, dto.CreateOperationDirective{
		Code: code, Name: code + " 指令", Facility: "主坝", Owner: "运行一组",
		Category: "泄洪", RiskLevel: "high", MetricValue: 30, MetricUnit: "%",
		EffectiveAt: time.Now().UTC().Add(time.Hour), Evidence: "水位窗口已核对",
		RelatedCode: gateCode, GateState: targetGateState,
	}, "operator", "req-create")
	if err != nil {
		t.Fatalf("create directive: %v", err)
	}
	submitted, err := wf.directives.Transition(ctx, created.ID, dto.TransitionRequest{Status: "pending", ExpectedVersion: created.Version, Reason: "提交复核"}, "operator", model.RoleOperator, "req-submit")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	approved, err := wf.directives.Transition(ctx, submitted.ID, dto.TransitionRequest{Status: "approved", ExpectedVersion: submitted.Version, Reason: "独立复核通过"}, "reviewer", model.RoleReviewer, "req-approve")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	return approved
}

func applyAndIssue(t *testing.T, wf permitWorkflow, directiveCode string) dto.DispatchPermitView {
	t.Helper()
	ctx := context.Background()
	applied, err := wf.permits.Apply(ctx, dto.CreateDispatchPermit{
		Code: "DP-" + directiveCode, DirectiveCode: directiveCode, DurationMinutes: 30, Reason: "操作员申请限时许可",
	}, "operator", "req-apply")
	if err != nil {
		t.Fatalf("apply permit: %v", err)
	}
	issued, err := wf.permits.Issue(ctx, applied.ID, dto.IssueDispatchPermit{
		ExpectedVersion: applied.Version, Reason: "复核员确认闸门无生效许可",
	}, "reviewer", model.RoleReviewer, "req-issue")
	if err != nil {
		t.Fatalf("issue permit: %v", err)
	}
	return issued
}

func TestDispatchPermitFullLifecycleGatesExecution(t *testing.T) {
	wf := newPermitWorkflow(t)
	ctx := context.Background()
	approved := approveDirective(t, wf, "OD-LIFE", "GU-P1", "open")

	// Without a permit, execution is rejected.
	if _, err := wf.directives.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "无许可尝试执行",
	}, "operator", model.RoleOperator, "req-no-permit"); !errors.Is(err, ErrPermitNotActive) {
		t.Fatalf("execution without permit must fail, got %v", err)
	}

	issued := applyAndIssue(t, wf, "OD-LIFE")
	if issued.Status != "active" || issued.IssuedBy != "reviewer" || !issued.Effective {
		t.Fatalf("permit not active after issue: %#v", issued)
	}
	if issued.ValidUntil == nil || issued.ValidUntil.Sub(*issued.ValidFrom) != 30*time.Minute {
		t.Fatalf("unexpected validity window: from=%v until=%v", issued.ValidFrom, issued.ValidUntil)
	}

	// With the effective permit, execution proceeds and consumes it.
	executing, err := wf.directives.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "持有效许可开始执行",
	}, "operator", model.RoleOperator, "req-execute")
	if err != nil {
		t.Fatalf("execute with permit: %v", err)
	}
	storedPermit, _ := wf.permits.Get(ctx, issued.ID)
	if storedPermit.UsedAt == nil {
		t.Fatal("permit must be marked used after execution")
	}
	if executing.Status != "executing" {
		t.Fatalf("directive not executing: %s", executing.Status)
	}

	// Revoking after execution must not roll the directive back.
	revoked, err := wf.permits.Revoke(ctx, issued.ID, dto.RevokeDispatchPermit{Reason: "执行后撤销仅记录原因"}, "reviewer", model.RoleReviewer, "req-revoke")
	if err != nil {
		t.Fatalf("revoke used permit: %v", err)
	}
	if revoked.Status != "revoked" || revoked.RevokeReason == "" || revoked.RevokedBy != "reviewer" {
		t.Fatalf("revocation not recorded: %#v", revoked)
	}
	directive, _ := wf.directives.Get(ctx, approved.ID)
	if directive.Status != "executing" {
		t.Fatalf("executed directive must not roll back, got %s", directive.Status)
	}
}

func TestDispatchPermitRequiresApprovedDirective(t *testing.T) {
	wf := newPermitWorkflow(t)
	ctx := context.Background()
	// Create a draft directive directly (not approved).
	created, err := wf.directives.Create(ctx, dto.CreateOperationDirective{
		Code: "OD-DRAFT", Name: "草稿指令", Facility: "主坝", Owner: "运行一组",
		Category: "泄洪", RiskLevel: "low", MetricValue: 1, MetricUnit: "%",
		EffectiveAt: time.Now().UTC().Add(time.Hour), Evidence: "草稿", RelatedCode: "GU-P1", GateState: "closed",
	}, "operator", "req-create")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := wf.permits.Apply(ctx, dto.CreateDispatchPermit{
		DirectiveCode: created.Code, DurationMinutes: 30, Reason: "草稿不应申请许可",
	}, "operator", "req-apply"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("applying on non-approved directive must fail, got %v", err)
	}
}

func TestDispatchPermitIssueInvalidatesPendingAndBlocksDuplicateIssue(t *testing.T) {
	wf := newPermitWorkflow(t)
	ctx := context.Background()
	first := approveDirective(t, wf, "OD-A", "GU-P1", "open")
	second := approveDirective(t, wf, "OD-B", "GU-P1", "open") // same gate, different directive

	applied1, err := wf.permits.Apply(ctx, dto.CreateDispatchPermit{Code: "DP-A", DirectiveCode: first.Code, DurationMinutes: 30, Reason: "申请一"}, "operator", "req-apply-1")
	if err != nil {
		t.Fatalf("apply first: %v", err)
	}
	applied2, err := wf.permits.Apply(ctx, dto.CreateDispatchPermit{Code: "DP-B", DirectiveCode: second.Code, DurationMinutes: 30, Reason: "申请二"}, "operator", "req-apply-2")
	if err != nil {
		t.Fatalf("two pending applications for a gate should queue, got %v", err)
	}

	issued, err := wf.permits.Issue(ctx, applied1.ID, dto.IssueDispatchPermit{ExpectedVersion: applied1.Version, Reason: "签发第一个"}, "reviewer", model.RoleReviewer, "req-issue-1")
	if err != nil {
		t.Fatalf("issue first: %v", err)
	}
	if !issued.Effective {
		t.Fatal("first permit should be effective")
	}
	loser, _ := wf.permits.Get(ctx, applied2.ID)
	if loser.Status != "invalidated" || loser.InvalidateCause == "" {
		t.Fatalf("competing pending application must be invalidated: %#v", loser)
	}

	// Duplicate issue of the invalidated permit fails.
	if _, err := wf.permits.Issue(ctx, applied2.ID, dto.IssueDispatchPermit{ExpectedVersion: applied2.Version, Reason: "重复签发"}, "reviewer", model.RoleReviewer, "req-issue-2"); !errors.Is(err, ErrPermitConflict) {
		t.Fatalf("duplicate issue must fail, got %v", err)
	}

	// Neither directive on that gate can execute while permit covers OD-A:
	// OD-B has no effective permit at all.
	if _, err := wf.directives.Transition(ctx, second.ID, dto.TransitionRequest{Status: "executing", ExpectedVersion: second.Version, Reason: "无有效许可"}, "operator", model.RoleOperator, "req-exec-b"); !errors.Is(err, ErrPermitNotActive) {
		t.Fatalf("directive without permit must not execute, got %v", err)
	}
}

func TestDispatchPermitConcurrentIssueOnlySucceedsOnceAtDB(t *testing.T) {
	wf := newPermitWorkflow(t)
	ctx := context.Background()
	first := approveDirective(t, wf, "OD-C1", "GU-P1", "open")
	second := approveDirective(t, wf, "OD-C2", "GU-P1", "open")
	a1, err := wf.permits.Apply(ctx, dto.CreateDispatchPermit{Code: "DP-C1", DirectiveCode: first.Code, DurationMinutes: 30, Reason: "并发一"}, "operator", "r1")
	if err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	a2, err := wf.permits.Apply(ctx, dto.CreateDispatchPermit{Code: "DP-C2", DirectiveCode: second.Code, DurationMinutes: 30, Reason: "并发二"}, "operator", "r2")
	if err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	// Simulate the losing sign committing its active slot row directly while
	// the winner already holds it: the unique index must reject it.
	winner := applyAndIssueRaw(t, wf, a1.ID)
	_ = winner
	loserPermit, err := wf.permitRepo.Get(ctx, a2.ID)
	if err != nil {
		t.Fatalf("load loser: %v", err)
	}
	loserPermit.Status = "active"
	loserPermit.Version++
	gate := "GU-P1"
	loserPermit.ActiveGateCode = &gate
	if err := wf.permitRepo.Save(ctx, &loserPermit); !repository.IsUniqueViolation(err) {
		t.Fatalf("concurrent active slot must violate unique index, got %v", err)
	}
}

func applyAndIssueRaw(t *testing.T, wf permitWorkflow, id uint) dto.DispatchPermitView {
	t.Helper()
	view, err := wf.permits.Issue(context.Background(), id, dto.IssueDispatchPermit{ExpectedVersion: 1, Reason: "占用闸门槽位"}, "reviewer", model.RoleReviewer, "r-issue")
	if err != nil {
		t.Fatalf("issue raw: %v", err)
	}
	return view
}

func TestDispatchPermitCrossGateApplicationRejectedAndExpiryBlocksExecution(t *testing.T) {
	wf := newPermitWorkflow(t)
	ctx := context.Background()
	approved := approveDirective(t, wf, "OD-X", "GU-P1", "open")

	// Cross-gate assertion: naming another gate is rejected.
	if _, err := wf.permits.Apply(ctx, dto.CreateDispatchPermit{
		DirectiveCode: approved.Code, GateCode: "GU-P2", DurationMinutes: 30, Reason: "跨闸门申请",
	}, "operator", "req-cross"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-gate application must fail, got %v", err)
	}

	issued := applyAndIssue(t, wf, "OD-X")

	// Force the permit beyond its validity window; execution must be blocked.
	raw, err := wf.permitRepo.Get(ctx, issued.ID)
	if err != nil {
		t.Fatalf("load permit: %v", err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	raw.ValidUntil = &past
	if err := wf.db.Model(&model.DispatchPermit{}).Where("id = ?", raw.ID).Update("valid_until", past).Error; err != nil {
		t.Fatalf("expire permit: %v", err)
	}
	if _, err := wf.directives.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "过期许可尝试执行",
	}, "operator", model.RoleOperator, "req-expired-exec"); !errors.Is(err, ErrPermitNotActive) {
		t.Fatalf("execution with expired permit must fail, got %v", err)
	}
	// The failed attempt lazily persisted the expiry and released the gate.
	swept, err := wf.permitRepo.Get(ctx, issued.ID)
	if err != nil || swept.Status != "expired" || swept.ExpiredAt == nil {
		t.Fatalf("permit should be lazily expired: %#v err=%v", swept, err)
	}
	if swept.ActiveGateCode != nil {
		t.Fatal("expired permit must release the active gate slot")
	}
}

func TestDispatchPermitRevocationBlocksExecution(t *testing.T) {
	wf := newPermitWorkflow(t)
	ctx := context.Background()
	approved := approveDirective(t, wf, "OD-R", "GU-P1", "open")
	issued := applyAndIssue(t, wf, "OD-R")
	if _, err := wf.permits.Revoke(ctx, issued.ID, dto.RevokeDispatchPermit{Reason: "现场条件不满足，撤销许可"}, "reviewer", model.RoleReviewer, "req-revoke"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := wf.directives.Transition(ctx, approved.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: approved.Version, Reason: "撤销后尝试执行",
	}, "operator", model.RoleOperator, "req-revoked-exec"); !errors.Is(err, ErrPermitNotActive) {
		t.Fatalf("execution after revocation must fail, got %v", err)
	}
	// After revocation the gate slot is free: the same approved directive can
	// apply again and receive a freshly signed permit.
	reapplied, err := wf.permits.Apply(ctx, dto.CreateDispatchPermit{
		Code: "DP-R2", DirectiveCode: approved.Code, DurationMinutes: 30, Reason: "重新申请",
	}, "operator", "req-reapply")
	if err != nil {
		t.Fatalf("re-apply after revocation: %v", err)
	}
	reissued, err := wf.permits.Issue(ctx, reapplied.ID, dto.IssueDispatchPermit{ExpectedVersion: reapplied.Version, Reason: "重新签发"}, "reviewer", model.RoleReviewer, "req-reissue")
	if err != nil || !reissued.Effective {
		t.Fatalf("reissue after revocation should succeed, view=%#v err=%v", reissued, err)
	}
}

func TestDispatchPermitTwoPersonRuleOnIssue(t *testing.T) {
	wf := newPermitWorkflow(t)
	ctx := context.Background()
	approved := approveDirective(t, wf, "OD-TP", "GU-P1", "open")
	applied, err := wf.permits.Apply(ctx, dto.CreateDispatchPermit{
		Code: "DP-TP", DirectiveCode: approved.Code, DurationMinutes: 30, Reason: "申请",
	}, "operator", "req-apply")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	// The submitter/applicant must not sign the permit.
	if _, err := wf.permits.Issue(ctx, applied.ID, dto.IssueDispatchPermit{ExpectedVersion: applied.Version, Reason: "自签"}, "operator", model.RoleReviewer, "req-self"); !errors.Is(err, ErrTwoPersonRequired) {
		t.Fatalf("self-issue must fail two-person rule, got %v", err)
	}
}
