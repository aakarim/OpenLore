package skillsremote

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateFilesRejectsUnsafeAndConflictingTrees(t *testing.T) {
	for _, files := range []map[string][]byte{
		{"../SKILL.md": nil},
		{"a": nil, "a/b": nil},
		{"a\\b": nil},
	} {
		if err := ValidateFiles(files); err == nil {
			t.Fatalf("accepted invalid tree: %#v", files)
		}
	}
	if err := ValidateFiles(map[string][]byte{"SKILL.md": nil, "a/b": nil}); err != nil {
		t.Fatalf("valid tree rejected: %v", err)
	}
}

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func TestResolveAndFetchFakeGitHub(t *testing.T) {
	sha := strings.Repeat("a", 40)
	mux := http.NewServeMux()
	mux.HandleFunc("/owner/repo/info/refs", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, pkt("# service=git-upload-pack\n")+"0000"+pkt(sha+" HEAD\x00symref=HEAD:refs/heads/main\n")+pkt(sha+" refs/heads/main\n")+"0000")
	})
	mux.HandleFunc("/owner/repo/tar.gz/"+sha, func(w http.ResponseWriter, r *http.Request) {
		gz := gzip.NewWriter(w)
		tr := tar.NewWriter(gz)
		for _, name := range []string{"repo-x/", "repo-x/skills/", "repo-x/skills/pdf/"} {
			_ = tr.WriteHeader(&tar.Header{Name: name, Mode: 0755, Typeflag: tar.TypeDir})
		}
		for name, content := range map[string]string{"repo-x/skills/pdf/SKILL.md": "---\nname: pdf\ndescription: PDFs\n---\n", "repo-x/skills/pdf/readme.txt": "ok", "repo-x/skills/pdf/.lore/secret": "no"} {
			_ = tr.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg})
			_, _ = tr.Write([]byte(content))
		}
		_ = tr.Close()
		_ = gz.Close()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := Client{HTTP: srv.Client(), GitHubBase: srv.URL, CodeloadBase: srv.URL}
	refs, err := c.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	got, kind, ref, err := refs.Resolve("")
	if err != nil || got != sha || kind != "tracking" || ref != "main" {
		t.Fatalf("%s %s %s %v", got, kind, ref, err)
	}
	files, err := c.Fetch(context.Background(), "owner/repo", sha, "skills/pdf")
	if err != nil {
		t.Fatal(err)
	}
	if string(files["readme.txt"]) != "ok" || files[".lore/secret"] != nil {
		t.Fatalf("files=%v", files)
	}
}

func TestParseSpecs(t *testing.T) {
	for _, raw := range []string{"owner/repo/skills/pdf@main", "https://github.com/owner/repo/tree/main/skills/pdf"} {
		s, err := ParseSpec(raw)
		if err != nil || s.Repo != "owner/repo" || s.Path != "skills/pdf" || s.Ref != "main" {
			t.Fatalf("%q: %+v %v", raw, s, err)
		}
	}
}

func TestFetchRejectsDuplicateEntries(t *testing.T) {
	sha := strings.Repeat("b", 40)
	mux := http.NewServeMux()
	mux.HandleFunc("/owner/repo/tar.gz/"+sha, func(w http.ResponseWriter, _ *http.Request) {
		gz := gzip.NewWriter(w)
		tr := tar.NewWriter(gz)
		for _, name := range []string{"repo-x/SKILL.md", "repo-x/docs/.lore/secret", "repo-x/duplicate", "repo-x/duplicate"} {
			content := "x"
			if name == "repo-x/SKILL.md" {
				content = "---\nname: repo\ndescription: test\n---\n"
			}
			_ = tr.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg})
			_, _ = tr.Write([]byte(content))
		}
		_ = tr.Close()
		_ = gz.Close()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := Client{HTTP: srv.Client(), CodeloadBase: srv.URL, MaxFiles: 10}
	files, err := c.Fetch(context.Background(), "owner/repo", sha, "")
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate archive path accepted: files=%v err=%v", files, err)
	}
}

