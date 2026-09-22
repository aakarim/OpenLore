package analytics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type IndexedFacts struct {
	Path        string
	Owner       string
	Size        int64
	MTimeNS     int64
	ContentHash string
	ComputedAt  time.Time
	Generation  int64
	Sources     map[string]map[string]float64
}

type FactsIndex interface {
	Lookup(context.Context, string, int64, int64, []string) (IndexedFacts, bool, error)
	Upsert(context.Context, IndexedFacts) error
	Delete(context.Context, string) error
	DeleteIfOlder(context.Context, string, int64) error
	PrefixScan(context.Context, string, ...int) ([]IndexedFacts, error)
	Prune(context.Context, map[string]struct{}) error
	StartScan(context.Context, []KnowledgeScope) (int64, error)
	ScanState(context.Context) (FactsScanState, error)
	NextScanPaths(context.Context, int64, int) ([]string, error)
	QueueScanPath(context.Context, int64, string) error
	CompleteScanPath(context.Context, int64, string, []string, bool) error
	FailScan(context.Context, int64, error) error
	FinishScan(context.Context, int64) (bool, error)
	PrefixScanOwners(context.Context, string, []string, int) ([]IndexedFacts, error)
	PrefixScanOwnersAfter(context.Context, string, []string, string, int) ([]IndexedFacts, error)
	FactsForPaths(context.Context, []string) ([]IndexedFacts, error)
	DirectoryTotals(context.Context, string, []string) (directoryTotals, time.Time, error)
	Close() error
}

type FactsScanState struct {
	Generation          int64
	State               string
	StartedAt           time.Time
	CompletedAt         time.Time
	Error               string
	OwnershipCompatible bool
	Processed           int64
	Skipped             int64
}

type sqliteFactsIndex struct{ db *sql.DB }

func newSQLiteFactsIndex(store *SQLiteAggregationStore) FactsIndex {
	return &sqliteFactsIndex{db: store.db}
}

func (x *sqliteFactsIndex) Lookup(ctx context.Context, p string, size, mtimeNS int64, sources []string) (IndexedFacts, bool, error) {
	p = vfs.CleanPath(p)
	fact := IndexedFacts{Path: p, Sources: map[string]map[string]float64{}}
	var computed int64
	err := x.db.QueryRowContext(ctx, `SELECT owner,size,mtime_ns,content_hash,computed_at,generation FROM files WHERE path=?`, p).Scan(&fact.Owner, &fact.Size, &fact.MTimeNS, &fact.ContentHash, &computed, &fact.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return fact, false, nil
	}
	if err != nil {
		return fact, false, err
	}
	fact.ComputedAt = time.Unix(0, computed).UTC()
	if fact.Size != size || fact.MTimeNS != mtimeNS {
		return fact, false, nil
	}
	rows, err := x.db.QueryContext(ctx, `SELECT source,scalar,value FROM file_scalars WHERE path=?`, p)
	if err != nil {
		return fact, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var source, scalar string
		var value float64
		if err := rows.Scan(&source, &scalar, &value); err != nil {
			return fact, false, err
		}
		if fact.Sources[source] == nil {
			fact.Sources[source] = map[string]float64{}
		}
		fact.Sources[source][scalar] = value
	}
	if err := rows.Err(); err != nil {
		return fact, false, err
	}
	for _, source := range sources {
		if len(fact.Sources[source]) == 0 {
			return fact, false, nil
		}
	}
	return fact, true, nil
}

