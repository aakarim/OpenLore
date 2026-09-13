package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
)

func analyticsParams(args []string) (analytics.Params, analytics.RunOptions, error) {
	p := analytics.Params{Since: time.Now().Add(-30 * 24 * time.Hour), Until: time.Now(), Limit: 100, Extra: map[string]string{}}
	opts := analytics.RunOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--fresh":
			opts.Fresh = true
		case "--json":
		case "--limit":
			i++
			if i >= len(args) {
				return p, opts, fmt.Errorf("--limit needs a value")
			}
			if _, err := fmt.Sscan(args[i], &p.Limit); err != nil {
				return p, opts, err
			}
		case "--since":
			i++
			if i >= len(args) {
				return p, opts, fmt.Errorf("--since needs a value")
			}
			d, err := parseAnalyticsDuration(args[i])
			if err != nil {
				return p, opts, err
			}
			p.Since = time.Now().Add(-d)
		case "--until":
			i++
			if i >= len(args) {
				return p, opts, fmt.Errorf("--until needs a value")
			}
			if args[i] != "now" {
				t, err := time.Parse(time.RFC3339, args[i])
				if err != nil {
					return p, opts, err
				}
				p.Until = t
			}
		case "--param":
			i++
			if i >= len(args) {
				return p, opts, fmt.Errorf("--param needs k=v")
			}
			parts := strings.SplitN(args[i], "=", 2)
			if len(parts) != 2 {
				return p, opts, fmt.Errorf("invalid parameter %q", args[i])
			}
			p.Extra[parts[0]] = parts[1]
		}
	}
	return p, opts, nil
}
func parseAnalyticsDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscan(strings.TrimSuffix(s, "d"), &days); err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func CmdAnalytics(ctx CmdContext, args []string, w, errW io.Writer, _ io.Reader) int {
	service := analyticsService(ctx)
	if service == nil {
		fmt.Fprintln(errW, "analytics: experimental analytics is not enabled")
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintln(errW, "usage: analytics list|show|refresh|replay|export|status|ship")
		return 1
	}
	switch args[0] {
	case "list":
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tTITLE\tSTATUS")
		for _, a := range service.Registry().List() {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", a.Name, a.Title, service.Registry().Status(a.Name))
		}
		tw.Flush()
		return 0
	case "show":
		if len(args) < 2 {
			fmt.Fprintln(errW, "analytics show: aggregation name required")
			return 1
		}
		p, opts, err := analyticsParams(args[2:])
		if err != nil {
			fmt.Fprintln(errW, "analytics show:", err)
			return 1
		}
		m, err := service.Registry().Run(context.Background(), args[1], p, opts)
		if err != nil {
			fmt.Fprintln(errW, "analytics show:", err)
			return 1
		}
		jsonOut := false
		for _, a := range args[2:] {
			jsonOut = jsonOut || a == "--json"
		}
		if jsonOut {
			_ = json.NewEncoder(w).Encode(m)
			return 0
		}
		fmt.Fprintln(w, "Experimental analytics")
		fmt.Fprintln(w, "Status:", m.Status)
		if m.Note != "" {
			fmt.Fprintln(w, m.Note)
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, c := range m.Table.Columns {
			fmt.Fprintf(tw, "%s\t", strings.ToUpper(c))
		}
		fmt.Fprintln(tw)
		for _, row := range m.Table.Rows {
			for _, v := range row {
				fmt.Fprintf(tw, "%v\t", v)
			}
			fmt.Fprintln(tw)
		}
		tw.Flush()
		return 0
	case "refresh":
		if err := service.Refresh(context.Background(), args[1:]...); err != nil {
			fmt.Fprintln(errW, "analytics refresh:", err)
			return 1
		}
		fmt.Fprintln(w, "refreshed")
		return 0
	case "replay":
		p, _, err := analyticsParams(args[1:])
		if err != nil || service.Replay(context.Background(), p.Since) != nil {
			fmt.Fprintln(errW, "analytics replay failed")
			return 1
		}
		fmt.Fprintln(w, "replayed")
		return 0
	case "export":
		p, _, err := analyticsParams(args[1:])
		if err != nil {
			fmt.Fprintln(errW, err)
			return 1
		}
		types := []string{}
		for i, a := range args {
			if a == "--type" && i+1 < len(args) {
				types = append(types, args[i+1])
			}
		}
		err = service.EventSource().Scan(context.Background(), analytics.EventFilter{From: p.Since, To: p.Until, Types: types}, func(e analytics.Event) error { return json.NewEncoder(w).Encode(e) })
		if err != nil {
			fmt.Fprintln(errW, err)
			return 1
		}
		return 0
	case "status":
		b, _ := json.MarshalIndent(service.Health(), "", "  ")
		fmt.Fprintln(w, string(b))
		return 0
	case "ship":
		if err := service.ShipNow(context.Background()); err != nil {
			fmt.Fprintln(errW, err)
			return 1
		}
		fmt.Fprintln(w, "shipped")
		return 0
	default:
		names := []string{"list", "show", "refresh", "replay", "export", "status", "ship"}
		sort.Strings(names)
		fmt.Fprintln(errW, "analytics: unknown subcommand", args[0])
		return 1
	}
}

func analyticsService(ctx CmdContext) *analytics.Service {
	provider, _ := ctx.(interface{ Analytics() *analytics.Service })
	if provider == nil {
		return nil
	}
	return provider.Analytics()
}
