package adf

import (
	"encoding/json"
	"testing"
)

func TestDocRoundTrip(t *testing.T) {
	doc := Doc("Worked on EDB-100")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got := Text(raw); got != "Worked on EDB-100" {
		t.Errorf("round trip = %q, want %q", got, "Worked on EDB-100")
	}
}

func TestTextFromPlainString(t *testing.T) {
	raw := json.RawMessage(`"just a string"`)
	if got := Text(raw); got != "just a string" {
		t.Errorf("Text(string) = %q", got)
	}
}

func TestTextFromNestedADF(t *testing.T) {
	raw := json.RawMessage(`{
      "type":"doc","version":1,
      "content":[
        {"type":"paragraph","content":[{"type":"text","text":"line one"}]},
        {"type":"paragraph","content":[{"type":"text","text":"line two"}]}
      ]
    }`)
	got := Text(raw)
	if got != "line one line two" {
		t.Errorf("Text(nested) = %q, want %q", got, "line one line two")
	}
}

func TestTextEmpty(t *testing.T) {
	if got := Text(nil); got != "" {
		t.Errorf("Text(nil) = %q, want empty", got)
	}
	if got := Text(json.RawMessage("null")); got != "" {
		t.Errorf("Text(null) = %q, want empty", got)
	}
}
