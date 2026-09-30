//go:build linux

package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestHostedDockerArgumentsMountOnlyTheKeysVolume(t *testing.T) {
	d := newDocker("/private/previews")
	p := hostedProfile()
	id := strings.Repeat("c", 64)
	volume := d.volume(strings.Repeat("7", 64))
	app := strings.Join(d.createArgs(id, "/private/previews/sources/"+id, p, p.Services[0], 0, volume), " ")
	db := strings.Join(d.createArgs(id, "/private/previews/sources/"+id, p, p.Services[1], 1, volume), " ")
	sleep := "/bin/sleep " + strconv.Itoa(hostedSleepSeconds)
	if strings.Contains(app, "type=volume") || !strings.Contains(app, sleep) {
		t.Fatalf("app service args = %s", app)
	}
	if !strings.Contains(db, "--mount type=volume,src="+volume+",dst=/data ") || !strings.Contains(db, sleep) || strings.Contains(db, "type=bind") {
		t.Fatalf("data service args = %s", db)
	}
	for _, required := range []string{"--cap-drop=ALL", "--read-only", "--security-opt=no-new-privileges", "--pull=never"} {
		if !strings.Contains(db, required) {
			t.Fatalf("data service lost %s", required)
		}
	}
	if !strings.HasPrefix(volume, "abcp-hosted-"+d.namespace[:16]+"-7777") {
		t.Fatalf("volume name %s", volume)
	}
	preview := strings.Join(d.createArgs(id, "/private/previews/sources/"+id, testProfile(), testProfile().Services[0], 0, ""), " ")
	if !strings.Contains(preview, "/bin/sleep 60") || strings.Contains(preview, "type=volume") {
		t.Fatalf("preview args changed: %s", preview)
	}
}

func hostedInspection(d *dockerRuntime, id string, p PreviewProfileV1, i int, volume string) map[string]any {
	v := dockerInspection(d, id, p, p.Services[i])
	if !p.Services[i].MountSource {
		v["Mounts"] = []map[string]any{}
	}
	if p.Services[i].DataVolume {
		v["Mounts"] = append(v["Mounts"].([]map[string]any), map[string]any{"Type": "volume", "Name": volume, "Source": "/var/lib/docker/volumes/" + volume + "/_data", "Destination": "/data", "RW": true})
	}
	return v
}

func TestHostedDockerInspectionAcceptsExactlyTheKeysVolume(t *testing.T) {
	d := newDocker("/private/previews")
	p := hostedProfile()
	id := strings.Repeat("c", 64)
	volume := d.volume(strings.Repeat("7", 64))
	check := func(v map[string]any, i int, want string) error {
		raw, _ := json.Marshal([]any{v})
		d.run = func(context.Context, ...string) (string, error) { return string(raw), nil }
		_, err := d.endpoint(context.Background(), id, p, p.Services[i], i, want)
		return err
	}
	if err := check(hostedInspection(d, id, p, 1, volume), 1, volume); err != nil {
		t.Fatal("the data service with its volume was rejected", err)
	}
	if err := check(hostedInspection(d, id, p, 0, volume), 0, volume); err != nil {
		t.Fatal("the app service was rejected", err)
	}
	cases := map[string]func(map[string]any){
		"other-volume":     func(v map[string]any) { v["Mounts"].([]map[string]any)[0]["Name"] = d.volume(strings.Repeat("8", 64)) },
		"read-only-volume": func(v map[string]any) { v["Mounts"].([]map[string]any)[0]["RW"] = false },
		"elsewhere":        func(v map[string]any) { v["Mounts"].([]map[string]any)[0]["Destination"] = "/etc" },
		"bind-instead":     func(v map[string]any) { v["Mounts"].([]map[string]any)[0]["Type"] = "bind" },
		"no-volume":        func(v map[string]any) { v["Mounts"] = []map[string]any{} },
		"second-volume": func(v map[string]any) {
			v["Mounts"] = append(v["Mounts"].([]map[string]any), map[string]any{"Type": "volume", "Name": volume, "Destination": "/data", "RW": true})
		},
	}
	for name, mutate := range cases {
		v := hostedInspection(d, id, p, 1, volume)
		mutate(v)
		if check(v, 1, volume) == nil {
			t.Error(name, "was accepted")
		}
	}
	appWithVolume := hostedInspection(d, id, p, 0, volume)
	appWithVolume["Mounts"] = append(appWithVolume["Mounts"].([]map[string]any), map[string]any{"Type": "volume", "Name": volume, "Destination": "/data", "RW": true})
	if check(appWithVolume, 0, volume) == nil {
		t.Error("the data volume was accepted on a service without data_volume")
	}
	if check(hostedInspection(d, id, p, 1, volume), 1, "") == nil {
		t.Error("a data volume was accepted on a preview")
	}
}

