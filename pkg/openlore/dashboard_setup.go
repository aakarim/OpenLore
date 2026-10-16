package openlore

import (
	"errors"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// dashboardMaxSetupDirs bounds the directory walk that discovers
// .lore/config.yaml files. The response says when it stopped early so the UI
// never presents a partial listing as the complete configuration.
const dashboardMaxSetupDirs = 10000

var errDashboardSetupFull = errors.New("folder listing limit reached")

type dashboardSetupServer struct {
	SSH       string `json:"ssh"`
	HTTPURL   string `json:"http_url"`
	MCPURL    string `json:"mcp_url,omitempty"`
	LorePath  string `json:"lore_path"`
	Readonly  bool   `json:"readonly"`
	Skills    bool   `json:"skills"`
	Analytics bool   `json:"analytics"`
	History   bool   `json:"history"`
}

type dashboardSetupDocset struct {
	Name     string   `json:"name"`
	Roots    []string `json:"roots"`
	Aliases  []string `json:"aliases"`
	Grants   []string `json:"grants"`
	Writable bool     `json:"writable"`
	// Readonly is the docset's own setting; the global write lock is
	// reported once as server.readonly.
	Readonly    bool     `json:"readonly"`
	Home        bool     `json:"home"`
	Inbox       string   `json:"inbox,omitempty"`
	AgentSkills bool     `json:"agent_skills"`
	OKF         bool     `json:"okf"`
	ConfigFiles []string `json:"config_files"`
	// Operator-only fields, present with lore:access:view.
	Roles      []dashboardRole           `json:"roles,omitempty"`
	Rules      map[string]rules.RuleSpec `json:"rules,omitempty"`
	ConfigEdit []string                  `json:"config_edit,omitempty"`
}

type dashboardConfigFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"` // "auth" for lore.json, "server" for openlore.yml, "folder" for .lore/config.yaml
	Scope  string `json:"scope"`
	Docset string `json:"docset,omitempty"`
}

type dashboardSetupResponse struct {
	Identity    string                    `json:"identity"`
	Roles       []string                  `json:"roles"`
	Home        string                    `json:"home,omitempty"`
	Access      bool                      `json:"access"`
	ConfigEdit  bool                      `json:"config_edit"`
	Server      dashboardSetupServer      `json:"server"`
	Docsets     []dashboardSetupDocset    `json:"docsets"`
	ConfigFiles []dashboardConfigFile     `json:"config_files"`
	Truncated   bool                      `json:"truncated"`
	Rules       map[string]rules.RuleSpec `json:"rules,omitempty"`
	Notes       []string                  `json:"notes"`
}

