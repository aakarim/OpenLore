// lore is the client for an OpenLore knowledge repository.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/internal/lore"
	"github.com/aakarim/go-openlore/internal/lore/api"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "lore:", err)
		os.Exit(1)
	}
}

// client is the command context: the repository, its server client (which
// holds the credentials) and where output and prompts go.
type client struct {
	repo   lore.Repository
	api    *api.Client
	out    io.Writer
	prompt prompter
}

func newClient(repo lore.Repository, out io.Writer) *client {
	c := &client{repo: repo, out: out}
	c.api = &api.Client{APIPath: repo.Config.APIPath, OnRefresh: func(cred lore.Credentials) error {
		return lore.SaveCredentials(repo.Root, cred)
	}}
	return c
}
func (c *client) identity() string { return c.api.Credentials.Identity }
func (c *client) loadCredentials() error {
	cred, err := lore.LoadCredentials(c.repo.Root)
	if err != nil {
		return err
	}
	c.api.Credentials = cred
	return nil
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lore init [directory] | login | source add/list/rm | status | sync")
	}
	if args[0] == "init" {
		return initRepository(args[1:], out)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := lore.FindRoot(cwd)
	if err != nil {
		return err
	}
	cfg, err := config.New(config.WithConfigFile(filepath.Join(root, "openlore.yml")))
	if err != nil {
		return err
	}
	c := newClient(lore.Repository{Root: root, Config: cfg}, out)
	if args[0] == "login" {
		return c.login(args[1:])
	}
	if args[0] == "agent" && len(args) > 1 && args[1] == "connect" {
		return c.connectAgent(args[2:])
	}
	if args[0] == "agent" && len(args) > 1 && args[1] == "import" {
		if len(args) == 2 {
			fmt.Fprintln(out, "Available profiles: claude")
			return nil
		}
		if args[2] != "claude" {
			return fmt.Errorf("unknown agent profile %q (available: claude)", args[2])
		}
		return c.addSource(append([]string{"claude"}, args[3:]...))
	}
	if args[0] == "source" && len(args) > 1 {
		switch args[1] {
		case "add":
			return c.addSource(args[2:])
		case "rm":
			return c.removeSource(args[2:])
		case "list":
			return c.listSources()
		}
	}
	if args[0] == "status" || args[0] == "sync" {
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		dry := flags.Bool("dry-run", false, "show changes without uploading")
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		if err := c.loadCredentials(); err != nil {
			return err
		}
		return c.syncSources(flags.Args(), *dry || args[0] == "status")
	}
	return fmt.Errorf("unknown command %q", strings.Join(args, " "))
}

// initRepository deliberately bypasses FindRoot: init targets the requested
// directory, never an enclosing repository or the last login's default.
func initRepository(args []string, out io.Writer) error {
	f := flag.NewFlagSet("init", flag.ContinueOnError)
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() > 1 {
		return fmt.Errorf("usage: lore init [directory]")
	}
	dir := f.Arg(0)
	if dir == "" {
		dir = "."
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	file := filepath.Join(root, "openlore.yml")
	fh, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("creating openlore.yml (existing files are not overwritten): %w", err)
	}
	_, writeErr := io.WriteString(fh, `# OpenLore server settings. Omitted settings use the built-in defaults.
# Access policy (identities, roles and docsets) belongs in lore.json.
version: "1"

# SSH and HTTP listeners. MCP and the JSON API share the HTTP port.
port: 2222
http_port: 8080
metrics_port: 3000

# The server is read-only by default.
readonly: true

mcp:
  enabled: true
  path: /mcp
api:
  enabled: true
  path: /api
`)
	closeErr := fh.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	fmt.Fprintf(out, "Created %s\n", file)
	fmt.Fprintln(out, "Knowledge repository ready. Add your documents, then run openlore ./docs from this directory to serve them.")
	return nil
}

