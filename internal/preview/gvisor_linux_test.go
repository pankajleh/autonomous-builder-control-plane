//go:build linux

package preview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gvisorProfile is an app and its database, as Repo C's node profiles are, under gVisor.
func gvisorProfile() PreviewProfileV1 {
	p := testProfile()
	db := p.Services[0]
	db.Name, db.Presented, db.MountSource, db.PrepareArgv = "db", false, false, nil
	p.Services = append(p.Services, db)
	p.Runtime = RuntimeGVisor
	return p
}

func createdIndex(f *dockerFixture, container string) int {
	for i, args := range f.commands {
		if args[0] == "create" && slices.Contains(args, container) {
			return i
		}
	}
	return -1
}

func TestGVisorProfileStartsTheDatabaseFirstAndGivesTheAppItsAddress(t *testing.T) {
	p := gvisorProfile()
	f := newDockerFixture(t, p)
	f.d.approved[p.Digest()] = true
	route, err := f.d.Start(context.Background(), f.id, filepath.Join(f.d.root, "sources", f.id), p)
	if err != nil || !sha256Pattern.MatchString(route) {
		t.Fatal("start", route, err)
	}
	t.Cleanup(func() { _ = f.d.Stop(context.Background(), f.id) })
	app, db := f.d.container(f.id, 0), f.d.container(f.id, 1)
	if createdIndex(f, db) < 0 || createdIndex(f, db) > createdIndex(f, app) {
		t.Fatal("under gVisor the database must start before the app it serves")
	}
	appArgs, dbArgs := strings.Join(f.created[app], " "), strings.Join(f.created[db], " ")
	for _, args := range []string{appArgs, dbArgs} {
		if !strings.Contains(args, "--runtime=runsc") {
			t.Fatal("a service did not run under gVisor:", args)
		}
	}
	// Docker's embedded DNS does not answer inside gVisor: the app finds the database by a hosts entry.
	if !strings.Contains(appArgs, "--add-host db:"+f.ip(1)) || strings.Contains(dbArgs, "--add-host") {
		t.Fatal("hosts entries", appArgs, dbArgs)
	}
	if ok, err := f.d.Healthy(context.Background(), f.id, p); err != nil {
		t.Fatal("a gVisor preview failed its isolation inspection", ok, err)
	}
}

func TestDefaultProfilesKeepDockersRuntimeOrderAndNoHostsEntries(t *testing.T) {
	p := gvisorProfile()
	p.Runtime = ""
	f := newDockerFixture(t, p)
	f.d.approved[p.Digest()] = true
	if _, err := f.d.Start(context.Background(), f.id, filepath.Join(f.d.root, "sources", f.id), p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.d.Stop(context.Background(), f.id) })
	app, db := f.d.container(f.id, 0), f.d.container(f.id, 1)
	if createdIndex(f, app) > createdIndex(f, db) {
		t.Fatal("a profile without a runtime changed its start order")
	}
	for _, name := range []string{app, db} {
		joined := strings.Join(f.created[name], " ")
		if strings.Contains(joined, "--runtime") || strings.Contains(joined, "--add-host") {
			t.Fatal("a profile without a runtime got gVisor arguments:", joined)
		}
	}
}

