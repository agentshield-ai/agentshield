package sigmalite

import (
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkDetectionMatches(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "sigma", "aws_cloudtrail_disable_logging.yml"))
	if err != nil {
		b.Fatal(err)
	}
	rule, err := ParseRule(data)
	if err != nil {
		b.Fatal(err)
	}
	entry := &LogEntry{
		Fields: map[string]string{
			"eventSource": "cloudtrail.amazonaws.com",
			"eventName":   "StopLogging",
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rule.Detection.Matches(entry, nil)
	}
}

func BenchmarkDetectionMatchesNoMatch(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "sigma", "aws_cloudtrail_disable_logging.yml"))
	if err != nil {
		b.Fatal(err)
	}
	rule, err := ParseRule(data)
	if err != nil {
		b.Fatal(err)
	}
	entry := &LogEntry{
		Fields: map[string]string{
			"eventSource": "example.com",
			"eventName":   "SomethingElse",
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rule.Detection.Matches(entry, nil)
	}
}

func BenchmarkDetectionMatchesComplex(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "sigma", "file_access_win_browser_credential_access.yml"))
	if err != nil {
		b.Fatal(err)
	}
	rule, err := ParseRule(data)
	if err != nil {
		b.Fatal(err)
	}
	entry := &LogEntry{
		Fields: map[string]string{
			"Image":    "example.exe",
			"FileName": `C:\Users\light\AppData\Local\Chrome\User Data\Default\Login Data`,
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rule.Detection.Matches(entry, nil)
	}
}

func BenchmarkDetectionMatchesCaseInsensitive(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "sigma", "aws_cloudtrail_disable_logging_caseinsensitive.yml"))
	if err != nil {
		b.Fatal(err)
	}
	rule, err := ParseRule(data)
	if err != nil {
		b.Fatal(err)
	}
	entry := &LogEntry{
		Fields: map[string]string{
			"eventSource": "cloudtrail.amazonaws.com",
			"eventName":   "StopLogging",
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rule.Detection.Matches(entry, nil)
	}
}

func BenchmarkDetectionMatchesMessage(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "sigma", "lnx_buffer_overflows.yml"))
	if err != nil {
		b.Fatal(err)
	}
	rule, err := ParseRule(data)
	if err != nil {
		b.Fatal(err)
	}
	entry := &LogEntry{
		Message: "there was an attempt to execute code on stack by main",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rule.Detection.Matches(entry, nil)
	}
}
