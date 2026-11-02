package main

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// needSh skips a test that runs scripts when there is no sh.
func needSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
}

// hostile are names a catalogue could send to get a command run.
var hostile = []string{
	"a b", "x;reboot", "$(reboot)", "`reboot`", "it's", "'", "a\nreboot", `\`, "", `"; reboot; "`, "a\rb",
}

func TestShellQuote(t *testing.T) {
	needSh(t)
	for _, s := range hostile {
		out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("shellQuote(%q): sh read %q (%v)", s, out, err)
		}
	}
}

func TestValidFolder(t *testing.T) {
	for _, ok := range []string{"MiyooFin", "My App", "App (beta)", "C++", "a.b", "x-y_z", "_x", strings.Repeat("a", 64)} {
		if err := validFolder(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", ".hidden", " lead", "trail ", "trail.", "a/b", `a\b`, "x;reboot",
		"$(x)", "a'b", "a\nb", "a`b", `a"b`, "a&b", "a|b", "a*b", strings.Repeat("a", 65)} {
		if validFolder(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A catalogue entry whose folder or name could break out of the boot hook is
// dropped; a folder with a space is fine.
func TestParseCatalogHostile(t *testing.T) {
	sum := strings.Repeat("a", 64)
	c, err := ParseCatalog([]byte(`{"apps":[
		{"name":"My App","folder":"My App","zip":"https://x/a.zip","sha256":"`+sum+`","secure":"s.txt"},
		{"name":"Semi","folder":"x;reboot","zip":"https://x/b.zip","sha256":"`+sum+`","secure":"s.txt"},
		{"name":"Sub","folder":"$(reboot)","zip":"https://x/c.zip","sha256":"`+sum+`"},
		{"name":"Dot","folder":".hidden","zip":"https://x/d.zip","sha256":"`+sum+`"},
		{"name":"Evil\nreboot","folder":"Evil","zip":"https://x/e.zip","sha256":"`+sum+`","secure":"s.txt"}]}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Apps) != 1 || c.Apps[0].Folder != "My App" {
		t.Fatalf("kept %+v", c.Apps)
	}
}

// The scripts made for an app with a hostile name stay valid sh, and the
// name only ever lands in comments.
func TestSecureScriptsHostile(t *testing.T) {
	needSh(t)
	for _, name := range hostile {
		a := App{Name: name + "\nreboot\n$(reboot)", Folder: "My App (x)", Secure: "data/s.txt", Tailnet: true}
		for kind, script := range map[string]string{"wrapper": wrapperFor(a), "hook": hookFor(a, "/mnt/SDCARD/App/"+a.Folder)} {
			p := filepath.Join(t.TempDir(), "s.sh")
			os.WriteFile(p, []byte(script), 0755)
			if out, err := exec.Command("sh", "-n", p).CombinedOutput(); err != nil {
				t.Errorf("%s for %q: sh -n: %v %s", kind, name, err, out)
			}
			for _, l := range strings.Split(script, "\n") {
				if strings.Contains(l, "reboot") && !strings.HasPrefix(strings.TrimSpace(l), "#") {
					t.Errorf("%s for %q: the name reached a command: %q", kind, name, l)
				}
			}
		}
	}
	if !strings.Contains(hookFor(App{Folder: "My App (x)", Secure: "s.txt"}, "/mnt/SDCARD/App/My App (x)"), "A='/mnt/SDCARD/App/My App (x)'\n") {
		t.Error("the hook's folder isn't quoted")
	}
	// secure itself refuses a folder that isn't plain, whatever called it
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	if secure(App{Name: "x", Folder: "x;reboot", Secure: "s.txt"}) == nil {
		t.Error("secure accepted a hostile folder")
	}
	if hookName(App{Folder: "My App"}) == hookName(App{Folder: "My-App"}) || !bootRe.MatchString(hookName(App{Folder: "My App (x)"})) {
		t.Errorf("hook names: %q %q", hookName(App{Folder: "My App"}), hookName(App{Folder: "My-App"}))
	}
}

