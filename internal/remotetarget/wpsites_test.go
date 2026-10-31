package remotetarget

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// W5b (SERVER.md, "The front door"): a WordPress website's block proxies to
// its container with a Node app's encoding and headers and without the Origin
// rewrite; a stopped one answers 503; with a relay credential its public block
// relays /__autobuild/* as any site's does, and the inside listener passes a
// post to /__autobuild/contact from its network to the relay, marked with the
// site's credential and host but not as a server asking. Every Node app's and
// static site's block, and every Node app's inside entry, stays as it was.
func TestTheFrontDoorProxiesAWordPressWebsiteAndPassesItsContactMessagesInside(t *testing.T) {
	credential := strings.Repeat("w", 43)
	r := goldenNodeAndStatic()
	r.Sites = append(r.Sites,
		siteRecord{Name: "blog", Host: "blog.example.com", Kind: kindWP, Release: "r8", State: siteLive, Relay: credential},
		siteRecord{Name: "quiet", Host: "quiet.example.com", Kind: kindWP, Release: "r9", State: siteStopped, Relay: strings.Repeat("q", 43)},
		siteRecord{Name: "plainwp", Host: "plainwp.example.com", Kind: kindWP, Release: "r10", State: siteLive},
	)
	off := caddyfileFor(r, "", "", "")
	if !strings.Contains(off, "\nplainwp.example.com {\n\treverse_proxy autobuild-app-plainwp:8080\n\tencode zstd gzip\n\theader {\n"+
		"\t\tX-Content-Type-Options nosniff\n\t\tReferrer-Policy strict-origin-when-cross-origin\n\t\t-Server\n\t}\n}\n") {
		t.Fatalf("a WordPress website's block:\n%s", off)
	}
	if !strings.Contains(off, "\nquiet.example.com {\n\trespond \"This website is paused.\" 503\n\tencode zstd gzip\n") {
		t.Fatalf("a stopped WordPress website answers 503:\n%s", off)
	}
	if strings.Contains(off, "autobuild-app-plainwp:8080 {") || strings.Contains(off, "__autobuild") {
		t.Fatalf("no Origin rewrite and nothing relayed without a relay host:\n%s", off)
	}
	on := caddyfileFor(r, "", "", "siterelays.deployflix.app")
	if !strings.Contains(on, "\nblog.example.com {\n\trequest_header -X-Autobuild-Site\n\trequest_header -X-Autobuild-Host\n\trequest_header -X-Autobuild-Client\n"+
		"\trequest_header -X-Autobuild-Caller\n\thandle /__autobuild/* {\n") ||
		!strings.Contains(on, "\thandle {\n\t\treverse_proxy autobuild-app-blog:8080\n\t}\n") {
		t.Fatalf("a WordPress website with a credential:\n%s", on)
	}
	start := strings.Index(on, "\nhttp://:8089 {\n")
	if start < 0 {
		t.Fatalf("no inside listener:\n%s", on)
	}
	inside := on[start:]
	contact := "\timport inside/blog.caddy\n\thandle @contact_blog {\n\t\trequest_body {\n\t\t\tmax_size 16KB\n\t\t}\n" +
		"\t\treverse_proxy https://siterelays.deployflix.app {\n\t\t\theader_up Host siterelays.deployflix.app\n" +
		"\t\t\theader_up X-Autobuild-Site " + credential + "\n\t\t\theader_up X-Autobuild-Host blog.example.com\n" +
		"\t\t\theader_up X-Autobuild-Client {remote_host}\n\t\t\theader_up -Cookie\n\t\t\theader_up -Authorization\n\t\t}\n\t}\n"
	if !strings.Contains(inside, contact) {
		t.Fatalf("the inside listener lacks the website's contact entry:\n%s", inside)
	}
	if strings.Contains(inside, "@from_blog") || strings.Contains(inside, "@ai_blog") || strings.Contains(inside, "quiet") || strings.Contains(inside, "plainwp") {
		t.Fatalf("a website asks only with contact messages, and only a running one with a credential:\n%s", inside)
	}
	// Every Node app's and static site's block, and the Node apps' inside
	// entries, are as they were.
	golden := goldenCaddyfiles(t)["relays on"]
	for _, block := range strings.Split(strings.SplitN(golden, "\n# The inside listener", 2)[0], "\n\n")[1:] {
		if !strings.Contains(on, block) {
			t.Fatalf("a block changed beside a WordPress website:\n%s", block)
		}
	}
	nodeInside := strings.SplitN(golden, "\trequest_header -X-Autobuild-Relay-Token\n", 2)[1]
	if !strings.Contains(on, strings.TrimSuffix(nodeInside, "\trespond 404\n}\n")) {
		t.Fatalf("the Node apps' inside entries changed:\n%s", inside)
	}
}

