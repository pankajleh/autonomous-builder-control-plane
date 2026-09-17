package governance

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var canonicalTableRecordPattern = regexp.MustCompile("^`([^`]+)`\\s*/\\s*(?:inherited\\s*)?`([^`]+)`$")
var namedPredicatePattern = regexp.MustCompile(`\b(?:P-[A-Z][A-Z0-9-]*-[0-9]{3}|P-[A-Z][A-Z0-9-]*)`)
var inlineRecordPattern = regexp.MustCompile(`([A-Z][A-Za-z0-9]+V[0-9]+)=\{`)

// CanonicalCatalogExtractionV1 exposes the deterministic extraction audit.
// A successful extraction always has an empty unresolved set.
type CanonicalCatalogExtractionV1 struct {
	Catalog                 CanonicalVectorCatalogV1
	DefinitionCount         int
	UnresolvedDigestTargets []string
}

// ExtractFrozenCanonicalVectorCatalogV1 extracts the record/type source spans
// from the accepted A model. It intentionally scans only field-list code spans
// and explicit Name={...} definitions, never illustrative JSON blocks.
func ExtractFrozenCanonicalVectorCatalogV1(model []byte) (CanonicalCatalogExtractionV1, error) {
	if len(model) == 0 {
		return CanonicalCatalogExtractionV1{}, errors.New("frozen assurance model is empty")
	}
	start := bytes.Index(model, []byte("## Canonical successor wire contracts and validity predicates"))
	end := bytes.Index(model, []byte("### Frozen semantic registry and final-review authority"))
	if start < 0 || end <= start {
		return CanonicalCatalogExtractionV1{}, errors.New("frozen canonical wire section is missing")
	}
	section := model[start:end]
	definitions := make(map[string]WireSchemaDefinitionV1)
	unresolved := make(map[string]struct{})

	scanner := bufio.NewScanner(bytes.NewReader(section))
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "| `") {
			definition, contract, matched, err := extractTableDefinition(line)
			if err != nil {
				return CanonicalCatalogExtractionV1{}, err
			}
			if matched {
				if err := normalizeDefinition(&definition, contract, unresolved); err != nil {
					return CanonicalCatalogExtractionV1{}, err
				}
				definitions[definition.SchemaID] = definition
			}
		}
		inlineDefinitions, err := extractInlineDefinitions(line)
		if err != nil {
			return CanonicalCatalogExtractionV1{}, err
		}
		for _, definition := range inlineDefinitions {
			if _, tableWins := definitions[definition.SchemaID]; tableWins {
				continue
			}
			if err := normalizeDefinition(&definition, line, unresolved); err != nil {
				return CanonicalCatalogExtractionV1{}, err
			}
			definitions[definition.SchemaID] = definition
		}
	}
	if err := scanner.Err(); err != nil {
		return CanonicalCatalogExtractionV1{}, err
	}
	addCatalogMetaDefinitions(definitions)
	if len(unresolved) != 0 {
		values := sortedKeys(unresolved)
		return CanonicalCatalogExtractionV1{DefinitionCount: len(definitions), UnresolvedDigestTargets: values}, fmt.Errorf("unresolved digest targets: %s", strings.Join(values, ","))
	}
	list := make([]WireSchemaDefinitionV1, 0, len(definitions))
	for _, definition := range definitions {
		list = append(list, definition)
	}
	catalog, err := BuildCanonicalVectorCatalogV1(list)
	if err != nil {
		return CanonicalCatalogExtractionV1{DefinitionCount: len(definitions)}, err
	}
	return CanonicalCatalogExtractionV1{Catalog: catalog, DefinitionCount: len(definitions), UnresolvedDigestTargets: []string{}}, nil
}

func extractTableDefinition(line string) (WireSchemaDefinitionV1, string, bool, error) {
	parts := strings.Split(line, "|")
	if len(parts) < 4 {
		return WireSchemaDefinitionV1{}, "", false, nil
	}
	recordCell := strings.TrimSpace(parts[1])
	match := canonicalTableRecordPattern.FindStringSubmatch(recordCell)
	if len(match) != 3 {
		return WireSchemaDefinitionV1{}, "", false, nil
	}
	recordName, schemaID := match[1], match[2]
	nested := strings.HasPrefix(schemaID, "nested:")
	if nested {
		schemaID = "nested:" + recordName
	} else if strings.Contains(schemaID, ".") {
		schemaID = "inherited:" + schemaID
	}
	contract := strings.TrimSpace(strings.Join(parts[2:len(parts)-1], "|"))
	fieldList := longestFieldCodeSpan(contract)
	if fieldList == "" {
		if recordName == "ArtifactBlobV1" {
			fieldList = "artifact_sha256:blob256,byte_size:u64=1..33554432"
		} else {
			return WireSchemaDefinitionV1{}, "", true, fmt.Errorf("record %s has no canonical field-list code span", recordName)
		}
	}
	fields, err := parseWireFieldList(fieldList)
	if err != nil {
		return WireSchemaDefinitionV1{}, "", true, fmt.Errorf("record %s fields: %w", recordName, err)
	}
	return WireSchemaDefinitionV1{SchemaID: schemaID, RecordName: recordName, Nested: nested, Fields: fields}, contract, true, nil
}

