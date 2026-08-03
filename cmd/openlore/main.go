package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/assets"
	"github.com/aakarim/go-openlore/internal/config"
	openlore "github.com/aakarim/go-openlore/pkg/openlore"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	gossh "golang.org/x/crypto/ssh"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func loadPolicy(path string) (openlore.AuthConfig, error) {
	var auth openlore.AuthConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return auth, err
	}
	err = json.Unmarshal(b, &auth)
	return auth, err
}
func savePolicy(path string, auth openlore.AuthConfig) error {
	if err := openlore.ValidateAuthConfig(&auth); err != nil {
		return err
	}
	b, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}

func runIdentityCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: identity add|role")
	}
	if args[0] == "add" {
		fs := flag.NewFlagSet("identity add", flag.ContinueOnError)
		name := fs.String("name", "", "")
		key := fs.String("key", "", "")
		comment := fs.String("comment", "", "")
		home := fs.String("home", "", "")
		path := fs.String("auth", "./lore.json", "")
		var roles stringList
		fs.Var(&roles, "role", "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || *name == "guest" {
			return fmt.Errorf("invalid or reserved identity name %q", *name)
		}
		auth, err := loadPolicy(*path)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, role := range roles {
			if seen[role] {
				return fmt.Errorf("duplicate role %q", role)
			}
			seen[role] = true
			if role != "guest" {
				if _, ok := auth.Roles[role]; !ok {
					return fmt.Errorf("unknown role %q", role)
				}
			}
		}
		if *home != "" {
			if _, ok := auth.Docsets[*home]; !ok {
				return fmt.Errorf("unknown home docset %q", *home)
			}
			for _, id := range auth.Identities {
				if id.Home == *home {
					return fmt.Errorf("home docset %q already belongs to %q", *home, id.Name)
				}
			}
		}
		for _, id := range auth.Identities {
			if id.Name == *name {
				return fmt.Errorf("identity %q already exists", *name)
			}
		}
		if *key != "" {
			if _, _, _, _, err := gossh.ParseAuthorizedKey([]byte(*key)); err != nil {
				return fmt.Errorf("invalid SSH public key: %w", err)
			}
		}
		auth.Identities = append(auth.Identities, openlore.AuthIdentity{Name: *name, PublicKey: strings.TrimSpace(*key), Comment: *comment, Home: *home, Roles: roles})
		if err := savePolicy(*path, auth); err != nil {
			return err
		}
		fmt.Fprintf(out, "Added identity %q\n", *name)
		return nil
	}
	if args[0] == "role" && len(args) > 1 {
		fs := flag.NewFlagSet("identity role", flag.ContinueOnError)
		identity := fs.String("identity", "", "")
		role := fs.String("role", "", "")
		path := fs.String("auth", "./lore.json", "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		auth, err := loadPolicy(*path)
		if err != nil {
			return err
		}
		if _, ok := auth.Roles[*role]; !ok {
			return fmt.Errorf("unknown role %q", *role)
		}
		for i := range auth.Identities {
			if auth.Identities[i].Name == *identity {
				if args[1] == "add" {
					for _, r := range auth.Identities[i].Roles {
						if r == *role {
							return savePolicy(*path, auth)
						}
					}
					auth.Identities[i].Roles = append(auth.Identities[i].Roles, *role)
				} else if args[1] == "remove" {
					rs := auth.Identities[i].Roles[:0]
					for _, r := range auth.Identities[i].Roles {
						if r != *role {
							rs = append(rs, r)
						}
					}
					auth.Identities[i].Roles = rs
				} else {
					return fmt.Errorf("usage: identity role add|remove")
				}
				return savePolicy(*path, auth)
			}
		}
		return fmt.Errorf("unknown identity %q", *identity)
	}
	return fmt.Errorf("usage: identity add|role add|remove")
}

func runRoleCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: role add|remove|grant|revoke|deny|undeny|capability")
	}
	fs := flag.NewFlagSet("role "+args[0], flag.ContinueOnError)
	name := fs.String("name", "", "")
	role := fs.String("role", "", "")
	docset := fs.String("docset", "", "")
	grant := fs.String("grant", "", "")
	comment := fs.String("comment", "", "")
	capability := fs.String("capability", "", "")
	effect := fs.String("effect", "", "")
	path := fs.String("auth", "./lore.json", "")
	parseFrom := 1
	capAction := ""
	if args[0] == "capability" {
		if len(args) < 2 {
			return fmt.Errorf("capability action required")
		}
		capAction = args[1]
		parseFrom = 2
	}
	if err := fs.Parse(args[parseFrom:]); err != nil {
		return err
	}
	auth, err := loadPolicy(*path)
	if err != nil {
		return err
	}
	if auth.Roles == nil {
		auth.Roles = map[string]config.RoleSpec{}
	}
	if *role == "" {
		*role = *name
	}
	spec, exists := auth.Roles[*role]
	switch args[0] {
	case "add":
		if *role == "guest" {
			return fmt.Errorf("guest is reserved")
		}
		if *role == "" || exists {
			return fmt.Errorf("invalid or existing role %q", *role)
		}
		auth.Roles[*role] = config.RoleSpec{Comment: *comment}
	case "remove":
		if *role == "guest" {
			return fmt.Errorf("guest is reserved")
		}
		if !exists {
			return fmt.Errorf("unknown role %q", *role)
		}
		for _, id := range auth.Identities {
			for _, r := range id.Roles {
				if r == *role {
					return fmt.Errorf("role %q is referenced by identity %q", *role, id.Name)
				}
			}
		}
		for n, ds := range auth.Docsets {
			if _, ok := ds.Access.Allow[*role]; ok {
				return fmt.Errorf("role %q is referenced by docset %q", *role, n)
			}
			for _, r := range ds.Access.Deny {
				if r == *role {
					return fmt.Errorf("role %q is referenced by docset %q", *role, n)
				}
			}
		}
		delete(auth.Roles, *role)
	case "grant", "revoke", "deny", "undeny":
		if !exists && *role != "guest" {
			return fmt.Errorf("unknown role %q", *role)
		}
		ds, ok := auth.Docsets[*docset]
		if !ok {
			return fmt.Errorf("unknown docset %q", *docset)
		}
		if ds.Access.Allow == nil {
			ds.Access.Allow = map[string]string{}
		}
		if args[0] == "grant" {
			if *grant == "" {
				return fmt.Errorf("grant required")
			}
			ds.Access.Allow[*role] = *grant
		} else if args[0] == "revoke" {
			delete(ds.Access.Allow, *role)
		} else if args[0] == "deny" {
			found := false
			for _, r := range ds.Access.Deny {
				found = found || r == *role
			}
			if !found {
				ds.Access.Deny = append(ds.Access.Deny, *role)
			}
		} else {
			rs := ds.Access.Deny[:0]
			for _, r := range ds.Access.Deny {
				if r != *role {
					rs = append(rs, r)
				}
			}
			ds.Access.Deny = rs
		}
		auth.Docsets[*docset] = ds
	case "capability":
		if *role == "guest" {
			return fmt.Errorf("guest capability mutation is forbidden")
		}
		if !exists || *capability == "" {
			return fmt.Errorf("unknown role or empty capability")
		}
		add := func(xs []string) []string {
			for _, x := range xs {
				if x == *capability {
					return xs
				}
			}
			return append(xs, *capability)
		}
		remove := func(xs []string) []string {
			out := xs[:0]
			for _, x := range xs {
				if x != *capability {
					out = append(out, x)
				}
			}
			return out
		}
		if capAction == "allow" {
			spec.Allow.Capabilities = add(spec.Allow.Capabilities)
		} else if capAction == "deny" {
			spec.Deny.Capabilities = add(spec.Deny.Capabilities)
		} else if capAction == "remove" {
			if *effect == "allow" {
				spec.Allow.Capabilities = remove(spec.Allow.Capabilities)
			} else if *effect == "deny" {
				spec.Deny.Capabilities = remove(spec.Deny.Capabilities)
			} else {
				return fmt.Errorf("effect must be allow or deny")
			}
		} else {
			return fmt.Errorf("invalid capability action")
		}
		auth.Roles[*role] = spec
	default:
		return fmt.Errorf("unknown role command %q", args[0])
	}
	if err := savePolicy(*path, auth); err != nil {
		return err
	}
	fmt.Fprintln(out, "Updated role policy")
	return nil
}

