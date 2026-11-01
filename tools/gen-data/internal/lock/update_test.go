// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

package lock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/redhat-ux/mcp/tools/gen-data/internal/fetch"
)

func TestDocsTagsFromElements(t *testing.T) {
	got := DocsTagsFromElements([]string{
		"1.0.0", "1.4.5",
		"2.1.2",
		"3.2.0",
		"4.2.2",
		"5.0.0", "5.1.0",
		"6.0.0", "6.1.0",
		"7.0.0-beta.1",
	})
	want := []string{"v1.4.5", "v2.1.2", "v3.2.0", "v4.2.2", "v5.1.0", "v6.1.0"}
	if len(got) != len(want) {
		t.Fatalf("DocsTagsFromElements() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DocsTagsFromElements() = %v, want %v", got, want)
		}
	}
}

func TestUpdatePinsStableVersionsAndDocs(t *testing.T) {
	const commit = "d2321d5a1feb5b677682305dee2c073422eda67c"
	const submodule = "3e53486f83eba02fdecdb67390d1c4704bda5817"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/@rhds/elements", "/%40rhds%2Felements":
			_, _ = w.Write([]byte(`{"name":"@rhds/elements","versions":{
				"5.0.0":{"dist":{"integrity":"sha512-YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYQ==","tarball":"https://example.test/e.tgz","shasum":"def"}},
				"6.0.0":{"dist":{"integrity":"sha512-YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYg==","tarball":"https://example.test/e6.tgz","shasum":"new"}},
				"6.1.0-beta.1":{"dist":{"integrity":"sha512-Y2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjYw==","tarball":"https://example.test/b.tgz"}}
			}}`))
		case "/@rhds/tokens", "/%40rhds%2Ftokens":
			_, _ = w.Write([]byte(`{"name":"@rhds/tokens","versions":{
				"3.2.0":{"dist":{"integrity":"sha512-ZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZGRkZA==","tarball":"https://example.test/t.tgz","shasum":"ghi"}}
			}}`))
		case "/repos/" + DocsRepo + "/commits/v1.4.5",
			"/repos/" + DocsRepo + "/commits/v2.1.2",
			"/repos/" + DocsRepo + "/commits/v3.2.0",
			"/repos/" + DocsRepo + "/commits/v4.2.2",
			"/repos/" + DocsRepo + "/commits/v5.0.0",
			"/repos/" + DocsRepo + "/commits/v6.0.0":
			_, _ = w.Write([]byte(`{"sha":"` + commit + `"}`))
		case "/repos/" + DocsRepo + "/contents/" + AIGuidelinesPath:
			_, _ = w.Write([]byte(`{"type":"file","sha":"` + submodule + `"}`))
		case "/" + DocsRepo + "/" + commit + "/.gitmodules":
			_, _ = w.Write([]byte("[submodule \"docs/ai-guidelines\"]\n\tpath = docs/ai-guidelines\n\turl = https://github.com/project-felt/ai-guidelines.git\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := fetch.New()
	client.RegistryBase = server.URL
	client.APIBase = server.URL
	client.RawBase = server.URL
	client.Retries = 1

	updated, err := Update(context.Background(), client)
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	elements, ok := updated.PackageByID("elements")
	if !ok {
		t.Fatal("no elements package")
	}
	if len(elements.Versions) != 2 || elements.Versions[0].Version != "5.0.0" ||
		elements.Versions[1].Version != "6.0.0" {
		t.Fatalf("elements versions = %+v, want stable 5.0.0 and 6.0.0", elements.Versions)
	}
	if elements.Versions[0].Integrity != "sha512-YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYQ==" {
		t.Errorf("integrity = %q", elements.Versions[0].Integrity)
	}
	tokens, ok := updated.PackageByID("tokens")
	if !ok || len(tokens.Versions) != 1 {
		t.Fatalf("tokens = %+v", tokens)
	}
	if len(updated.Docs.Snapshots) != 2 {
		t.Fatalf("snapshots = %d, want one per elements major", len(updated.Docs.Snapshots))
	}
	if got := updated.Docs.Snapshots[0].Tag; got != "v5.0.0" {
		t.Errorf("first docs tag = %q, want v5.0.0", got)
	}
	if got := updated.Docs.Snapshots[1].Tag; got != "v6.0.0" {
		t.Errorf("second docs tag = %q, want v6.0.0 (derived from the new major)", got)
	}
	if updated.Docs.Snapshots[0].AIGuidelinesCommit != submodule {
		t.Errorf("submodule commit = %q", updated.Docs.Snapshots[0].AIGuidelinesCommit)
	}
	if updated.Docs.AIGuidelines.Repo != "project-felt/ai-guidelines" {
		t.Errorf("ai guidelines repo = %q", updated.Docs.AIGuidelines.Repo)
	}
}