// W5b: the inside matcher of a WordPress website takes its network's range
// and only a post to /__autobuild/contact; a Node app's is as it was.
func TestTheInsideMatcherOfAWordPressWebsiteIsItsNetworksContactPostsAlone(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	fake := "#!/bin/sh\ncase \"$*\" in *autobuild-net-cafe*) echo '172.21.0.0/16 ' ;; *autobuild-net-blog*) echo '172.22.0.0/16 ' ;; *) exit 1 ;; esac\n"
	if err := os.WriteFile(bin+"/docker", []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "set -eu\n"+fill(insideScript, "NETS", "cafe", "WPNETS", "blog lost"))
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	read := func(site string) string {
		b, err := os.ReadFile(home + "/autobuild/frontdoor/config/inside/" + site + ".caddy")
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if blog := read("blog"); blog != "@contact_blog {\n\tremote_ip 172.22.0.0/16\n\tmethod POST\n\tpath /__autobuild/contact\n}\n" {
		t.Fatalf("blog's matcher:\n%s", blog)
	}
	if lost := read("lost"); lost != "@contact_lost {\n\tremote_ip 255.255.255.255/32\n\tmethod POST\n\tpath /__autobuild/contact\n}\n" {
		t.Fatalf("a website without a network matches someone:\n%s", lost)
	}
	if cafe := read("cafe"); cafe != "@from_cafe {\n\tremote_ip 172.21.0.0/16\n\tmethod GET\n\tpath_regexp ^/__autobuild/connect/[a-z0-9-]+/[a-z0-9-]+/[A-Za-z0-9_-]+$\n}\n"+
		"@ai_cafe {\n\tremote_ip 172.21.0.0/16\n\tmethod POST\n\tpath /__autobuild/ai\n}\n" {
		t.Fatalf("cafe's matcher changed:\n%s", cafe)
	}
	if !strings.Contains(frontDoorScript, "for n in @NETS@ @WPNETS@; do") {
		t.Fatal("the front door does not join WordPress websites' networks")
	}
}

func TestAnAdministratorsAddressIsCheckedAndKeptInLowerCase(t *testing.T) {
	for raw, want := range map[string]string{
		"owner@example.com":           "owner@example.com",
		" Owner.Name+site@Example.IN": "owner.name+site@example.in",
		"a_b%c-d@mail.co.uk":          "a_b%c-d@mail.co.uk",
	} {
		if got, ok := checkAdminEmail(raw); !ok || got != want {
			t.Errorf("%q = %q %v", raw, got, ok)
		}
	}
	for _, bad := range []string{"", "owner", "owner@localhost", "owner@example", ".owner@example.com", "own..er@example.com", "owner.@example.com",
		"own er@example.com", "owner@exa_mple.com", "owner@-example.com", "owner@example.123", "o'wner@example.com", "owner@example.com\nx",
		"$(id)@example.com", "owner@example.com;reboot", strings.Repeat("a", 65) + "@example.com", "a@" + strings.Repeat("b", 250) + ".com"} {
		if got, ok := checkAdminEmail(bad); ok {
			t.Errorf("%q taken as %q", bad, got)
		}
	}
	if adminUser("owner@example.com") != "owner@example.com" || adminUser(strings.Repeat("a", 50)+"@example.com") != "admin" {
		t.Fatal("the administrator's user name is the address, or admin past 60 characters")
	}
}

// W5b: the images are pinned by digest, as the Node runtime's are, and the
// scripts run them exactly as SERVER.md and its ci/server-helpers.sh do.
func TestAWordPressWebsiteRunsAsTheKitsContractSays(t *testing.T) {
	for _, image := range []string{WordPressServerImage, WordPressMariaDBImage} {
		name, digest, ok := strings.Cut(image, "@sha256:")
		if !ok || !digestPattern.MatchString(digest) || !strings.HasPrefix(name, "ghcr.io/pankajleh/autobuild-wp-") {
			t.Fatalf("not pinned by digest: %s", image)
		}
	}
	start := fill(wpStartAppScript, "N", "blog", "RUNTIME", "", "RELAY", "-e AUTOBUILD_RELAY="+insideRelay+" ")
	for _, want := range []string{
		"docker run -d --name autobuild-app-blog --restart unless-stopped --network autobuild-net-blog --network-alias app  \\\n" +
			"  --user 33:33 --read-only --cap-drop ALL --security-opt no-new-privileges --memory 512m --memory-swap 512m \\\n" +
			"  --pids-limit 256 --ulimit nofile=1024:1024 --log-driver json-file --log-opt max-size=1m --log-opt max-file=2 \\\n" +
			"  --tmpfs /scratch:rw,nosuid,nodev,noexec,size=134217728,mode=700,uid=33,gid=33 --workdir /scratch \\\n" +
			"  --mount type=bind,src=\"$src\",dst=/source,readonly --mount type=volume,src=autobuild-wp-blog,dst=/data \\\n" +
			"  --env-file \"$A/apps/blog/database.env\" --env-file \"$A/apps/blog/site.env\" \\\n" +
			"  -e HOME=/scratch -e TMPDIR=/scratch -e AUTOBUILD_RELAY=http://autobuild-frontdoor:8089 --label autobuild.site=blog --entrypoint /usr/local/bin/autobuild-wp-server \\\n" +
			"  " + WordPressServerImage + " >/dev/null\n",
		"docker volume inspect autobuild-wp-blog >/dev/null 2>&1 || docker volume create autobuild-wp-blog >/dev/null\n",
		"printf 'SITE_URL=%s\\nADMIN_EMAIL=%s\\n' \"$url\" \"$email\" > \"$A/apps/blog/.site.env.next\"\n",
	} {
		if !strings.Contains(start, want) {
			t.Fatalf("the website's start lacks %q:\n%s", want, start)
		}
	}
	if off := fill(wpStartAppScript, "N", "blog", "RUNTIME", "--runtime=runsc", "RELAY", ""); strings.Contains(off, "AUTOBUILD_RELAY") ||
		!strings.Contains(off, "--network-alias app --runtime=runsc \\\n") || !strings.Contains(off, "-e TMPDIR=/scratch --label autobuild.site=blog ") {
		t.Fatalf("without relays, under gVisor:\n%s", off)
	}
	database := wpStartDatabaseScript
	for _, want := range []string{
		"docker run -d --name autobuild-mariadb --restart unless-stopped --network autobuild-db --user 999:999 --read-only --cap-drop ALL \\\n" +
			"  --security-opt no-new-privileges --memory 512m --memory-swap 512m --pids-limit 256 --ulimit nofile=4096:4096 \\\n" +
			"  --tmpfs /scratch:rw,nosuid,nodev,noexec,size=268435456,mode=700,uid=999,gid=999 --workdir /scratch \\\n" +
			"  --mount type=volume,src=autobuild-mariadb-data,dst=/data --entrypoint /usr/local/bin/autobuild-mariadb \\\n" +
			"  " + WordPressMariaDBImage + " server >/dev/null\n",
		"until docker exec autobuild-mariadb mariadb-admin --no-defaults --socket=/scratch/mysqld.sock --user=mysql --silent ping >/dev/null 2>&1; do",
	} {
		if !strings.Contains(database, want) {
			t.Fatalf("the database's start lacks %q:\n%s", want, database)
		}
	}
	for name, script := range map[string]string{"backup": wpBackupScript, "restore": wpRestoreFilesScript} {
		if !strings.Contains(script, "docker run --rm ") || !strings.Contains(script, " --user 33:33 --read-only --cap-drop ALL --security-opt no-new-privileges --network none \\\n"+
			"  --mount type=volume,src=autobuild-wp-@N@,dst=/data --entrypoint /usr/local/bin/files "+WordPressServerImage) {
			t.Fatalf("%s runs files otherwise:\n%s", name, script)
		}
	}
	if !strings.Contains(fill(wpAdminPasswordScript, "N", "blog"), "exec docker exec autobuild-app-blog admin-password take\n") {
		t.Fatal("the password is not taken as SERVER.md says")
	}
}

// The tar a WordPress website's backup downloads as is exactly the size
// ABCP announces for it, whatever the two files' sizes.
func TestAWordPressBackupDownloadsAsATarOfAKnownSize(t *testing.T) {
	for _, sizes := range [][2]int64{{1, 1}, {511, 512}, {513, 10240}, {3211, 1 << 20}} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for i, name := range []string{websiteDumpName, websiteUploadsName} {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: sizes[i], Typeflag: tar.TypeReg,
				ModTime: time.Date(2026, 10, 31, 2, 30, 0, 0, time.UTC), Format: tar.FormatUSTAR}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(bytes.Repeat([]byte{'x'}, int(sizes[i]))); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if got := int64(buf.Len()); got != websiteBackupSize(sizes[0], sizes[1]) {
			t.Fatalf("sizes %v: the tar is %d bytes, announced %d", sizes, got, websiteBackupSize(sizes[0], sizes[1]))
		}
	}
}
