package assets

import (
	"io/fs"
	"strings"
	"testing"
)

func TestWebConnectionExamplesUseCurrentHostAndAdvertisedSSHPort(t *testing.T) {
	index, err := fs.ReadFile(Web(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)

	if strings.Contains(html, "openlore.sh") {
		t.Fatal("web connection examples must not hard-code the production hostname")
	}
	if count := strings.Count(html, "data-server-host"); count != 5 {
		t.Fatalf("data-server-host occurrences: got %d, want 5", count)
	}
	if count := strings.Count(html, "data-ssh-port"); count != 5 {
		t.Fatalf("data-ssh-port occurrences: got %d, want 5", count)
	}
	for _, script := range []string{
		"var host = location.hostname;",
		"element.textContent = host;",
		"var portFlag = sshPort !== '22' ? ' -p ' + sshPort : '';",
		"element.textContent = portFlag;",
	} {
		if !strings.Contains(html, script) {
			t.Fatalf("index.html does not contain %q", script)
		}
	}
}