func TestFetchBoundsSkippedArchiveContent(t *testing.T) {
	sha := strings.Repeat("c", 40)
	mux := http.NewServeMux()
	mux.HandleFunc("/owner/repo/tar.gz/"+sha, func(w http.ResponseWriter, _ *http.Request) {
		gz := gzip.NewWriter(w)
		archive := tar.NewWriter(gz)
		content := strings.Repeat("x", 64)
		_ = archive.WriteHeader(&tar.Header{Name: "repo-x/unrelated/large", Size: int64(len(content)), Typeflag: tar.TypeReg})
		_, _ = archive.Write([]byte(content))
		_ = archive.Close()
		_ = gz.Close()
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := Client{HTTP: server.Client(), CodeloadBase: server.URL, MaxArchiveBytes: 32}
	if _, err := client.Fetch(context.Background(), "owner/repo", sha, "skills/pdf"); err == nil || !strings.Contains(err.Error(), "decompressed size") {
		t.Fatalf("skipped archive content was not bounded: %v", err)
	}
}

func TestFetchValidatesRootSkillAndConflictingTrees(t *testing.T) {
	sha := strings.Repeat("d", 40)
	mux := http.NewServeMux()
	mux.HandleFunc("/owner/repo/tar.gz/"+sha, func(w http.ResponseWriter, _ *http.Request) {
		gz := gzip.NewWriter(w)
		archive := tar.NewWriter(gz)
		content := "---\nname: ../escape\ndescription: bad\n---\n"
		_ = archive.WriteHeader(&tar.Header{Name: "repo-x/SKILL.md", Size: int64(len(content)), Typeflag: tar.TypeReg})
		_, _ = archive.Write([]byte(content))
		_ = archive.Close()
		_ = gz.Close()
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := Client{HTTP: server.Client(), CodeloadBase: server.URL}
	if _, err := client.Fetch(context.Background(), "owner/repo", sha, ""); err == nil {
		t.Fatal("invalid root-level skill accepted")
	}
	if err := ValidateFiles(map[string][]byte{"assets": nil, "assets/icon.md": nil}); err == nil {
		t.Fatal("file/directory conflict accepted")
	}
}

func TestFetchRejectsNonCanonicalArchivePath(t *testing.T) {
	sha := strings.Repeat("e", 40)
	mux := http.NewServeMux()
	mux.HandleFunc("/owner/repo/tar.gz/"+sha, func(w http.ResponseWriter, _ *http.Request) {
		gz := gzip.NewWriter(w)
		archive := tar.NewWriter(gz)
		content := "bad"
		_ = archive.WriteHeader(&tar.Header{Name: "repo-x/.lore/../SKILL.md", Size: int64(len(content)), Typeflag: tar.TypeReg})
		_, _ = archive.Write([]byte(content))
		_ = archive.Close()
		_ = gz.Close()
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := Client{HTTP: server.Client(), CodeloadBase: server.URL}
	if _, err := client.Fetch(context.Background(), "owner/repo", sha, ""); err == nil || !strings.Contains(err.Error(), "unsafe archive path") {
		t.Fatalf("non-canonical path accepted: %v", err)
	}
}

func TestFetchBoundsPAXMetadataDecompression(t *testing.T) {
	sha := strings.Repeat("f", 40)
	mux := http.NewServeMux()
	mux.HandleFunc("/owner/repo/tar.gz/"+sha, func(w http.ResponseWriter, _ *http.Request) {
		gz := gzip.NewWriter(w)
		archive := tar.NewWriter(gz)
		_ = archive.WriteHeader(&tar.Header{Name: "repo-x/file", Size: 0, Typeflag: tar.TypeReg, PAXRecords: map[string]string{"comment": strings.Repeat("x", 4096)}})
		_ = archive.Close()
		_ = gz.Close()
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := Client{HTTP: server.Client(), CodeloadBase: server.URL, MaxArchiveBytes: 1024}
	if _, err := client.Fetch(context.Background(), "owner/repo", sha, "missing"); err == nil || !strings.Contains(err.Error(), "decompressed size") {
		t.Fatalf("PAX metadata exceeded no decompression bound: %v", err)
	}
}
