package openlore

import (
	"context"
	"io"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
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
}

func NewScalarProcessor(history HistoryCursor, blobs BlobStore, writer WriterClassifier, providers ...analytics.ContentScalarProvider) analytics.Processor {
	return &ScalarProcessor{history: history, blobs: blobs, writer: writer, providers: providers}
}
func (p *ScalarProcessor) Name() string { return "doc-scalars" }
func (p *ScalarProcessor) Process(ctx context.Context, e analytics.Event) []analytics.Event {
	if e.Type != "doc.write" {
		return nil
	}
	target, _ := e.Fields["commit_id"].(string)
	var out []analytics.Event
	for {
		record, ok, err := p.history.Next(ctx)
		if err != nil || !ok {
			return out
		}
		writer := p.writer.Classify(ctx, record.Attribution)
		changes := map[string][]byte{}
		for _, leaf := range record.ChangeSet.Leaves() {
			if leaf.Write != nil {
				changes[leaf.Target] = leaf.Write.Bytes
			}
		}
		for _, leaf := range record.Leaves {
			var before, after map[string]float64
			firstSeen := leaf.BeforeUnknown
			if leaf.BeforeHash != "" && p.blobs != nil {
				r, _, err := p.blobs.Get(ctx, leaf.BeforeHash)
				if err == nil {
					b, _ := io.ReadAll(r)
					r.Close()
					facts := analytics.ComputeScalars(leaf.Target, b)
					before = facts.Scalars
				} else {
					firstSeen = true
				}
			}
			contentHash := leaf.AfterHash
			action := "delete"
			if b, ok := changes[leaf.Target]; ok {
				action = "create"
				if leaf.BeforeHash != "" {
					action = "update"
				}
				facts := analytics.ComputeScalars(leaf.Target, b)
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
			parent := ""
			invocation := record.ID
			if record.ID == target {
				parent = e.ID
				invocation = e.InvocationID
			}
			out = append(out, analytics.Event{ID: analytics.NewID(), Time: time.Now().UTC(), Type: "doc.scalars", Principal: record.Attribution.Principal, Actor: record.Attribution.Actor, InvocationID: invocation, ParentID: parent, Fields: map[string]any{"path": leaf.Target, "docset": docsetFromPath(leaf.Target), "action": action, "writer": string(writer), "commit_id": record.ID, "content_hash": contentHash, "before": before, "after": after, "delta": delta, "tokenizer": "approx", "first_seen": firstSeen}})
		}
		if record.ID == target {
			return out
		}
	}
}
