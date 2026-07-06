// Package jira is a small client for the Jira Cloud REST v3 API. The same
// client points at either the real Jira instance or the local mock server,
// depending on the base URL and credentials it is constructed with.
package jira

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/adf"
	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

// WorklogMarker is appended to comments this tool writes, so re-runs can detect
// worklogs it already created and avoid duplicating them.
const WorklogMarker = "[timereporting]"

// jiraTimeLayout matches the "started" timestamp format Jira expects/returns,
// e.g. 2026-06-01T12:00:00.000+0000.
const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

// Client talks to a Jira Cloud REST v3 endpoint.
type Client struct {
	baseURL string
	email   string
	token   string
	http    *http.Client
}

// NewClient builds a client for baseURL. When email and token are both set,
// requests carry HTTP Basic auth; the mock server needs no auth so they may be
// empty.
func NewClient(baseURL, email, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		email:   email,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ResolveAPIBase auto-detects whether token is a scoped token by trying Basic
// auth at baseURL/rest/api/3/myself. If that fails with a cloud-ID-required
// error, it fetches the cloudId from baseURL/_edge/tenant_info and returns the
// api.atlassian.com base URL instead. Returns the base URL to use for all REST
// calls, plus any error if both attempts fail.
func ResolveAPIBase(baseURL, email, token string) (string, error) {
	httpClient := &http.Client{Timeout: 15 * time.Second}
	base := strings.TrimRight(baseURL, "/")

	// First try: classic token at the domain URL.
	if err := tryMyself(httpClient, base, email, token); err == nil {
		return base, nil
	}

	// Second try: scoped token needs cloudId → api.atlassian.com base URL.
	cloudID, err := fetchCloudID(httpClient, base)
	if err != nil {
		return "", fmt.Errorf("could not resolve Jira cloud ID from %s: %w", base, err)
	}
	apiBase := "https://api.atlassian.com/ex/jira/" + cloudID
	if err := tryMyself(httpClient, apiBase, email, token); err != nil {
		return "", fmt.Errorf("authentication failed with scoped token at %s — ensure the token has 'read:jira-work' and 'write:jira-work' scopes and was copied in full", apiBase)
	}
	return apiBase, nil
}

func tryMyself(client *http.Client, base, email, token string) error {
	req, err := http.NewRequest(http.MethodGet, base+"/rest/api/3/myself", nil)
	if err != nil {
		return err
	}
	setAuth(req, base, email, token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	// "scope does not match" on /myself means auth passed but the /myself endpoint
	// isn't in scope (read:me was not granted). For our purposes (read/write worklogs)
	// this is fine — consider auth validated.
	if resp.StatusCode == http.StatusUnauthorized {
		var body struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if strings.Contains(strings.ToLower(body.Message), "scope") {
			return nil // auth valid; /myself just not in token scope
		}
	}
	return fmt.Errorf("status %d", resp.StatusCode)
}

// setAuth sets the correct Authorization header.
// Both classic and scoped Atlassian API tokens use Basic auth (email:token).
// Bearer is NOT used — the api.atlassian.com URL uses Basic auth too.
func setAuth(req *http.Request, base, email, token string) {
	if email != "" && token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(email + ":" + token))
		req.Header.Set("Authorization", "Basic "+auth)
	}
}

