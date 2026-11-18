package run

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
)

// A site-kit website's version: content.json holds the same gallery item, word for word, on Home and on Gallery.
var siteVersion = map[string]string{
	"content.json": siteContent,
	"index.html":   "<main><img src=\"public/assets/section-1.png\" alt=\"Our café\"></main>\n",
}

func TestAnExactEditsJSONReplacementChangesTheOnePickedValue(t *testing.T) {
	repository, _, _ := buildRepository(t)
	source := addVersion(t, repository, "abcp/earlier-run", siteVersion)
	edit := authority.ExactEditManifest{SourceSHA: source, Replacements: []authority.ExactEditReplacement{{
		Path: "content.json", BaseBlob: blobAt(t, repository, source, "content.json"),
		Pointer: "/pages/gallery/sections/0/items/0/image", From: `"public/assets/section-1.png"`, To: `"public/assets/picked-7.png"`,
	}}}
	files, err := readExactEdit(context.Background(), repository, edit)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].path != "content.json" {
		t.Fatalf("files = %+v", files)
	}
	got := string(files[0].updated)
	if strings.Count(got, "picked-7.png") != 1 || strings.Count(got, "section-1.png") != 1 ||
		got != strings.Replace(siteContent, `{"image": "public/assets/section-1.png", "label": "Our café"},`, `{"image": "public/assets/picked-7.png", "label": "Our café"},`, 1) {
		t.Fatalf("the Gallery item alone should change, the rest byte for byte:\n%s", got)
	}
}

func TestAnExactEditsJSONReplacementIsRefusedWhenItDoesNotApply(t *testing.T) {
	repository, _, _ := buildRepository(t)
	source := addVersion(t, repository, "abcp/earlier-run", siteVersion)
	json := func(pointer, from string) authority.ExactEditManifest {
		return authority.ExactEditManifest{SourceSHA: source, Replacements: []authority.ExactEditReplacement{{
			Path: "content.json", BaseBlob: blobAt(t, repository, source, "content.json"), Pointer: pointer, From: from, To: `"x.png"`,
		}}}
	}
	for _, test := range []struct {
		name   string
		edit   authority.ExactEditManifest
		reason string
	}{
		{"another picture there", json("/pages/gallery/sections/0/items/0/image", `"public/assets/section-2.png"`), "not the one expected"},
		{"no such item", json("/pages/gallery/sections/0/items/5/image", `"public/assets/section-1.png"`), "names no value"},
		{"not a JSON file", authority.ExactEditManifest{SourceSHA: source, Replacements: []authority.ExactEditReplacement{{
			Path: "index.html", BaseBlob: blobAt(t, repository, source, "index.html"), Pointer: "/a", From: `1`, To: `2`,
		}}}, "not valid JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := CheckExactEdit(context.Background(), repository, test.edit)
			if !errors.Is(err, ErrExactEditDoesNotApply) || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("CheckExactEdit = %v, want %q", err, test.reason)
			}
		})
	}
}
