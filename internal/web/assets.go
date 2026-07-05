package web

import _ "embed"

//go:embed assets/jira-step1.b64
var jiraStep1B64 string

//go:embed assets/jira-step2.b64
var jiraStep2B64 string

//go:embed assets/jira-step3.b64
var jiraStep3B64 string

//go:embed assets/jira-step5.b64
var jiraStep5B64 string

//go:embed assets/favicon.png
var faviconPNG []byte