func main() {
	// Handle subcommands before flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("openlore %s\n", assets.Version())
			os.Exit(0)
		case "export":
			exportCmd := flag.NewFlagSet("export", flag.ExitOnError)
			outputDir := exportCmd.String("o", "", "output directory (required)")
			exportCmd.StringVar(outputDir, "output", "", "output directory (required)")
			exportCmd.Usage = func() {
				fmt.Fprintf(os.Stderr, "Usage: openlore export -o <directory>\n\n")
				fmt.Fprintf(os.Stderr, "Export embedded documentation to a local directory.\n\n")
				exportCmd.PrintDefaults()
			}
			exportCmd.Parse(os.Args[2:])

			if *outputDir == "" {
				exportCmd.Usage()
				os.Exit(1)
			}

			loreFS := assets.Lore()
			if loreFS == nil {
				fmt.Fprintln(os.Stderr, "error: no embedded docs found")
				os.Exit(1)
			}

			count := 0
			err := fs.WalkDir(loreFS, ".", func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}

				outPath := filepath.Join(*outputDir, p)

				if d.IsDir() {
					return os.MkdirAll(outPath, 0755)
				}

				data, err := fs.ReadFile(loreFS, p)
				if err != nil {
					return fmt.Errorf("reading %s: %w", p, err)
				}

				if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
					return err
				}

				if err := os.WriteFile(outPath, data, 0644); err != nil {
					return fmt.Errorf("writing %s: %w", outPath, err)
				}

				fmt.Printf("  %s\n", p)
				count++
				return nil
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("\nExported %d files to %s\n", count, *outputDir)
			os.Exit(0)
		case "mcp":
			mcpCmd := flag.NewFlagSet("mcp", flag.ExitOnError)
			mcpAllowed := mcpCmd.String("allowed", "", "comma-separated file patterns (e.g. '*.md,*.txt')")
			mcpIgnore := mcpCmd.String("ignore", "", "comma-separated ignore patterns (e.g. '.git,node_modules')")
			mcpConfig := mcpCmd.String("config", "./openlore.yml", "path to config file")
			mcpCmd.StringVar(mcpConfig, "c", "./openlore.yml", "path to config file (shorthand)")
			mcpCmd.Usage = func() {
				fmt.Fprintf(os.Stderr, "Usage: openlore mcp [flags] [directory]\n\n")
				fmt.Fprintf(os.Stderr, "Run as an MCP server over stdio. Exposes documentation\n")
				fmt.Fprintf(os.Stderr, "via the Model Context Protocol for Claude Desktop, Cowork, etc.\n\n")
				fmt.Fprintf(os.Stderr, "Arguments:\n")
				fmt.Fprintf(os.Stderr, "  directory    Directory to serve (default: embedded docs)\n\n")
				fmt.Fprintf(os.Stderr, "Flags:\n")
				mcpCmd.PrintDefaults()
			}
			mcpCmd.Parse(os.Args[2:])

			// Build filesystem
			var files config.FilesConfig
			if *mcpAllowed != "" {
				files.Allowed = splitAndTrim(*mcpAllowed)
			}
			if *mcpIgnore != "" {
				files.Ignore = splitAndTrim(*mcpIgnore)
			}

			// Try loading config file for file filters
			embeddedCfg, _ := assets.EmbeddedConfig()
			cfgOpts := []config.Option{
				config.WithConfigFile(*mcpConfig),
				config.WithEmbeddedConfig(embeddedCfg, ""),
			}
			var resolvedCfg config.Config
			if cfg, err := config.New(cfgOpts...); err == nil {
				resolvedCfg = cfg
				if len(files.Allowed) == 0 {
					files.Allowed = cfg.Files.Allowed
				}
				if len(files.Ignore) == 0 {
					files.Ignore = cfg.Files.Ignore
				}
			}

			var vfs openlore.FileSystem
			if mcpCmd.NArg() > 0 {
				dir := mcpCmd.Arg(0)
				absDir, err := filepath.Abs(dir)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error: invalid directory %q: %v\n", dir, err)
					os.Exit(1)
				}
				vfs = openlore.NewDirFS(absDir, files)
			} else if loreFS := assets.Lore(); loreFS != nil {
				lower := openlore.NewFSAdapter(loreFS)
				if resolvedCfg.WritableDir != "" {
					upper := openlore.NewDirFS(resolvedCfg.WritableDir, files)
					vfs = openlore.NewOverlayFS(upper, lower)
				} else {
					vfs = lower
				}
			} else {
				fmt.Fprintln(os.Stderr, "error: no directory specified and no embedded docs found")
				mcpCmd.Usage()
				os.Exit(1)
			}

			server := openlore.NewMCPServer(vfs)
			if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
				fmt.Fprintf(os.Stderr, "mcp server error: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)

		case "mcpb":
			mcpbCmd := flag.NewFlagSet("mcpb", flag.ExitOnError)
			output := mcpbCmd.String("o", "", "output .mcpb file path (default: openlore.mcpb)")
			mcpbCmd.StringVar(output, "output", "", "output .mcpb file path")
			name := mcpbCmd.String("name", "openlore", "extension name")
			description := mcpbCmd.String("description", "Access documentation via OpenLore", "extension description")
			docsDir := mcpbCmd.String("docs-dir", "", "directory to embed as docs (optional)")
			mcpbCmd.Usage = func() {
				fmt.Fprintf(os.Stderr, "Usage: openlore mcpb [flags]\n\n")
				fmt.Fprintf(os.Stderr, "Package the current openlore binary as an MCPB desktop extension\n")
				fmt.Fprintf(os.Stderr, "for one-click installation in Claude Desktop.\n\n")
				fmt.Fprintf(os.Stderr, "Flags:\n")
				mcpbCmd.PrintDefaults()
			}
			mcpbCmd.Parse(os.Args[2:])

			if *output == "" {
				*output = "openlore.mcpb"
			}

			buildMCPB(*output, *name, *description, *docsDir)
			os.Exit(0)

		case "identity":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "Usage: openlore identity add|role add|remove")
				os.Exit(1)
			}
			if err := runIdentityCommand(os.Args[2:], os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return

		case "role":
			if err := runRoleCommand(os.Args[2:], os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return

		case "token":
			if len(os.Args) < 3 {
				fmt.Fprintf(os.Stderr, "Usage: openlore token <command>\n\n")
				fmt.Fprintf(os.Stderr, "Commands:\n")
				fmt.Fprintf(os.Stderr, "  mint     Mint an access token for an identity (debug / local PAT)\n")
				fmt.Fprintf(os.Stderr, "  verify   Verify an access token and print its claims\n")
				os.Exit(1)
			}

			switch os.Args[2] {
			case "mint":
				tokCmd := flag.NewFlagSet("token mint", flag.ExitOnError)
				identity := tokCmd.String("identity", "", "identity name = token `sub` (required)")
				scope := tokCmd.String("scope", openlore.ScopeFull, "token scope")
				ttl := tokCmd.Duration("ttl", 30*time.Minute, "access token lifetime")
				configPath := tokCmd.String("config", "./openlore.yml", "path to openlore.yml (holds the tokens block + data dir)")
				tokCmd.Parse(os.Args[3:])

				if *identity == "" {
					tokCmd.Usage()
					os.Exit(1)
				}
				cfg, err := config.New(config.WithConfigFile(*configPath))
				if err != nil {
					fmt.Fprintf(os.Stderr, "error: loading %s: %v\n", *configPath, err)
					os.Exit(1)
				}
				issuer, err := openlore.NewIssuerFromConfig(cfg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
					os.Exit(1)
				}
				token, exp, err := issuer.Mint(*identity, *scope, *ttl)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error: minting token: %v\n", err)
					os.Exit(1)
				}
				fmt.Println(token)
				fmt.Fprintf(os.Stderr, "sub=%s scope=%s expires=%s\n", *identity, *scope, exp.Format(time.RFC3339))
				os.Exit(0)

			case "verify":
				tokCmd := flag.NewFlagSet("token verify", flag.ExitOnError)
				configPath := tokCmd.String("config", "./openlore.yml", "path to openlore.yml (holds the tokens block + data dir)")
				tokCmd.Parse(os.Args[3:])

				args := tokCmd.Args()
				if len(args) != 1 {
					fmt.Fprintf(os.Stderr, "Usage: openlore token verify [flags] <token>\n")
					os.Exit(1)
				}
				cfg, err := config.New(config.WithConfigFile(*configPath))
				if err != nil {
					fmt.Fprintf(os.Stderr, "error: loading %s: %v\n", *configPath, err)
					os.Exit(1)
				}
				issuer, err := openlore.NewIssuerFromConfig(cfg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
					os.Exit(1)
				}
				claims, err := issuer.Verify(args[0])
				if err != nil {
					fmt.Fprintf(os.Stderr, "invalid: %v\n", err)
					os.Exit(1)
				}
				out, _ := json.MarshalIndent(claims.Raw, "", "  ")
				fmt.Println(string(out))
				os.Exit(0)

			default:
				fmt.Fprintf(os.Stderr, "Unknown token command: %s\n", os.Args[2])
				os.Exit(1)
			}

		case "inbox":
			if len(os.Args) < 4 || os.Args[2] != "token" {
				fmt.Fprintln(os.Stderr, "Usage: openlore inbox token <create|list|revoke>")
				os.Exit(1)
			}
			if err := inboxTokenCommand(os.Args[3:], os.Stdout, os.Stderr, time.Now); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}

	port := flag.Int("port", 0, "SSH server port (default 2222)")
	flag.IntVar(port, "p", 0, "SSH server port (shorthand)")
	metricsPort := flag.Int("metrics-port", 0, "Prometheus metrics port (0 to disable, default 3000)")
	hostKey := flag.String("host-key", "", "path to host key file (default .ssh/openlore_ed25519)")
	motd := flag.String("motd", "", "inline MOTD string shown on connect")
	motdFile := flag.String("motd-file", "", "path to MOTD file shown on connect")
	authFile := flag.String("auth", "", "path to auth.json for identity-based access control")
	configFile := flag.String("config", "./openlore.yml", "path to config file")
	flag.StringVar(configFile, "c", "./openlore.yml", "path to config file (shorthand)")
	httpPort := flag.Int("http-port", 0, "HTTP front page port (default 8080, 0 to disable)")
	mcpPath := flag.String("mcp-path", "", "path to mount the MCP-over-HTTP endpoint on the HTTP server (default /mcp)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate file for HTTP server")
	tlsKey := flag.String("tls-key", "", "TLS key file for HTTP server")
	caKeysFile := flag.String("ca-keys", "", "path to trusted CA public keys file for SSH certificate auth")
	hostCertFile := flag.String("host-cert", "", "path to SSH host certificate (signed by CA)")
	skillsDir := flag.String("skills-dir", "", "directory containing runtime skills")
	allowed := flag.String("allowed", "", "comma-separated file patterns to serve (e.g. '*.md,*.txt')")
	ignore := flag.String("ignore", "", "comma-separated patterns to ignore (e.g. '.git,node_modules')")
	readonly := flag.Bool("readonly", true, "global write lock; pass --readonly=false to enable the experimental writable substrate")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: openlore [flags] [directory]\n\n")
		fmt.Fprintf(os.Stderr, "Serve your docs to AI agents over SSH.\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  directory    Directory to serve (default: current directory)\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// Set up structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Determine directory to serve (if provided)
	var rootDir string
	if flag.NArg() > 0 {
		dir := flag.Arg(0)
		absDir, err := filepath.Abs(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid directory %q: %v\n", dir, err)
			os.Exit(1)
		}
		info, err := os.Stat(absDir)
		if err != nil || !info.IsDir() {
			fmt.Fprintf(os.Stderr, "error: %q is not a directory\n", absDir)
			os.Exit(1)
		}
		rootDir = absDir
	}

	// Build config options.
	// 1. Config file (from disk)
	// 2. Embedded config (from assets/config/openlore.yml, if present)
	// 3. CLI flag overrides (always win)
	// Using both a config file and embedded config is an error.
	embeddedCfg, _ := assets.EmbeddedConfig()
	opts := []openlore.Option{
		openlore.WithConfigFile(*configFile),
		openlore.WithEmbeddedConfig(embeddedCfg, assets.DefaultMOTD()),
		openlore.WithLogger(logger),
	}

	if *port != 0 {
		opts = append(opts, openlore.WithPort(*port))
	}
	if isFlagSet("metrics-port") {
		opts = append(opts, openlore.WithMetricsPort(*metricsPort))
	}
	if *hostKey != "" {
		opts = append(opts, openlore.WithHostKeyPath(*hostKey))
	}
	if *motd != "" {
		opts = append(opts, openlore.WithMOTD(*motd))
	}
	if *motdFile != "" {
		opts = append(opts, openlore.WithMOTDFile(*motdFile))
	}
	if *authFile != "" {
		opts = append(opts, openlore.WithAuthFile(*authFile))
	}
	if *allowed != "" {
		opts = append(opts, openlore.WithAllowedPatterns(splitAndTrim(*allowed)))
	}
	if *ignore != "" {
		opts = append(opts, openlore.WithIgnorePatterns(splitAndTrim(*ignore)))
	}
	if isFlagSet("http-port") {
		opts = append(opts, openlore.WithHTTPPort(*httpPort))
	}
	if *mcpPath != "" {
		opts = append(opts, openlore.WithMCPPath(*mcpPath))
	}
	if *tlsCert != "" && *tlsKey != "" {
		opts = append(opts, openlore.WithTLS(*tlsCert, *tlsKey))
	}
	if *caKeysFile != "" {
		opts = append(opts, openlore.WithCAKeysFile(*caKeysFile))
	}
	if *hostCertFile != "" {
		opts = append(opts, openlore.WithHostCertFile(*hostCertFile))
	}
	if *skillsDir != "" {
		opts = append(opts, openlore.WithSkillsDir(*skillsDir))
	}
	if isFlagSet("readonly") {
		opts = append(opts, openlore.WithReadonly(*readonly))
	}

	// Create server. Embedded docs are installed as the lower layer during
	// construction so a configured writable_dir can be the upper layer before
	// the ordered write log is initialized.
	var srv *openlore.Server
	var err error
	if rootDir == "" && assets.Lore() != nil {
		srv, err = openlore.NewServerWithLowerFS(assets.Lore(), opts...)
	} else {
		srv, err = openlore.NewServer(rootDir, opts...)
	}
	if err != nil {
		slog.Error("failed to create server", "error", err)
		os.Exit(1)
	}

	// The inbox plugin contributes the `publish` grant (read whole docset, no
	// deletes, create/edit only within the docset's inbox). Registered by
	// default so lore.json may use `"grant": "publish"`.
	if err := srv.RegisterPlugin(openlore.NewInboxPlugin()); err != nil {
		slog.Error("failed to register inbox plugin", "error", err)
		os.Exit(1)
	}

	cmds.VersionString = assets.Version()

	cfg := srv.Config()

	// Print startup banner
	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────┐")
	fmt.Println("  │        📜  OpenLore  📜              │")
	fmt.Println("  │    Serve your docs to AI agents      │")
	fmt.Println("  └─────────────────────────────────────┘")
	fmt.Println()
	if rootDir != "" {
		fmt.Printf("  Directory:  %s\n", rootDir)
	} else if assets.Lore() != nil {
		fmt.Printf("  Directory:  (embedded docs)\n")
	}
	fmt.Printf("  SSH:        ssh -p %d localhost\n", cfg.Port)
	if cfg.MetricsPort > 0 {
		fmt.Printf("  Metrics:    http://localhost:%d/metrics\n", cfg.MetricsPort)
	}
	if cfg.HTTPPort > 0 {
		fmt.Printf("  HTTP:       http://localhost:%d\n", cfg.HTTPPort)
	}
	if cfg.MCPEnabled && cfg.MCPPath != "" && cfg.HTTPPort > 0 {
		fmt.Printf("  MCP:        http://localhost:%d%s\n", cfg.HTTPPort, "/"+strings.Trim(cfg.MCPPath, "/"))
	}
	fmt.Println()

	slog.Info("starting openlore",
		"port", cfg.Port,
		"metrics_port", cfg.MetricsPort,
		"allow_keyless", cfg.AllowKeyless,
	)

	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

func inboxTokenCommand(args []string, stdout, stderr io.Writer, now func() time.Time) error {
	if len(args) == 0 {
		return fmt.Errorf("missing inbox token command")
	}
	command := args[0]
	flags := flag.NewFlagSet("inbox token "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "./openlore.yml", "path to openlore.yml")
	var identity, label *string
	var ttl *time.Duration
	parseArgs := args[1:]
	switch command {
	case "create":
		identity = flags.String("identity", "", "identity name")
		label = flags.String("label", "", "credential label")
		ttl = flags.Duration("ttl", 0, "credential lifetime (zero means no expiry)")
	case "list":
	case "revoke":
		// The documented TOKEN_ID-before-flags form is normalized for flag.FlagSet,
		// which otherwise stops parsing at the first positional argument.
		if len(parseArgs) > 0 && !strings.HasPrefix(parseArgs[0], "-") {
			parseArgs = append(append([]string{}, parseArgs[1:]...), parseArgs[0])
		}
	default:
		return fmt.Errorf("unknown inbox token command: %s", command)
	}
	if err := flags.Parse(parseArgs); err != nil {
		return err
	}
	if command != "revoke" && flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	cfg, err := config.New(config.WithConfigFile(*configPath))
	if err != nil {
		return err
	}
	store, err := openlore.NewInboxTokenStore(cfg.DataDir)
	if err != nil {
		return err
	}
	switch command {
	case "create":
		if *identity == "" {
			return fmt.Errorf("--identity is required")
		}
		if cfg.AuthFile == "" {
			return fmt.Errorf("auth_file is required to validate the inbox token identity")
		}
		if *ttl < 0 {
			return fmt.Errorf("ttl must not be negative")
		}
		{
			auth, err := config.LoadAuthConfig(cfg.AuthFile)
			if err != nil {
				return err
			}
			found := false
			for _, id := range auth.Identities {
				if id.Name == *identity {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("identity %q not found", *identity)
			}
		}
		var expires *time.Time
		if *ttl > 0 {
			t := now().UTC().Add(*ttl)
			expires = &t
		}
		t, err := store.Create(*identity, *label, expires)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, t.Credential())
		fmt.Fprintf(stderr, "id=%s identity=%s expires=%v\n", t.ID, t.Identity, t.ExpiresAt)
	case "list":
		tokens, err := store.List()
		if err != nil {
			return err
		}
		for _, t := range tokens {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", t.ID, t.Identity, t.Label, expiryText(t.ExpiresAt))
		}
	case "revoke":
		if flags.NArg() != 1 {
			return fmt.Errorf("revoke requires TOKEN_ID")
		}
		ok, err := store.Delete(flags.Arg(0))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("token not found")
		}
	}
	return nil
}