// fakeBin is a folder for PATH whose reboot leaves a mark instead.
func fakeBin(t *testing.T) (dir, mark string) {
	dir = t.TempDir()
	mark = filepath.Join(dir, "rebooted")
	os.WriteFile(filepath.Join(dir, "reboot"), []byte("#!/bin/sh\ntouch '"+mark+"'\n"), 0755)
	return dir, mark
}

// The boot hook, run on a folder with spaces and brackets: it keeps the
// launch command an update brought, points the menu at the wrapper, and
// runs nothing from the app's name.
func TestBootHookRuns(t *testing.T) {
	needSh(t)
	bin, mark := fakeBin(t)
	dir := filepath.Join(t.TempDir(), "My App (x)")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "secure-launch.sh"), []byte("#!/bin/sh\n"), 0755)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("{\n  \"label\": \"x\",\n  \"launch\": \"bin\\/run.sh\"\n}\n"), 0644)
	a := App{Name: "x\nreboot", Folder: "My App (x)", Secure: "s.txt"}
	hook := filepath.Join(t.TempDir(), "hook.sh")
	os.WriteFile(hook, []byte(hookFor(a, dir)), 0755)
	run := func() {
		cmd := exec.Command("sh", hook)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook: %v %s", err, out)
		}
	}
	run()
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	got, _ := os.ReadFile(filepath.Join(dir, launchFile))
	if !strings.Contains(string(cfg), `"launch": "./secure-launch.sh"`) || string(got) != "bin/run.sh\n" {
		t.Fatalf("config %s, launch %q", cfg, got)
	}
	run() // again: the wrapper is never recorded as the app's launch
	if got, _ := os.ReadFile(filepath.Join(dir, launchFile)); string(got) != "bin/run.sh\n" {
		t.Errorf("second run: launch %q", got)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Error("the hook ran reboot")
	}
}

// secure keeps the app's own launch command, the wrapper runs exactly that
// (relative, absolute, or launch.sh when none was kept), and installing
// again doesn't wrap the wrapper.
func TestSecureKeepsLaunch(t *testing.T) {
	needSh(t)
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	a := App{Name: "Thing", Folder: "Thing", Secure: "s.txt"}
	dir := a.dir()
	os.MkdirAll(dir, 0755)
	setLaunch := func(l string) {
		os.WriteFile(filepath.Join(dir, "config.json"), []byte("{\n  \"label\": \"Thing\",\n  \"launch\": \""+l+"\"\n}\n"), 0644)
	}
	runWrapper := func() (string, int) {
		os.Remove(filepath.Join(sd, "ran"))
		err := exec.Command("sh", filepath.Join(dir, "secure-launch.sh")).Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(sd, "ran"))
		return string(b), code
	}
	os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho run.sh > '"+sd+"/ran'\nexit 3\n"), 0755)
	os.WriteFile(filepath.Join(dir, "launch.sh"), []byte("#!/bin/sh\necho launch.sh > '"+sd+"/ran'\n"), 0755)
	abs := filepath.Join(t.TempDir(), "elsewhere.sh")
	os.WriteFile(abs, []byte("#!/bin/sh\necho elsewhere > '"+sd+"/ran'\n"), 0755)

	setLaunch("run.sh")
	if err := secure(a); err != nil {
		t.Fatal(err)
	}
	if got, code := runWrapper(); got != "run.sh\n" || code != 3 {
		t.Errorf("ran %q, exit %d", got, code)
	}
	if err := secure(a); err != nil { // installed again over itself
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, launchFile)); string(got) != "run.sh\n" {
		t.Errorf("after a second install the launch is %q", got)
	}
	setLaunch(abs) // an update brings a new config.json
	if err := secure(a); err != nil {
		t.Fatal(err)
	}
	if got, _ := runWrapper(); got != "elsewhere\n" {
		t.Errorf("absolute launch: ran %q", got)
	}
	os.Remove(filepath.Join(dir, launchFile)) // installed before the launch was kept
	if got, _ := runWrapper(); got != "launch.sh\n" {
		t.Errorf("no kept launch: ran %q", got)
	}
	setLaunch("bad\\u000areboot")
	if secure(a) == nil {
		t.Error("a launch entry with a newline accepted")
	}
}

