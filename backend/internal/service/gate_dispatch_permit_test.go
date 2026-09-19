package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/config"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/dto"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/model"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/repository"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func buildPermitStack(db *gorm.DB) permitStack {
	gateRepo := repository.NewGateUnitRepository(db)
	directiveRepo := repository.NewOperationDirectiveRepository(db)
	permitRepo := repository.NewGateDispatchPermitRepository(db)
	security := NewSecurityService(repository.NewSecurityRepository(db), config.Config{})
	permits := NewGateDispatchPermitService(permitRepo, directiveRepo, gateRepo, security)
	directives := NewOperationDirectiveService(directiveRepo, gateRepo, permits, security)
	return permitStack{permits: permits, directives: directives, db: db}
}

type permitStack struct {
	permits    GateDispatchPermitService
	directives OperationDirectiveService
	db         *gorm.DB
}

func newPermitStack(t *testing.T) permitStack {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.GateUnit{}, &model.OperationDirective{}, &model.DirectiveApproval{}, &model.AuditLog{}, &model.GateDispatchPermit{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gate := model.GateUnit{BaseModel: model.BaseModel{Code: "GU-A", Name: "左岸泄洪闸", Status: "closed", Version: 1}, Facility: "左坝段", Owner: "运行一组"}
	other := model.GateUnit{BaseModel: model.BaseModel{Code: "GU-B", Name: "右岸泄洪闸", Status: "closed", Version: 1}, Facility: "右坝段", Owner: "运行一组"}
	if err := db.Create(&gate).Error; err != nil {
		t.Fatalf("create gate: %v", err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other gate: %v", err)
	}
	return buildPermitStack(db)
}

func approvedPermitDirective(t *testing.T, stack permitStack, code, gateCode string) model.OperationDirective {
	t.Helper()
	ctx := context.Background()
	facility := "左坝段"
	if gateCode == "GU-B" {
		facility = "右坝段"
	}
	created, err := stack.directives.Create(ctx, dto.CreateOperationDirective{
		Code: code, Name: "限时闸门调度", Facility: facility, Owner: "运行一组",
		Category: "泄洪", RiskLevel: "high", MetricValue: 30, MetricUnit: "%",
		EffectiveAt: time.Now().UTC().Add(time.Hour), Evidence: "水位窗口已核对",
		RelatedCode: gateCode, GateState: "open",
	}, "operator", "req-"+code+"-create")
	if err != nil {
		t.Fatalf("create directive: %v", err)
	}
	submitted, err := stack.directives.Transition(ctx, created.ID, dto.TransitionRequest{Status: "pending", ExpectedVersion: created.Version, Reason: "提交复核"}, "operator", model.RoleOperator, "req-"+code+"-submit")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	approved, err := stack.directives.Transition(ctx, submitted.ID, dto.TransitionRequest{Status: "approved", ExpectedVersion: submitted.Version, Reason: "复核通过"}, "reviewer", model.RoleReviewer, "req-"+code+"-approve")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	return approved
}

func applyAndIssue(t *testing.T, stack permitStack, directiveID uint, code string, minutes int) model.GateDispatchPermit {
	t.Helper()
	ctx := context.Background()
	directive, err := stack.directives.Get(ctx, directiveID)
	if err != nil {
		t.Fatalf("load directive: %v", err)
	}
	application, err := stack.permits.Apply(ctx, dto.ApplyGateDispatchPermit{
		Code: code, DirectiveID: directiveID, GateCode: directive.RelatedCode,
		ValidMinutes: minutes, RequestReason: "操作员限时许可申请",
	}, "operator", "req-"+code+"-apply")
	if err != nil {
		t.Fatalf("apply permit: %v", err)
	}
	issued, err := stack.permits.Issue(ctx, application.ID, dto.IssueGateDispatchPermit{
		ExpectedVersion: application.Version, IssueReason: "复核员确认无生效许可，签发",
	}, "reviewer", "req-"+code+"-issue")
	if err != nil {
		t.Fatalf("issue permit: %v", err)
	}
	return issued
}

func TestPermitApplicationRequiresApprovedDirectiveAndMatchingGate(t *testing.T) {
	stack := newPermitStack(t)
	ctx := context.Background()
	// Draft directive cannot be used.
	draft, err := stack.directives.Create(ctx, dto.CreateOperationDirective{
		Code: "OD-DRAFT", Name: "草稿指令", Facility: "左坝段", Owner: "运行一组",
		Category: "泄洪", RiskLevel: "high", EffectiveAt: time.Now().UTC().Add(time.Hour),
		Evidence: "证据", RelatedCode: "GU-A", GateState: "open",
	}, "operator", "req-draft")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = stack.permits.Apply(ctx, dto.ApplyGateDispatchPermit{
		Code: "GDP-DRAFT", DirectiveID: draft.ID, GateCode: "GU-A",
		ValidMinutes: 30, RequestReason: "未批准申请应拒绝",
	}, "operator", "req-apply-draft")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("application on draft directive should fail validation, got %v", err)
	}

	approved := approvedPermitDirective(t, stack, "OD-MATCH", "GU-A")
	// Cross-gate application is rejected.
	_, err = stack.permits.Apply(ctx, dto.ApplyGateDispatchPermit{
		Code: "GDP-CROSS", DirectiveID: approved.ID, GateCode: "GU-B",
		ValidMinutes: 30, RequestReason: "跨闸门申请",
	}, "operator", "req-apply-cross")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-gate application should fail, got %v", err)
	}
}

