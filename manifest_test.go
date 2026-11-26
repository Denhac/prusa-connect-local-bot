package main

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/manifest"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
	"gopkg.in/yaml.v3"

	"github.com/denhac/prusa-connect-local-bot/internal/app"
	"github.com/denhac/prusa-connect-local-bot/internal/secret"
	"github.com/denhac/prusa-connect-local-bot/internal/settings"
)

// readManifest parses module.yaml the way the server does: the SDK's parser
// over gopkg.in/yaml.v3, the decoder the server itself hands it.
func readManifest(t *testing.T) manifest.Manifest {
	t.Helper()

	doc, err := os.ReadFile("module.yaml")
	if err != nil {
		t.Fatal(err)
	}

	held, err := manifest.Parse("module.yaml", doc, yaml.Unmarshal)
	if err != nil {
		t.Fatalf("the server would refuse module.yaml: %v", err)
	}

	return held
}

func TestManifestMatchesTheCode(t *testing.T) {
	m := readManifest(t)

	checks := map[string][2]string{
		"name":           {m.Name, app.Name},
		"version":        {m.Version, string(app.Version)},
		"license":        {m.License, "MIT"},
		"openapi":        {m.OpenAPI, "openapi.json"},
		"kind":           {m.Kind, "sink"},
		"contract_major": {strings.Repeat("1", m.ContractMajor), "1"},
	}
	for key, pair := range checks {
		if pair[0] != pair[1] {
			t.Errorf("module.yaml %s is %q, and the code says %q", key, pair[0], pair[1])
		}
	}

	for _, platform := range []string{"linux_arm64", "linux_amd64"} {
		if !slices.Contains(m.Platforms, platform) {
			t.Errorf("module.yaml platforms leaves out %s, which the Makefile builds", platform)
		}
	}
}

// TestOptionsMatchSettings holds the options in module.yaml and the fields of
// settings.Sent to the same keys and types. The host refuses a start whose
// settings carry a key the struct does not read, and the struct would silently
// miss a key the manifest added.
func TestOptionsMatchSettings(t *testing.T) {
	declared := map[string]string{}

	for _, o := range readManifest(t).Options {
		declared[o.Key] = string(o.Type)

		if o.Required {
			t.Errorf("option %s is required. A required option left empty makes the server refuse "+
				"the install for good, so every option is optional and the module reports what is missing", o.Key)
		}
	}

	read := map[string]string{}
	sent := reflect.TypeFor[settings.Sent]()

	for field := range sent.Fields() {
		key, _, _ := strings.Cut(field.Tag.Get("json"), ",")

		switch field.Type {
		case reflect.TypeFor[secret.Secret]():
			read[key] = "secret"
		case reflect.TypeFor[bool]():
			read[key] = "bool"
		case reflect.TypeFor[string]():
			read[key] = "string"
		default:
			read[key] = field.Type.String()
		}
	}

	for key, kind := range declared {
		got, ok := read[key]

		switch {
		case !ok:
			t.Errorf("module.yaml declares %s and settings.Sent does not read it", key)
		case kind == "url" || kind == "duration":
			if got != "string" {
				t.Errorf("%s is a %s option and settings.Sent reads it as %s", key, kind, got)
			}
		case got != kind:
			t.Errorf("%s is a %s option and settings.Sent reads it as %s", key, kind, got)
		}
	}

	for key := range read {
		if _, ok := declared[key]; !ok {
			t.Errorf("settings.Sent reads %s and module.yaml does not declare it", key)
		}
	}
}

// TestRoutesAreDescribed checks openapi.json the way the host checks it at
// launch, and that it carries the module's version.
func TestRoutesAreDescribed(t *testing.T) {
	if _, err := routesapi.Describe(wirerpc.Info{Name: app.Name, Version: app.Version, Contract: wirerpc.Contract}, openAPI); err != nil {
		t.Fatalf("the host would refuse openapi.json: %v", err)
	}

	var doc struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.Unmarshal(openAPI, &doc); err != nil {
		t.Fatal(err)
	}

	if doc.Info.Version != string(app.Version) {
		t.Errorf("openapi.json is version %q and the module is %q", doc.Info.Version, app.Version)
	}
}
