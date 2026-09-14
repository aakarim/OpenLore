package openlore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type WriterClassifier interface {
	Classify(context.Context, Attribution) analytics.Writer
}
type writerClassifierFunc func(context.Context, Attribution) analytics.Writer

func (f writerClassifierFunc) Classify(ctx context.Context, a Attribution) analytics.Writer {
	return f(ctx, a)
}
func IdentityStoreClassifier(ids IdentityStore) WriterClassifier {
	return writerClassifierFunc(func(ctx context.Context, a Attribution) analytics.Writer {
		if a.Actor != "" || a.internal || ids == nil || a.Principal == "" {
			return analytics.WriterAgent
		}
		identity, err := ids.Resolve(ctx, Claims{Subject: a.Principal, Scope: ScopeFull})
		if err != nil || identity.IdentityName == "" || identity.IdentityName == "guest" {
			return analytics.WriterAgent
		}
		return analytics.WriterHuman
	})
}

type ScalarProcessor struct {
	history   HistoryCursor
	blobs     BlobStore
	writer    WriterClassifier
	providers []analytics.ContentScalarProvider
	mu        sync.Mutex
	processed map[string]struct{}
	commits   map[string]struct{}
	emitFrom  time.Time
	docset    func(string) string
}

func NewScalarProcessor(history HistoryCursor, blobs BlobStore, writer WriterClassifier, providers ...analytics.ContentScalarProvider) analytics.Processor {
	return &ScalarProcessor{history: history, blobs: blobs, writer: writer, providers: providers, processed: map[string]struct{}{}, commits: map[string]struct{}{}}
}
func (p *ScalarProcessor) Name() string { return "doc-scalars" }
func (p *ScalarProcessor) Process(ctx context.Context, e analytics.Event) []analytics.Event {
	if e.Type != "doc.write" {
		return nil
	}
	target, _ := e.Fields["commit_id"].(string)
	if target == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.commits[target]; ok {
		return nil
	}
	return p.advance(ctx, target, &e)
}

func (p *ScalarProcessor) Drain(ctx context.Context) []analytics.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.advance(ctx, "", nil)
}

func (p *ScalarProcessor) advance(ctx context.Context, target string, correlation *analytics.Event) []analytics.Event {
	var out []analytics.Event
	for {
		record, ok, err := p.history.Next(ctx)
		if err != nil || !ok {
			return out
		}
		var event *analytics.Event
		if correlation != nil && record.ID == target {
			event = correlation
		}
		out = append(out, p.processRecord(ctx, record, event)...)
		if target != "" && record.ID == target {
			return out
		}
	}
}

func (p *ScalarProcessor) processRecord(ctx context.Context, record CommitRecord, correlation *analytics.Event) []analytics.Event {
	var out []analytics.Event
	p.commits[record.ID] = struct{}{}
	writer := p.writer.Classify(ctx, record.Attribution)
	changes := map[string][]byte{}
	for _, leaf := range record.ChangeSet.Leaves() {
		if leaf.Write != nil {
			changes[leaf.Target] = leaf.Write.Bytes
		}
	}
	for _, leaf := range record.Leaves {
		// Empty actions occur in journals written before LeafRecord.Action was
		// added. Keep those records replayable, but ignore explicit namespace
		// and metadata-only leaves.
		if leaf.Action != "" && leaf.Action != vfs.ChangeActionWrite && leaf.Action != vfs.ChangeActionRemove && leaf.Action != vfs.ChangeActionRemoveAll {
			continue
		}
		key := record.ID + "\x00" + leaf.Target
		if _, ok := p.processed[key]; ok {
			continue
		}
		p.processed[key] = struct{}{}
		var before, after map[string]float64
		beforeExists := leaf.BeforeExists || leaf.BeforeHash != "" // BeforeHash supports older journal records.
		firstSeen := !beforeExists || leaf.BeforeUnknown
		if leaf.BeforeHash != "" && p.blobs != nil {
			r, _, err := p.blobs.Get(ctx, leaf.BeforeHash)
			if err == nil {
				b, _ := io.ReadAll(r)
				r.Close()
				facts := p.computeScalars(leaf.Target, b)
				before = facts.Scalars
			} else {
				firstSeen = true
			}
		}
		contentHash := leaf.AfterHash
		action := "delete"
		if b, ok := changes[leaf.Target]; ok {
			action = "create"
			if beforeExists {
				action = "update"
			}
			facts := p.computeScalars(leaf.Target, b)
			after = facts.Scalars
			if contentHash == "" {
				contentHash = facts.ContentHash
			}
		}
		delta := map[string]any{}
		keys := map[string]bool{}
		for k := range before {
			keys[k] = true
		}
		for k := range after {
			keys[k] = true
		}
		for k := range keys {
			delta[k] = after[k] - before[k]
		}
		if beforeExists && before == nil && leaf.BeforeHash != "" {
			// Blob capture is optional, but the journal always records sizes.
			// Preserve the useful byte delta when the pre-image itself is absent.
			delta["bytes"] = float64(leaf.AfterSize - leaf.BeforeSize)
		}
		var invocationID, parentID string
		if correlation != nil {
			invocationID, parentID = correlation.InvocationID, correlation.ID
		}
		if p.emitFrom.IsZero() || !record.Time.Before(p.emitFrom) {
			docset := docsetFromPath(leaf.Target)
			if p.docset != nil {
				docset = p.docset(leaf.Target)
			}
			out = append(out, analytics.Event{ID: analytics.NewID(), Time: record.Time, Type: "doc.scalars", Principal: record.Attribution.Principal, Actor: record.Attribution.Actor, InvocationID: invocationID, ParentID: parentID, Fields: map[string]any{"path": leaf.Target, "docset": docset, "action": action, "writer": string(writer), "commit_id": record.ID, "content_hash": contentHash, "before": before, "after": after, "delta": delta, "tokenizer": "approx", "first_seen": firstSeen}})
		}
	}
	return out
}

func (p *ScalarProcessor) ResetForReplay(from time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	restorer, ok := p.history.(historyCursorRestorer)
	if !ok {
		return errors.New("history cursor cannot restore position")
	}
	p.processed = map[string]struct{}{}
	p.commits = map[string]struct{}{}
	p.emitFrom = from
	return restorer.RestorePosition(HistoryPosition{})
}

func (p *ScalarProcessor) MarshalCheckpointState() (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return json.Marshal(p.history.Position())
}

func (p *ScalarProcessor) RestoreCheckpointState(raw json.RawMessage) error {
	var position HistoryPosition
	if err := json.Unmarshal(raw, &position); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	restorer, ok := p.history.(historyCursorRestorer)
	if !ok {
		return errors.New("history cursor cannot restore position")
	}
	p.processed = map[string]struct{}{}
	p.commits = map[string]struct{}{}
	p.emitFrom = time.Time{}
	return restorer.RestorePosition(position)
}

func (p *ScalarProcessor) computeScalars(path string, content []byte) analytics.DocScalars {
	if len(p.providers) == 0 {
		return analytics.ComputeScalars(path, content)
	}
	scalars := make(map[string]float64)
	for _, provider := range p.providers {
		for name, value := range provider.Scalars(path, content) {
			scalars[name] = value
		}
	}
	return analytics.DocScalars{ContentHash: hashContent(content), Scalars: scalars}
}