// flag.FlagSet stops at the first positional; the CLI also accepts flags after
// the source path, without changing the repository's standard flag package.
func parseFlags(f *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
		if strings.Contains(a, "=") {
			continue
		}
		fl := f.Lookup(name)
		if fl == nil {
			return fmt.Errorf("unknown flag %s", a)
		}
		if b, ok := fl.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		i++
		if i >= len(args) {
			return fmt.Errorf("missing value for %s", a)
		}
		flags = append(flags, args[i])
	}
	return f.Parse(append(flags, positional...))
}

type globs []string

func (g *globs) String() string     { return strings.Join(*g, ",") }
func (g *globs) Set(s string) error { *g = append(*g, s); return nil }

// sourcesFile locates the caller's committed source definitions. They live
// under the login identity's home, so every source command needs lore login.
func (c *client) sourcesFile() (string, error) {
	if c.identity() == "" {
		if err := c.loadCredentials(); err != nil {
			return "", err
		}
		if c.identity() == "" {
			return "", fmt.Errorf("run lore login first: saved credentials have no identity")
		}
	}
	return c.repo.SourcesFile(c.identity()), nil
}
func (c *client) addSource(args []string) error {
	f := flag.NewFlagSet("source add", flag.ContinueOnError)
	name := f.String("name", "", "source name")
	dry := f.Bool("dry-run", false, "preview only")
	branch := f.String("branch", "", "git branch (default: remote HEAD)")
	project := f.Bool("project", false, "Claude: offer project-scope groups")
	personal := f.Bool("personal", false, "Claude: offer personal-scope groups")
	var include, exclude globs
	f.Var(&include, "include", "include glob (repeatable)")
	f.Var(&exclude, "exclude", "exclude glob (repeatable)")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.Arg(0) == "claude" {
		if f.NArg() > 2 {
			return fmt.Errorf("usage: lore source add claude [path] [--project] [--personal] [--name name] [--include glob] [--exclude glob] [--dry-run]")
		}
		var scopes []string
		if *project {
			scopes = append(scopes, "project")
		}
		if *personal {
			scopes = append(scopes, "personal")
		}
		return c.addClaude(f.Arg(1), *name, scopes, include, exclude, *dry)
	}
	if f.NArg() != 1 {
		return fmt.Errorf("usage: lore source add <path> [--name name] [--include glob] [--exclude glob]")
	}
	if *project || *personal {
		return fmt.Errorf("--project and --personal apply only to Claude")
	}
	file, err := c.sourcesFile()
	if err != nil {
		return err
	}
	var spec config.SourceSpec
	origin := f.Arg(0)
	if lore.IsGitOrigin(origin) {
		spec, err = lore.GitSource(origin, *name, *branch, include, exclude)
	} else {
		if *branch != "" {
			return fmt.Errorf("--branch applies only to git")
		}
		spec, origin, err = c.repo.FolderSource(origin, *name, include, exclude)
	}
	if err != nil {
		return err
	}
	doc, err := lore.LoadSourceDocument(file)
	if err != nil {
		return err
	}
	if err := doc.Append(spec); err != nil {
		return err
	}
	if err := doc.Validate(); err != nil {
		return err
	}
	store, err := c.repo.StorePath(spec, c.identity())
	if err != nil {
		return err
	}
	if spec.Type == "folder" {
		files, _, err := c.repo.ScanFolder(spec, store, false)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.out, "%s · folder · %s · %d files → %s\n", spec.Name, origin, len(files), store)
	} else {
		fmt.Fprintf(c.out, "%s · git · %s → %s\n", spec.Name, origin, store)
	}
	if *dry {
		return nil
	}
	return doc.Save(file)
}
func (c *client) removeSource(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: lore source rm <name>")
	}
	file, err := c.sourcesFile()
	if err != nil {
		return err
	}
	doc, err := lore.LoadSourceDocument(file)
	if err != nil {
		return err
	}
	spec, err := doc.Remove(args[0])
	if err != nil {
		return err
	}
	store, err := c.repo.StorePath(spec, c.identity())
	if err != nil {
		return err
	}
	if err := doc.Save(file); err != nil {
		return err
	}
	return c.repo.ForgetSource(lore.StateKey(c.api.Credentials), args[0], store)
}
func (c *client) listSources() error {
	file, err := c.sourcesFile()
	if err != nil {
		return err
	}
	sources, err := config.LoadSources(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	state, err := c.repo.LoadState()
	if err != nil {
		return err
	}
	projections, err := c.repo.Projections()
	if err != nil {
		return err
	}
	for _, s := range sources.Sources {
		store, err := c.repo.StorePath(s, c.identity())
		if err != nil {
			return err
		}
		fmt.Fprintf(c.out, "%s · %s · %s · %s · %s · %s\n", s.Name, s.Type, lore.Origin(s), store, strings.Join(projections[store], ","), state.Identities[lore.StateKey(c.api.Credentials)].Snapshots[s.Name])
	}
	return nil
}

// addClaude offers the profile groups whose scope is in scopes (all groups
// when scopes is empty) and connects the chosen ones as synced sources.
func (c *client) addClaude(project, name string, scopes, include, exclude []string, dry bool) error {
	fmt.Fprintln(c.out, "Connecting a synced Claude source, not a one-off import.")
	file, err := c.sourcesFile()
	if err != nil {
		return err
	}
	offers, err := c.repo.ClaudeOffers(c.identity(), project, name, scopes, include, exclude)
	if err != nil {
		return err
	}
	options := []string{}
	preselected := []int{}
	for i, o := range offers {
		if o.Preselected {
			preselected = append(preselected, i)
		}
		options = append(options, o.Label)
	}
	chosen, err := c.ui().multiSelect("Choose groups to sync", options, preselected)
	if err != nil {
		return err
	}
	if len(chosen) == 0 {
		fmt.Fprintln(c.out, "No sources connected.")
		return nil
	}
	doc, err := lore.LoadSourceDocument(file)
	if err != nil {
		return err
	}
	specs, err := lore.ClaudeSelection(offers, chosen)
	if err != nil {
		return err
	}
	for _, s := range specs {
		preview, err := c.repo.Preview(s, c.identity())
		if err != nil {
			return err
		}
		fmt.Fprintf(c.out, "\n%s → %s (%s)\n", s.Name, preview.Store, lore.FileCount(len(preview.Files)))
		for _, p := range preview.Files {
			fmt.Fprintln(c.out, "    "+p)
		}
		if err := doc.Append(s); err != nil {
			return err
		}
	}
	// Separate the listing from the Confirm prompt below.
	fmt.Fprintln(c.out)
	if dry {
		return nil
	}
	ok, err := c.ui().confirm("Write these synced source definitions to sources.yaml?")
	if err != nil || !ok {
		return err
	}
	return doc.Save(file)
}

func (c *client) syncSources(names []string, dry bool) error {
	file, err := c.sourcesFile()
	if err != nil {
		return err
	}
	sources, err := config.LoadSources(file)
	if err != nil {
		return err
	}
	selected, err := lore.SelectSources(sources, names)
	if err != nil {
		return err
	}
	state, err := c.repo.LoadState()
	if err != nil {
		return err
	}
	key := lore.StateKey(c.api.Credentials)
	local := state.Identities[key]
	if local.Snapshots == nil {
		local.Snapshots = map[string]string{}
	}
	if !dry && !local.Acknowledged {
		fmt.Fprintf(c.out, "Syncing as %s to %s. Identity-scoped sources:\n", c.identity(), c.api.Credentials.Server)
		for _, s := range selected {
			store, err := c.repo.StorePath(s, c.identity())
			if err != nil {
				return err
			}
			fmt.Fprintf(c.out, "  %s → %s\n", s.Name, store)
		}
		ok, err := c.ui().confirm("Push these sources under this identity?")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("sync cancelled")
		}
		local.Acknowledged = true
	}
	var failures []error
	for _, s := range selected {
		store, err := c.repo.StorePath(s, c.identity())
		if err != nil {
			return err
		}
		res, err := c.repo.SyncSource(c.api, s, store, dry, &local)
		switch {
		case err != nil:
			fmt.Fprintf(c.out, "%s: %v\n", s.Name, err)
			failures = append(failures, err)
		case res.Unchanged:
			fmt.Fprintf(c.out, "%s: %d files · 0 new blobs · snapshot %s (unchanged)\n", s.Name, res.Files, res.Snapshot)
		case dry:
			fmt.Fprintf(c.out, "%s: %d files · +%d ~%d -%d · %d blobs to upload\n", s.Name, res.Files, res.Added, res.Changed, res.Removed, res.Missing)
		default:
			fmt.Fprintf(c.out, "%s: %d files · %d new blobs · +%d ~%d -%d · snapshot %s\n", s.Name, res.Files, res.Missing, res.Added, res.Changed, res.Removed, res.Snapshot)
		}
		if !dry {
			state.Identities[key] = local
			if err := c.repo.SaveState(state); err != nil {
				return err
			}
		}
	}
	return errors.Join(failures...)
}

