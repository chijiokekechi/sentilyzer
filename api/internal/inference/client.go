// Package inference is a thin client around the Python ML worker's
// InferenceService. It handles enum mapping back into the domain types
// and exposes a Client interface that the service layer (and tests) consume.
package inference

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/chijiokekechi/sentilyzer/api/gen/go/sentilyzer/v1"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Client is the contract the service layer needs. Tests can supply a fake.
type Client interface {
	Classify(ctx context.Context, texts []string) ([]domain.Score, error)
	ClassifyAspects(ctx context.Context, items []AspectInput) ([][]domain.AspectScore, error)
	Ready(ctx context.Context) (*ReadyInfo, error)
	Close() error
}

// AspectInput pairs a body of text with the aspects to score within it.
type AspectInput struct {
	Text    string
	Aspects []string
}

// ReadyInfo is the worker's self-report.
type ReadyInfo struct {
	Ready        bool
	GeneralModel string
	AspectModel  string
	Device       string
}

// GRPCClient connects to the Python worker over gRPC.
//
// It splits oversized requests into batches the worker will accept. The
// worker enforces a hard ceiling and aborts with RESOURCE_EXHAUSTED rather
// than chunking, so batching has to happen here — below the Client
// interface, which keeps the service layer unaware that chunks exist.
type GRPCClient struct {
	conn     *grpc.ClientConn
	stub     pb.InferenceServiceClient
	maxBatch int
	timeout  time.Duration
}

// Option configures a GRPCClient.
type Option func(*GRPCClient)

// WithMaxBatch caps how many texts the client sends per Classify call. It
// must not exceed the worker's SENTILYZER_ML_MAX_BATCH; the worker rejects
// anything larger outright. Non-positive values are ignored.
func WithMaxBatch(n int) Option {
	return func(c *GRPCClient) {
		if n > 0 {
			c.maxBatch = n
		}
	}
}

