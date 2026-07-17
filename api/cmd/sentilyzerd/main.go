// sentilyzerd is the Sentilyzer API gateway.
//
// One process serves all four protocol surfaces backed by a single Service:
//
//	REST/JSON  on $SENTILYZER_HTTP_ADDR (default :8080)   /v1/*
//	REST/XML   on the same listener (content negotiation) Accept: application/xml
//	GraphQL    on the same listener                       /graphql
//	gRPC       on $SENTILYZER_GRPC_ADDR (default :9090)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/config"
	"github.com/chijiokekechi/sentilyzer/api/internal/connectors"
	"github.com/chijiokekechi/sentilyzer/api/internal/inference"
	"github.com/chijiokekechi/sentilyzer/api/internal/server"
	"github.com/chijiokekechi/sentilyzer/api/internal/service"
	"github.com/chijiokekechi/sentilyzer/api/internal/store"
	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
)

const Version = "0.1.0"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}
	logger.Info("starting sentilyzerd",
		"version", Version,
		"http", cfg.HTTPAddr,
		"grpc", cfg.GRPCAddr,
		"ml", cfg.MLAddr,
		"use_mock", cfg.UseMock,
	)

	infClient, err := inference.Dial(cfg.MLAddr, inference.WithMaxBatch(cfg.MLMaxBatch))
	if err != nil {
		logger.Error("inference dial failed", "err", err)
		os.Exit(1)
	}
	defer infClient.Close()

	st, err := store.Open(cfg.DBDSN)
	if err != nil {
		logger.Warn("store unavailable, continuing without persistence", "err", err)
	} else {
		defer st.Close()
	}

	registry := connectors.BuildRegistry(cfg)
	for _, info := range registry.Info() {
		logger.Info("connector",
			"id", info.ID,
			"display", info.DisplayName,
			"enabled", info.Enabled,
			"reason", info.DisabledReason,
		)
	}

	svc, err := service.New(infClient, registry, st, cfg.CacheTTL)
	if err != nil {
		logger.Error("service init failed", "err", err)
		os.Exit(1)
	}

	rest := &server.REST{Service: svc, Version: Version}
	gql := &server.GraphQL{Service: svc}
	grpcSrv := &server.GRPC{Service: svc, Version: Version}

	root := chi.NewRouter()
	root.Mount("/", rest.Router())
	root.Handle("/graphql", gql.Handler())

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
	}

	gSrv := grpc.NewServer()
	grpcSrv.Register(gSrv)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, 2)

	go func() {
		logger.Info("HTTP listening", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		lis, err := net.Listen("tcp", cfg.GRPCAddr)
		if err != nil {
			errCh <- err
			return
		}
		logger.Info("gRPC listening", "addr", cfg.GRPCAddr)
		if err := gSrv.Serve(lis); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		logger.Error("server error", "err", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	gSrv.GracefulStop()
	logger.Info("sentilyzerd stopped")
}