func TestGVisorInspectionRejectsTheWrongRuntimeAndForeignHostsEntries(t *testing.T) {
	cases := map[string]struct {
		runtime string
		hosts   []string
		gvisor  bool
	}{
		"gvisor-profile-on-runc":     {runtime: "runc", hosts: []string{"db:10.213.0.3"}, gvisor: true},
		"gvisor-profile-no-runtime":  {runtime: "", hosts: nil, gvisor: true},
		"entry-for-an-unknown-name":  {runtime: "runsc", hosts: []string{"metadata:10.0.0.9"}, gvisor: true},
		"entry-for-itself":           {runtime: "runsc", hosts: []string{"web:10.213.0.2"}, gvisor: true},
		"entry-to-loopback":          {runtime: "runsc", hosts: []string{"db:127.0.0.1"}, gvisor: true},
		"entry-to-a-public-address":  {runtime: "runsc", hosts: []string{"db:1.1.1.1"}, gvisor: true},
		"entry-twice":                {runtime: "runsc", hosts: []string{"db:10.213.0.3", "db:10.213.0.4"}, gvisor: true},
		"default-profile-on-runsc":   {runtime: "runsc", hosts: nil, gvisor: false},
		"default-profile-with-entry": {runtime: "runc", hosts: []string{"db:10.213.0.3"}, gvisor: false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := gvisorProfile()
			if !c.gvisor {
				p.Runtime = ""
			}
			d := newDocker("/private/previews")
			id := strings.Repeat("c", 64)
			v := dockerInspection(d, id, p, p.Services[0])
			good := map[string]any{"Runtime": "runsc", "ExtraHosts": []string{"db:10.213.0.3"}}
			if !c.gvisor {
				good = map[string]any{"Runtime": "runc", "ExtraHosts": []string{}}
			}
			for k, value := range good {
				v["HostConfig"].(map[string]any)[k] = value
			}
			raw, _ := json.Marshal([]any{v})
			d.run = func(context.Context, ...string) (string, error) { return string(raw), nil }
			if _, err := d.endpoint(context.Background(), id, p, p.Services[0], 0, ""); err != nil {
				t.Fatal("baseline rejected", err)
			}
			v["HostConfig"].(map[string]any)["Runtime"], v["HostConfig"].(map[string]any)["ExtraHosts"] = c.runtime, c.hosts
			raw, _ = json.Marshal([]any{v})
			if _, err := d.endpoint(context.Background(), id, p, p.Services[0], 0, ""); err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}

func TestGVisorProfileIsProvedOnlyWhereDockerHasTheRuntime(t *testing.T) {
	p := gvisorProfile()
	f := newDockerFixture(t, p)
	f.id = jsonDigest([]string{f.d.namespace, p.Digest(), "isolation-probe"})
	if f.d.prove(context.Background(), p) {
		t.Fatal("a gVisor profile was proved on a host without runsc")
	}
	for _, args := range f.commands {
		if args[0] == "create" {
			t.Fatal("the probe started containers on a host without runsc")
		}
	}
	f.runtimes = `{"runc":{},"runsc":{"path":"/usr/bin/runsc"}}`
	// The probe's own placeholder answers 404 through the presentation, as in the other probe tests.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer target.Close()
	f.external = func() { connectPresentation(t, f.d.groups[f.id], target.URL) }
	if !f.d.prove(context.Background(), p) {
		t.Fatal("a gVisor profile was not proved on a host with runsc")
	}
}

func TestProfileRuntimeIsGVisorOrNothingAndKeepsOldDigests(t *testing.T) {
	p := testProfile()
	before := p.Digest()
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "runtime") {
		t.Fatal("an empty runtime changed the profile's JSON and so its digest")
	}
	for _, runtime := range []string{"runc", "kata", "runsc ", "io.containerd.runc.v2"} {
		q := cloneProfile(p)
		q.Runtime = runtime
		if q.Validate() == nil {
			t.Fatal("runtime accepted:", runtime)
		}
	}
	q := cloneProfile(p)
	q.Runtime = RuntimeGVisor
	if q.Validate() != nil || q.Digest() == before {
		t.Fatal("gVisor profile invalid, or its digest does not differ")
	}
}

func TestGVisorSourceMountIsReadOnlyWithoutTheRecursiveOptionGVisorRefuses(t *testing.T) {
	d := newDocker("/private/previews")
	id := strings.Repeat("c", 64)
	path := filepath.Join(d.root, "sources", id)
	mount := func(p PreviewProfileV1) string {
		args := d.createArgs(id, path, p, p.Services[0], 0, "")
		i := slices.Index(args, "--mount")
		if i < 0 || i+1 >= len(args) {
			t.Fatal("no source mount", args)
		}
		return args[i+1]
	}
	if got := mount(gvisorProfile()); got != "type=bind,src="+path+",dst=/source,readonly,bind-propagation=rprivate" {
		t.Fatal("gVisor source mount", got)
	}
	p := gvisorProfile()
	p.Runtime = ""
	if got := mount(p); got != "type=bind,src="+path+",dst=/source,readonly,bind-recursive=readonly,bind-propagation=rprivate" {
		t.Fatal("a profile without a runtime lost its recursive read-only source mount", got)
	}
}