// WithTimeout bounds one whole Classify or ClassifyAspects call — every
// batch it fans out to, not each one individually. Non-positive values are
// ignored.
func WithTimeout(d time.Duration) Option {
	return func(c *GRPCClient) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// Dial returns a Client connected to addr. It does not block — the connection
// is established lazily on first call.
func Dial(addr string, opts ...Option) (*GRPCClient, error) {
	if addr == "" {
		return nil, errors.New("inference: empty address")
	}
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dial inference: %w", err)
	}
	c := &GRPCClient{
		conn:     conn,
		stub:     pb.NewInferenceServiceClient(conn),
		maxBatch: defaultMaxBatch,
		timeout:  30 * time.Second,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// batchRejected turns the worker's RESOURCE_EXHAUSTED into an error that
// names the fix. Reaching it means the client and worker disagree about the
// ceiling, which is a config problem no retry will resolve.
func (c *GRPCClient) batchRejected(err error, sent int, limit int) error {
	if status.Code(err) != codes.ResourceExhausted {
		return nil
	}
	return fmt.Errorf(
		"worker rejected a batch of %d (client limit %d): the worker's "+
			"SENTILYZER_ML_MAX_BATCH is lower than this client's — set "+
			"SENTILYZER_ML_MAX_BATCH on the API to match the worker: %w",
		sent, limit, err,
	)
}

func (c *GRPCClient) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Classify scores every text, splitting the work into worker-sized batches
// and returning scores in the caller's original order.
func (c *GRPCClient) Classify(ctx context.Context, texts []string) ([]domain.Score, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	order := lengthOrder(texts)
	out := make([]domain.Score, len(texts))

	for _, r := range chunkRanges(len(order), c.maxBatch) {
		window := order[r[0]:r[1]]
		batch := make([]string, len(window))
		for i, src := range window {
			batch[i] = texts[src]
		}

		resp, err := c.stub.Classify(ctx, &pb.ClassifyRequest{Texts: batch})
		if err != nil {
			if rejected := c.batchRejected(err, len(batch), c.maxBatch); rejected != nil {
				return nil, rejected
			}
			return nil, fmt.Errorf("classify (%d of %d texts): %w", len(batch), len(texts), err)
		}
		// A short or long response would silently shift every subsequent
		// score onto the wrong document, so refuse it rather than unpack it.
		if len(resp.Scores) != len(batch) {
			return nil, fmt.Errorf(
				"classify: worker returned %d scores for %d texts",
				len(resp.Scores), len(batch),
			)
		}
		for i, s := range resp.Scores {
			out[window[i]] = ProtoToScore(s)
		}
	}
	return out, nil
}

// ClassifyAspects scores every (document, aspect) pair, splitting the work
// into batches within the worker's cumulative-aspect ceiling and returning
// rows aligned to the caller's items and aspect order.
func (c *GRPCClient) ClassifyAspects(ctx context.Context, items []AspectInput) ([][]domain.AspectScore, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([][]domain.AspectScore, len(items))
	for i, item := range items {
		out[i] = make([]domain.AspectScore, len(item.Aspects))
	}
	refs := aspectRefs(items)
	if len(refs) == 0 {
		return out, nil
	}

	limit := c.maxBatch * aspectBatchMultiple
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	for _, r := range chunkRanges(len(refs), limit) {
		groups := groupByItem(refs[r[0]:r[1]])

		in := make([]*pb.AspectInput, len(groups))
		for gi, group := range groups {
			aspects := make([]string, len(group))
			for ai, ref := range group {
				aspects[ai] = items[ref.item].Aspects[ref.pos]
			}
			in[gi] = &pb.AspectInput{Text: items[group[0].item].Text, Aspects: aspects}
		}

		resp, err := c.stub.ClassifyAspects(ctx, &pb.ClassifyAspectsRequest{Inputs: in})
		if err != nil {
			if rejected := c.batchRejected(err, r[1]-r[0], limit); rejected != nil {
				return nil, rejected
			}
			return nil, fmt.Errorf("classify aspects (%d of %d pairs): %w", r[1]-r[0], len(refs), err)
		}
		if len(resp.Results) != len(groups) {
			return nil, fmt.Errorf(
				"classify aspects: worker returned %d rows for %d inputs",
				len(resp.Results), len(groups),
			)
		}
		for gi, group := range groups {
			row := resp.Results[gi]
			if len(row.Scores) != len(group) {
				return nil, fmt.Errorf(
					"classify aspects: worker returned %d scores for %d aspects",
					len(row.Scores), len(group),
				)
			}
			for ai, ref := range group {
				sc := row.Scores[ai]
				out[ref.item][ref.pos] = domain.AspectScore{
					Aspect: sc.Aspect,
					Score:  ProtoToScore(sc.Score),
				}
			}
		}
	}
	return out, nil
}

func (c *GRPCClient) Ready(ctx context.Context) (*ReadyInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := c.stub.Ready(ctx, &pb.ReadyRequest{})
	if err != nil {
		return nil, err
	}
	return &ReadyInfo{
		Ready:        resp.Ready,
		GeneralModel: resp.GeneralModel,
		AspectModel:  resp.AspectModel,
		Device:       resp.Device,
	}, nil
}

// ProtoToScore converts a proto Score into the domain Score, mapping enum
// values to canonical lowercase strings.
func ProtoToScore(s *pb.Score) domain.Score {
	if s == nil {
		return domain.Score{Probabilities: map[string]float32{}}
	}
	probs := make(map[string]float32, len(s.Probabilities))
	for k, v := range s.Probabilities {
		probs[k] = v
	}
	return domain.Score{
		Label:         protoLabelToDomain(s.Label),
		Confidence:    s.Confidence,
		Polarity:      s.Polarity,
		Probabilities: probs,
	}
}

func protoLabelToDomain(l pb.Sentiment) domain.Sentiment {
	switch l {
	case pb.Sentiment_SENTIMENT_NEGATIVE:
		return domain.SentimentNegative
	case pb.Sentiment_SENTIMENT_NEUTRAL:
		return domain.SentimentNeutral
	case pb.Sentiment_SENTIMENT_POSITIVE:
		return domain.SentimentPositive
	default:
		return domain.SentimentUnspecified
	}
}

// DomainLabelToProto exposes the inverse mapping for the gRPC server layer.
func DomainLabelToProto(l domain.Sentiment) pb.Sentiment {
	switch l {
	case domain.SentimentNegative:
		return pb.Sentiment_SENTIMENT_NEGATIVE
	case domain.SentimentNeutral:
		return pb.Sentiment_SENTIMENT_NEUTRAL
	case domain.SentimentPositive:
		return pb.Sentiment_SENTIMENT_POSITIVE
	default:
		return pb.Sentiment_SENTIMENT_UNSPECIFIED
	}
}
