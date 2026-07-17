package inference

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	pb "github.com/chijiokekechi/sentilyzer/api/gen/go/sentilyzer/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The tests below defend one property above all others: a score must come
// back attached to the text it was computed from.
//
// That property has no natural tripwire. service.go zips scores to documents
// positionally without checking ids, so a permutation bug produces a
// well-formed response, a plausible aggregate, and no error — every document
// simply carries another document's sentiment. Nothing downstream notices.
// So the fakes here encode each text's identity into its own score, and the
// assertions demand it survive the round trip.

// tagged builds a text whose identity (i) and padded length are both
// recoverable, so a fake can score it self-referentially.
func tagged(i, length int) string {
	prefix := strconv.Itoa(i) + ":"
	if pad := length - len(prefix); pad > 0 {
		return prefix + strings.Repeat("x", pad)
	}
	return prefix
}

// identityOf recovers the i that tagged() encoded.
func identityOf(t *testing.T, text string) int {
	t.Helper()
	head, _, ok := strings.Cut(text, ":")
	if !ok {
		t.Fatalf("malformed tagged text %q", text)
	}
	n, err := strconv.Atoi(head)
	if err != nil {
		t.Fatalf("malformed tagged text %q: %v", text, err)
	}
	return n
}

// echoStub scores each text as its own encoded identity: polarity == i.
// It also records every batch it was handed, so tests can assert on batching
// behavior rather than just on results.
type echoStub struct {
	t          *testing.T
	maxAccept  int // reject batches above this, as the real worker does
	batchSizes []int
	aspectHits []int // cumulative aspects per ClassifyAspects call
}

func (s *echoStub) Classify(_ context.Context, in *pb.ClassifyRequest, _ ...grpc.CallOption) (*pb.ClassifyResponse, error) {
	s.batchSizes = append(s.batchSizes, len(in.Texts))
	if s.maxAccept > 0 && len(in.Texts) > s.maxAccept {
		return nil, status.Errorf(codes.ResourceExhausted,
			"batch size %d > max %d", len(in.Texts), s.maxAccept)
	}
	scores := make([]*pb.Score, len(in.Texts))
	for i, text := range in.Texts {
		scores[i] = &pb.Score{
			Label:    pb.Sentiment_SENTIMENT_NEUTRAL,
			Polarity: float32(identityOf(s.t, text)),
		}
	}
	return &pb.ClassifyResponse{Scores: scores}, nil
}

func (s *echoStub) ClassifyAspects(_ context.Context, in *pb.ClassifyAspectsRequest, _ ...grpc.CallOption) (*pb.ClassifyAspectsResponse, error) {
	total := 0
	for _, inp := range in.Inputs {
		total += len(inp.Aspects)
	}
	s.aspectHits = append(s.aspectHits, total)
	if s.maxAccept > 0 && total > s.maxAccept*aspectBatchMultiple {
		return nil, status.Errorf(codes.ResourceExhausted, "aspect batch size %d too large", total)
	}
	results := make([]*pb.AspectResult, len(in.Inputs))
	for i, inp := range in.Inputs {
		id := identityOf(s.t, inp.Text)
		scores := make([]*pb.AspectScore, len(inp.Aspects))
		for j, aspect := range inp.Aspects {
			// Encode BOTH coordinates: the document via polarity, the aspect
			// via the echoed name. A row that lands on the wrong document or
			// the wrong aspect slot fails.
			scores[j] = &pb.AspectScore{
				Aspect: aspect,
				Score:  &pb.Score{Polarity: float32(id)},
			}
		}
		results[i] = &pb.AspectResult{Scores: scores}
	}
	return &pb.ClassifyAspectsResponse{Results: results}, nil
}

func (s *echoStub) Ready(context.Context, *pb.ReadyRequest, ...grpc.CallOption) (*pb.ReadyResponse, error) {
	return &pb.ReadyResponse{Ready: true}, nil
}

func newTestClient(t *testing.T, stub pb.InferenceServiceClient, maxBatch int) *GRPCClient {
	t.Helper()
	return &GRPCClient{stub: stub, maxBatch: maxBatch, timeout: 5_000_000_000}
}

