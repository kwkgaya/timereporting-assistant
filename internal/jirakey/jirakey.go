// Package jirakey extracts Jira issue keys from free-form text and groups a
// slice of activities by key, computing a weight (activity count) per key for
// proportional time allocation.
package jirakey

import (
	"regexp"
	"sort"
	"strings"

	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

// keyRe matches Jira keys like EDB-100, PROJ-1, AB-9999.
// One or more uppercase letters (project prefix), dash, one or more digits.
var keyRe = regexp.MustCompile(`\b([A-Z][A-Z0-9]+-\d+)\b`)

// Extract returns all unique Jira keys found in text.
func Extract(text string) []string {
	matches := keyRe.FindAllString(strings.ToUpper(text), -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// KeyGroup represents a Jira issue key and its associated activity weight.
type KeyGroup struct {
	Key        string
	Weight     int // number of activities referencing this key
	Activities []model.Activity
}

// GroupResult is the output of GroupByKey.
type GroupResult struct {
	Groups     []KeyGroup       // keyed activities, sorted by key
	Unassigned []model.Activity // activities with no Jira key
}

// GroupByKey scans each activity's Text and Ref for Jira keys, groups them,
// and counts a weight per key. Activities yielding no key go to Unassigned.
func GroupByKey(acts []model.Activity) GroupResult {
	byKey := map[string]*KeyGroup{}
	var unassigned []model.Activity

	for _, a := range acts {
		combined := a.Text + " " + a.Ref
		keys := Extract(combined)
		if len(keys) == 0 {
			unassigned = append(unassigned, a)
			continue
		}
		for _, k := range keys {
			g, ok := byKey[k]
			if !ok {
				g = &KeyGroup{Key: k}
				byKey[k] = g
			}
			g.Weight++
			g.Activities = append(g.Activities, a)
		}
	}

	groups := make([]KeyGroup, 0, len(byKey))
	for _, g := range byKey {
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })

	return GroupResult{Groups: groups, Unassigned: unassigned}
}
