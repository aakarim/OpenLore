package analytics

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

type remoteSource struct {
	store  RemoteEventStore
	prefix string
}

// NewRemoteSource adapts shipped immutable JSONL ranges and sealed JSONL/zstd
// segments to EventSource.
func NewRemoteSource(store RemoteEventStore, prefixes ...string) EventSource {
	prefix := "events"
	if len(prefixes) > 0 {
		prefix = prefixes[0]
	}
	return &remoteSource{store: store, prefix: strings.Trim(prefix, "/")}
}

func RemoteSource(store RemoteEventStore) EventSource { return NewRemoteSource(store) }

func (s *remoteSource) Scan(ctx context.Context, filter EventFilter, fn func(Event) error) error {
	keys, err := s.store.List(ctx, s.prefix)
	if err != nil {
		return err
	}
	sort.Strings(keys)
	byDay := map[string][]string{}
	var days []string
	for _, key := range keys {
		if !(strings.HasSuffix(key, ".jsonl") || strings.HasSuffix(key, ".jsonl.zst")) {
			continue
		}
		day := path.Dir(key)
		if _, exists := byDay[day]; !exists {
			days = append(days, day)
		}
		byDay[day] = append(byDay[day], key)
	}
	sort.Strings(days)
	for _, day := range days {
		dayKeys := byDay[day]
		var sealed []string
		for _, key := range dayKeys {
			if strings.HasSuffix(key, ".jsonl.zst") {
				sealed = append(sealed, key)
			}
		}
		if len(sealed) > 0 {
			dayKeys = sealed
		}
		seen := map[string]struct{}{}
		for _, key := range dayKeys {
			if err := s.scanObject(ctx, key, filter, seen, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *remoteSource) scanObject(ctx context.Context, key string, filter EventFilter, seen map[string]struct{}, fn func(Event) error) error {
	r, err := s.store.Get(ctx, key)
	if err != nil {
		return err
	}
	var input io.Reader = r
	var dec *zstd.Decoder
	if strings.HasSuffix(key, ".zst") {
		dec, err = zstd.NewReader(r)
		if err != nil {
			r.Close()
			return fmt.Errorf("decode %s: %w", key, err)
		}
		input = dec
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			if dec != nil {
				dec.Close()
			}
			_ = r.Close()
			return err
		}
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			if dec != nil {
				dec.Close()
			}
			r.Close()
			return fmt.Errorf("decode %s: %w", key, err)
		}
		identity := event.ID
		if identity == "" {
			identity = string(scanner.Bytes())
		}
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		if eventMatches(event, filter) {
			if err := fn(event); err != nil {
				if dec != nil {
					dec.Close()
				}
				_ = r.Close()
				return err
			}
		}
	}
	scanErr := scanner.Err()
	if dec != nil {
		dec.Close()
	}
	closeErr := r.Close()
	if scanErr != nil {
		return scanErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func eventMatches(e Event, f EventFilter) bool {
	if !f.From.IsZero() && e.Time.Before(f.From) || !f.To.IsZero() && e.Time.After(f.To) {
		return false
	}
	if len(f.Types) > 0 {
		found := false
		for _, v := range f.Types {
			if e.Type == v {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(f.Principals) > 0 {
		found := false
		for _, v := range f.Principals {
			if e.Principal == v {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