func (x *sqliteFactsIndex) Upsert(ctx context.Context, fact IndexedFacts) error {
	fact.Path = vfs.CleanPath(fact.Path)
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldOwner string
	var oldTotals directoryTotals
	err = tx.QueryRowContext(ctx, `SELECT owner,size FROM files WHERE path=?`, fact.Path).Scan(&oldOwner, &oldTotals.bytes)
	hadOld := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if hadOld {
		if err = loadDirectoryScalars(ctx, tx, fact.Path, &oldTotals); err != nil {
			return err
		}
	}
	if fact.ComputedAt.IsZero() {
		fact.ComputedAt = time.Now().UTC()
	}
	if fact.Generation == 0 {
		_ = tx.QueryRowContext(ctx, `SELECT generation FROM facts_scan_state WHERE id=1`).Scan(&fact.Generation)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO files(path,owner,size,mtime_ns,content_hash,computed_at,generation) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(path) DO UPDATE SET owner=excluded.owner,size=excluded.size,mtime_ns=excluded.mtime_ns,content_hash=excluded.content_hash,computed_at=excluded.computed_at,generation=excluded.generation`, fact.Path, fact.Owner, fact.Size, fact.MTimeNS, fact.ContentHash, fact.ComputedAt.UnixNano(), fact.Generation)
	if err != nil {
		return err
	}
	// file_scalars is the currently compatible fact projection. Replacing it
	// avoids mixing values from old tokenizers/providers in durable totals.
	if hadOld {
		if _, err = tx.ExecContext(ctx, `DELETE FROM file_scalars WHERE path=?`, fact.Path); err != nil {
			return err
		}
	}
	for source, scalars := range fact.Sources {
		for scalar, value := range scalars {
			if _, err = tx.ExecContext(ctx, `INSERT INTO file_scalars(path,source,scalar,value) VALUES(?,?,?,?)
ON CONFLICT(path,source,scalar) DO UPDATE SET value=excluded.value`, fact.Path, source, scalar, value); err != nil {
				return err
			}
		}
	}
	if hadOld && oldOwner != "" {
		if err = applyDirectoryDelta(ctx, tx, fact.Path, oldOwner, oldTotals, -1); err != nil {
			return err
		}
	}
	if fact.Owner != "" {
		if err = applyDirectoryDelta(ctx, tx, fact.Path, fact.Owner, totalsForFact(fact), 1); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM directory_facts WHERE files<=0`); err != nil {
		return err
	}
	return tx.Commit()
}

func (x *sqliteFactsIndex) Delete(ctx context.Context, p string) error {
	return x.delete(ctx, p, 0)
}

func (x *sqliteFactsIndex) DeleteIfOlder(ctx context.Context, p string, generation int64) error {
	return x.delete(ctx, p, generation)
}

