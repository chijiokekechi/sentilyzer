package inference

import (
	"context"
	"math"
	"strings"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
)

// FakeClient is a deterministic in-process Client used by tests and by the
// API server when running fully offline. It mirrors the heuristic stub on
// the Python side closely enough to give end-to-end coverage of the gateway
// without touching the network.
type FakeClient struct {
	GeneralModel string
	AspectModel  string
}

// NewFakeClient returns a Client that performs lexicon-based scoring.
func NewFakeClient() *FakeClient {
	return &FakeClient{
		GeneralModel: "fake-heuristic",
		AspectModel:  "fake-heuristic",
	}
}

var (
	positiveWords = map[string]struct{}{
		"good": {}, "great": {}, "love": {}, "loved": {}, "amazing": {}, "awesome": {},
		"excellent": {}, "fantastic": {}, "wonderful": {}, "best": {}, "perfect": {},
		"happy": {}, "win": {}, "winning": {}, "bullish": {}, "soaring": {},
	}
	negativeWords = map[string]struct{}{
		"bad": {}, "worst": {}, "hate": {}, "terrible": {}, "awful": {}, "horrible": {},
		"disappointed": {}, "broken": {}, "issue": {}, "issues": {}, "bug": {}, "buggy": {},
		"scam": {}, "fraud": {}, "loss": {}, "bearish": {}, "crash": {}, "dump": {},
	}
)

func score(text string) domain.Score {
	tokens := strings.Fields(strings.ToLower(text))
	pos, neg := 0, 0
	for _, t := range tokens {
		t = strings.Trim(t, ".,!?;:'\"()")
		if _, ok := positiveWords[t]; ok {
			pos++
		}
		if _, ok := negativeWords[t]; ok {
			neg++
		}
	}
	// Smoothed softmax matching the Python stub's shape.
	logits := []float64{float64(neg), 0.5, float64(pos)}
	maxL := logits[0]
	for _, l := range logits[1:] {
		if l > maxL {
			maxL = l
		}
	}
	exp := make([]float64, 3)
	var sum float64
	for i, l := range logits {
		exp[i] = math.Exp(l - maxL)
		sum += exp[i]
	}
	probs := make(map[string]float32, 3)
	probsArr := make([]float32, 3)
	names := []string{"negative", "neutral", "positive"}
	for i, name := range names {
		probsArr[i] = float32(exp[i] / sum)
		probs[name] = probsArr[i]
	}
	// Tie-breaking: any tie at max collapses to neutral.
	maxP := probsArr[0]
	winners := 1
	maxIdx := 0
	for i := 1; i < 3; i++ {
		switch {
		case probsArr[i] > maxP:
			maxP = probsArr[i]
			maxIdx = i
			winners = 1
		case probsArr[i] == maxP:
			winners++
		}
	}
	label := domain.SentimentNeutral
	if winners == 1 {
		label = domain.Sentiment(names[maxIdx])
	}
	return domain.Score{
		Label:         label,
		Confidence:    maxP,
		Polarity:      probsArr[2] - probsArr[0],
		Probabilities: probs,
	}
}

func (f *FakeClient) Classify(_ context.Context, texts []string) ([]domain.Score, error) {
	out := make([]domain.Score, len(texts))
	for i, t := range texts {
		out[i] = score(t)
	}
	return out, nil
}

func (f *FakeClient) ClassifyAspects(_ context.Context, items []AspectInput) ([][]domain.AspectScore, error) {
	out := make([][]domain.AspectScore, len(items))
	for i, item := range items {
		row := make([]domain.AspectScore, len(item.Aspects))
		for j, asp := range item.Aspects {
			row[j] = domain.AspectScore{Aspect: asp, Score: score(item.Text)}
		}
		out[i] = row
	}
	return out, nil
}

func (f *FakeClient) Ready(_ context.Context) (*ReadyInfo, error) {
	return &ReadyInfo{Ready: true, GeneralModel: f.GeneralModel, AspectModel: f.AspectModel, Device: "cpu"}, nil
}

func (f *FakeClient) Close() error { return nil }
