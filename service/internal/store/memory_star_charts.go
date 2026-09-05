package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type memoryStarChart struct {
	mu sync.Mutex

	nextStarChartID int
	starCharts      map[int]StarChartRow
}

func (m *Memory) starChartState() *memoryStarChart {
	if m.starCharts == nil {
		m.starCharts = &memoryStarChart{starCharts: map[int]StarChartRow{}}
	}
	return m.starCharts
}

func (m *Memory) ensureDefaultStarChartLocked(familyID int) int {
	st := m.starChartState()
	for _, sc := range st.starCharts {
		if sc.FamilyID == familyID {
			return sc.ID
		}
	}
	st.nextStarChartID++
	id := st.nextStarChartID
	st.starCharts[id] = StarChartRow{
		ID: id, FamilyID: familyID, Name: "Star Chart", SortOrder: 0, Active: true, CreatedAt: familyNow(),
	}
	return id
}

func (m *Memory) ListStarCharts(_ context.Context, familyID int, includeInactive bool) ([]StarChartRow, error) {
	st := m.starChartState()
	st.mu.Lock()
	defer st.mu.Unlock()
	m.ensureDefaultStarChartLocked(familyID)
	var out []StarChartRow
	for _, sc := range st.starCharts {
		if sc.FamilyID != familyID {
			continue
		}
		if !includeInactive && !sc.Active {
			continue
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (m *Memory) GetStarChartByID(_ context.Context, id int) (*StarChartRow, error) {
	st := m.starChartState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if sc, ok := st.starCharts[id]; ok {
		cp := sc
		return &cp, nil
	}
	return nil, nil
}

func (m *Memory) GetDefaultStarChartID(_ context.Context, familyID int) (int, error) {
	st := m.starChartState()
	st.mu.Lock()
	defer st.mu.Unlock()
	id := m.ensureDefaultStarChartLocked(familyID)
	return id, nil
}

func (m *Memory) CreateStarChart(_ context.Context, familyID int, name string, sortOrder, childMemberID int) (int, error) {
	st := m.starChartState()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextStarChartID++
	id := st.nextStarChartID
	st.starCharts[id] = StarChartRow{
		ID: id, FamilyID: familyID, Name: name, SortOrder: sortOrder, Active: true, CreatedAt: familyNow(),
		ChildMemberID: childMemberID,
	}
	return id, nil
}

func (m *Memory) UpdateStarChart(_ context.Context, id int, name string, sortOrder int, active bool, childMemberID int) error {
	st := m.starChartState()
	st.mu.Lock()
	defer st.mu.Unlock()
	sc, ok := st.starCharts[id]
	if !ok {
		return fmt.Errorf("star chart not found")
	}
	sc.Name = name
	sc.SortOrder = sortOrder
	sc.Active = active
	sc.ChildMemberID = childMemberID
	st.starCharts[id] = sc
	return nil
}

func (m *Memory) DeleteStarChart(_ context.Context, id int) error {
	st := m.starChartState()
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.starCharts, id)
	return nil
}

func (m *Memory) CountChoresForStarChart(_ context.Context, starChartID int) (int, error) {
	ch := m.choreState()
	ch.mu.Lock()
	defer ch.mu.Unlock()
	count := 0
	for _, cw := range ch.chores {
		if cw.Chore.StarChartID == starChartID {
			count++
		}
	}
	return count, nil
}

func (m *Memory) CountChoresForStarChartAndMember(_ context.Context, starChartID, memberID int) (int, error) {
	ch := m.choreState()
	ch.mu.Lock()
	defer ch.mu.Unlock()
	count := 0
	for _, cw := range ch.chores {
		if cw.Chore.StarChartID != starChartID || !cw.Chore.Active {
			continue
		}
		for _, assign := range cw.Assignments {
			if assign.ChildMemberID == memberID {
				count++
				break
			}
		}
	}
	return count, nil
}

// ListStarChartAssignees returns the distinct members a chart's chores are assigned to.
func (m *Memory) ListStarChartAssignees(_ context.Context, starChartID int) ([]int, error) {
	ch := m.choreState()
	ch.mu.Lock()
	defer ch.mu.Unlock()
	seen := map[int]bool{}
	for _, cw := range ch.chores {
		if cw.Chore.StarChartID != starChartID {
			continue
		}
		for _, assign := range cw.Assignments {
			seen[assign.ChildMemberID] = true
		}
	}
	out := make([]int, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Ints(out)
	return out, nil
}

// DuplicateStarChart copies a chart and its chores. A non-zero childMemberID
// assigns every copied chore to that member alone; otherwise the source
// assignments are copied as they are.
func (m *Memory) DuplicateStarChart(_ context.Context, sourceID, familyID int, name string, sortOrder, childMemberID int) (int, error) {
	st := m.starChartState()
	st.mu.Lock()
	defer st.mu.Unlock()
	ch := m.choreState()
	ch.mu.Lock()
	defer ch.mu.Unlock()

	st.nextStarChartID++
	newChartID := st.nextStarChartID
	st.starCharts[newChartID] = StarChartRow{
		ID: newChartID, FamilyID: familyID, Name: name, SortOrder: sortOrder, Active: true,
		CreatedAt: familyNow(), ChildMemberID: childMemberID,
	}

	sources := make([]ChoreWithAssignments, 0, len(ch.chores))
	maxSort := 0
	for _, cw := range ch.chores {
		if cw.Chore.FamilyID == familyID && cw.Chore.SortOrder > maxSort {
			maxSort = cw.Chore.SortOrder
		}
		if cw.Chore.StarChartID == sourceID && cw.Chore.FamilyID == familyID {
			sources = append(sources, cw)
		}
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Chore.SortOrder != sources[j].Chore.SortOrder {
			return sources[i].Chore.SortOrder < sources[j].Chore.SortOrder
		}
		return sources[i].Chore.ID < sources[j].Chore.ID
	})

	for _, src := range sources {
		childIDs := []int{childMemberID}
		if childMemberID == 0 {
			childIDs = make([]int, 0, len(src.Assignments))
			for _, assign := range src.Assignments {
				childIDs = append(childIDs, assign.ChildMemberID)
			}
		}
		if len(childIDs) == 0 {
			continue
		}
		ch.nextChoreID++
		choreID := ch.nextChoreID
		maxSort++
		c := src.Chore
		c.ID = choreID
		c.StarChartID = newChartID
		c.SortOrder = maxSort
		c.CreatedAt = familyNow()
		var assigns []ChoreAssignmentRow
		for _, childID := range childIDs {
			ch.nextAssignmentID++
			assigns = append(assigns, ChoreAssignmentRow{ID: ch.nextAssignmentID, ChoreID: choreID, ChildMemberID: childID})
		}
		ch.chores[choreID] = ChoreWithAssignments{Chore: c, Assignments: assigns}
	}
	return newChartID, nil
}