func (x *sqliteFactsIndex) delete(ctx context.Context, p string, olderThan int64) error {
	p = vfs.CleanPath(p)
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	var totals directoryTotals
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT owner,size,generation FROM files WHERE path=?`, p).Scan(&owner, &totals.bytes, &generation); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if olderThan > 0 && generation >= olderThan {
		return nil
	}
	if err = loadDirectoryScalars(ctx, tx, p, &totals); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM files WHERE path=?`, p); err != nil {
		return err
	}
	if owner != "" {
		if err = applyDirectoryDelta(ctx, tx, p, owner, totals, -1); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM directory_facts WHERE files<=0`); err != nil {
		return err
	}
	return tx.Commit()
}

func (x *sqliteFactsIndex) PrefixScan(ctx context.Context, prefix string, limits ...int) ([]IndexedFacts, error) {
	prefix = vfs.CleanPath(prefix)
	start := prefix
	if prefix != "/" {
		start += "/"
	} else {
		start = "/"
	}
	end := start + "\U0010ffff"
	limit := int(^uint(0) >> 1)
	if len(limits) > 0 {
		limit = limits[0]
	}
	if limit <= 0 {
		return nil, fmt.Errorf("facts scan limit must be positive")
	}
	rows, err := x.db.QueryContext(ctx, `SELECT path,owner,size,mtime_ns,content_hash,computed_at,generation FROM files WHERE path=? OR (path>=? AND path<?) ORDER BY path LIMIT ?`, prefix, start, end, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facts []IndexedFacts
	for rows.Next() {
		var fact IndexedFacts
		var computed int64
		if err := rows.Scan(&fact.Path, &fact.Owner, &fact.Size, &fact.MTimeNS, &fact.ContentHash, &computed, &fact.Generation); err != nil {
			return nil, err
		}
		fact.ComputedAt = time.Unix(0, computed).UTC()
		fact.Sources = map[string]map[string]float64{}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range facts {
		scalars, err := x.db.QueryContext(ctx, `SELECT source,scalar,value FROM file_scalars WHERE path=?`, facts[i].Path)
		if err != nil {
			return nil, err
		}
		for scalars.Next() {
			var source, scalar string
			var value float64
			if err := scalars.Scan(&source, &scalar, &value); err != nil {
				scalars.Close()
				return nil, err
			}
			if facts[i].Sources[source] == nil {
				facts[i].Sources[source] = map[string]float64{}
			}
			facts[i].Sources[source][scalar] = value
		}
		if err := scalars.Err(); err != nil {
			scalars.Close()
			return nil, err
		}
		if err := scalars.Close(); err != nil {
			return nil, err
		}
	}
	return facts, nil
}

type directoryTotals struct {
	files                            int64
	bytes, lines, characters, tokens float64
}

func loadDirectoryScalars(ctx context.Context, tx *sql.Tx, filePath string, totals *directoryTotals) error {
	return tx.QueryRowContext(ctx, `SELECT
COALESCE(SUM(CASE WHEN scalar='lines' THEN value ELSE 0 END),0),
COALESCE(SUM(CASE WHEN scalar='characters' THEN value ELSE 0 END),0),
COALESCE(SUM(CASE WHEN scalar='tokens' THEN value ELSE 0 END),0)
FROM file_scalars WHERE path=?`, filePath).Scan(&totals.lines, &totals.characters, &totals.tokens)
}

func totalsForFact(fact IndexedFacts) directoryTotals {
	totals := directoryTotals{bytes: float64(fact.Size)}
	for _, values := range fact.Sources {
		totals.lines += values["lines"]
		totals.characters += values["characters"]
		totals.tokens += values["tokens"]
	}
	return totals
}

func applyDirectoryDelta(ctx context.Context, tx *sql.Tx, filePath, owner string, totals directoryTotals, sign float64) error {
	for dir := path.Dir(vfs.CleanPath(filePath)); ; dir = path.Dir(dir) {
		_, err := tx.ExecContext(ctx, `INSERT INTO directory_facts(path,owner,files,bytes,lines,characters,tokens,computed_at)
VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(path,owner) DO UPDATE SET
files=directory_facts.files+excluded.files,bytes=directory_facts.bytes+excluded.bytes,
lines=directory_facts.lines+excluded.lines,characters=directory_facts.characters+excluded.characters,
tokens=directory_facts.tokens+excluded.tokens,computed_at=excluded.computed_at`,
			dir, owner, int(sign), sign*totals.bytes, sign*totals.lines, sign*totals.characters, sign*totals.tokens, time.Now().UTC().UnixNano())
		if err != nil {
			return err
		}
		if dir == "/" {
			return nil
		}
	}
}

func (x *sqliteFactsIndex) Prune(ctx context.Context, seen map[string]struct{}) error {
	rows, err := x.db.QueryContext(ctx, `SELECT path FROM files`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		if _, ok := seen[p]; !ok {
			stale = append(stale, p)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, p := range stale {
		if err := x.Delete(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func (x *sqliteFactsIndex) StartScan(ctx context.Context, scopes []KnowledgeScope) (int64, error) {
	clean := append([]KnowledgeScope(nil), scopes...)
	sort.Slice(clean, func(i, j int) bool {
		if clean[i].Root == clean[j].Root {
			return clean[i].Name < clean[j].Name
		}
		return clean[i].Root < clean[j].Root
	})
	encoded, _ := json.Marshal(clean)
	hash := sha256.Sum256(encoded)
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	scopeHash := hex.EncodeToString(hash[:])
	var generation int64
	var existingState, existingHash string
	stateErr := tx.QueryRowContext(ctx, `SELECT generation,state,scope_hash FROM facts_scan_state WHERE id=1`).Scan(&generation, &existingState, &existingHash)
	if stateErr == nil && existingHash == scopeHash && (existingState == "updating" || existingState == "failed") {
		if _, err := tx.ExecContext(ctx, `UPDATE facts_scan_state SET state='updating',error='' WHERE id=1`); err != nil {
			return 0, err
		}
		return generation, tx.Commit()
	}
	if stateErr != nil && !errors.Is(stateErr, sql.ErrNoRows) {
		return 0, stateErr
	}
	generation++
	now := time.Now().UTC().UnixNano()
	if _, err = tx.ExecContext(ctx, `INSERT INTO facts_scan_state(id,generation,state,started_at,completed_at,error,scope_hash,completed_scope_hash)
VALUES(1,?,'updating',?,0,'',?,'') ON CONFLICT(id) DO UPDATE SET generation=excluded.generation,state='updating',started_at=excluded.started_at,error='',scope_hash=excluded.scope_hash,processed=0,skipped=0`, generation, now, scopeHash); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM facts_scan_queue`); err != nil {
		return 0, err
	}
	seen := map[string]struct{}{}
	for _, scope := range clean {
		root := vfs.CleanPath(scope.Root)
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		if _, err = tx.ExecContext(ctx, `INSERT INTO facts_scan_queue(generation,path) VALUES(?,?)`, generation, root); err != nil {
			return 0, err
		}
	}
	return generation, tx.Commit()
}

