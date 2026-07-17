package server

import (
	"context"

	pb "github.com/chijiokekechi/sentilyzer/api/gen/go/sentilyzer/v1"
	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"github.com/chijiokekechi/sentilyzer/api/internal/inference"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GRPC adapts the gRPC SentilyzerServiceServer onto the protocol-agnostic
// service.Service.
type GRPC struct {
	pb.UnimplementedSentilyzerServiceServer
	Service *service.Service
	Version string
}

// Register registers the gRPC server onto srv.
func (g *GRPC) Register(srv *grpc.Server) {
	pb.RegisterSentilyzerServiceServer(srv, g)
}

func (g *GRPC) AnalyzeText(
	ctx context.Context, req *pb.AnalyzeTextRequest,
) (*pb.AnalyzeTextResponse, error) {
	docs := make([]domain.Document, len(req.Documents))
	for i, d := range req.Documents {
		docs[i] = domain.Document{
			ID:       d.Id,
			Text:     d.Text,
			Language: d.Language,
			Aspects:  d.Aspects,
			Metadata: d.Metadata,
		}
	}
	resp, err := g.Service.AnalyzeText(ctx, service.AnalyzeTextRequest{
		Documents:      docs,
		IncludeAspects: req.IncludeAspects,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.AnalyzeTextResponse{
		Results:   docResultsToProto(resp.Results),
		Aggregate: aggregateToProto(resp.Aggregate),
	}, nil
}

func (g *GRPC) AnalyzeTopic(
	ctx context.Context, req *pb.AnalyzeTopicRequest,
) (*pb.AnalyzeTopicResponse, error) {
	resp, err := g.Service.AnalyzeTopic(ctx, service.AnalyzeTopicRequest{
		Topic:            req.Topic,
		Platforms:        req.Platforms,
		LimitPerPlatform: int(req.LimitPerPlatform),
		Aspects:          req.Aspects,
		Language:         req.Language,
		SinceSeconds:     req.SinceSeconds,
	})
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	out := &pb.AnalyzeTopicResponse{
		Topic:      resp.Topic,
		Results:    sourcedResultsToProto(resp.Results),
		Aggregate:  aggregateToProto(resp.Aggregate),
		ByPlatform: aggregateMapToProto(resp.ByPlatform),
		ByAspect:   aggregateMapToProto(resp.ByAspect),
		Partial:    resp.Partial,
		Warnings:   warningsToProto(resp.Warnings),
	}
	return out, nil
}

func warningsToProto(in []domain.Warning) []*pb.Warning {
	if len(in) == 0 {
		return nil
	}
	out := make([]*pb.Warning, len(in))
	for i, w := range in {
		out[i] = &pb.Warning{Platform: w.Platform, Message: w.Message}
	}
	return out
}

func (g *GRPC) ListPlatforms(
	_ context.Context, _ *pb.ListPlatformsRequest,
) (*pb.ListPlatformsResponse, error) {
	infos := g.Service.ListPlatforms()
	out := &pb.ListPlatformsResponse{Platforms: make([]*pb.PlatformInfo, len(infos))}
	for i, p := range infos {
		out.Platforms[i] = &pb.PlatformInfo{
			Id:             p.ID,
			DisplayName:    p.DisplayName,
			Enabled:        p.Enabled,
			DisabledReason: p.DisabledReason,
		}
	}
	return out, nil
}

func (g *GRPC) Health(ctx context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	info, err := g.Service.Health(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.HealthResponse{
		Healthy:     info.Healthy,
		MlReachable: info.MLReachable,
		Version:     g.Version,
	}, nil
}

// --- domain -> proto -------------------------------------------------------

func scoreToProto(s domain.Score) *pb.Score {
	return &pb.Score{
		Label:         inference.DomainLabelToProto(s.Label),
		Confidence:    s.Confidence,
		Polarity:      s.Polarity,
		Probabilities: s.Probabilities,
	}
}

func docResultsToProto(in []domain.DocumentResult) []*pb.DocumentResult {
	out := make([]*pb.DocumentResult, len(in))
	for i, r := range in {
		aspects := make([]*pb.AspectScore, len(r.Aspects))
		for j, a := range r.Aspects {
			aspects[j] = &pb.AspectScore{Aspect: a.Aspect, Score: scoreToProto(a.Score)}
		}
		out[i] = &pb.DocumentResult{
			Id:         r.ID,
			Overall:    scoreToProto(r.Overall),
			Aspects:    aspects,
			AnalyzedAt: timestamppb.New(r.AnalyzedAt),
		}
	}
	return out
}

func sourcedResultsToProto(in []domain.SourcedDocumentResult) []*pb.SourcedDocumentResult {
	out := make([]*pb.SourcedDocumentResult, len(in))
	for i, r := range in {
		out[i] = &pb.SourcedDocumentResult{
			Document: &pb.SourcedDocument{
				Document: &pb.Document{
					Id:       r.Document.Document.ID,
					Text:     r.Document.Document.Text,
					Language: r.Document.Document.Language,
					Aspects:  r.Document.Document.Aspects,
					Metadata: r.Document.Document.Metadata,
				},
				Platform:  r.Document.Platform,
				SourceUrl: r.Document.URL,
				Author:    r.Document.Author,
				PostedAt:  timestamppb.New(r.Document.PostedAt),
			},
			Result: docResultsToProto([]domain.DocumentResult{r.Result})[0],
		}
	}
	return out
}

func aggregateToProto(a domain.Aggregate) *pb.Aggregate {
	return &pb.Aggregate{
		MeanPolarity: a.MeanPolarity,
		LabelCounts:  a.LabelCounts,
		ModalLabel:   inference.DomainLabelToProto(a.ModalLabel),
		SampleSize:   a.SampleSize,
	}
}

func aggregateMapToProto(m map[string]domain.Aggregate) map[string]*pb.Aggregate {
	out := make(map[string]*pb.Aggregate, len(m))
	for k, v := range m {
		out[k] = aggregateToProto(v)
	}
	return out
}
