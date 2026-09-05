package server

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	apiv1 "github.com/jamesread/starapp/service/gen/starapp/api/v1"
	"github.com/jamesread/starapp/service/internal/rbac"
	"github.com/jamesread/starapp/service/internal/store"
)

func toProtoStarChart(sc *store.StarChartRow, choreCount int, childName string) *apiv1.StarChart {
	if sc == nil {
		return nil
	}
	return &apiv1.StarChart{
		Id:               int32(sc.ID),
		FamilyId:         int32(sc.FamilyID),
		Name:             sc.Name,
		SortOrder:        int32(sc.SortOrder),
		Active:           sc.Active,
		CreatedAt:        sc.CreatedAt,
		ChoreCount:       int32(choreCount),
		ChildMemberId:    int32(sc.ChildMemberID),
		ChildDisplayName: childName,
	}
}

// starChartChildName looks up the display name of a chart's assigned member.
func (s *Server) starChartChildName(ctx context.Context, sc *store.StarChartRow) string {
	if sc == nil || sc.ChildMemberID == 0 {
		return ""
	}
	m, err := s.store.GetMemberByID(ctx, sc.ChildMemberID)
	if err != nil || m == nil {
		return ""
	}
	return m.DisplayName
}

// validateStarChartChild checks that a chart's assigned member belongs to the
// family. Zero means the chart covers everyone.
func (s *Server) validateStarChartChild(ctx context.Context, familyID int, memberID int32) (int, error) {
	if memberID == 0 {
		return 0, nil
	}
	m, err := s.store.GetMemberByID(ctx, int(memberID))
	if err != nil || !isFamilyStarMember(m, familyID) {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid member id %d", memberID))
	}
	return m.ID, nil
}

// assertStarChartNameFree keeps the unique (family, name) index from surfacing
// as a raw database error, which duplicating a chart makes easy to hit.
func (s *Server) assertStarChartNameFree(ctx context.Context, familyID int, name string, excludeID int) error {
	charts, err := s.store.ListStarCharts(ctx, familyID, true)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for i := range charts {
		if charts[i].ID != excludeID && strings.EqualFold(charts[i].Name, name) {
			return connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("a star chart named %q already exists", name))
		}
	}
	return nil
}

func (s *Server) resolveStarChartID(ctx context.Context, familyID int, starChartID int) (int, *store.StarChartRow, error) {
	if starChartID > 0 {
		sc, err := s.store.GetStarChartByID(ctx, starChartID)
		if err != nil {
			return 0, nil, connect.NewError(connect.CodeInternal, err)
		}
		if sc == nil || sc.FamilyID != familyID {
			return 0, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("star chart not found"))
		}
		return sc.ID, sc, nil
	}
	id, err := s.store.GetDefaultStarChartID(ctx, familyID)
	if err != nil {
		return 0, nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no star chart found"))
	}
	sc, err := s.store.GetStarChartByID(ctx, id)
	if err != nil {
		return 0, nil, connect.NewError(connect.CodeInternal, err)
	}
	return id, sc, nil
}

func (s *Server) ListStarCharts(ctx context.Context, req *connect.Request[apiv1.ListStarChartsRequest]) (*connect.Response[apiv1.ListStarChartsResponse], error) {
	fc, err := s.requireFamilyContext(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.requirePermission(ctx, rbac.PermissionChoresViewFamily); err != nil {
		if _, err2 := s.requirePermission(ctx, rbac.PermissionStarsViewOwn); err2 != nil {
			return nil, err
		}
	}
	memberFilter := s.chartMemberFilter(fc)
	if req.Msg.GetAssignedToMe() && fc.member != nil {
		memberFilter = fc.member.ID
	}
	rows, err := s.store.ListStarCharts(ctx, fc.family.ID, req.Msg.IncludeInactive)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	members, _ := s.store.ListMembersByFamily(ctx, fc.family.ID)
	nameByID := map[int]string{}
	for _, m := range members {
		nameByID[m.ID] = m.DisplayName
	}
	out := &apiv1.ListStarChartsResponse{}
	for i := range rows {
		var count int
		if memberFilter != 0 {
			if rows[i].ChildMemberID != 0 && rows[i].ChildMemberID != memberFilter {
				continue
			}
			count, err = s.store.CountChoresForStarChartAndMember(ctx, rows[i].ID, memberFilter)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, err)
			}
			if count == 0 {
				continue
			}
		} else {
			count, err = s.store.CountChoresForStarChart(ctx, rows[i].ID)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, err)
			}
		}
		out.StarCharts = append(out.StarCharts, toProtoStarChart(&rows[i], count, nameByID[rows[i].ChildMemberID]))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) CreateStarChart(ctx context.Context, req *connect.Request[apiv1.CreateStarChartRequest]) (*connect.Response[apiv1.CreateStarChartResponse], error) {
	fc, err := s.requireFamilyContext(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireWrite(ctx); err != nil {
		return nil, err
	}
	if _, err := s.requirePermission(ctx, rbac.PermissionChoresManage); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name required"))
	}
	if err := s.assertStarChartNameFree(ctx, fc.family.ID, name, 0); err != nil {
		return nil, err
	}
	childMemberID, err := s.validateStarChartChild(ctx, fc.family.ID, req.Msg.ChildMemberId)
	if err != nil {
		return nil, err
	}
	id, err := s.store.CreateStarChart(ctx, fc.family.ID, name, int(req.Msg.SortOrder), childMemberID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	sc, _ := s.store.GetStarChartByID(ctx, id)
	return connect.NewResponse(&apiv1.CreateStarChartResponse{
		StandardResponse: &apiv1.StandardResponse{Success: true, Message: "Star chart created"},
		StarChart:        toProtoStarChart(sc, 0, s.starChartChildName(ctx, sc)),
	}), nil
}

func (s *Server) UpdateStarChart(ctx context.Context, req *connect.Request[apiv1.UpdateStarChartRequest]) (*connect.Response[apiv1.UpdateStarChartResponse], error) {
	fc, err := s.requireFamilyContext(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireWrite(ctx); err != nil {
		return nil, err
	}
	if _, err := s.requirePermission(ctx, rbac.PermissionChoresManage); err != nil {
		return nil, err
	}
	sc, err := s.store.GetStarChartByID(ctx, int(req.Msg.Id))
	if err != nil || sc == nil || sc.FamilyID != fc.family.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("star chart not found"))
	}
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name required"))
	}
	if err := s.assertStarChartNameFree(ctx, fc.family.ID, name, sc.ID); err != nil {
		return nil, err
	}
	childMemberID, err := s.validateStarChartChild(ctx, fc.family.ID, req.Msg.ChildMemberId)
	if err != nil {
		return nil, err
	}
	if childMemberID != 0 && childMemberID != sc.ChildMemberID {
		if err := s.assertChartChoresOnlyFor(ctx, sc.ID, childMemberID); err != nil {
			return nil, err
		}
	}
	if err := s.store.UpdateStarChart(ctx, sc.ID, name, int(req.Msg.SortOrder), req.Msg.Active, childMemberID); err != nil {
		return nil, mapStoreError(err)
	}
	updated, _ := s.store.GetStarChartByID(ctx, sc.ID)
	count, _ := s.store.CountChoresForStarChart(ctx, sc.ID)
	return connect.NewResponse(&apiv1.UpdateStarChartResponse{
		StandardResponse: &apiv1.StandardResponse{Success: true, Message: "Star chart updated"},
		StarChart:        toProtoStarChart(updated, count, s.starChartChildName(ctx, updated)),
	}), nil
}