// TestClassifyPreservesOrderAcrossChunks is the load-bearing test: whatever
// the batch size and however the length ordering permutes the inputs, score
// i must belong to text i.
func TestClassifyPreservesOrderAcrossChunks(t *testing.T) {
	rng := rand.New(rand.NewSource(1))

	for _, n := range []int{1, 2, 31, 32, 33, 64, 65, 200, 501} {
		for _, maxBatch := range []int{1, 2, 7, 32, 1000} {
			name := fmt.Sprintf("n=%d/max=%d", n, maxBatch)
			t.Run(name, func(t *testing.T) {
				// Wildly varied lengths, with deliberate ties, so the length
				// ordering actually permutes and its stability is exercised.
				texts := make([]string, n)
				for i := range texts {
					texts[i] = tagged(i, 1+rng.Intn(40)*10)
				}

				// A correctly configured deployment has both sides on the
				// same ceiling; disagreement is its own test below.
				stub := &echoStub{t: t, maxAccept: maxBatch}
				c := newTestClient(t, stub, maxBatch)

				got, err := c.Classify(context.Background(), texts)
				if err != nil {
					t.Fatalf("Classify: %v", err)
				}
				if len(got) != n {
					t.Fatalf("got %d scores, want %d", len(got), n)
				}
				for i, s := range got {
					if int(s.Polarity) != i {
						t.Fatalf("score %d carries identity %d — scores are misaligned to texts",
							i, int(s.Polarity))
					}
				}
			})
		}
	}
}

// TestClassifyNeverExceedsWorkerCeiling proves the blocker is actually fixed:
// with the worker's real default of 32, a default 60-document request (three
// credential-free connectors at the default limit of 20) must succeed.
func TestClassifyNeverExceedsWorkerCeiling(t *testing.T) {
	texts := make([]string, 60)
	for i := range texts {
		texts[i] = tagged(i, 50)
	}
	stub := &echoStub{t: t, maxAccept: 32} // aborts above 32, like server.py
	c := newTestClient(t, stub, defaultMaxBatch)

	got, err := c.Classify(context.Background(), texts)
	if err != nil {
		t.Fatalf("the default 60-document request still fails: %v", err)
	}
	if len(got) != 60 {
		t.Fatalf("got %d scores, want 60", len(got))
	}
	for _, size := range stub.batchSizes {
		if size > 32 {
			t.Fatalf("sent a batch of %d to a worker that caps at 32", size)
		}
	}
	if len(stub.batchSizes) != 2 {
		t.Fatalf("expected 60 texts to split into 2 batches, got %v", stub.batchSizes)
	}
}

// TestClassifyGroupsByLength guards the padding optimization. The worker pads
// every text in a batch to the longest one in it, so a long outlier must not
// be scattered across batches of short texts.
func TestClassifyGroupsByLength(t *testing.T) {
	// 32 short texts and 32 long ones, interleaved on input.
	texts := make([]string, 64)
	for i := range texts {
		if i%2 == 0 {
			texts[i] = tagged(i, 10)
		} else {
			texts[i] = tagged(i, 2000)
		}
	}
	stub := &echoStub{t: t, maxAccept: 32}
	c := newTestClient(t, stub, 32)

	got, err := c.Classify(context.Background(), texts)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// Order must still hold even though every text moved.
	for i, s := range got {
		if int(s.Polarity) != i {
			t.Fatalf("score %d carries identity %d", i, int(s.Polarity))
		}
	}
	// The short texts should have been collected into one batch, not mixed
	// with the 2000-char ones.
	order := lengthOrder(texts)
	for _, i := range order[:32] {
		if len(texts[i]) != 10 {
			t.Fatalf("length ordering mixed a %d-char text into the short half", len(texts[i]))
		}
	}
}

// TestClassifyRejectsMiscountedResponse ensures a worker that returns the
// wrong number of scores is refused rather than silently unpacked — an
// off-by-one there would shift every later score onto the wrong document.
func TestClassifyRejectsMiscountedResponse(t *testing.T) {
	c := newTestClient(t, &shortStub{}, 32)
	_, err := c.Classify(context.Background(), []string{"0:a", "1:b", "2:c"})
	if err == nil {
		t.Fatal("expected an error when the worker returns fewer scores than texts")
	}
	if !strings.Contains(err.Error(), "returned 2 scores for 3 texts") {
		t.Fatalf("error should name the mismatch, got: %v", err)
	}
}

type shortStub struct{}

func (shortStub) Classify(_ context.Context, in *pb.ClassifyRequest, _ ...grpc.CallOption) (*pb.ClassifyResponse, error) {
	return &pb.ClassifyResponse{Scores: make([]*pb.Score, len(in.Texts)-1)}, nil
}
func (shortStub) ClassifyAspects(context.Context, *pb.ClassifyAspectsRequest, ...grpc.CallOption) (*pb.ClassifyAspectsResponse, error) {
	return nil, nil
}
func (shortStub) Ready(context.Context, *pb.ReadyRequest, ...grpc.CallOption) (*pb.ReadyResponse, error) {
	return nil, nil
}

