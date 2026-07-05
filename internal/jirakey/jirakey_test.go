package jirakey

import (
	"testing"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"EDB-100 fix login", []string{"EDB-100"}},
		{"fix EDB-100 and EDB-200 and EDB-100 again", []string{"EDB-100", "EDB-200"}},
		{"feat/EDB-300-new-widget", []string{"EDB-300"}},
		{"no key here", nil},
		{"lowercase edb-100 normalised to key", []string{"EDB-100"}}, // branch names like edb-100-foo are common
		{"AB-1 and Z9-99", []string{"AB-1", "Z9-99"}},
	}
	for _, tc := range cases {
		got := Extract(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("Extract(%q) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("Extract(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

func act(text, ref string) model.Activity {
	return model.Activity{
		Date:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Source: "test",
		Text:   text,
		Ref:    ref,
	}
}

func TestGroupByKey(t *testing.T) {
	acts := []model.Activity{
		act("EDB-100 add widget", "feat/EDB-100-widget abc123"),
		act("EDB-100 fix widget tests", "feat/EDB-100-widget def456"),
		act("EDB-200 review", "https://github.com/org/repo/pull/42"),
		act("update readme", "main abc789"), // no key
	}
	res := GroupByKey(acts)

	if len(res.Groups) != 2 {
		t.Fatalf("groups = %d, want 2; groups = %+v", len(res.Groups), res.Groups)
	}
	if res.Groups[0].Key != "EDB-100" || res.Groups[0].Weight != 2 {
		t.Errorf("group[0] = %+v, want key=EDB-100 weight=2", res.Groups[0])
	}
	if res.Groups[1].Key != "EDB-200" || res.Groups[1].Weight != 1 {
		t.Errorf("group[1] = %+v, want key=EDB-200 weight=1", res.Groups[1])
	}
	if len(res.Unassigned) != 1 {
		t.Errorf("unassigned = %d, want 1", len(res.Unassigned))
	}
}

func TestGroupByKey_AllUnassigned(t *testing.T) {
	acts := []model.Activity{act("just a note", "no-key"), act("another note", "")}
	res := GroupByKey(acts)
	if len(res.Groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(res.Groups))
	}
	if len(res.Unassigned) != 2 {
		t.Errorf("expected 2 unassigned, got %d", len(res.Unassigned))
	}
}

func TestGroupByKey_Empty(t *testing.T) {
	res := GroupByKey(nil)
	if len(res.Groups) != 0 || len(res.Unassigned) != 0 {
		t.Errorf("expected empty result for nil input")
	}
}
