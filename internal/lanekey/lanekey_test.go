package lanekey

import (
	"errors"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func TestAKeyHasItsProvidersShapeOrIsRefusedWithoutBeingShown(t *testing.T) {
	for _, test := range []struct {
		provider, key string
		ok            bool
	}{
		{"together", strings.Repeat("0123456789abcdef", 4), true},
		{"together", "tgp_v1_" + strings.Repeat("Ab3-_", 8) + "xyz", true},
		{"together", strings.Repeat("a", 32), true},
		{"together", strings.Repeat("a", 128), true},
		{"together", strings.Repeat("a", 31), false},
		{"together", strings.Repeat("a", 129), false},
		{"together", "tgp_v1_" + strings.Repeat("a", 30) + " b", false},
		{"together", "tgp_v1_" + strings.Repeat("a", 30) + "\n", false},
		{"together", "TOGETHER_API_KEY=" + strings.Repeat("a", 40), false},
		{"together", "tgp_v1_" + strings.Repeat("a", 30) + "é", false},
		{"together", "tgp_v1_" + strings.Repeat("a", 30) + `"`, false},
		{"together", "", false},
		{"groq", "gsk_" + strings.Repeat("Ab9", 17) + "x", true},
		{"groq", "gsk_" + strings.Repeat("a", 35), false},
		{"groq", "gsk_" + strings.Repeat("a", 30) + "+/", false},
		// Another provider's key has the builds' own rule: 16 to 512 printable characters without spaces, never NAME=value.
		{"other", "sk-" + strings.Repeat("a", 13), true},
		{"other", "key+/=" + strings.Repeat("a", 20), true},
		{"other", strings.Repeat("a", 512), true},
		{"other", strings.Repeat("a", 15), false},
		{"other", strings.Repeat("a", 513), false},
		{"other", "OTHER_API_KEY=" + strings.Repeat("a", 20), false},
		{"other", strings.Repeat("a", 20) + "\t", false},
	} {
		err := CheckShape(test.provider, test.key)
		if (err == nil) != test.ok {
			t.Errorf("%s key %.20q… = %v", test.provider, test.key, err)
		}
		if err != nil && (!errors.Is(err, serviceapi.ErrLaneKeyInvalid) || (len(test.key) > 3 && strings.Contains(err.Error(), test.key))) {
			t.Errorf("%s key %.20q…: the error is %q", test.provider, test.key, err)
		}
	}
}

func TestAFingerprintIsTheFirstEightHexOfTheKeysSHA256(t *testing.T) {
	// sha256("abc") = ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
	if got := Fingerprint("abc"); got != "ba7816bf" {
		t.Fatalf("fingerprint = %q", got)
	}
}