// assertChartChoresOnlyFor refuses to bind a chart to one person while its
// chores are still assigned to somebody else. Reassigning them here would drop
// the other person's completion history.
func (s *Server) assertChartChoresOnlyFor(ctx context.Context, starChartID, memberID int) error {
	assignees, err := s.store.ListStarChartAssignees(ctx, starChartID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for _, id := range assignees {
		if id != memberID {
			return connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("chart has chores assigned to other people; duplicate the chart instead"))
		}
	}
	return nil
}

func (s *Server) DeleteStarChart(ctx context.Context, req *connect.Request[apiv1.DeleteStarChartRequest]) (*connect.Response[apiv1.DeleteStarChartResponse], error) {
	fc, err := s.requireFamilyContext(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireWrite(ctx); err != nil {
		return nil, err
	}
	if _, err := s.requirePermission(ctx, rbac.PermissionChoresManage); err != nil {
		return nil, err
	}
	sc, err := s.store.GetStarChartByID(ctx, int(req.Msg.Id))
	if err != nil || sc == nil || sc.FamilyID != fc.family.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("star chart not found"))
	}
	count, err := s.store.CountChoresForStarChart(ctx, sc.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if count > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("star chart has assigned chores"))
	}
	charts, err := s.store.ListStarCharts(ctx, fc.family.ID, true)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if len(charts) <= 1 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot delete the only star chart"))
	}
	if err := s.store.DeleteStarChart(ctx, sc.ID); err != nil {
		return nil, mapStoreError(err)
	}
	return connect.NewResponse(&apiv1.DeleteStarChartResponse{
		StandardResponse: &apiv1.StandardResponse{Success: true, Message: "Star chart deleted"},
	}), nil
}

// DuplicateStarChart copies a chart and its chores, so a family can give each
// child their own chart without retyping the whole list.
func (s *Server) DuplicateStarChart(ctx context.Context, req *connect.Request[apiv1.DuplicateStarChartRequest]) (*connect.Response[apiv1.DuplicateStarChartResponse], error) {
	fc, err := s.requireFamilyContext(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireWrite(ctx); err != nil {
		return nil, err
	}
	if _, err := s.requirePermission(ctx, rbac.PermissionChoresManage); err != nil {
		return nil, err
	}
	source, err := s.store.GetStarChartByID(ctx, int(req.Msg.Id))
	if err != nil || source == nil || source.FamilyID != fc.family.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("star chart not found"))
	}
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name required"))
	}
	if err := s.assertStarChartNameFree(ctx, fc.family.ID, name, 0); err != nil {
		return nil, err
	}
	childMemberID, err := s.validateStarChartChild(ctx, fc.family.ID, req.Msg.ChildMemberId)
	if err != nil {
		return nil, err
	}
	newID, err := s.store.DuplicateStarChart(ctx, source.ID, fc.family.ID, name, source.SortOrder, childMemberID)
	if err != nil {
		return nil, mapStoreError(err)
	}
	created, _ := s.store.GetStarChartByID(ctx, newID)
	count, _ := s.store.CountChoresForStarChart(ctx, newID)
	return connect.NewResponse(&apiv1.DuplicateStarChartResponse{
		StandardResponse: &apiv1.StandardResponse{Success: true, Message: "Star chart duplicated"},
		StarChart:        toProtoStarChart(created, count, s.starChartChildName(ctx, created)),
	}), nil
}