// TestClassifyExplainsCeilingMismatch: when the worker's ceiling is lower
// than the client's, no retry can help, so the error must name the fix.
func TestClassifyExplainsCeilingMismatch(t *testing.T) {
	texts := make([]string, 40)
	for i := range texts {
		texts[i] = tagged(i, 20)
	}
	stub := &echoStub{t: t, maxAccept: 16} // worker configured lower than client
	c := newTestClient(t, stub, 32)

	_, err := c.Classify(context.Background(), texts)
	if err == nil {
		t.Fatal("expected an error when the worker's ceiling is below the client's")
	}
	if !strings.Contains(err.Error(), "SENTILYZER_ML_MAX_BATCH") {
		t.Fatalf("error should name the variable to fix, got: %v", err)
	}
}

// TestClassifyAspectsPreservesCoordinates: every aspect score must land on
// the right document AND the right aspect slot, across chunk boundaries.
func TestClassifyAspectsPreservesCoordinates(t *testing.T) {
	for _, spec := range []struct{ docs, aspects, maxBatch int }{
		{docs: 1, aspects: 1, maxBatch: 32},
		{docs: 10, aspects: 3, maxBatch: 32},  // 30 pairs, one chunk of 128
		{docs: 60, aspects: 3, maxBatch: 32},  // 180 pairs, splits
		{docs: 5, aspects: 40, maxBatch: 32},  // 200 pairs, docs split mid-way
		{docs: 3, aspects: 200, maxBatch: 32}, // one doc alone exceeds the ceiling
		{docs: 7, aspects: 5, maxBatch: 1},    // ceiling of 4 pairs
	} {
		name := fmt.Sprintf("docs=%d/aspects=%d/max=%d", spec.docs, spec.aspects, spec.maxBatch)
		t.Run(name, func(t *testing.T) {
			items := make([]AspectInput, spec.docs)
			for i := range items {
				aspects := make([]string, spec.aspects)
				for j := range aspects {
					aspects[j] = fmt.Sprintf("aspect-%d", j)
				}
				items[i] = AspectInput{Text: tagged(i, 10+i*7), Aspects: aspects}
			}

			stub := &echoStub{t: t, maxAccept: 32}
			c := newTestClient(t, stub, spec.maxBatch)

			got, err := c.ClassifyAspects(context.Background(), items)
			if err != nil {
				t.Fatalf("ClassifyAspects: %v", err)
			}
			if len(got) != spec.docs {
				t.Fatalf("got %d rows, want %d", len(got), spec.docs)
			}
			for i, row := range got {
				if len(row) != spec.aspects {
					t.Fatalf("row %d has %d aspects, want %d", i, len(row), spec.aspects)
				}
				for j, as := range row {
					if int(as.Score.Polarity) != i {
						t.Fatalf("aspect [%d][%d] carries document identity %d",
							i, j, int(as.Score.Polarity))
					}
					if want := fmt.Sprintf("aspect-%d", j); as.Aspect != want {
						t.Fatalf("aspect [%d][%d] is %q, want %q", i, j, as.Aspect, want)
					}
				}
			}
			// Never exceed the worker's cumulative-aspect ceiling.
			for _, total := range stub.aspectHits {
				if total > spec.maxBatch*aspectBatchMultiple {
					t.Fatalf("sent %d aspects, ceiling is %d",
						total, spec.maxBatch*aspectBatchMultiple)
				}
			}
		})
	}
}

// TestClassifyAspectsSkipsDocumentsWithoutAspects: a document carrying no
// aspects contributes no pairs and must still get an (empty) row back at its
// own index, or every later row shifts.
func TestClassifyAspectsSkipsDocumentsWithoutAspects(t *testing.T) {
	items := []AspectInput{
		{Text: tagged(0, 10), Aspects: []string{"a"}},
		{Text: tagged(1, 20), Aspects: nil},
		{Text: tagged(2, 30), Aspects: []string{"b", "c"}},
	}
	c := newTestClient(t, &echoStub{t: t, maxAccept: 32}, 32)

	got, err := c.ClassifyAspects(context.Background(), items)
	if err != nil {
		t.Fatalf("ClassifyAspects: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	if len(got[0]) != 1 || len(got[1]) != 0 || len(got[2]) != 2 {
		t.Fatalf("row widths %d/%d/%d, want 1/0/2", len(got[0]), len(got[1]), len(got[2]))
	}
	if int(got[2][0].Score.Polarity) != 2 {
		t.Fatalf("row 2 carries identity %d — the empty row shifted results",
			int(got[2][0].Score.Polarity))
	}
}

func TestClassifyEmptyInput(t *testing.T) {
	c := newTestClient(t, &echoStub{t: t, maxAccept: 32}, 32)
	got, err := c.Classify(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("Classify(nil) = %v, %v; want nil, nil", got, err)
	}
	rows, err := c.ClassifyAspects(context.Background(), nil)
	if err != nil || rows != nil {
		t.Fatalf("ClassifyAspects(nil) = %v, %v; want nil, nil", rows, err)
	}
}
