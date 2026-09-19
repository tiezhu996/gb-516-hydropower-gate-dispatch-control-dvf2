package handler

import (
	"net/http"

	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/dto"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/middleware"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/service"
	"github.com/blueship581/hydropower-gate-dispatch-control/backend/internal/util"
	"github.com/gin-gonic/gin"
)

type GateDispatchPermitHandler struct {
	service service.GateDispatchPermitService
}

func NewGateDispatchPermitHandler(s service.GateDispatchPermitService) *GateDispatchPermitHandler {
	return &GateDispatchPermitHandler{service: s}
}

func (h *GateDispatchPermitHandler) Register(group *gin.RouterGroup) {
	resource := group.Group("/permits")
	resource.GET("", h.list)
	resource.GET("/:id", h.get)
	resource.POST("", middleware.RequireRoles("operator", "admin"), h.apply)
	resource.POST("/:id/issue", middleware.RequireRoles("reviewer", "admin"), h.issue)
	resource.POST("/:id/revoke", middleware.RequireRoles("reviewer", "admin"), h.revoke)
}

func (h *GateDispatchPermitHandler) list(c *gin.Context) {
	var query dto.GateDispatchPermitQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		util.Fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := h.service.List(c.Request.Context(), query)
	if err != nil {
		handleError(c, err)
		return
	}
	util.Page(c, result.Items, result.Page, result.PageSize, result.Total)
}

func (h *GateDispatchPermitHandler) get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	item, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		handleError(c, err)
		return
	}
	util.OK(c, item)
}

func (h *GateDispatchPermitHandler) apply(c *gin.Context) {
	var input dto.ApplyGateDispatchPermit
	if err := c.ShouldBindJSON(&input); err != nil {
		util.Fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := h.service.Apply(c.Request.Context(), input, actorFromContext(c), requestIDFromContext(c))
	if err != nil {
		handleError(c, err)
		return
	}
	util.Created(c, item)
}

func (h *GateDispatchPermitHandler) issue(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input dto.IssueGateDispatchPermit
	if err := c.ShouldBindJSON(&input); err != nil {
		util.Fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := h.service.Issue(c.Request.Context(), id, input, actorFromContext(c), requestIDFromContext(c))
	if err != nil {
		handleError(c, err)
		return
	}
	util.OK(c, item)
}

func (h *GateDispatchPermitHandler) revoke(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input dto.RevokeGateDispatchPermit
	if err := c.ShouldBindJSON(&input); err != nil {
		util.Fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := h.service.Revoke(c.Request.Context(), id, input, actorFromContext(c), requestIDFromContext(c))
	if err != nil {
		handleError(c, err)
		return
	}
	util.OK(c, item)
}