// dashboardSetup describes how the caller's workspace is put together: the
// docsets their session can reach (the same view as `lore docsets`), every
// readable `.lore/config.yaml`, the live lore.json when their session may edit
// it, and how clients connect. Role grants and lore.json rule layers are
// operator detail and need lore:access:view, like the Access tab.
func (s *Server) dashboardSetup(w http.ResponseWriter, r *http.Request) {
	id, scoped, _ := s.dashboardPath(r)
	access := s.hasCurrentCapability(id, CapabilityAccessView)
	auth := s.currentAuth()

	docsets := map[string]*dashboardSetupDocset{}
	for _, row := range s.sessionDocsets(id) {
		item := docsets[row.Name]
		if item == nil {
			ds := auth.Docsets[row.Name]
			item = &dashboardSetupDocset{
				Name: row.Name, Roots: []string{}, Aliases: []string{}, Grants: row.Grants,
				Writable: row.Writable, Readonly: ds.Readonly != nil && *ds.Readonly,
				Inbox: inboxPath(ds), AgentSkills: row.AgentSkills, OKF: ds.OKF != nil, ConfigFiles: []string{},
			}
			if access {
				item.Roles = dashboardRoles(ds.Access.Allow, ds.Access.Deny)
				item.Rules = ds.Rules
				if ds.Config != nil {
					item.ConfigEdit = ds.Config.Edit
				}
			}
			docsets[row.Name] = item
		}
		item.Home = item.Home || row.Home
		if row.AliasTarget != "" {
			item.Aliases = append(item.Aliases, row.Paths...)
		} else {
			item.Roots = append(item.Roots, row.Paths...)
		}
	}

	files := make([]dashboardConfigFile, 0)
	if authConfigVisible(scoped) {
		files = append(files, dashboardConfigFile{Path: authConfigVFSPath, Kind: "auth", Scope: "/"})
		if info, err := scoped.Stat(serverConfigVFSPath); err == nil && !info.Dir {
			files = append(files, dashboardConfigFile{Path: serverConfigVFSPath, Kind: "server", Scope: "/"})
		}
	}
	folders, walkErr := s.dashboardFolderConfigs(r, scoped)
	for _, dir := range folders {
		file := dashboardConfigFile{Path: path.Join(dir, ".lore/config.yaml"), Kind: "folder", Scope: dir}
		if name, _, ok := s.mostSpecificDocset(dir); ok {
			file.Docset = name
			if item := docsets[name]; item != nil {
				item.ConfigFiles = append(item.ConfigFiles, file.Path)
			}
		}
		files = append(files, file)
	}

	list := make([]dashboardSetupDocset, 0, len(docsets))
	for _, item := range docsets {
		sort.Strings(item.Roots)
		sort.Strings(item.Aliases)
		list = append(list, *item)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	notes := []string{
		"Folder rules in .lore/config.yaml apply to their folder and everything beneath it; lore.json rules apply server-wide or per docset.",
		"This dashboard is read-only. Edit lore.json and folder rules over SSH, then run `lore config reload`; openlore.yml is read at startup, so edit it on the host and restart the server.",
	}
	if walkErr != nil {
		notes = append(notes, "Only the first 10,000 folders were scanned for .lore/config.yaml files; this list may be incomplete.")
	}
	if access {
		notes = append(notes, "Roles are configured by the operator. Denials override grants; delegated restrictions and token scopes may further narrow access.")
	}
	response := dashboardSetupResponse{
		Identity: id.IdentityName, Roles: []string{}, Access: access, ConfigEdit: authConfigVisible(scoped),
		Server: dashboardSetupServer{
			SSH: "ssh " + s.config.SSHTarget(), HTTPURL: s.config.HTTPBaseURL(), LorePath: s.dashboardLorePath(),
			Readonly: s.config.Readonly, Skills: s.config.Plugins.Skills.Enabled, Analytics: s.analytics != nil, History: s.history != nil,
		},
		Docsets: list, ConfigFiles: files, Truncated: walkErr != nil, Notes: notes,
	}
	if s.config.MCPEnabled && s.config.HTTPPort != 0 {
		response.Server.MCPURL = s.config.HTTPBaseURL() + "/" + strings.TrimLeft(s.config.MCPPath, "/")
	}
	if id.policySnapshot != nil {
		response.Roles = append(response.Roles, id.policySnapshot.Roles...)
		response.Home = id.policySnapshot.HomeDocset
	}
	if access {
		response.Rules = auth.Rules
	}
	dashboardJSON(w, response)
}

// dashboardFolderConfigs walks readable folders (not files) and returns, in
// path order, every folder that carries a readable .lore/config.yaml. The
// session filesystem exposes `.lore` only when its config.yaml exists, so a
// listed `.lore` entry is the file. A folder that cannot be listed is skipped
// rather than failing the whole view.
func (s *Server) dashboardFolderConfigs(r *http.Request, scoped vfs.FileSystem) ([]string, error) {
	var folders []string
	visited := 0
	var walk func(dir string) error
	walk = func(dir string) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		visited++
		if visited > dashboardMaxSetupDirs {
			return errDashboardSetupFull
		}
		entries, err := scoped.ReadDir(dir)
		if err != nil {
			return nil
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].FileName < entries[j].FileName })
		for _, entry := range entries {
			if !entry.Dir {
				continue
			}
			child := path.Join(dir, entry.FileName)
			if entry.FileName == ".lore" {
				if info, err := scoped.Stat(path.Join(child, "config.yaml")); err == nil && !info.Dir {
					folders = append(folders, dir)
				}
				continue
			}
			if child == "/opt" && authConfigVisible(scoped) {
				continue
			}
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	err := walk("/")
	sort.Strings(folders)
	return folders, err
}

func dashboardRoles(allow map[string]string, deny []string) []dashboardRole {
	denied := map[string]bool{}
	for _, role := range deny {
		denied[role] = true
	}
	roles := make([]dashboardRole, 0, len(allow)+len(deny))
	for role, grant := range allow {
		roles = append(roles, dashboardRole{Role: role, Grant: grant, Denied: denied[role]})
		delete(denied, role)
	}
	for role := range denied {
		roles = append(roles, dashboardRole{Role: role, Denied: true})
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Role < roles[j].Role })
	return roles
}
