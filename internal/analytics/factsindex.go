package analytics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
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
	Sources     map[string]map[string]float64
}

type FactsIndex interface {
	Lookup(context.Context, string, int64, int64, []string) (IndexedFacts, bool, error)
	Upsert(context.Context, IndexedFacts) error
	Delete(context.Context, string) error
	PrefixScan(context.Context, string, ...int) ([]IndexedFacts, error)
	Prune(context.Context, map[string]struct{}) error
	Close() error
}

type sqliteFactsIndex struct{ db *sql.DB }

func newSQLiteFactsIndex(store *SQLiteAggregationStore) FactsIndex {
	return &sqliteFactsIndex{db: store.db}
}

func (x *sqliteFactsIndex) Lookup(ctx context.Context, p string, size, mtimeNS int64, sources []string) (IndexedFacts, bool, error) {
	p = vfs.CleanPath(p)
	fact := IndexedFacts{Path: p, Sources: map[string]map[string]float64{}}
	var computed int64
	err := x.db.QueryRowContext(ctx, `SELECT owner,size,mtime_ns,content_hash,computed_at FROM files WHERE path=?`, p).Scan(&fact.Owner, &fact.Size, &fact.MTimeNS, &fact.ContentHash, &computed)
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
	_, err = tx.ExecContext(ctx, `INSERT INTO files(path,owner,size,mtime_ns,content_hash,computed_at) VALUES(?,?,?,?,?,?)
ON CONFLICT(path) DO UPDATE SET owner=excluded.owner,size=excluded.size,mtime_ns=excluded.mtime_ns,content_hash=excluded.content_hash,computed_at=excluded.computed_at`, fact.Path, fact.Owner, fact.Size, fact.MTimeNS, fact.ContentHash, fact.ComputedAt.UnixNano())
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
	p = vfs.CleanPath(p)
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	var totals directoryTotals
	if err = tx.QueryRowContext(ctx, `SELECT owner,size FROM files WHERE path=?`, p).Scan(&owner, &totals.bytes); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
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
	rows, err := x.db.QueryContext(ctx, `SELECT path,owner,size,mtime_ns,content_hash,computed_at FROM files WHERE path=? OR (path>=? AND path<?) ORDER BY path LIMIT ?`, prefix, start, end, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facts []IndexedFacts
	for rows.Next() {
		var fact IndexedFacts
		var computed int64
		if err := rows.Scan(&fact.Path, &fact.Owner, &fact.Size, &fact.MTimeNS, &fact.ContentHash, &computed); err != nil {
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

type directoryTotals struct{ bytes, lines, characters, tokens float64 }

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
