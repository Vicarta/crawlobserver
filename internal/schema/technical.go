package schema

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strings"
)

const (
	StructuredDataNotChecked = "not_checked"
	StructuredDataNoMarkup   = "no_markup"
	StructuredDataIssues     = "issues"
	StructuredDataClean      = "clean"

	maxStructuredDataBlocks   = 100
	maxStructuredDataEntities = 1000
	maxStructuredDataBlock    = 512 * 1024
	maxStructuredDataType     = 256
)

// TechnicalStructuredDataReport records parser-level observations only. It does
// not evaluate Schema.org conformance or search-engine eligibility.
type TechnicalStructuredDataReport struct {
	Status      string                              `json:"status"`
	Coverage    string                              `json:"coverage"`
	Formats     []string                            `json:"formats"`
	Types       []string                            `json:"types"`
	Sources     []TechnicalStructuredDataSource     `json:"sources"`
	Entities    []TechnicalStructuredDataEntity     `json:"entities"`
	Diagnostics []TechnicalStructuredDataDiagnostic `json:"diagnostics"`
	Truncated   bool                                `json:"truncated,omitempty"`
}

type TechnicalStructuredDataSource struct {
	Name        string                              `json:"name"`
	Coverage    string                              `json:"coverage"`
	Formats     []string                            `json:"formats"`
	Entities    []TechnicalStructuredDataEntity     `json:"entities"`
	Diagnostics []TechnicalStructuredDataDiagnostic `json:"diagnostics"`
	Truncated   bool                                `json:"truncated,omitempty"`
}

type TechnicalStructuredDataEntity struct {
	Source      string `json:"source"`
	Format      string `json:"format"`
	Type        string `json:"type"`
	BlockIndex  int    `json:"block_index,omitempty"`
	EntityIndex int    `json:"entity_index"`
}

type TechnicalStructuredDataDiagnostic struct {
	Source     string `json:"source"`
	Format     string `json:"format"`
	Code       string `json:"code"`
	BlockIndex int    `json:"block_index,omitempty"`
	ByteStart  int    `json:"byte_start"`
	ByteEnd    int    `json:"byte_end"`
}

type StructuredDataReportSummary struct {
	Total      uint64 `json:"total"`
	NotChecked uint64 `json:"not_checked"`
	NoMarkup   uint64 `json:"no_markup"`
	Issues     uint64 `json:"issues"`
	Clean      uint64 `json:"clean"`
}

type StructuredDataReportPage struct {
	URL                  string                              `json:"url"`
	Status               string                              `json:"status"`
	Coverage             string                              `json:"coverage"`
	Sources              []string                            `json:"sources"`
	Formats              []string                            `json:"formats"`
	Types                []string                            `json:"types"`
	JSONLDEntityCount    uint64                              `json:"json_ld_entity_count"`
	DiagnosticCount      uint64                              `json:"diagnostic_count"`
	DiagnosticsTruncated bool                                `json:"diagnostics_truncated,omitempty"`
	Truncated            bool                                `json:"truncated,omitempty"`
	Diagnostics          []TechnicalStructuredDataDiagnostic `json:"diagnostics"`
	Entities             []TechnicalStructuredDataEntity     `json:"entities,omitempty"`
}

type StructuredDataReportResponse struct {
	Summary StructuredDataReportSummary `json:"summary"`
	Pages   []StructuredDataReportPage  `json:"pages"`
	Total   uint64                      `json:"total"`
	Limit   int                         `json:"limit"`
	Offset  int                         `json:"offset"`
}

type StructuredDataReportOptions struct {
	Limit  int
	Offset int
	Status string
	Format string
	Type   string
	URL    string
}

