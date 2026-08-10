# --- build stage ------------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

COPY api/go.mod api/go.sum ./api/
RUN cd api && go mod download

COPY proto ./proto
COPY api ./api
# CGO-free: the gateway has no C dependencies (modernc.org/sqlite was the only
# one, and the store package that used it is gone), so this emits a fully
# static binary that runs on scratch/distroless with no libc.
RUN cd api && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sentilyzerd ./cmd/sentilyzerd && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sentilyzer-harvest ./cmd/sentilyzer-harvest

# --- runtime stage ----------------------------------------------------------
# distroless static already ships ca-certificates (the connectors call HTTPS),
# tzdata, and a non-root user (uid 65532) — no shell, no package manager, no
# apk/adduser step to maintain.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/sentilyzerd /usr/local/bin/sentilyzerd
COPY --from=build /out/sentilyzer-harvest /usr/local/bin/sentilyzer-harvest

EXPOSE 8080 9090
ENV SENTILYZER_HTTP_ADDR=":8080" \
    SENTILYZER_GRPC_ADDR=":9090" \
    SENTILYZER_ML_ADDR="ml:50051"

# The binary probes its own /livez — distroless ships no shell or curl, so a
# `CMD curl ...` healthcheck cannot run here. Liveness is ML-independent, so
# this restarts only a genuinely wedged gateway, not one waiting on the worker.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD ["/usr/local/bin/sentilyzerd", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/sentilyzerd"]