func (x *sqliteFactsIndex) ScanState(ctx context.Context) (FactsScanState, error) {
	var state FactsScanState
	var started, completed int64
	var compatible int
	err := x.db.QueryRowContext(ctx, `SELECT generation,state,started_at,completed_at,error,scope_hash=completed_scope_hash,processed,skipped FROM facts_scan_state WHERE id=1`).Scan(&state.Generation, &state.State, &started, &completed, &state.Error, &compatible, &state.Processed, &state.Skipped)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.StartedAt = time.Unix(0, started).UTC()
	if completed > 0 {
		state.CompletedAt = time.Unix(0, completed).UTC()
	}
	state.OwnershipCompatible = compatible != 0
	return state, nil
}

func (x *sqliteFactsIndex) NextScanPaths(ctx context.Context, generation int64, limit int) ([]string, error) {
	rows, err := x.db.QueryContext(ctx, `SELECT path FROM facts_scan_queue WHERE generation=? ORDER BY path LIMIT ?`, generation, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

func (x *sqliteFactsIndex) QueueScanPath(ctx context.Context, generation int64, p string) error {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE facts_scan_state SET state='updating',error='' WHERE id=1 AND generation=?`, generation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO facts_scan_queue(generation,path) VALUES(?,?)`, generation, vfs.CleanPath(p)); err != nil {
		return err
	}
	return tx.Commit()
}

func (x *sqliteFactsIndex) CompleteScanPath(ctx context.Context, generation int64, p string, children []string, skipped bool) error {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM facts_scan_queue WHERE generation=? AND path=?`, generation, vfs.CleanPath(p))
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE facts_scan_state SET processed=processed+1,skipped=skipped+? WHERE id=1 AND generation=?`, skipped, generation); err != nil {
		return err
	}
	for _, child := range children {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO facts_scan_queue(generation,path) VALUES(?,?)`, generation, vfs.CleanPath(child)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (x *sqliteFactsIndex) FailScan(ctx context.Context, generation int64, cause error) error {
	_, err := x.db.ExecContext(ctx, `UPDATE facts_scan_state SET state='failed',error=? WHERE id=1 AND generation=?`, cause.Error(), generation)
	return err
}

func (x *sqliteFactsIndex) FinishScan(ctx context.Context, generation int64) (bool, error) {
	var current, queued int64
	if err := x.db.QueryRowContext(ctx, `SELECT generation FROM facts_scan_state WHERE id=1`).Scan(&current); err != nil || current != generation {
		return false, err
	}
	if err := x.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM facts_scan_queue WHERE generation=?`, generation).Scan(&queued); err != nil || queued != 0 {
		return false, err
	}
	rows, err := x.db.QueryContext(ctx, `SELECT path FROM files WHERE owner<>'' AND generation<? ORDER BY path LIMIT ?`, generation, factsBatchSize)
	if err != nil {
		return false, err
	}
	var stale []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return false, err
		}
		stale = append(stale, p)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, p := range stale {
		if err := x.DeleteIfOlder(ctx, p, generation); err != nil {
			return false, err
		}
	}
	if len(stale) == factsBatchSize {
		return false, nil
	}
	result, err := x.db.ExecContext(ctx, `UPDATE facts_scan_state SET state='ready',completed_at=?,error='',completed_scope_hash=scope_hash WHERE id=1 AND generation=? AND NOT EXISTS(SELECT 1 FROM facts_scan_queue WHERE generation=?)`, time.Now().UTC().UnixNano(), generation, generation)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	return changed == 1, nil
}