func TestHostedDockerRefusesAVolumeItDidNotMake(t *testing.T) {
	d := newDocker("/private/previews")
	volume := d.volume(strings.Repeat("7", 64))
	good := map[string]any{"Name": volume, "Driver": "local", "Labels": map[string]string{"abcp.preview.owner": d.namespace, "abcp.hosted.volume": volume}, "Options": map[string]string{}}
	var inspected map[string]any
	var created []string
	d.run = func(_ context.Context, args ...string) (string, error) {
		switch {
		case args[0] == "volume" && args[1] == "create":
			created = args
		case args[0] == "volume" && args[1] == "inspect":
			raw, _ := json.Marshal([]any{inspected})
			return string(raw), nil
		}
		return "", nil
	}
	inspected = good
	if err := d.ensureVolume(context.Background(), volume); err != nil {
		t.Fatal("own volume refused", err)
	}
	if strings.Join(created, " ") != "volume create --driver local --label abcp.preview.owner="+d.namespace+" --label abcp.hosted.volume="+volume+" "+volume {
		t.Fatalf("create = %v", created)
	}
	cases := map[string]func(map[string]any){
		"bind-options": func(v map[string]any) {
			v["Options"] = map[string]string{"type": "none", "o": "bind", "device": "/etc"}
		},
		"other-owner": func(v map[string]any) {
			v["Labels"] = map[string]string{"abcp.preview.owner": strings.Repeat("0", 64), "abcp.hosted.volume": volume}
		},
		"no-labels":    func(v map[string]any) { v["Labels"] = map[string]string{} },
		"other-driver": func(v map[string]any) { v["Driver"] = "nfs" },
		"renamed-label": func(v map[string]any) {
			v["Labels"] = map[string]string{"abcp.preview.owner": d.namespace, "abcp.hosted.volume": "other"}
		},
	}
	for name, mutate := range cases {
		v := map[string]any{}
		b, _ := json.Marshal(good)
		_ = json.Unmarshal(b, &v)
		mutate(v)
		inspected = v
		if err := d.ensureVolume(context.Background(), volume); !errors.Is(err, ErrIntegrity) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestHostedDockerBackupAndRestoreRunTheProfilesCommandsInTheDataService(t *testing.T) {
	p := hostedProfile()
	f := newDockerFixture(t, p)
	d := f.d
	volume := d.volume(strings.Repeat("7", 64))
	id := f.id
	d.approved[p.Digest()] = true
	g := &presentation{volume: volume}
	d.groups[id] = g
	inspect := func(i int) string {
		raw, _ := json.Marshal([]any{hostedInspection(d, id, p, i, volume)})
		return string(raw)
	}
	d.run = func(_ context.Context, args ...string) (string, error) {
		switch args[0] {
		case "network":
			raw, _ := json.Marshal([]any{map[string]any{"Internal": true, "Driver": "bridge", "Labels": map[string]string{"abcp.preview.owner": d.namespace}}})
			return string(raw), nil
		case "inspect":
			return inspect(1), nil
		}
		return "", nil
	}
	var streamed [][]string
	var input []byte
	d.stream = func(_ context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
		streamed = append(streamed, args)
		if stdin != nil {
			input, _ = io.ReadAll(stdin)
		}
		_, err := io.WriteString(stdout, "dump")
		return err
	}
	var out bytes.Buffer
	if err := d.Dump(context.Background(), id, p, &out); err != nil || out.String() != "dump" {
		t.Fatalf("dump = %q, %v", out.String(), err)
	}
	want := append([]string{"exec", d.container(id, 1), "/usr/bin/env"}, cleanEnv(p.Services[1])...)
	want = append(want, p.Services[1].BackupArgv...)
	if strings.Join(streamed[0], " ") != strings.Join(want, " ") {
		t.Fatalf("backup exec = %v", streamed[0])
	}
	if err := d.Load(context.Background(), id, p, strings.NewReader("restore-me")); err != nil || string(input) != "restore-me" {
		t.Fatalf("load = %q, %v", input, err)
	}
	if streamed[1][1] != "-i" || streamed[1][len(streamed[1])-1] != "app" || streamed[1][len(streamed[1])-len(p.Services[1].RestoreArgv)] != p.Services[1].RestoreArgv[0] {
		t.Fatalf("restore exec = %v", streamed[1])
	}
	// A container that drifted from its isolation is not exec'd into.
	d.run = func(_ context.Context, args ...string) (string, error) {
		if args[0] == "inspect" {
			v := hostedInspection(d, id, p, 1, volume)
			v["HostConfig"].(map[string]any)["Privileged"] = true
			raw, _ := json.Marshal([]any{v})
			return string(raw), nil
		}
		raw, _ := json.Marshal([]any{map[string]any{"Internal": true, "Driver": "bridge", "Labels": map[string]string{"abcp.preview.owner": d.namespace}}})
		return string(raw), nil
	}
	streamed = nil
	if err := d.Dump(context.Background(), id, p, &out); err == nil || len(streamed) != 0 {
		t.Fatal("a drifted data service was exec'd into")
	}
	if err := d.Dump(context.Background(), id, testProfile(), &out); err == nil {
		t.Fatal("a preview profile was dumped")
	}
}

func TestHostedDockerStartsOnlyHostedProfilesWithAVolume(t *testing.T) {
	d := newDocker("/private/previews")
	p := hostedProfile()
	id := strings.Repeat("c", 64)
	d.approved[p.Digest()] = true
	called := false
	d.run = func(context.Context, ...string) (string, error) { called = true; return "", ErrUnavailable }
	if _, err := d.Start(context.Background(), id, "/private/previews/sources/"+id, p); err == nil || called {
		t.Fatal("a hosted profile started as a preview")
	}
	if _, err := d.StartHosted(context.Background(), id, "/private/previews/sources/"+id, testProfile(), strings.Repeat("7", 64)); err == nil || called {
		t.Fatal("a preview profile started as hosted")
	}
	if _, err := d.StartHosted(context.Background(), id, "/private/previews/sources/"+id, p, "not-a-digest"); err == nil || called {
		t.Fatal("a hosted start without a key digest was accepted")
	}
}

func TestHostedProfileProbeWritesItsDataVolumeAndRemovesIt(t *testing.T) {
	for _, writable := range []bool{true, false} {
		t.Run(strconv.FormatBool(writable), func(t *testing.T) {
			f := newDockerFixture(t, hostedProfile())
			f.id = jsonDigest([]string{f.d.namespace, f.p.Digest(), "isolation-probe"})
			volume := f.d.volume(jsonDigest([]string{f.d.namespace, f.p.Digest(), "isolation-probe-volume"}))
			exists, touched, removed := false, false, false
			fixture := f.run
			f.d.run = func(ctx context.Context, args ...string) (string, error) {
				switch {
				case args[0] == "volume" && args[1] == "create":
					exists = args[len(args)-1] == volume
					return volume, nil
				case args[0] == "volume" && args[1] == "inspect":
					raw, _ := json.Marshal([]any{map[string]any{"Name": volume, "Driver": "local", "Labels": map[string]string{"abcp.preview.owner": f.d.namespace, "abcp.hosted.volume": volume}}})
					return string(raw), nil
				case args[0] == "volume" && args[1] == "ls":
					if exists {
						return volume, nil
					}
					return "", nil
				case args[0] == "volume" && args[1] == "rm":
					removed, exists = args[2] == volume, false
					return "", nil
				case args[0] == "inspect" && strings.HasSuffix(args[1], "-1"):
					f.commands = append(f.commands, args)
					v := hostedInspection(f.d, f.id, f.p, 1, volume)
					v["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[f.d.network(f.id)] = map[string]string{"IPAddress": "10.88.0.3"}
					raw, _ := json.Marshal([]any{v})
					return string(raw), nil
				case args[0] == "exec" && strings.Contains(strings.Join(args, " "), "touch /data/.abcp-probe"):
					touched = true
					if !writable {
						return "", ErrUnavailable
					}
					return "", nil
				}
				return fixture(ctx, args...)
			}
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
			defer target.Close()
			f.external = func() { connectPresentation(t, f.d.groups[f.id], target.URL) }
			proved := f.d.prove(context.Background(), f.p)
			if proved != writable || f.d.Available(f.p) != writable || !touched {
				t.Fatalf("proved %v, touched %v", proved, touched)
			}
			if !removed || exists {
				t.Fatal("the probe's data volume was not removed")
			}
		})
	}
}