func extractInlineDefinitions(line string) ([]WireSchemaDefinitionV1, error) {
	matches := inlineRecordPattern.FindAllStringSubmatchIndex(line, -1)
	result := make([]WireSchemaDefinitionV1, 0, len(matches))
	for _, match := range matches {
		name := line[match[2]:match[3]]
		open := match[1] - 1
		close := matchingBrace(line, open)
		if close < 0 {
			return nil, fmt.Errorf("inline record %s has unterminated field list", name)
		}
		fields, err := parseWireFieldList(line[open+1 : close])
		if err != nil || len(fields) == 0 {
			return nil, fmt.Errorf("inline record %s fields: %w", name, err)
		}
		topLevel := strings.Contains(name, "RequestV") || strings.Contains(name, "ResultV") || name == "CanonicalVectorCatalogV1"
		schemaID := "nested:" + name
		if topLevel {
			schemaID = kebabRecordName(name)
		}
		result = append(result, WireSchemaDefinitionV1{SchemaID: schemaID, RecordName: name, Nested: !topLevel, Fields: fields})
	}
	return result, nil
}

func normalizeDefinition(definition *WireSchemaDefinitionV1, contract string, unresolved map[string]struct{}) error {
	if definition == nil {
		return errors.New("nil wire definition")
	}
	for index := range definition.Fields {
		field := &definition.Fields[index]
		if field.Type == "self" {
			field.Type = "sha256<" + definition.RecordName + ">"
		}
		if field.FieldPath == "kind" && !definition.Nested {
			if definition.RecordName == "PhaseCheckpointV2" {
				field.Type = "checkpoint_kind"
			} else {
				field.Type = `id="` + definition.RecordName + `"`
			}
		}
		if field.FieldPath == "schema_version" && !definition.Nested {
			field.Type = `id="` + strings.TrimPrefix(definition.SchemaID, "inherited:") + `"`
		}
		if field.Type == "" {
			field.Type = specialBareFieldType(definition.RecordName, field.FieldPath)
		}
		if strings.HasSuffix(field.FieldPath, "_sha256") && field.Type == "" {
			unresolved[definition.RecordName+"."+field.FieldPath] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	for _, match := range namedPredicatePattern.FindAllString(contract, -1) {
		if _, duplicate := seen[match]; duplicate {
			continue
		}
		seen[match] = struct{}{}
		clause := normalizedClause(contract, match)
		paths := clauseFieldPaths(clause, definition.Fields)
		operator := predicateOperatorFromClause(clause)
		if len(paths) == 0 && operator == PredicateExactlyOne {
			for _, field := range definition.Fields {
				if field.Optional {
					paths = append(paths, field.FieldPath)
				}
			}
		}
		if len(paths) == 0 {
			paths = []string{"$"}
		}
		definition.Predicates = append(definition.Predicates, WirePredicateSpecV1{PredicateID: match, FieldPaths: paths, Operator: operator, Arguments: []string{clause}})
	}
	rowOrdinal := 0
	if definition.SchemaID == "nested:WireFieldDescriptorV1" {
		return nil
	}
	for _, clause := range rowPredicateClauses(contract) {
		rowOrdinal++
		if namedPredicatePattern.MatchString(clause) || !crossFieldClause(clause, definition.Fields) {
			continue
		}
		paths := clauseFieldPaths(clause, definition.Fields)
		if len(paths) == 0 {
			paths = []string{"$"}
		}
		operator := predicateOperatorFromClause(clause)
		if operator == PredicateExactlyOne {
			optionalCount := 0
			for _, path := range paths {
				for _, field := range definition.Fields {
					if field.FieldPath == path && field.Optional {
						optionalCount++
					}
				}
			}
			if optionalCount < 2 {
				continue
			}
		}
		definition.Predicates = append(definition.Predicates, WirePredicateSpecV1{
			PredicateID: fmt.Sprintf("P-%s-ROW-%03d", definition.SchemaID, rowOrdinal),
			FieldPaths:  paths, Operator: operator, Arguments: []string{strings.Join(strings.Fields(clause), " ")},
		})
	}
	return nil
}

func parseWireFieldList(list string) ([]WireFieldSpecV1, error) {
	list = strings.TrimSpace(list)
	if strings.HasPrefix(list, "{") && strings.HasSuffix(list, "}") {
		list = strings.TrimSuffix(strings.TrimPrefix(list, "{"), "}")
	}
	tokens := splitTopLevel(list, ',')
	fields := make([]WireFieldSpecV1, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" || strings.Contains(token, " fields retained in order") || strings.HasPrefix(token, "then ") {
			continue
		}
		colon := topLevelIndex(token, ':')
		name, sourceType := token, ""
		if colon >= 0 {
			name, sourceType = strings.TrimSpace(token[:colon]), strings.TrimSpace(token[colon+1:])
		} else if equal := topLevelIndex(token, '='); equal > 0 {
			name = strings.TrimSpace(token[:equal])
			baseType := inferBareWireType(strings.TrimSuffix(name, "?"))
			if baseType == "" {
				return nil, fmt.Errorf("exact literal field %s has no frozen base type", name)
			}
			literal := strings.Trim(strings.TrimSpace(token[equal+1:]), `"`)
			if values, enum := frozenEnumValues[baseType]; enum {
				index := sort.SearchStrings(values, literal)
				if index >= len(values) || values[index] != literal {
					baseType = "text"
				}
			}
			sourceType = baseType + `=` + strings.TrimSpace(token[equal+1:])
		}
		optional := strings.HasSuffix(name, "?")
		name = strings.TrimSuffix(name, "?")
		name = strings.TrimSpace(name)
		if !wireFieldName(name) {
			return nil, fmt.Errorf("invalid field token %q", token)
		}
		fields = append(fields, WireFieldSpecV1{FieldPath: name, Type: sourceType, Optional: optional})
	}
	if len(fields) == 0 {
		return nil, errors.New("field list is empty")
	}
	return fields, nil
}

func splitTopLevel(value string, separator byte) []string {
	var result []string
	start, angle, round, square, curly := 0, 0, 0, 0, 0
	quoted, escaped := false, false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character == '"' {
				quoted = false
			}
			continue
		}
		if character == '"' {
			quoted = true
			continue
		}
		switch character {
		case '<':
			if index+1 >= len(value) || value[index+1] != '=' {
				angle++
			}
		case '>':
			if (index+1 >= len(value) || value[index+1] != '=') && angle > 0 {
				angle--
			}
		case '(':
			round++
		case ')':
			if round > 0 {
				round--
			}
		case '[':
			square++
		case ']':
			if square > 0 {
				square--
			}
		case '{':
			curly++
		case '}':
			if curly > 0 {
				curly--
			}
		}
		if character == separator && angle == 0 && round == 0 && square == 0 && curly == 0 {
			result = append(result, value[start:index])
			start = index + 1
		}
	}
	result = append(result, value[start:])
	return result
}