// The Tailnet wrapper reads MiniScale's HTTP proxy port from miniscale.conf
// without running it, and falls back to 1056.
func TestWrapperProxyPort(t *testing.T) {
	needSh(t)
	const confPath = "/mnt/SDCARD/App/MiniScale/miniscale.conf"
	const sock = "[ -S /tmp/miniscale/tailscaled.sock ]"
	if !strings.Contains(tailnetProxy, confPath) || !strings.Contains(tailnetProxy, sock) {
		t.Fatal("the proxy snippet changed: update this test")
	}
	bin, mark := fakeBin(t)
	for _, tc := range []struct{ conf, want string }{
		{"SOCKS_PORT=1055\nHTTP_PROXY_PORT=2056\n", "2056"},
		{"HTTP_PROXY_PORT=\"3056\"\n", "3056"},
		{"HTTP_PROXY_PORT='4056' # mine\n", "4056"},
		{"  HTTP_PROXY_PORT=1057\nHTTP_PROXY_PORT=1058\n", "1058"},
		{"#HTTP_PROXY_PORT=9\n", "1056"},
		{"HTTP_PROXY_PORT=99999\n", "1056"},
		{"HTTP_PROXY_PORT=$(reboot)\n", "1056"},
		{"HTTP_PROXY_PORT=`reboot`\nreboot\n", "1056"},
		{"", "1056"},
	} {
		conf := filepath.Join(t.TempDir(), "miniscale.conf")
		if tc.conf != "" {
			os.WriteFile(conf, []byte(tc.conf), 0644)
		}
		script := strings.NewReplacer(confPath, conf, sock, "true").Replace(tailnetProxy) + "echo \"$https_proxy\"\n"
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := cmd.Output()
		if got := strings.TrimSpace(string(out)); err != nil || got != "http://127.0.0.1:"+tc.want {
			t.Errorf("conf %q: proxy %q (%v), want port %s", tc.conf, got, err, tc.want)
		}
	}
	if _, err := os.Stat(mark); err == nil {
		t.Error("reading miniscale.conf ran a command")
	}
}

// A folder that can't be created is an error with context, not a failure
// further on (or none).
func TestMkdirErrorsReturned(t *testing.T) {
	sd := t.TempDir()
	t.Setenv("STORE_SDCARD", sd)
	os.WriteFile(filepath.Join(sd, ".tmp_update"), []byte("a file, not a folder"), 0644)
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	e, _ := w.Create(".tmp_update/startup/x.sh")
	e.Write([]byte("#!/bin/sh\n"))
	w.Close()
	zp := filepath.Join(sd, "a.zip")
	os.WriteFile(zp, buf.Bytes(), 0644)
	if err := installBoot(zp, []string{"x.sh"}); err == nil || !strings.Contains(err.Error(), "startup folder") {
		t.Errorf("installBoot: %v", err)
	}

	a := App{Name: "Thing", Folder: "Thing", Secure: "s.txt"}
	os.MkdirAll(a.dir(), 0755)
	os.WriteFile(filepath.Join(a.dir(), "config.json"), []byte(`{"launch": "launch.sh"}`), 0644)
	if err := secure(a); err == nil || !strings.Contains(err.Error(), "startup folder") {
		t.Errorf("secure: %v", err)
	}

	out := filepath.Join(sd, "App")
	os.WriteFile(filepath.Join(out, "Foo"), []byte("a file, not a folder"), 0644)
	zp2 := filepath.Join(sd, "b.zip")
	os.WriteFile(zp2, makeZip(t, map[string]string{"Foo/data/": "", "Foo/config.json": "{}"}), 0644)
	if err := extractApp(zp2, out, "", "Foo", false); err == nil || !strings.Contains(err.Error(), "unpacking Foo/") {
		t.Errorf("extractApp: %v", err)
	}
}
