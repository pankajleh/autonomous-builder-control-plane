package run

import (
	"errors"
	"testing"
)

// A site-kit website's content.json: the same gallery item, word for word, on two pages.
const siteContent = `{
  "pages": {
    "home": { "sections": [ { "kind": "gallery", "items": [ {"image": "public/assets/section-1.png", "label": "Our café"} ] } ] },
    "gallery": {
      "sections": [
        { "kind": "gallery", "items": [
          {"image": "public/assets/section-1.png", "label": "Our café"},
          {"image": "public/assets/section-2.png", "label": "Coffee"}
        ] }
      ]
    }
  },
  "a/b": {"x~y": 1}
}
`

func TestJSONPointerChangesOneValueAndKeepsTheRest(t *testing.T) {
	updated, err := replaceJSONValue([]byte(siteContent), "/pages/gallery/sections/0/items/0/image",
		[]byte(`"public/assets/section-1.png"`), []byte(` "public/assets/picked-7.png" `))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "pages": {
    "home": { "sections": [ { "kind": "gallery", "items": [ {"image": "public/assets/section-1.png", "label": "Our café"} ] } ] },
    "gallery": {
      "sections": [
        { "kind": "gallery", "items": [
          {"image": "public/assets/picked-7.png", "label": "Our café"},
          {"image": "public/assets/section-2.png", "label": "Coffee"}
        ] }
      ]
    }
  },
  "a/b": {"x~y": 1}
}
`
	if string(updated) != want {
		t.Fatalf("got\n%s\nwant\n%s", updated, want)
	}
}

func TestJSONPointerEscapesAndWholeValues(t *testing.T) {
	updated, err := replaceJSONValue([]byte(siteContent), "/a~1b/x~0y", []byte(`1.0`), []byte(`2`))
	if err != nil {
		t.Fatal(err)
	}
	start, end, err := jsonPointerSpan(updated, "/a~1b")
	if err != nil || string(updated[start:end]) != `{"x~y": 2}` {
		t.Fatalf("got %q, %v", updated[start:end], err)
	}
	// An object or an array is one value too.
	start, end, err = jsonPointerSpan([]byte(siteContent), "/pages/home/sections/0/items/0")
	if err != nil || string([]byte(siteContent)[start:end]) != `{"image": "public/assets/section-1.png", "label": "Our café"}` {
		t.Fatalf("got %q, %v", []byte(siteContent)[start:end], err)
	}
}

func TestJSONPointerRefusals(t *testing.T) {
	for _, c := range []struct {
		name, content, pointer, from string
		want                         error
	}{
		{"the whole file", siteContent, "", `{}`, errPointerInvalid},
		{"no leading slash", siteContent, "pages", `{}`, errPointerInvalid},
		{"a bad escape", siteContent, "/a~2b", `1`, errPointerInvalid},
		{"nothing there", siteContent, "/pages/gallery/sections/0/items/9/image", `"x"`, errPointerNotFound},
		{"past a scalar", siteContent, "/pages/gallery/sections/0/kind/0", `"x"`, errPointerNotFound},
		{"a leading zero is no index", siteContent, "/pages/gallery/sections/00", `{}`, errPointerNotFound},
		{"the same key twice", `{"a": 1, "a": 2}`, "/a", `1`, errPointerTwice},
		{"another value there", siteContent, "/pages/gallery/sections/0/items/0/image", `"public/assets/section-2.png"`, errPointerDiffers},
		{"not JSON", `{"a": `, "/a", `1`, errJSONInvalid},
		{"two documents", `{"a": 1} {"a": 2}`, "/a", `1`, errJSONInvalid},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := replaceJSONValue([]byte(c.content), c.pointer, []byte(c.from), []byte(`"y"`))
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
	if _, err := replaceJSONValue([]byte(siteContent), "/a~1b/x~0y", []byte(`1`), []byte(`{`)); !errors.Is(err, errJSONInvalid) {
		t.Fatalf("a new value that isn't JSON: got %v", err)
	}
}
