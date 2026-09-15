package openlore

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/aakarim/go-openlore/internal/analytics"
)

// Public aliases keep analytics plugin implementations outside this module
// from needing to import OpenLore's internal analytics package.
type AnalyticsEvent = analytics.Event
type AnalyticsSink = analytics.Sink
type AnalyticsConsumer = analytics.Consumer
type AnalyticsProcessor = analytics.Processor
type ContentScalarProvider = analytics.ContentScalarProvider
type AnalyticsTokenizer = analytics.Tokenizer
type AnalyticsAggregation = analytics.Aggregation
type AnalyticsEventSource = analytics.EventSource
type AnalyticsEventFilter = analytics.EventFilter
type AnalyticsContentFacts = analytics.ContentFacts
type AnalyticsWalkOptions = analytics.WalkOptions
type AnalyticsContentUnit = analytics.ContentUnit
type AnalyticsLineRange = analytics.LineRange
type AnalyticsDocScalars = analytics.DocScalars
type AnalyticsParams = analytics.Params
type AnalyticsParamSpec = analytics.ParamSpec
type AnalyticsTable = analytics.Table

type MetricsEmitterProvider interface {
	SetAnalyticsSink(AnalyticsSink)
}

type MetricsSubscriberProvider interface {
	AnalyticsConsumers() []AnalyticsConsumer
}

type MetricsProcessorProvider interface {
	AnalyticsProcessors() []AnalyticsProcessor
}

type ContentScalarProviderProvider interface {
	ContentScalarProviders() []ContentScalarProvider
}

type TokenizerProvider interface {
	Tokenizer() AnalyticsTokenizer
}

type AggregationProvider interface {
	Aggregations() []AnalyticsAggregation
}

var analyticsPluginName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func (s *Server) registerAnalyticsPlugin(p any) error {
	if !hasAnalyticsCapability(p) {
		return nil
	}
	infoProvider, ok := p.(PluginInfoProvider)
	if !ok {
		return fmt.Errorf("analytics plugin %T must implement PluginInfoProvider", p)
	}
	name := strings.TrimSpace(infoProvider.Info().Name)
	if !analyticsPluginName.MatchString(name) {
		return fmt.Errorf("analytics plugin %T has invalid name %q", p, name)
	}

	var base analytics.Sink
	if s.analytics != nil {
		base = s.analytics.Sink()
	}
	if provider, ok := p.(MetricsEmitterProvider); ok {
		provider.SetAnalyticsSink(analytics.NamespacedSink(base, name))
	}
	if s.analytics == nil {
		return nil
	}
	if provider, ok := p.(MetricsSubscriberProvider); ok {
		for _, consumer := range provider.AnalyticsConsumers() {
			s.analytics.AddConsumer(consumer)
		}
	}
	if provider, ok := p.(MetricsProcessorProvider); ok {
		for _, processor := range provider.AnalyticsProcessors() {
			s.analytics.AddProcessor(processor)
		}
	}
	if provider, ok := p.(ContentScalarProviderProvider); ok {
		for _, scalarProvider := range provider.ContentScalarProviders() {
			s.analytics.AddContentScalarProvider(scalarProvider)
		}
	}
	if provider, ok := p.(TokenizerProvider); ok {
		s.analytics.SetTokenizer(provider.Tokenizer())
	}
	if provider, ok := p.(AggregationProvider); ok {
		prefix := "plugin." + name + "."
		for _, aggregation := range provider.Aggregations() {
			if !strings.HasPrefix(aggregation.Name, prefix) {
				aggregation.Name = prefix + aggregation.Name
			}
			if err := s.analytics.RegisterAggregation(aggregation); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasAnalyticsCapability(p any) bool {
	_, emitter := p.(MetricsEmitterProvider)
	_, subscriber := p.(MetricsSubscriberProvider)
	_, processor := p.(MetricsProcessorProvider)
	_, scalars := p.(ContentScalarProviderProvider)
	_, tokenizer := p.(TokenizerProvider)
	_, aggregations := p.(AggregationProvider)
	return emitter || subscriber || processor || scalars || tokenizer || aggregations
}
