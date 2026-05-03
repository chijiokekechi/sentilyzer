FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_NO_CACHE_DIR=1 \
    HF_HOME=/cache/huggingface \
    SENTILYZER_ML_LISTEN="[::]:50051"

WORKDIR /app

# System deps for tokenizers + sentencepiece (DeBERTa).
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY ml/pyproject.toml ./ml/
COPY ml/sentilyzer_ml ./ml/sentilyzer_ml
COPY proto ./proto

# Generate Python stubs at image-build time so we don't ship stale ones.
RUN pip install grpcio-tools && \
    mkdir -p ml/sentilyzer_ml/gen && \
    python -m grpc_tools.protoc \
        -I proto \
        --python_out=ml/sentilyzer_ml/gen \
        --grpc_python_out=ml/sentilyzer_ml/gen \
        proto/sentilyzer/v1/sentilyzer.proto proto/sentilyzer/v1/inference.proto && \
    pip install ./ml

# Pre-create the cache dir; models are downloaded on first call.
RUN mkdir -p /cache/huggingface && chmod 0777 /cache/huggingface

EXPOSE 50051
ENTRYPOINT ["python", "-m", "sentilyzer_ml.server"]