func fetchCloudID(client *http.Client, base string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/_edge/tenant_info", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tenant_info status %d", resp.StatusCode)
	}
	var result struct {
		CloudID string `json:"cloudId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.CloudID == "" {
		return "", fmt.Errorf("cloudId not found in tenant_info response")
	}
	return result.CloudID, nil
}

// Issue is a Jira issue's identity and summary.
type Issue struct {
	Key     string
	Summary string
}

func (c *Client) do(method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	setAuth(req, c.baseURL, c.email, c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, fmt.Errorf("jira %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// GetIssue returns an issue's summary.
func (c *Client) GetIssue(key string) (Issue, error) {
	data, err := c.do(http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"?fields=summary", nil)
	if err != nil {
		return Issue{}, err
	}
	var wire struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return Issue{}, err
	}
	return Issue{Key: wire.Key, Summary: wire.Fields.Summary}, nil
}

// SearchIssues runs a JQL query and returns matching issues (key + summary).
// Uses POST /rest/api/3/search/jql as required by Jira Cloud since mid-2025
// (the old GET /rest/api/3/search was removed — see CHANGE-2046).
func (c *Client) SearchIssues(jql string) ([]Issue, error) {
	body := map[string]any{
		"jql":        jql,
		"fields":     []string{"summary"},
		"maxResults": 100,
	}
	data, err := c.do(http.MethodPost, "/rest/api/3/search/jql", body)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(wire.Issues))
	for _, i := range wire.Issues {
		out = append(out, Issue{Key: i.Key, Summary: i.Fields.Summary})
	}
	return out, nil
}

// AssignedIssuesForDay returns open issues assigned to the user that were
// updated on the given day. It is used as a fallback when no git/GitHub
// activity is detected, so the engine can suggest logging to the tasks the
// user was actively working on.
func (c *Client) AssignedIssuesForDay(day time.Time) ([]Issue, error) {
	d := day.UTC().Format("2006-01-02")
	next := day.UTC().AddDate(0, 0, 1).Format("2006-01-02")
	jql := fmt.Sprintf(
		`assignee = currentUser() AND updated >= "%s" AND updated < "%s" AND statusCategory != Done ORDER BY updated DESC`,
		d, next,
	)
	issues, err := c.SearchIssues(jql)
	if err != nil {
		return nil, err
	}
	if len(issues) > 8 {
		issues = issues[:8] // cap to avoid noisy suggestions
	}
	return issues, nil
}

// ListWorklogs returns all worklogs on an issue as model.Worklog values
// (Category = existing).
func (c *Client) ListWorklogs(key string) ([]model.Worklog, error) {
	data, err := c.do(http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"/worklog", nil)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Worklogs []struct {
			ID     string `json:"id"`
			Author struct {
				EmailAddress string `json:"emailAddress"`
			} `json:"author"`
			TimeSpentSeconds int             `json:"timeSpentSeconds"`
			Started          string          `json:"started"`
			Comment          json.RawMessage `json:"comment"`
		} `json:"worklogs"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	out := make([]model.Worklog, 0, len(wire.Worklogs))
	for _, w := range wire.Worklogs {
		started, _ := time.Parse(jiraTimeLayout, w.Started)
		out = append(out, model.Worklog{
			ID:       w.ID,
			IssueKey: key,
			Minutes:  w.TimeSpentSeconds / 60,
			Comment:  adf.Text(w.Comment),
			Category: model.CategoryExisting,
			Started:  started,
			Author:   w.Author.EmailAddress,
		})
	}
	return out, nil
}

// AddWorklog logs work against an issue. minutes is converted to seconds,
// started is sent as noon-UTC (per the caller), and comment is wrapped in ADF.
func (c *Client) AddWorklog(key string, minutes int, started time.Time, comment string) (model.Worklog, error) {
	body := map[string]any{
		"timeSpentSeconds": minutes * 60,
		"started":          started.UTC().Format(jiraTimeLayout),
	}
	if comment != "" {
		body["comment"] = adf.Doc(comment)
	}
	data, err := c.do(http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/worklog", body)
	if err != nil {
		return model.Worklog{}, err
	}
	var w struct {
		ID               string          `json:"id"`
		TimeSpentSeconds int             `json:"timeSpentSeconds"`
		Started          string          `json:"started"`
		Comment          json.RawMessage `json:"comment"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return model.Worklog{}, err
	}
	startedBack, _ := time.Parse(jiraTimeLayout, w.Started)
	return model.Worklog{
		IssueKey: key,
		Minutes:  w.TimeSpentSeconds / 60,
		Comment:  adf.Text(w.Comment),
		Category: model.CategoryActivity,
		Started:  startedBack,
	}, nil
}

// ExistingWorklogsByDay returns the worklogs authored by authorEmail (if set;
// empty matches any author) within [start, end], keyed by YYYY-MM-DD. It first
// searches for candidate issues, then reads and filters each issue's worklogs.
func (c *Client) ExistingWorklogsByDay(authorEmail string, start, end time.Time) (map[string][]model.Worklog, error) {
	jql := fmt.Sprintf(`worklogAuthor = currentUser() AND worklogDate >= "%s" AND worklogDate <= "%s"`,
		start.Format("2006-01-02"), end.Format("2006-01-02"))
	issues, err := c.SearchIssues(jql)
	if err != nil {
		return nil, err
	}
	result := map[string][]model.Worklog{}
	startDay := model.Day(start)
	endDay := model.Day(end)
	for _, iss := range issues {
		logs, err := c.ListWorklogs(iss.Key)
		if err != nil {
			return nil, err
		}
		for _, w := range logs {
			if authorEmail != "" && !strings.EqualFold(w.Author, authorEmail) {
				continue
			}
			day := model.Day(w.Started)
			if day.Before(startDay) || day.After(endDay) {
				continue
			}
			key := day.Format("2006-01-02")
			result[key] = append(result[key], w)
		}
	}
	return result, nil
}

// UpdateWorklog updates an existing worklog's duration, start time, and comment.
func (c *Client) UpdateWorklog(issueKey, worklogID string, minutes int, started time.Time, comment string) error {
	body := map[string]any{
		"timeSpentSeconds": minutes * 60,
		"started":          started.UTC().Format(jiraTimeLayout),
	}
	if comment != "" {
		body["comment"] = adf.Doc(comment)
	}
	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/worklog/" + url.PathEscape(worklogID)
	_, err := c.do(http.MethodPut, path, body)
	return err
}

// DeleteWorklog removes a worklog by ID from an issue.
func (c *Client) DeleteWorklog(issueKey, worklogID string) error {
	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/worklog/" + url.PathEscape(worklogID)
	_, err := c.do(http.MethodDelete, path, nil)
	return err
}