func topLevelIndex(value string, target byte) int {
	parts := splitTopLevel(value, target)
	if len(parts) < 2 {
		return -1
	}
	return len(parts[0])
}

func longestFieldCodeSpan(value string) string {
	spans := markdownCodeSpans(value)
	longest := ""
	for _, span := range spans {
		if strings.Contains(span, ",") && len(span) > len(longest) {
			longest = span
		}
	}
	return longest
}

func markdownCodeSpans(value string) []string {
	var result []string
	for {
		start := strings.IndexByte(value, '`')
		if start < 0 {
			break
		}
		value = value[start+1:]
		end := strings.IndexByte(value, '`')
		if end < 0 {
			break
		}
		result = append(result, value[:end])
		value = value[end+1:]
	}
	return result
}

func matchingBrace(value string, open int) int {
	depth := 0
	quoted, escaped := false, false
	for index := open; index < len(value); index++ {
		character := value[index]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character == '"' {
				quoted = false
			}
			continue
		}
		if character == '"' {
			quoted = true
			continue
		}
		if character == '{' {
			depth++
		}
		if character == '}' {
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func wireFieldName(value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	for index, character := range value {
		if character != '_' && !unicode.IsLetter(character) && !(index > 0 && unicode.IsDigit(character)) {
			return false
		}
	}
	return true
}

func specialBareFieldType(record, field string) string {
	if strings.HasSuffix(field, "_sha256") {
		return inferBareDigestType(field)
	}
	if record == "ResourceEvidenceV1" || record == "ResourceCountersV2" {
		return "u64"
	}
	if field == "stage" {
		switch record {
		case "PhaseParentV2", "PhaseAuthorityV4", "ContextCapsuleV4", "PhaseCheckpointV2", "StageGrantV2", "FailedStageV1", "AssuranceEscapeV1":
			return "phase"
		default:
			return "evidence_stage"
		}
	}
	if field == "classification" {
		if record == "WorkClassificationV1" {
			return "work_class"
		}
		return "finding_class"
	}
	if field == "lifecycle" || field == "expected_lifecycle" || field == "new_lifecycle" {
		if strings.Contains(record, "Publisher") {
			return "publisher_lifecycle"
		}
		return "reservation_lifecycle"
	}
	if strings.HasSuffix(field, "_revision") || strings.HasSuffix(field, "_ordinal") || strings.HasSuffix(field, "_count") || strings.HasSuffix(field, "_bytes") || strings.HasSuffix(field, "_ms") || strings.HasSuffix(field, "_sequence") || strings.HasSuffix(field, "_limit") {
		return "u64"
	}
	return inferBareWireType(field)
}

func predicateOperatorFromClause(clause string) PredicateOperator {
	lower := strings.ToLower(clause)
	switch {
	case strings.Contains(lower, "sum"):
		return PredicateAggregateLEQ
	case strings.Contains(lower, "transition"):
		return PredicateStateTransition
	case strings.Contains(lower, "successor") || strings.Contains(lower, "ordinal"):
		return PredicateOrdinalSuccess
	case strings.Contains(lower, "subset") || strings.Contains(lower, "narrow"):
		return PredicateSubset
	case strings.Contains(lower, "exactly one"):
		return PredicateExactlyOne
	case strings.Contains(lower, "if and only if") || strings.Contains(lower, " iff "):
		return PredicateIfAndOnlyIf
	case strings.Contains(lower, "requires") || strings.Contains(lower, " only ") || strings.Contains(lower, " when "):
		return PredicateImplies
	case strings.Contains(lower, "digest") || strings.Contains(lower, "hash"):
		return PredicateDigestPreimage
	case strings.Contains(lower, "reference") || strings.Contains(lower, "target") || strings.Contains(lower, "type"):
		return PredicateTypedReference
	case strings.Contains(lower, "equal") || strings.Contains(lower, "match") || strings.Contains(lower, "repeat"):
		return PredicateAllEqual
	default:
		return PredicateDerivation
	}
}

func normalizedClause(contract, predicate string) string {
	index := strings.Index(contract, predicate)
	if index < 0 {
		return predicate
	}
	start := strings.LastIndex(contract[:index], ";")
	if start < 0 {
		start = 0
	} else {
		start++
	}
	endRelative := strings.Index(contract[index:], ";")
	end := len(contract)
	if endRelative >= 0 {
		end = index + endRelative
	}
	return strings.Join(strings.Fields(contract[start:end]), " ")
}

func rowPredicateClauses(contract string) []string {
	spans := markdownCodeSpans(contract)
	fieldList := longestFieldCodeSpan(contract)
	if fieldList != "" {
		needle := "`" + fieldList + "`"
		if index := strings.Index(contract, needle); index >= 0 {
			contract = contract[index+len(needle):]
		}
	} else if len(spans) == 0 {
		return nil
	}
	var clauses []string
	start := 0
	inCode := false
	for index := 0; index < len(contract); index++ {
		if contract[index] == '`' {
			inCode = !inCode
			continue
		}
		if contract[index] == ';' && !inCode {
			if clause := strings.TrimSpace(contract[start:index]); clause != "" {
				clauses = append(clauses, clause)
			}
			start = index + 1
		}
	}
	if clause := strings.TrimSpace(contract[start:]); clause != "" {
		clauses = append(clauses, clause)
	}
	return clauses
}

func crossFieldClause(clause string, fields []WireFieldSpecV1) bool {
	if len(clauseFieldPaths(clause, fields)) == 0 {
		return false
	}
	lower := strings.ToLower(clause)
	for _, keyword := range []string{" requires ", " equal", " iff ", "if and only if", " present", " absent", " must ", " only ", " when ", " sum", " transition", " successor", " ordinal", " subset", " narrow", " digest", " hash", " reference", " target", " type", " fixes ", " copies ", " selects ", " derived", " recompute", " forbidden", " omit"} {
		if strings.Contains(" "+lower+" ", keyword) {
			return true
		}
	}
	return false
}

func clauseFieldPaths(clause string, fields []WireFieldSpecV1) []string {
	paths := make([]string, 0, 4)
	for _, field := range fields {
		if containsFieldToken(clause, field.FieldPath) {
			paths = append(paths, field.FieldPath)
		}
	}
	return paths
}

func containsFieldToken(value, field string) bool {
	for offset := 0; ; {
		index := strings.Index(value[offset:], field)
		if index < 0 {
			return false
		}
		index += offset
		beforeOK := index == 0 || !isWireNameByte(value[index-1])
		after := index + len(field)
		afterOK := after == len(value) || !isWireNameByte(value[after])
		if beforeOK && afterOK {
			return true
		}
		offset = index + len(field)
	}
}

func isWireNameByte(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func kebabRecordName(name string) string {
	var result strings.Builder
	for index, character := range name {
		if unicode.IsUpper(character) && index > 0 {
			previous := rune(name[index-1])
			nextLower := index+1 < len(name) && unicode.IsLower(rune(name[index+1]))
			if unicode.IsLower(previous) || unicode.IsDigit(previous) || (unicode.IsUpper(previous) && nextLower) {
				result.WriteByte('-')
			}
		}
		result.WriteRune(unicode.ToLower(character))
	}
	return result.String()
}

func addCatalogMetaDefinitions(definitions map[string]WireSchemaDefinitionV1) {
	meta := []WireSchemaDefinitionV1{
		{SchemaID: "canonical-vector-catalog-v1", RecordName: "CanonicalVectorCatalogV1", Fields: []WireFieldSpecV1{{"kind", `id="CanonicalVectorCatalogV1"`, false}, {"schema_version", `id="canonical-vector-catalog-v1"`, false}, {"descriptor_version", `id="CANONICAL-VECTOR-CATALOG-V3"`, false}, {"entries", "[]CanonicalVectorEntryV1(set,1..256)", false}, {"catalog_sha256", "sha256<CanonicalVectorCatalogV1>", false}}},
		{SchemaID: "nested:CanonicalVectorEntryV1", RecordName: "CanonicalVectorEntryV1", Nested: true, Fields: []WireFieldSpecV1{{"schema_id", "id", false}, {"record_name", "id", false}, {"fields", "[]WireFieldDescriptorV1(order,1..256)", false}, {"predicates", "[]PredicateDescriptorV1(set,0..256)", false}, {"positive_recipe", `id="MINIMAL-VALID-V3"`, false}, {"rejections", "[]CanonicalRejectionVectorV1(set,1..4096)", false}}},
		{SchemaID: "nested:WireFieldDescriptorV1", RecordName: "WireFieldDescriptorV1", Nested: true, Fields: []WireFieldSpecV1{{"schema_id", "id", false}, {"field_path", "text<=512", false}, {"ordinal", "u64>=1", false}, {"json_type", "{STRING,INTEGER,BOOLEAN,OBJECT,ARRAY,RAW_JSON}", false}, {"value_type", "text<=256", false}, {"min_u64", "u64", true}, {"max_u64", "u64", true}, {"min_bytes", "u64", true}, {"max_bytes", "u64", true}, {"min_items", "u64", true}, {"max_items", "u64", true}, {"record_target", "id", true}, {"digest_target", "id", true}, {"optional", "bool", false}, {"literal_value", "text<=4096", true}}},
		{SchemaID: "nested:PredicateDescriptorV1", RecordName: "PredicateDescriptorV1", Nested: true, Fields: []WireFieldSpecV1{{"predicate_id", "id", false}, {"schema_id", "id", false}, {"field_paths", "[]text(set,1..64)", false}, {"operator", "{ALL_EQUAL,EXACT_LITERAL,IF_AND_ONLY_IF,IMPLIES,EXACTLY_ONE,SUBSET,ORDINAL_SUCCESSOR,STATE_TRANSITION,TYPED_REFERENCE,DIGEST_PREIMAGE,DERIVATION,AGGREGATE_LEQ}", false}, {"arguments", "[]text(order,0..64)", false}, {"required_error", "{PREDICATE_INVALID}", false}}},
		{SchemaID: "nested:CanonicalRejectionVectorV1", RecordName: "CanonicalRejectionVectorV1", Nested: true, Fields: []WireFieldSpecV1{{"vector_id", "text<=512", false}, {"mutation", "text<=128", false}, {"required_error", "{NON_CANONICAL,UNKNOWN_FIELD,DUPLICATE_FIELD,MISSING_FIELD,TYPE_INVALID,BOUND_INVALID,ENUM_INVALID,DIGEST_INVALID,REFERENCE_TYPE_INVALID,ORDER_INVALID,PREDICATE_INVALID}", false}}},
	}
	for _, definition := range meta {
		if _, exists := definitions[definition.SchemaID]; !exists {
			definitions[definition.SchemaID] = definition
		}
	}
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