func TestPermitIssueOnlyOnceAndSupersedesPendingApplications(t *testing.T) {
	stack := newPermitStack(t)
	ctx := context.Background()
	first := approvedPermitDirective(t, stack, "OD-FIRST", "GU-A")
	second := approvedPermitDirective(t, stack, "OD-SECOND", "GU-A")

	firstApp, err := stack.permits.Apply(ctx, dto.ApplyGateDispatchPermit{
		Code: "GDP-1", DirectiveID: first.ID, GateCode: "GU-A",
		ValidMinutes: 30, RequestReason: "申请一",
	}, "operator", "req-apply-1")
	if err != nil {
		t.Fatalf("apply first: %v", err)
	}
	secondApp, err := stack.permits.Apply(ctx, dto.ApplyGateDispatchPermit{
		Code: "GDP-2", DirectiveID: second.ID, GateCode: "GU-A",
		ValidMinutes: 30, RequestReason: "申请二",
	}, "operator", "req-apply-2")
	if err != nil {
		t.Fatalf("apply second: %v", err)
	}
	issued, err := stack.permits.Issue(ctx, firstApp.ID, dto.IssueGateDispatchPermit{
		ExpectedVersion: firstApp.Version, IssueReason: "签发申请一",
	}, "reviewer", "req-issue-1")
	if err != nil {
		t.Fatalf("issue first: %v", err)
	}
	if issued.Status != "issued" || issued.IssuedBy != "reviewer" {
		t.Fatalf("unexpected issued permit: %#v", issued)
	}
	// Duplicate issuance of the same application fails.
	_, err = stack.permits.Issue(ctx, firstApp.ID, dto.IssueGateDispatchPermit{
		ExpectedVersion: firstApp.Version, IssueReason: "重复签发",
	}, "reviewer", "req-issue-dup")
	if !errors.Is(err, ErrPermitStateChange) {
		t.Fatalf("duplicate issuance must fail, got %v", err)
	}
	// Second pending application was superseded and cannot be issued.
	_, err = stack.permits.Issue(ctx, secondApp.ID, dto.IssueGateDispatchPermit{
		ExpectedVersion: secondApp.Version, IssueReason: "抢占闸门",
	}, "reviewer", "req-issue-2")
	if !errors.Is(err, ErrPermitStateChange) {
		t.Fatalf("superseded application cannot be issued, got %v", err)
	}
	superseded, err := stack.permits.Get(ctx, secondApp.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if superseded.Status != "superseded" || superseded.CloseReason == "" {
		t.Fatalf("pending application should be superseded with reason: %#v", superseded)
	}
	// A fresh application for the occupied gate is rejected.
	third := approvedPermitDirective(t, stack, "OD-THIRD", "GU-A")
	_, err = stack.permits.Apply(ctx, dto.ApplyGateDispatchPermit{
		Code: "GDP-3", DirectiveID: third.ID, GateCode: "GU-A",
		ValidMinutes: 30, RequestReason: "闸门被占用",
	}, "operator", "req-apply-3")
	if !errors.Is(err, ErrPermitConflict) {
		t.Fatalf("application while permit effective must conflict, got %v", err)
	}
	// Another gate is unaffected.
	fourth := approvedPermitDirective(t, stack, "OD-FOURTH", "GU-B")
	if _, err := stack.permits.Apply(ctx, dto.ApplyGateDispatchPermit{
		Code: "GDP-4", DirectiveID: fourth.ID, GateCode: "GU-B",
		ValidMinutes: 30, RequestReason: "其他闸门可申请",
	}, "operator", "req-apply-4"); err != nil {
		t.Fatalf("permit for another gate should succeed: %v", err)
	}
}

func TestConcurrentIssueOnlySucceedsOnce(t *testing.T) {
	stack := newPermitStack(t)
	directive := approvedPermitDirective(t, stack, "OD-RACE", "GU-A")
	application, err := stack.permits.Apply(context.Background(), dto.ApplyGateDispatchPermit{
		Code: "GDP-RACE", DirectiveID: directive.ID, GateCode: "GU-A",
		ValidMinutes: 30, RequestReason: "并发签发",
	}, "operator", "req-apply-race")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, issueErr := stack.permits.Issue(context.Background(), application.ID, dto.IssueGateDispatchPermit{
				ExpectedVersion: application.Version, IssueReason: "并发复核签发",
			}, "reviewer", "req-issue-race")
			results <- issueErr
		}()
	}
	wg.Wait()
	close(results)
	successes, failures := 0, 0
	for issueErr := range results {
		if issueErr == nil {
			successes++
		} else if errors.Is(issueErr, ErrPermitStateChange) {
			failures++
		} else {
			t.Fatalf("unexpected concurrent issue error: %v", issueErr)
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("expected exactly one successful issue, got success=%d failure=%d", successes, failures)
	}
}

