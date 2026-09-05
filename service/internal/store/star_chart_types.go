package store

type StarChartRow struct {
	ID        int
	FamilyID  int
	Name      string
	SortOrder int
	Active    bool
	CreatedAt string
	// ChildMemberID is zero when the chart covers every family member.
	ChildMemberID int
}