func expiryText(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Format(time.RFC3339)
}

func isFlagSet(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func buildMCPB(output, name, description, docsDir string) {
	// Find the current binary
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot find current executable: %v\n", err)
		os.Exit(1)
	}
	exe, _ = filepath.EvalSymlinks(exe)

	// Determine platform suffix for the binary name
	binaryName := "openlore"
	if runtime.GOOS == "windows" {
		binaryName = "openlore.exe"
	}

	// Build manifest
	mcpConfig := map[string]any{
		"command": "${__dirname}/server/" + binaryName,
		"args":    []string{"mcp"},
	}

	// If no embedded docs, add user_config for docs directory
	hasEmbeddedDocs := assets.Lore() != nil
	userConfig := map[string]any{}
	if !hasEmbeddedDocs && docsDir == "" {
		userConfig["docs_directory"] = map[string]any{
			"type":        "directory",
			"title":       "Documentation Directory",
			"description": "Select the directory containing your documentation files",
			"required":    true,
		}
		mcpConfig["args"] = []string{"mcp", "${user_config.docs_directory}"}
	}

	// Platform mapping
	platform := "darwin"
	switch runtime.GOOS {
	case "windows":
		platform = "win32"
	case "linux":
		platform = "linux"
	}

	manifest := map[string]any{
		"manifest_version": "0.3",
		"name":             name,
		"version":          assets.Version(),
		"description":      description,
		"author": map[string]string{
			"name": "OpenLore",
			"url":  "https://github.com/aakarim/go-openlore",
		},
		"server": map[string]any{
			"type":        "binary",
			"entry_point": "server/" + binaryName,
			"mcp_config":  mcpConfig,
		},
		"tools": []map[string]string{
			{"name": "shell", "description": "Execute bash commands against the documentation filesystem"},
			{"name": "list_commands", "description": "List all available shell commands"},
		},
		"compatibility": map[string]any{
			"platforms": []string{platform},
		},
	}
	if len(userConfig) > 0 {
		manifest["user_config"] = userConfig
	}

	// Create the .mcpb zip archive
	f, err := os.Create(output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: creating %s: %v\n", output, err)
		os.Exit(1)
	}
	defer f.Close()

	w := zip.NewWriter(f)

	// Write manifest.json
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	mw, err := w.Create("manifest.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: writing manifest: %v\n", err)
		os.Exit(1)
	}
	mw.Write(manifestJSON)

	// Copy the binary into server/
	binData, err := os.ReadFile(exe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading binary %s: %v\n", exe, err)
		os.Exit(1)
	}
	bh := &zip.FileHeader{
		Name:   "server/" + binaryName,
		Method: zip.Deflate,
	}
	bh.SetMode(0755)
	bw, err := w.CreateHeader(bh)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: adding binary to archive: %v\n", err)
		os.Exit(1)
	}
	bw.Write(binData)

	// If docs-dir is specified, copy docs into server/assets/lore/ so they
	// get picked up if someone rebuilds. But since this is a binary bundle,
	// the binary already has its own embedded docs (or not).
	if docsDir != "" {
		absDocsDir, _ := filepath.Abs(docsDir)
		filepath.WalkDir(absDocsDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(absDocsDir, p)
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			dw, err := w.Create("docs/" + filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			dw.Write(data)
			return nil
		})
	}

	if err := w.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "error: finalizing archive: %v\n", err)
		os.Exit(1)
	}

	info, _ := os.Stat(output)
	fmt.Printf("Created %s (%.1f MB)\n", output, float64(info.Size())/(1024*1024))
	fmt.Println()
	fmt.Println("Install in Claude Desktop:")
	fmt.Println("  Double-click the .mcpb file, or drag it into Claude Desktop")
	fmt.Println()
	fmt.Printf("  Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if hasEmbeddedDocs {
		fmt.Println("  Docs: embedded in binary")
	} else if docsDir != "" {
		fmt.Printf("  Docs: bundled from %s\n", docsDir)
	} else {
		fmt.Println("  Docs: user will configure directory on install")
	}
}