// ObserveTechnicalStructuredData parses JSON-LD syntax and records detected
// Microdata types without applying conformance rules to either format.
func ObserveTechnicalStructuredData(blocks, formats, microdataTypes []string, source string) TechnicalStructuredDataReport {
	observation := TechnicalStructuredDataSource{
		Name:        source,
		Formats:     uniqueStrings(formats),
		Entities:    []TechnicalStructuredDataEntity{},
		Diagnostics: []TechnicalStructuredDataDiagnostic{},
	}
	if len(blocks) > 0 && !containsString(observation.Formats, "json-ld") {
		observation.Formats = append(observation.Formats, "json-ld")
	}
	if len(microdataTypes) > 0 && !containsString(observation.Formats, "microdata") {
		observation.Formats = append(observation.Formats, "microdata")
	}
	observation.Coverage = structuredDataCoverage(observation.Formats)

	entityIndex := 0
	blockCount := len(blocks)
	if blockCount > maxStructuredDataBlocks {
		blockCount = maxStructuredDataBlocks
		observation.Truncated = true
		appendTechnicalDiagnostic(&observation, TechnicalStructuredDataDiagnostic{
			Source: source, Format: "json-ld", Code: "observation_limit_reached",
			BlockIndex: maxStructuredDataBlocks + 1,
		})
	}
	for index, block := range blocks[:blockCount] {
		blockNumber := index + 1
		if len(block) > maxStructuredDataBlock {
			appendTechnicalDiagnostic(&observation, TechnicalStructuredDataDiagnostic{
				Source: source, Format: "json-ld", Code: "json_ld_block_too_large",
				BlockIndex: blockNumber, ByteStart: 0, ByteEnd: len(block),
			})
			continue
		}
		if strings.TrimSpace(block) == "" {
			appendTechnicalDiagnostic(&observation, TechnicalStructuredDataDiagnostic{
				Source: source, Format: "json-ld", Code: "json_ld_empty_block", BlockIndex: blockNumber,
			})
			continue
		}

		decoder := json.NewDecoder(strings.NewReader(block))
		decoder.UseNumber()
		var value interface{}
		if err := decoder.Decode(&value); err != nil {
			start, end := jsonErrorRange(err, len(block))
			appendTechnicalDiagnostic(&observation, TechnicalStructuredDataDiagnostic{
				Source: source, Format: "json-ld", Code: "json_ld_invalid_json",
				BlockIndex: blockNumber, ByteStart: start, ByteEnd: end,
			})
			continue
		}
		firstValueEnd := int(decoder.InputOffset())
		var trailing interface{}
		if err := decoder.Decode(&trailing); err != io.EOF {
			start := firstNonWhitespaceByte(block, firstValueEnd)
			end := min(start+1, len(block))
			appendTechnicalDiagnostic(&observation, TechnicalStructuredDataDiagnostic{
				Source: source, Format: "json-ld", Code: "json_ld_trailing_data",
				BlockIndex: blockNumber, ByteStart: start, ByteEnd: end,
			})
			continue
		}
		entityIndex = collectJSONLDEntities(value, source, blockNumber, entityIndex, &observation)
	}

	for _, microdataType := range microdataTypes {
		name, tooLong := schemaTypeName(microdataType)
		if tooLong {
			appendTechnicalDiagnostic(&observation, TechnicalStructuredDataDiagnostic{
				Source: source, Format: "microdata", Code: "schema_type_too_long",
			})
			continue
		}
		if name == "" {
			continue
		}
		entityIndex++
		appendTechnicalEntity(&observation, TechnicalStructuredDataEntity{
			Source: source, Format: "microdata", Type: name, EntityIndex: entityIndex,
		})
	}
	return AggregateTechnicalStructuredData(TechnicalStructuredDataReport{
		Sources: []TechnicalStructuredDataSource{observation},
	})
}

// AggregateTechnicalStructuredData combines source observations while
// preserving each entity occurrence, including duplicates.
func AggregateTechnicalStructuredData(sources ...TechnicalStructuredDataReport) TechnicalStructuredDataReport {
	combined := TechnicalStructuredDataReport{
		Status:   StructuredDataNotChecked,
		Coverage: "not_checked",
		Formats:  []string{}, Types: []string{}, Sources: []TechnicalStructuredDataSource{},
		Entities: []TechnicalStructuredDataEntity{}, Diagnostics: []TechnicalStructuredDataDiagnostic{},
	}
	seenFormats := map[string]bool{}
	seenTypes := map[string]bool{}
	for _, report := range sources {
		for _, source := range report.Sources {
			combined.Sources = append(combined.Sources, source)
			combined.Truncated = combined.Truncated || source.Truncated
			combined.Entities = append(combined.Entities, source.Entities...)
			combined.Diagnostics = append(combined.Diagnostics, source.Diagnostics...)
			for _, format := range source.Formats {
				if !seenFormats[format] {
					seenFormats[format] = true
					combined.Formats = append(combined.Formats, format)
				}
			}
			for _, entity := range source.Entities {
				if !seenTypes[entity.Type] {
					seenTypes[entity.Type] = true
					combined.Types = append(combined.Types, entity.Type)
				}
			}
		}
	}
	if len(combined.Sources) == 0 {
		return combined
	}
	combined.Coverage = structuredDataCoverage(combined.Formats)
	switch {
	case len(combined.Diagnostics) > 0:
		combined.Status = StructuredDataIssues
	case hasUnsupportedStructuredDataFormat(combined.Formats):
		combined.Status = StructuredDataNotChecked
	case len(combined.Formats) == 0:
		combined.Status = StructuredDataNoMarkup
	default:
		combined.Status = StructuredDataClean
	}
	return combined
}

func MarkStructuredDataInputTruncated(report TechnicalStructuredDataReport, source string) TechnicalStructuredDataReport {
	for i := range report.Sources {
		if report.Sources[i].Name != source {
			continue
		}
		appendTechnicalDiagnostic(&report.Sources[i], TechnicalStructuredDataDiagnostic{
			Source: source, Code: "html_body_truncated",
		})
		report.Sources[i].Truncated = true
		report = AggregateTechnicalStructuredData(report)
		return report
	}
	return report
}