func TestRevokedAndExpiredPermitsBlockExecution(t *testing.T) {
	stack := newPermitStack(t)
	ctx := context.Background()
	directive := approvedPermitDirective(t, stack, "OD-REV", "GU-A")
	permit := applyAndIssue(t, stack, directive.ID, "GDP-REV", 60)

	if err := stack.permits.ValidateForExecution(ctx, directive.ID, "GU-A", "operator", "req-check-valid"); err != nil {
		t.Fatalf("valid permit should allow execution check: %v", err)
	}
	// Gate mismatch blocks execution.
	if err := stack.permits.ValidateForExecution(ctx, directive.ID, "GU-B", "operator", "req-check-gate"); !errors.Is(err, ErrPermitRequired) {
		t.Fatalf("cross-gate execution should be blocked, got %v", err)
	}
	if _, err := stack.permits.Revoke(ctx, permit.ID, dto.RevokeGateDispatchPermit{
		ExpectedVersion: permit.Version, RevokeReason: "上游来水突变，立即撤销",
	}, "reviewer", "req-revoke"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	err := stack.permits.ValidateForExecution(ctx, directive.ID, "GU-A", "operator", "req-check-revoked")
	if !errors.Is(err, ErrPermitRequired) {
		t.Fatalf("revoked permit must block execution, got %v", err)
	}
	revoked, _ := stack.permits.Get(ctx, permit.ID)
	if revoked.Status != "revoked" || revoked.RevokeReason == "" || revoked.RevokedBy != "reviewer" {
		t.Fatalf("revocation evidence missing: %#v", revoked)
	}
	// Re-revocation is rejected.
	_, err = stack.permits.Revoke(ctx, permit.ID, dto.RevokeGateDispatchPermit{
		ExpectedVersion: revoked.Version, RevokeReason: "再次撤销",
	}, "reviewer", "req-revoke-again")
	if !errors.Is(err, ErrPermitStateChange) {
		t.Fatalf("re-revocation must fail, got %v", err)
	}
}

func TestExpiredPermitBlocksExecutionAndFreesGateAfterObservation(t *testing.T) {
	stack := newPermitStack(t)
	ctx := context.Background()
	directive := approvedPermitDirective(t, stack, "OD-EXP", "GU-A")
	permit := applyAndIssue(t, stack, directive.ID, "GDP-EXP", 1)
	// Force the validity window into the past.
	if err := stack.db.Model(&model.GateDispatchPermit{}).Where("id = ?", permit.ID).
		Updates(map[string]any{"valid_until": time.Now().UTC().Add(-time.Minute)}).Error; err != nil {
		t.Fatalf("age permit: %v", err)
	}
	loaded, err := stack.permits.Get(ctx, permit.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !loaded.Expired || loaded.DisplayStatus != "expired" {
		t.Fatalf("read view should present expired state: %#v", loaded)
	}
	if err := stack.permits.ValidateForExecution(ctx, directive.ID, "GU-A", "operator", "req-check-expired"); !errors.Is(err, ErrPermitRequired) {
		t.Fatalf("expired permit must block execution, got %v", err)
	}
	persisted, err := stack.permits.Get(ctx, permit.ID)
	if err != nil {
		t.Fatalf("reload after blocked execution: %v", err)
	}
	if persisted.Status != "expired" {
		t.Fatalf("expired permit should be persisted as expired, got %s", persisted.Status)
	}
	// Once expiry is recorded the gate can accept a new approved directive.
	next := approvedPermitDirective(t, stack, "OD-EXP-2", "GU-A")
	application, err := stack.permits.Apply(ctx, dto.ApplyGateDispatchPermit{
		Code: "GDP-EXP-2", DirectiveID: next.ID, GateCode: "GU-A",
		ValidMinutes: 30, RequestReason: "过期后重新申请",
	}, "operator", "req-apply-exp2")
	if err != nil {
		t.Fatalf("new application after expiry should succeed: %v", err)
	}
	if _, err := stack.permits.Issue(ctx, application.ID, dto.IssueGateDispatchPermit{
		ExpectedVersion: application.Version, IssueReason: "旧许可已过期，签发新许可",
	}, "reviewer", "req-issue-exp2"); err != nil {
		t.Fatalf("issue new permit after expiry: %v", err)
	}
}

func TestExecutionWithoutPermitIsRejectedAndAbortTerminatesPermit(t *testing.T) {
	stack := newPermitStack(t)
	ctx := context.Background()
	directive := approvedPermitDirective(t, stack, "OD-NOPERMIT", "GU-A")
	_, err := stack.directives.Transition(ctx, directive.ID, dto.TransitionRequest{
		Status: "executing", ExpectedVersion: directive.Version, Reason: "无许可执行",
	}, "operator", model.RoleOperator, "req-execute-no-permit")
	if !errors.Is(err, ErrPermitRequired) {
		t.Fatalf("execution without permit must be rejected, got %v", err)
	}
	unchanged, _ := stack.directives.Get(ctx, directive.ID)
	if unchanged.Status != "approved" {
		t.Fatalf("directive must remain approved without permit, got %s", unchanged.Status)
	}

	permitted := applyAndIssue(t, stack, directive.ID, "GDP-ABORT", 60)
	aborted, err := stack.directives.Transition(ctx, directive.ID, dto.TransitionRequest{
		Status: "aborted", ExpectedVersion: directive.Version, Reason: "批准后取消作业",
	}, "reviewer", model.RoleReviewer, "req-abort-before-exec")
	if err != nil {
		t.Fatalf("abort approved directive: %v", err)
	}
	if aborted.Status != "aborted" {
		t.Fatalf("expected aborted directive, got %s", aborted.Status)
	}
	closed, _ := stack.permits.Get(ctx, permitted.ID)
	if closed.Status != "terminated" || closed.CloseReason == "" {
		t.Fatalf("permit should be terminated with reason, got %#v", closed)
	}
}
