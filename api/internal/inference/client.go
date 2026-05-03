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
	"google.golang.org/grpc/credentials/insecure"
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
type GRPCClient struct {
	conn *grpc.ClientConn
	stub pb.InferenceServiceClient
}

// Dial returns a Client connected to addr. It does not block — the connection
// is established lazily on first call.
func Dial(addr string) (*GRPCClient, error) {
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
	return &GRPCClient{conn: conn, stub: pb.NewInferenceServiceClient(conn)}, nil
}

func (c *GRPCClient) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *GRPCClient) Classify(ctx context.Context, texts []string) ([]domain.Score, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.stub.Classify(ctx, &pb.ClassifyRequest{Texts: texts})
	if err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}
	out := make([]domain.Score, len(resp.Scores))
	for i, s := range resp.Scores {
		out[i] = ProtoToScore(s)
	}
	return out, nil
}

func (c *GRPCClient) ClassifyAspects(ctx context.Context, items []AspectInput) ([][]domain.AspectScore, error) {
	if len(items) == 0 {
		return nil, nil
	}
	in := make([]*pb.AspectInput, len(items))
	for i, item := range items {
		in[i] = &pb.AspectInput{Text: item.Text, Aspects: item.Aspects}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.stub.ClassifyAspects(ctx, &pb.ClassifyAspectsRequest{Inputs: in})
	if err != nil {
		return nil, fmt.Errorf("classify aspects: %w", err)
	}
	out := make([][]domain.AspectScore, len(resp.Results))
	for i, row := range resp.Results {
		row2 := make([]domain.AspectScore, len(row.Scores))
		for j, sc := range row.Scores {
			row2[j] = domain.AspectScore{Aspect: sc.Aspect, Score: ProtoToScore(sc.Score)}
		}
		out[i] = row2
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