func collectJSONLDEntities(value interface{}, source string, blockIndex, entityIndex int, report *TechnicalStructuredDataSource) int {
	switch node := value.(type) {
	case []interface{}:
		for _, child := range node {
			entityIndex = collectJSONLDEntities(child, source, blockIndex, entityIndex, report)
		}
	case map[string]interface{}:
		if _, isValueObject := node["@value"]; isValueObject {
			return entityIndex
		}
		types, tooLong := jsonLDTypes(node["@type"])
		if tooLong {
			appendTechnicalDiagnostic(report, TechnicalStructuredDataDiagnostic{
				Source: source, Format: "json-ld", Code: "schema_type_too_long", BlockIndex: blockIndex,
			})
		}
		if len(types) > 0 {
			entityIndex++
			for _, typ := range types {
				appendTechnicalEntity(report, TechnicalStructuredDataEntity{
					Source: source, Format: "json-ld", Type: typ, BlockIndex: blockIndex, EntityIndex: entityIndex,
				})
			}
		}
		keys := make([]string, 0, len(node))
		for key := range node {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key == "@context" {
				continue
			}
			entityIndex = collectJSONLDEntities(node[key], source, blockIndex, entityIndex, report)
		}
	}
	return entityIndex
}

func appendTechnicalEntity(report *TechnicalStructuredDataSource, entity TechnicalStructuredDataEntity) {
	if len(report.Entities) >= maxStructuredDataEntities {
		if !report.Truncated {
			report.Truncated = true
			appendTechnicalDiagnostic(report, TechnicalStructuredDataDiagnostic{
				Source: entity.Source, Format: entity.Format, Code: "observation_limit_reached",
			})
		}
		return
	}
	report.Entities = append(report.Entities, entity)
}

func appendTechnicalDiagnostic(report *TechnicalStructuredDataSource, diagnostic TechnicalStructuredDataDiagnostic) {
	const maxStructuredDataDiagnostics = 1000
	if len(report.Diagnostics) < maxStructuredDataDiagnostics {
		report.Diagnostics = append(report.Diagnostics, diagnostic)
		return
	}
	if report.Truncated {
		return
	}
	report.Truncated = true
	report.Diagnostics[maxStructuredDataDiagnostics-1] = TechnicalStructuredDataDiagnostic{
		Source: diagnostic.Source, Format: diagnostic.Format, Code: "observation_limit_reached",
	}
}

func jsonLDTypes(value interface{}) ([]string, bool) {
	var values []string
	switch item := value.(type) {
	case string:
		values = append(values, item)
	case []interface{}:
		for _, entry := range item {
			if text, ok := entry.(string); ok {
				values = append(values, text)
			}
		}
	}
	result := make([]string, 0, len(values))
	tooLong := false
	for _, item := range values {
		name, long := schemaTypeName(item)
		tooLong = tooLong || long
		if name != "" {
			result = append(result, name)
		}
	}
	return result, tooLong
}

func schemaTypeName(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if len(value) > maxStructuredDataType {
		return "", true
	}
	if strings.HasPrefix(value, "schema:") {
		if compact := strings.TrimSpace(strings.TrimPrefix(value, "schema:")); compact != "" {
			return compact, false
		}
	}
	if parsed, err := url.Parse(value); err == nil && strings.EqualFold(parsed.Hostname(), "schema.org") {
		localName := strings.Trim(strings.TrimSpace(parsed.Path), "/")
		if localName == "" {
			localName = strings.TrimSpace(parsed.Fragment)
		}
		if idx := strings.LastIndex(localName, "/"); idx >= 0 {
			localName = localName[idx+1:]
		}
		if localName != "" {
			return localName, false
		}
	}
	return value, false
}

func firstNonWhitespaceByte(value string, offset int) int {
	for offset < len(value) && (value[offset] == ' ' || value[offset] == '\t' || value[offset] == '\n' || value[offset] == '\r') {
		offset++
	}
	if offset > len(value) {
		return len(value)
	}
	return offset
}

func jsonErrorRange(err error, length int) (int, int) {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return length, length
	}
	offset := 0
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntaxError):
		if syntaxError.Error() == "unexpected end of JSON input" {
			return length, length
		}
		offset = int(syntaxError.Offset)
	case errors.As(err, &typeError):
		offset = int(typeError.Offset)
	}
	if offset > 0 {
		offset--
	}
	if offset > length {
		offset = length
	}
	end := offset + 1
	if end > length {
		end = length
	}
	return offset, end
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func structuredDataCoverage(formats []string) string {
	hasJSONLD := containsString(formats, "json-ld")
	hasMicrodata := containsString(formats, "microdata")
	hasUnsupported := hasUnsupportedStructuredDataFormat(formats)
	coverage := ""
	switch {
	case hasJSONLD && hasMicrodata:
		coverage = "json_ld_syntax_and_microdata_detection_only"
	case hasJSONLD:
		coverage = "json_ld_syntax"
	case hasMicrodata:
		coverage = "microdata_detection_only"
	case hasUnsupported:
		return "unsupported_format_not_checked"
	default:
		coverage = "checked_no_markup"
	}
	if hasUnsupported {
		coverage += "_with_unsupported_format_not_checked"
	}
	return coverage
}

func hasUnsupportedStructuredDataFormat(formats []string) bool {
	for _, format := range formats {
		if format != "json-ld" && format != "microdata" {
			return true
		}
	}
	return false
}
