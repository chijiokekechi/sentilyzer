package server

import (
	"net/http"

	"github.com/chijiokekechi/sentilyzer/api/internal/domain"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
	"github.com/graphql-go/graphql"
	gqlhandler "github.com/graphql-go/handler"
)

// GraphQL exposes a single root Query node:
//
//	type Query {
//	  platforms: [Platform!]!
//	  analyzeText(documents: [DocumentInput!]!, includeAspects: Boolean): TextAnalysis!
//	  analyzeTopic(topic: String!, platforms: [String!], limitPerPlatform: Int, aspects: [String!], language: String, sinceSeconds: Int): TopicAnalysis!
//	}
//
// Since each call already runs through service.Service, the resolvers are
// thin shims.
type GraphQL struct {
	Service *service.Service
}

func (g *GraphQL) Handler() http.Handler {
	schema := g.buildSchema()
	return gqlhandler.New(&gqlhandler.Config{
		Schema:     &schema,
		Pretty:     true,
		GraphiQL:   true,
		Playground: false,
	})
}

func (g *GraphQL) buildSchema() graphql.Schema {
	scoreType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Score",
		Fields: graphql.Fields{
			"label":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"confidence": &graphql.Field{Type: graphql.NewNonNull(graphql.Float)},
			"polarity":   &graphql.Field{Type: graphql.NewNonNull(graphql.Float)},
		},
	})
	aspectScoreType := graphql.NewObject(graphql.ObjectConfig{
		Name: "AspectScore",
		Fields: graphql.Fields{
			"aspect": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"score":  &graphql.Field{Type: graphql.NewNonNull(scoreType)},
		},
	})
	documentResultType := graphql.NewObject(graphql.ObjectConfig{
		Name: "DocumentResult",
		Fields: graphql.Fields{
			"id":      &graphql.Field{Type: graphql.String},
			"overall": &graphql.Field{Type: graphql.NewNonNull(scoreType)},
			"aspects": &graphql.Field{Type: graphql.NewList(aspectScoreType)},
			"analyzedAt": &graphql.Field{
				Type: graphql.String,
				Resolve: func(p graphql.ResolveParams) (any, error) {
					return p.Source.(domain.DocumentResult).AnalyzedAt.Format("2006-01-02T15:04:05Z07:00"), nil
				},
			},
		},
	})
	aggregateType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Aggregate",
		Fields: graphql.Fields{
			"meanPolarity": &graphql.Field{
				Type:    graphql.NewNonNull(graphql.Float),
				Resolve: func(p graphql.ResolveParams) (any, error) { return p.Source.(domain.Aggregate).MeanPolarity, nil },
			},
			"modalLabel": &graphql.Field{
				Type:    graphql.NewNonNull(graphql.String),
				Resolve: func(p graphql.ResolveParams) (any, error) { return string(p.Source.(domain.Aggregate).ModalLabel), nil },
			},
			"sampleSize": &graphql.Field{
				Type:    graphql.NewNonNull(graphql.Int),
				Resolve: func(p graphql.ResolveParams) (any, error) { return int(p.Source.(domain.Aggregate).SampleSize), nil },
			},
		},
	})
	textAnalysisType := graphql.NewObject(graphql.ObjectConfig{
		Name: "TextAnalysis",
		Fields: graphql.Fields{
			"results":   &graphql.Field{Type: graphql.NewList(documentResultType)},
			"aggregate": &graphql.Field{Type: graphql.NewNonNull(aggregateType)},
		},
	})

	sourcedDocumentType := graphql.NewObject(graphql.ObjectConfig{
		Name: "SourcedDocument",
		Fields: graphql.Fields{
			"id":       &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) { return p.Source.(domain.SourcedDocument).Document.ID, nil }},
			"text":     &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) { return p.Source.(domain.SourcedDocument).Document.Text, nil }},
			"platform": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"url":      &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) { return p.Source.(domain.SourcedDocument).URL, nil }},
			"author":   &graphql.Field{Type: graphql.String},
			"postedAt": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) {
				return p.Source.(domain.SourcedDocument).PostedAt.Format("2006-01-02T15:04:05Z07:00"), nil
			}},
		},
	})
	sourcedResultType := graphql.NewObject(graphql.ObjectConfig{
		Name: "SourcedDocumentResult",
		Fields: graphql.Fields{
			"document": &graphql.Field{Type: graphql.NewNonNull(sourcedDocumentType)},
			"result":   &graphql.Field{Type: graphql.NewNonNull(documentResultType)},
		},
	})
	topicAnalysisType := graphql.NewObject(graphql.ObjectConfig{
		Name: "TopicAnalysis",
		Fields: graphql.Fields{
			"topic":     &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"results":   &graphql.Field{Type: graphql.NewList(sourcedResultType)},
			"aggregate": &graphql.Field{Type: graphql.NewNonNull(aggregateType)},
		},
	})
	platformType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Platform",
		Fields: graphql.Fields{
			"id":             &graphql.Field{Type: graphql.NewNonNull(graphql.String), Resolve: func(p graphql.ResolveParams) (any, error) { return p.Source.(domain.PlatformInfo).ID, nil }},
			"displayName":    &graphql.Field{Type: graphql.NewNonNull(graphql.String), Resolve: func(p graphql.ResolveParams) (any, error) { return p.Source.(domain.PlatformInfo).DisplayName, nil }},
			"enabled":        &graphql.Field{Type: graphql.NewNonNull(graphql.Boolean)},
			"disabledReason": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) { return p.Source.(domain.PlatformInfo).DisabledReason, nil }},
		},
	})

	documentInput := graphql.NewInputObject(graphql.InputObjectConfig{
		Name: "DocumentInput",
		Fields: graphql.InputObjectConfigFieldMap{
			"id":       &graphql.InputObjectFieldConfig{Type: graphql.String},
			"text":     &graphql.InputObjectFieldConfig{Type: graphql.NewNonNull(graphql.String)},
			"language": &graphql.InputObjectFieldConfig{Type: graphql.String},
			"aspects":  &graphql.InputObjectFieldConfig{Type: graphql.NewList(graphql.String)},
		},
	})

	root := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"platforms": &graphql.Field{
				Type: graphql.NewList(platformType),
				Resolve: func(p graphql.ResolveParams) (any, error) {
					return g.Service.ListPlatforms(), nil
				},
			},
			"analyzeText": &graphql.Field{
				Type: graphql.NewNonNull(textAnalysisType),
				Args: graphql.FieldConfigArgument{
					"documents":      &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(documentInput)))},
					"includeAspects": &graphql.ArgumentConfig{Type: graphql.Boolean, DefaultValue: false},
				},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					rawDocs, _ := p.Args["documents"].([]any)
					docs := make([]domain.Document, 0, len(rawDocs))
					for _, raw := range rawDocs {
						m, _ := raw.(map[string]any)
						docs = append(docs, domain.Document{
							ID:       getString(m, "id"),
							Text:     getString(m, "text"),
							Language: getString(m, "language"),
							Aspects:  getStringList(m, "aspects"),
						})
					}
					includeAspects, _ := p.Args["includeAspects"].(bool)
					resp, err := g.Service.AnalyzeText(p.Context, service.AnalyzeTextRequest{
						Documents:      docs,
						IncludeAspects: includeAspects,
					})
					if err != nil {
						return nil, err
					}
					return resp, nil
				},
			},
			"analyzeTopic": &graphql.Field{
				Type: graphql.NewNonNull(topicAnalysisType),
				Args: graphql.FieldConfigArgument{
					"topic":            &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
					"platforms":        &graphql.ArgumentConfig{Type: graphql.NewList(graphql.String)},
					"limitPerPlatform": &graphql.ArgumentConfig{Type: graphql.Int},
					"aspects":          &graphql.ArgumentConfig{Type: graphql.NewList(graphql.String)},
					"language":         &graphql.ArgumentConfig{Type: graphql.String},
					"sinceSeconds":     &graphql.ArgumentConfig{Type: graphql.Int},
				},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					req := service.AnalyzeTopicRequest{
						Topic:            getStringFromArgs(p.Args, "topic"),
						Platforms:        getStringListFromArgs(p.Args, "platforms"),
						LimitPerPlatform: getIntFromArgs(p.Args, "limitPerPlatform"),
						Aspects:          getStringListFromArgs(p.Args, "aspects"),
						Language:         getStringFromArgs(p.Args, "language"),
						SinceSeconds:     int64(getIntFromArgs(p.Args, "sinceSeconds")),
					}
					return g.Service.AnalyzeTopic(p.Context, req)
				},
			},
		},
	})
	schema, _ := graphql.NewSchema(graphql.SchemaConfig{Query: root})
	return schema
}

// AnalyzeText resolver returns *service.AnalyzeTextResponse, which the
// "results" and "aggregate" subfields need to project.
//
// The graphql-go library will attempt to call methods or struct fields named
// after the field key — `Results` and `Aggregate` already match, so no extra
// resolver wiring is required.

func getString(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func getStringList(m map[string]any, k string) []string {
	raw, ok := m[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func getStringFromArgs(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

func getStringListFromArgs(args map[string]any, k string) []string {
	if v, ok := args[k].([]any); ok {
		return getStringList(map[string]any{k: v}, k)
	}
	return nil
}

func getIntFromArgs(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}
