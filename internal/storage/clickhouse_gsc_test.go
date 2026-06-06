package storage

import (
	"reflect"
	"testing"
)

func TestGSCSortClauseWhitelistsColumnsAndDirection(t *testing.T) {
	tests := []struct {
		name          string
		sort          string
		direction     string
		dimension     string
		wantSortExpr  string
		wantDirection string
	}{
		{
			name:          "query ascending",
			sort:          "query",
			direction:     "asc",
			dimension:     "query",
			wantSortExpr:  "query",
			wantDirection: "ASC",
		},
		{
			name:          "metric descending",
			sort:          "impressions",
			direction:     "desc",
			dimension:     "page",
			wantSortExpr:  "total_impressions",
			wantDirection: "DESC",
		},
		{
			name:          "unknown sort falls back",
			sort:          "clicks; DROP TABLE",
			direction:     "asc; DROP TABLE",
			dimension:     "query",
			wantSortExpr:  "total_clicks",
			wantDirection: "DESC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSortExpr, gotDirection := gscSortClause(tt.sort, tt.direction, tt.dimension)
			if gotSortExpr != tt.wantSortExpr || gotDirection != tt.wantDirection {
				t.Fatalf(
					"gscSortClause(%q, %q, %q) = (%q, %q), want (%q, %q)",
					tt.sort,
					tt.direction,
					tt.dimension,
					gotSortExpr,
					gotDirection,
					tt.wantSortExpr,
					tt.wantDirection,
				)
			}
		})
	}
}

func TestGSCPageQueryFilterBindsExactPageAndOptionalSearch(t *testing.T) {
	where, args := gscPageQueryFilter("project-1", "https://example.com/page/", "linux")
	wantWhere := "project_id = ? AND page = ? AND positionCaseInsensitive(query, ?) > 0"
	wantArgs := []any{"project-1", "https://example.com/page/", "linux"}
	if where != wantWhere || !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("gscPageQueryFilter with search = (%q, %#v), want (%q, %#v)", where, args, wantWhere, wantArgs)
	}

	where, args = gscPageQueryFilter("project-1", "https://example.com/page/", " ")
	wantWhere = "project_id = ? AND page = ?"
	wantArgs = []any{"project-1", "https://example.com/page/"}
	if where != wantWhere || !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("gscPageQueryFilter without search = (%q, %#v), want (%q, %#v)", where, args, wantWhere, wantArgs)
	}
}