func (c *client) connectAgent(args []string) error {
	if len(args) != 1 || (args[0] != "claude" && args[0] != "codex") {
		return fmt.Errorf("usage: lore agent connect claude|codex")
	}
	if err := c.loadCredentials(); err != nil {
		return err
	}
	snippet, err := c.api.ShellSnippet("agents")
	if err != nil {
		return err
	}
	if snippet.IsError || strings.TrimSpace(snippet.Stdout) == "" {
		return fmt.Errorf("server agents snippet unavailable: %s", snippet.Stderr)
	}
	mcpURL := c.api.Credentials.Server + "/" + strings.Trim(c.repo.Config.MCPPath, "/")
	argv, file, err := lore.MCPRegistration(args[0], mcpURL)
	if err != nil {
		return err
	}
	cmd := exec.Command(args[0], argv...)
	cmd.Dir, cmd.Stdout, cmd.Stderr, cmd.Stdin = c.repo.Root, c.out, c.out, os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("registering MCP server with %s: %w", args[0], err)
	}
	file = filepath.Join(c.repo.Root, file)
	if err := lore.UpdateInstructions(file, snippet.Stdout); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Connected %s to %s; added server instructions to %s.\n", args[0], mcpURL, filepath.Base(file))
	fmt.Fprintln(c.out, "MCP authentication is managed by your agent; lore login credentials are not copied to its configuration.")
	ok, err := c.ui().confirm("Connect Claude instruction or memory sources too (lore agent import claude " + c.repo.Root + ")?")
	if err != nil || !ok {
		return err
	}
	// connect wired Claude up in the knowledge repository itself, so that is
	// the project whose instructions and memory are offered.
	return c.addSource([]string{"claude", c.repo.Root})
}

