# --- build stage ------------------------------------------------------------
FROM golang:1.25-alpine AS build

RUN apk add --no-cache build-base git
WORKDIR /src

COPY api/go.mod api/go.sum ./api/
RUN cd api && go mod download

COPY proto ./proto
COPY api ./api
RUN cd api && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/sentilyzerd ./cmd/sentilyzerd

# --- runtime stage ----------------------------------------------------------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S sentilyzer && adduser -S sentilyzer -G sentilyzer

COPY --from=build /out/sentilyzerd /usr/local/bin/sentilyzerd

USER sentilyzer
EXPOSE 8080 9090
ENV SENTILYZER_HTTP_ADDR=":8080" \
    SENTILYZER_GRPC_ADDR=":9090" \
    SENTILYZER_ML_ADDR="ml:50051"

ENTRYPOINT ["/usr/local/bin/sentilyzerd"]
