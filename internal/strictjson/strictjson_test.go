package strictjson

import (
	"encoding/json"
	"testing"
)

type fixture struct {
	SchemaVersion int               `json:"schema_version"`
	Nested        []nestedFixture   `json:"nested"`
	Dynamic       map[string]string `json:"dynamic"`
	Payload       json.RawMessage   `json:"payload"`
}

type nestedFixture struct {
	Value string `json:"value"`
}

func TestDecodeRequiresExactFieldsUniqueValidUTF8AndOneValue(t *testing.T) {
	valid := []byte(`{"schema_version":1,"nested":[{"value":"ok"}],"dynamic":{"Key":"value"},"payload":{"free_form":true}}`)
	var decoded fixture
	if err := Decode(valid, &decoded); err != nil || decoded.SchemaVersion != 1 || decoded.Nested[0].Value != "ok" {
		t.Fatalf("valid strict JSON = %+v, %v", decoded, err)
	}
	for name, data := range map[string][]byte{
		"case alias":    []byte(`{"Schema_Version":1,"nested":[],"dynamic":{},"payload":{}}`),
		"nested alias":  []byte(`{"schema_version":1,"nested":[{"Value":"bad"}],"dynamic":{},"payload":{}}`),
		"duplicate":     []byte(`{"schema_version":1,"schema_version":1,"nested":[],"dynamic":{},"payload":{}}`),
		"unknown":       []byte(`{"schema_version":1,"nested":[],"dynamic":{},"payload":{},"extra":true}`),
		"multiple":      append(append([]byte(nil), valid...), []byte(` {}`)...),
		"invalid UTF-8": append([]byte(`{"schema_version":1,"nested":[{"value":"`), append([]byte{0xff}, []byte(`"}],"dynamic":{},"payload":{}}`)...)...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := Decode(data, &fixture{}); err == nil {
				t.Fatal("unsafe JSON accepted")
			}
		})
	}
}