func (c *client) login(args []string) error {
	f := flag.NewFlagSet("login", flag.ContinueOnError)
	token := f.String("token", "", "headless access token")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() > 1 {
		return fmt.Errorf("usage: lore login [server-url] [--token token]")
	}
	server := c.repo.Config.ExternalURL
	if f.NArg() == 1 {
		server = f.Arg(0)
	}
	server, err := lore.ServerURL(server)
	if err != nil {
		return err
	}
	c.api.Credentials = lore.Credentials{Server: server, Token: *token}
	if *token == "" {
		if err := lore.OAuthLogin(c.api, c.openURL); err != nil {
			return err
		}
	}
	identity, err := c.api.Identity()
	if err != nil {
		return err
	}
	c.api.Credentials.Identity = identity
	if err := lore.SaveCredentials(c.repo.Root, c.api.Credentials); err != nil {
		return err
	}
	// The last login's repository is where lore looks when run from elsewhere,
	// such as a Claude project directory.
	if err := lore.SaveDefaultRoot(c.repo.Root); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Logged in as %s to %s\n", identity, server)
	return nil
}

// openURL shows the login link and asks the desktop to open it.
func (c *client) openURL(link string) {
	fmt.Fprintln(c.out, "Open this URL to log in:", link)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	_ = cmd.Run()
}