func (x *sqliteFactsIndex) PrefixScanOwners(ctx context.Context, prefix string, owners []string, limit int) ([]IndexedFacts, error) {
	return x.PrefixScanOwnersAfter(ctx, prefix, owners, "", limit)
}

func (x *sqliteFactsIndex) PrefixScanOwnersAfter(ctx context.Context, prefix string, owners []string, after string, limit int) ([]IndexedFacts, error) {
	if len(owners) == 0 {
		return nil, nil
	}
	prefix = vfs.CleanPath(prefix)
	start := prefix
	if prefix != "/" {
		start += "/"
	}
	end := start + "\U0010ffff"
	args := []any{prefix, start, end, after}
	marks := make([]string, len(owners))
	for i, owner := range owners {
		marks[i] = "?"
		args = append(args, owner)
	}
	args = append(args, limit)
	query := `SELECT path,owner,size,mtime_ns,content_hash,computed_at,generation FROM files WHERE (path=? OR (path>=? AND path<?)) AND path>? AND owner IN (` + strings.Join(marks, ",") + `) ORDER BY path LIMIT ?`
	rows, err := x.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facts []IndexedFacts
	for rows.Next() {
		var fact IndexedFacts
		var computed int64
		if err := rows.Scan(&fact.Path, &fact.Owner, &fact.Size, &fact.MTimeNS, &fact.ContentHash, &computed, &fact.Generation); err != nil {
			return nil, err
		}
		fact.ComputedAt = time.Unix(0, computed).UTC()
		fact.Sources = map[string]map[string]float64{}
		facts = append(facts, fact)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range facts {
		rows, err := x.db.QueryContext(ctx, `SELECT source,scalar,value FROM file_scalars WHERE path=?`, facts[i].Path)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var source, scalar string
			var value float64
			if err := rows.Scan(&source, &scalar, &value); err != nil {
				rows.Close()
				return nil, err
			}
			if facts[i].Sources[source] == nil {
				facts[i].Sources[source] = map[string]float64{}
			}
			facts[i].Sources[source][scalar] = value
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return facts, nil
}

func (x *sqliteFactsIndex) FactsForPaths(ctx context.Context, paths []string) ([]IndexedFacts, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	facts := make([]IndexedFacts, 0, len(paths))
	for _, p := range paths {
		var fact IndexedFacts
		var computed int64
		err := x.db.QueryRowContext(ctx, `SELECT path,owner,size,mtime_ns,content_hash,computed_at,generation FROM files WHERE path=?`, p).Scan(&fact.Path, &fact.Owner, &fact.Size, &fact.MTimeNS, &fact.ContentHash, &computed, &fact.Generation)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		fact.ComputedAt = time.Unix(0, computed).UTC()
		fact.Sources = map[string]map[string]float64{}
		rows, err := x.db.QueryContext(ctx, `SELECT source,scalar,value FROM file_scalars WHERE path=?`, p)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var source, scalar string
			var value float64
			if err := rows.Scan(&source, &scalar, &value); err != nil {
				rows.Close()
				return nil, err
			}
			if fact.Sources[source] == nil {
				fact.Sources[source] = map[string]float64{}
			}
			fact.Sources[source][scalar] = value
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

func (x *sqliteFactsIndex) DirectoryTotals(ctx context.Context, prefix string, owners []string) (directoryTotals, time.Time, error) {
	if len(owners) == 0 {
		return directoryTotals{}, time.Time{}, nil
	}
	args := []any{vfs.CleanPath(prefix)}
	marks := make([]string, len(owners))
	for i, owner := range owners {
		marks[i] = "?"
		args = append(args, owner)
	}
	var totals directoryTotals
	var computed sql.NullInt64
	err := x.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(files),0),COALESCE(SUM(bytes),0),COALESCE(SUM(lines),0),COALESCE(SUM(characters),0),COALESCE(SUM(tokens),0),MAX(computed_at) FROM directory_facts WHERE path=? AND owner IN (`+strings.Join(marks, ",")+`)`, args...).Scan(&totals.files, &totals.bytes, &totals.lines, &totals.characters, &totals.tokens, &computed)
	if err != nil {
		return directoryTotals{}, time.Time{}, err
	}
	var at time.Time
	if computed.Valid {
		at = time.Unix(0, computed.Int64).UTC()
	}
	return totals, at, nil
}

func (x *sqliteFactsIndex) Close() error { return nil }

func sourceNames(providers []ContentScalarProvider) []string {
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		name := provider.Name()
		if token, ok := provider.(interface{ tokenizerName() string }); ok {
			name = token.tokenizerName()
		}
		names = append(names, name)
	}
	return names
}

func indexedFacts(p string, info *vfs.FileInfo, content []byte, providers []ContentScalarProvider) IndexedFacts {
	hash := sha256.Sum256(content)
	fact := IndexedFacts{Path: vfs.CleanPath(p), Size: int64(len(content)), MTimeNS: info.ModTime().UnixNano(), ContentHash: hex.EncodeToString(hash[:]), ComputedAt: time.Now().UTC(), Sources: map[string]map[string]float64{}}
	for _, provider := range providers {
		source := provider.Name()
		if token, ok := provider.(interface{ tokenizerName() string }); ok {
			source = token.tokenizerName()
		}
		values := map[string]float64{}
		for scalar, value := range provider.Scalars(p, content) {
			if reservedScalar(scalar) && !builtinScalarProvider(provider, scalar) {
				continue
			}
			values[scalar] = value
		}
		fact.Sources[source] = values
		if _, ok := provider.(sizeProvider); ok {
			values["characters"] = float64(utf8.RuneCount(content))
		}
	}
	return fact
}

func docScalarsFromIndexed(fact IndexedFacts, providers []ContentScalarProvider) (DocScalars, error) {
	doc := DocScalars{Path: fact.Path, ContentHash: fact.ContentHash, Scalars: map[string]float64{}, Tokenizer: tokenizerName(providers), ComputedAt: fact.ComputedAt}
	for _, source := range sourceNames(providers) {
		values, ok := fact.Sources[source]
		if !ok {
			return DocScalars{}, fmt.Errorf("missing scalar source %q", source)
		}
		for scalar, value := range values {
			doc.Scalars[scalar] = value
		}
	}
	return doc, nil
}

func pathWithinPrefix(p, prefix string) bool {
	p, prefix = vfs.CleanPath(p), vfs.CleanPath(prefix)
	return prefix == "/" || p == prefix || strings.HasPrefix(p, prefix+"/")
}
