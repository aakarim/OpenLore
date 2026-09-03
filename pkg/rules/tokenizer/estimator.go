// Package tokenizer provides the stable Phase 1 token estimator.
package tokenizer

const Version = "estimate/v1"

type Estimator struct{}

func (Estimator) Name() string                  { return Version }
func (Estimator) Estimate(content []byte) int64 { return int64((len(content) + 3) / 4) }
func Estimate(content []byte) int64             { return Estimator{}.Estimate(content) }
