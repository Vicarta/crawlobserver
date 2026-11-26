package schema

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTechnicalStructuredDataPreservesEntitiesAndLocatesMalformedBlocks(t *testing.T) {
	malformed := "\n {\"@type\":\"Product\", broken}"
	report := ObserveTechnicalStructuredData(
		[]string{`{"@type":"Product"}`, malformed, `[{"@type":"Product"},{"@type":"Product"},{"@type":"https://schema.org/Service"}]`},
		[]string{"json-ld"}, nil, "static",
	)

	if report.Status != StructuredDataIssues || report.Coverage != "json_ld_syntax" {
		t.Fatalf("status/coverage = %q/%q", report.Status, report.Coverage)
	}
	if len(report.Entities) != 4 {
		t.Fatalf("entities = %#v, want four occurrences", report.Entities)
	}
	if report.Entities[0].Type != "Product" || report.Entities[1].Type != "Product" || report.Entities[2].Type != "Product" || report.Entities[3].Type != "Service" {
		t.Fatalf("entity types/order = %#v", report.Entities)
	}
	if report.Entities[2].BlockIndex != 3 || report.Entities[3].BlockIndex != 3 {
		t.Fatalf("third block entity indexes = %#v", report.Entities[2:])
	}
	if len(report.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one malformed JSON-LD issue", report.Diagnostics)
	}
	diagnostic := report.Diagnostics[0]
	if diagnostic.Code != "json_ld_invalid_json" || diagnostic.BlockIndex != 2 || diagnostic.Source != "static" {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
	var syntaxError *json.SyntaxError
	if err := json.Unmarshal([]byte(malformed), new(interface{})); !errors.As(err, &syntaxError) {
		t.Fatalf("expected syntax error, got %v", err)
	}
	if diagnostic.ByteStart != int(syntaxError.Offset)-1 || diagnostic.ByteEnd != diagnostic.ByteStart+1 {
		t.Fatalf("byte range = [%d,%d), want [%d,%d)", diagnostic.ByteStart, diagnostic.ByteEnd, int(syntaxError.Offset)-1, int(syntaxError.Offset))
	}
}

func TestTechnicalStructuredDataMicrodataIsDetectionOnly(t *testing.T) {
	report := ObserveTechnicalStructuredData(nil, []string{"microdata"}, []string{
		"https://schema.org/Product", "https://schema.org/Product",
	}, "static")
	if report.Status != StructuredDataClean || report.Coverage != "microdata_detection_only" {
		t.Fatalf("status/coverage = %q/%q", report.Status, report.Coverage)
	}
	if len(report.Entities) != 2 || len(report.Types) != 1 || report.Types[0] != "Product" {
		t.Fatalf("microdata entities/types = %#v / %#v", report.Entities, report.Types)
	}
}

func TestTechnicalStructuredDataUsesJSONNumbersAndRejectsTrailingValues(t *testing.T) {
	valid := ObserveTechnicalStructuredData(
		[]string{`{"@type":"Product","value":1e1000}`}, []string{"json-ld"}, nil, "static",
	)
	if valid.Status != StructuredDataClean || len(valid.Entities) != 1 {
		t.Fatalf("large JSON number report = %#v", valid)
	}

	block := `{"@type":"Product"} {"@type":"Article"}`
	trailing := ObserveTechnicalStructuredData([]string{block}, []string{"json-ld"}, nil, "static")
	if trailing.Status != StructuredDataIssues || len(trailing.Diagnostics) != 1 || trailing.Diagnostics[0].Code != "json_ld_trailing_data" {
		t.Fatalf("trailing value report = %#v", trailing)
	}
	expectedStart := strings.Index(block, `{"@type":"Article"}`)
	if trailing.Diagnostics[0].ByteStart != expectedStart || trailing.Diagnostics[0].ByteEnd != expectedStart+1 {
		t.Fatalf("trailing value byte range = [%d,%d), want [%d,%d)", trailing.Diagnostics[0].ByteStart, trailing.Diagnostics[0].ByteEnd, expectedStart, expectedStart+1)
	}
}

func TestTechnicalStructuredDataUnexpectedEOFUsesBlockEnd(t *testing.T) {
	block := `{"@type":"Product"`
	report := ObserveTechnicalStructuredData([]string{block}, []string{"json-ld"}, nil, "static")
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "json_ld_invalid_json" {
		t.Fatalf("unexpected EOF report = %#v", report)
	}
	diagnostic := report.Diagnostics[0]
	if diagnostic.ByteStart != len(block) || diagnostic.ByteEnd != len(block) {
		t.Fatalf("unexpected EOF byte range = [%d,%d), want [%d,%d)", diagnostic.ByteStart, diagnostic.ByteEnd, len(block), len(block))
	}
	encoded, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	var serialized struct {
		ByteStart int `json:"byte_start"`
		ByteEnd   int `json:"byte_end"`
	}
	if err := json.Unmarshal(encoded, &serialized); err != nil {
		t.Fatal(err)
	}
	if serialized.ByteStart != len(block) || serialized.ByteEnd != len(block) {
		t.Fatalf("serialized EOF range = %#v", serialized)
	}
	startReport := ObserveTechnicalStructuredData([]string{"x"}, []string{"json-ld"}, nil, "static")
	startJSON, err := json.Marshal(startReport.Diagnostics[0])
	if err != nil {
		t.Fatal(err)
	}
	var startSerialized struct {
		ByteStart int `json:"byte_start"`
	}
	if err := json.Unmarshal(startJSON, &startSerialized); err != nil {
		t.Fatal(err)
	}
	if startSerialized.ByteStart != 0 {
		t.Fatalf("serialized byte zero = %#v", startSerialized)
	}
}

func TestTechnicalStructuredDataPreservesCompactTypesAndSkipsMetadataObjects(t *testing.T) {
	report := ObserveTechnicalStructuredData(
		[]string{`{"@context":{"@type":"ContextMetadata"},"@type":["schema:Product","https://schema.org/Service"],"description":{"@value":"date","@type":"ValueMetadata"}}`},
		[]string{"json-ld"}, nil, "static",
	)
	if report.Status != StructuredDataClean || len(report.Types) != 2 || report.Types[0] != "Product" || report.Types[1] != "Service" {
		t.Fatalf("compact/meta types = %#v", report)
	}
	if len(report.Entities) != 2 || report.Entities[0].EntityIndex != report.Entities[1].EntityIndex {
		t.Fatalf("metadata entity enumeration = %#v", report.Entities)
	}
}

func TestTechnicalStructuredDataPreservesNonSchemaTypeIdentifiers(t *testing.T) {
	report := ObserveTechnicalStructuredData(
		[]string{`{"@type":["https://other.example/Product","other:Product","schema:Product"]}`},
		[]string{"json-ld"}, nil, "static",
	)
	want := []string{"https://other.example/Product", "other:Product", "Product"}
	if len(report.Types) != len(want) {
		t.Fatalf("types = %#v, want %#v", report.Types, want)
	}
	for i := range want {
		if report.Types[i] != want[i] {
			t.Fatalf("types = %#v, want %#v", report.Types, want)
		}
	}
}

func TestTechnicalStructuredDataUnsupportedFormatRemainsNotChecked(t *testing.T) {
	report := ObserveTechnicalStructuredData(nil, []string{"rdfa"}, nil, "static")
	if report.Status != StructuredDataNotChecked || report.Coverage != "unsupported_format_not_checked" {
		t.Fatalf("unsupported format status/coverage = %q/%q", report.Status, report.Coverage)
	}

	partial := ObserveTechnicalStructuredData([]string{`{"@type":"Product"}`}, []string{"json-ld", "rdfa"}, nil, "static")
	if partial.Status != StructuredDataNotChecked || partial.Coverage != "json_ld_syntax_with_unsupported_format_not_checked" {
		t.Fatalf("mixed format status/coverage = %q/%q", partial.Status, partial.Coverage)
	}
}

func TestTechnicalStructuredDataStaticRenderedMergeAndHistoricalUnknown(t *testing.T) {
	static := ObserveTechnicalStructuredData([]string{`{"@type":"Article"}`}, []string{"json-ld"}, nil, "static")
	rendered := ObserveTechnicalStructuredData([]string{`{"@type":"Article"}`}, []string{"json-ld"}, nil, "rendered")
	merged := AggregateTechnicalStructuredData(static, rendered)
	if merged.Status != StructuredDataClean || len(merged.Entities) != 2 || len(merged.Types) != 1 {
		t.Fatalf("merged report = %#v", merged)
	}
	if merged.Entities[0].Source != "static" || merged.Entities[1].Source != "rendered" {
		t.Fatalf("merged origins = %#v", merged.Entities)
	}

	historical := AggregateTechnicalStructuredData()
	if historical.Status != StructuredDataNotChecked || historical.Coverage != "not_checked" {
		t.Fatalf("historical report = %#v", historical)
	}
}

func TestTechnicalStructuredDataTruncationIsNotClean(t *testing.T) {
	partial := ObserveTechnicalStructuredData(nil, []string{"microdata"}, []string{"Product"}, "static")
	partial = MarkStructuredDataInputTruncated(partial, "static")
	if partial.Status != StructuredDataIssues || len(partial.Diagnostics) != 1 || partial.Diagnostics[0].Code != "html_body_truncated" {
		t.Fatalf("truncated report = %#v", partial)
	}
}
