package unit

import (
	"testing"

	"elida/internal/redaction"
	"elida/internal/storage"
)

func TestRedactionOnCapturedContent(t *testing.T) {
	r := redaction.NewPatternRedactor()

	record := storage.SessionRecord{
		CapturedContent: []storage.CapturedRequest{
			{
				RequestBody:  `{"model":"claude","messages":[{"role":"user","content":"My API key is sk-1234567890abcdefghij1234567890"}]}`,
				ResponseBody: `{"content":"Here is the key: sk-1234567890abcdefghij1234567890"}`,
			},
			{
				RequestBody:  `{"prompt":"Send email to user@example.com with password=SuperSecret123"}`,
				ResponseBody: `{"response":"Done"}`,
			},
		},
		Violations: []storage.Violation{
			{
				MatchedText: "sk-1234567890abcdefghij1234567890",
			},
		},
	}

	for i := range record.CapturedContent {
		record.CapturedContent[i].RequestBody = r.Redact(record.CapturedContent[i].RequestBody)
		record.CapturedContent[i].ResponseBody = r.Redact(record.CapturedContent[i].ResponseBody)
	}
	for i := range record.Violations {
		record.Violations[i].MatchedText = r.Redact(record.Violations[i].MatchedText)
	}

	if record.CapturedContent[0].RequestBody == `{"model":"claude","messages":[{"role":"user","content":"My API key is sk-1234567890abcdefghij1234567890"}]}` {
		t.Error("API key should be redacted in request body")
	}
	if record.CapturedContent[0].ResponseBody == `{"content":"Here is the key: sk-1234567890abcdefghij1234567890"}` {
		t.Error("API key should be redacted in response body")
	}
	if record.CapturedContent[1].RequestBody == `{"prompt":"Send email to user@example.com with password=SuperSecret123"}` {
		t.Error("email and password should be redacted")
	}
	if record.Violations[0].MatchedText == "sk-1234567890abcdefghij1234567890" {
		t.Error("matched text should be redacted")
	}
}

func TestRedactionDisabled(t *testing.T) {
	r := redaction.NewPatternRedactor()
	r.SetEnabled(false)

	original := "My key is sk-1234567890abcdefghij1234567890"
	result := r.Redact(original)
	if result != original {
		t.Error("disabled redactor should not modify content")
	}
}
