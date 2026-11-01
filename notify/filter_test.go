package notify

import "testing"

func TestParseFilterMalformedFallsBackToMatchAll(t *testing.T) {
	// Malformed JSON, empty input, fields of the wrong type - all must degenerate into a zero-valued Filter,
	// That is "no filtering". This invariant is the starting point of “I would rather push more than miss”:
	// Once this is changed to error reporting or semi-parsing, if the user mismatches a character, all high-risk notifications will be silently discarded.
	cases := []struct {
		name string
		raw  string
	}{
		{"空输入", ""},
		{"非法 JSON", `{not json`},
		{"截断的 JSON", `{"min_severity":`},
		{"类型不匹配", `{"min_severity": 123, "task_ids": "abc"}`},
		{"顶层是数组", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseFilter([]byte(tc.raw))
			if f.MinSeverity != "" || len(f.TaskIDs) != 0 || len(f.AssetIDs) != 0 {
				t.Fatalf("The malformed configuration should degenerate to a zero-valued Filter, resulting in %+v", f)
			}
			// A zero value Filter must hit any event.
			ev := Snapshot{Kind: EventFindingCreated, Severity: "low", VulnClass: "XSS"}
			if !Match(f, ev) {
				t.Fatal("A zero value Filter should hit all events")
			}
		})
	}
}

func TestMatchSeverityThreshold(t *testing.T) {
	ev := func(sev string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: sev}
	}
	cases := []struct {
		min    string
		sev    string
		expect bool
	}{
		{"", "low", true},
		{"", "critical", true},
		{"high", "critical", true},
		{"high", "high", true},
		{"high", "medium", false},
		{"high", "low", false},
		{"critical", "high", false},
		{"critical", "critical", true},
		// The unknown level ordinal is 0 and should be blocked by any non-empty threshold (do not push when in doubt).
		{"low", "", false},
		{"low", "unknown", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := Match(Filter{MinSeverity: tc.min}, ev(tc.sev))
		if got != tc.expect {
			t.Errorf("min=%q sev=%q: Expect %v and get %v", tc.min, tc.sev, tc.expect, got)
		}
	}
}

func TestMatchScopeRestrictions(t *testing.T) {
	ev := Snapshot{
		Kind:      EventFindingCreated,
		Severity:  "high",
		TaskID:    7,
		AssetIDs:  []int64{10, 20},
		VulnClass: "SQL注入",
	}
	cases := []struct {
		name   string
		filter Filter
		expect bool
	}{
		{"空范围=不限", Filter{}, true},
		{"任务命中", Filter{TaskIDs: []int64{7}}, true},
		{"任务未命中", Filter{TaskIDs: []int64{8}}, false},
		{"任务多选含命中", Filter{TaskIDs: []int64{8, 7}}, true},
		{"资产有交集", Filter{AssetIDs: []int64{20, 99}}, true},
		{"资产无交集", Filter{AssetIDs: []int64{99}}, false},
		{"任务与资产同时命中", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{10}}, true},
		{"任务命中但资产未命中", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev); got != tc.expect {
				t.Errorf("Expect %v get %v", tc.expect, got)
			}
		})
	}
}

func TestMatchVulnClassKeywords(t *testing.T) {
	ev := func(class string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: "high", VulnClass: class}
	}
	cases := []struct {
		name   string
		filter Filter
		class  string
		expect bool
	}{
		{"include 为空=全收", Filter{}, "任意类型", true},
		{"include 命中", Filter{VulnClassInclude: []string{"SQL"}}, "SQL注入", true},
		{"include 未命中", Filter{VulnClassInclude: []string{"命令执行"}}, "SQL注入", false},
		{"include 多词任一命中", Filter{VulnClassInclude: []string{"命令执行", "SQL"}}, "SQL注入", true},
		{"大小写不敏感", Filter{VulnClassInclude: []string{"sql"}}, "SQL注入", true},
		{"exclude 命中即排除", Filter{VulnClassExclude: []string{"信息泄露"}}, "信息泄露", false},
		{"exclude 未命中则放行", Filter{VulnClassExclude: []string{"信息泄露"}}, "SQL注入", true},
		// Exclusion takes precedence over inclusion: simultaneous hits should result in an out.
		{"排除优先于包含", Filter{
			VulnClassInclude: []string{"SQL"},
			VulnClassExclude: []string{"注入"},
		}, "SQL注入", false},
		// Pure whitespace keywords should be ignored, otherwise they will degenerate into "match all strings containing spaces".
		{"空白关键词被忽略", Filter{VulnClassInclude: []string{"", "  "}}, "SQL注入", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev(tc.class)); got != tc.expect {
				t.Errorf("Expect %v get %v", tc.expect, got)
			}
		})
	}
}

func TestMatchStatusChangeRequiresOptIn(t *testing.T) {
	ev := Snapshot{Kind: EventFindingStatusChanged, Severity: "critical", FromStatus: "pending", ToStatus: "fixed"}
	// Default off: What most people refer to as "push vulnerabilities" refers to the discovery of new vulnerabilities, not a status account.
	if Match(Filter{MinSeverity: "low"}, ev) {
		t.Fatal("State change events should be skipped if not enabled")
	}
	if !Match(Filter{OnStatusChange: true}, ev) {
		t.Fatal("After turning on on_status_change, the status change event should hit")
	}
	// Creation events are not affected by on_status_change.
	created := Snapshot{Kind: EventFindingCreated, Severity: "critical"}
	if !Match(Filter{MinSeverity: "low"}, created) {
		t.Fatal("Create events should not rely on on_status_change")
	}
}
