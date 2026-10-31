package preview

import (
	"os"
	"path/filepath"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// PageFiles keeps the page files each preview was created with (Repo C design note STANDARD_PAGES §14): a WordPress
// website's standard pages, as named files ABCP never reads. <Root>/<preview> is the folder mounted read-only at
// /pages in the profile's pages service, made before the preview starts and removed with its source once its
// containers are gone; Root itself stays private (0700) to the controller, as the sources' root does, while the
// folder and its files are readable for the service's non-root user (readableSource). A restart removes what an
// earlier controller left.
type PageFiles struct{ Root string }

// Write makes a preview's folder of page files and answers its path. The files were checked by
// serviceapi.DecodePageFiles; each name is checked again to be a plain name here.
func (f PageFiles) Write(id string, files []serviceapi.PageFile) (path string, err error) {
	if !sha256Pattern.MatchString(id) || len(files) == 0 || len(files) > serviceapi.MaxPageFiles {
		return "", ErrIntegrity
	}
	d, err := privateDirectory(f.Root, true)
	if err != nil {
		return "", err
	}
	d.Close()
	path = filepath.Join(f.Root, id)
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		return "", ErrIntegrity
	}
	if err = os.Mkdir(path, 0700); err != nil {
		return "", ErrIntegrity
	}
	defer func() {
		if err != nil {
			_ = f.Remove(id)
		}
	}()
	for _, file := range files {
		if filepath.Base(file.Name) != file.Name || file.Name == "" || file.Name[0] == '.' || len(file.Content) > serviceapi.MaxPageFileBytes {
			return "", ErrIntegrity
		}
		out, e := os.OpenFile(filepath.Join(path, file.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return "", ErrIntegrity
		}
		_, e = out.Write(file.Content)
		closeErr := out.Close()
		if e != nil || closeErr != nil {
			return "", ErrIntegrity
		}
	}
	if err = readableSource(path); err != nil {
		return "", ErrIntegrity
	}
	return path, nil
}

// Remove removes a preview's page files; a preview created without any has nothing to remove.
func (f PageFiles) Remove(id string) error { return Checkout{Root: f.Root}.Remove(id) }

// Reconcile removes every preview's page files an earlier controller left, as Checkout.Reconcile does sources.
func (f PageFiles) Reconcile() error { return Checkout{Root: f.Root}.Reconcile() }
