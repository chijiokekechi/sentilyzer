# sentilyzer-ml

Python gRPC inference worker for Sentilyzer.

## Models

- **General sentiment** — `cardiffnlp/twitter-roberta-base-sentiment-latest`
  (RoBERTa-base fine-tuned on ~124M tweets, 3-class). Strong on consumer/social
  text and the model card is permissive (MIT).
- **Aspect-based sentiment** — `yangheng/deberta-v3-base-absa-v1.1`
  (DeBERTa-v3 fine-tuned on SemEval ABSA datasets). Used when the caller
  supplies an `aspects` list ("battery", "service", "UI"…).

Both models are downloaded on first call into the HuggingFace cache and are
held in memory thereafter. CPU inference works; pass `SENTILYZER_ML_DEVICE=cuda`
or `mps` to use accelerator.

## Run

```bash
python -m pip install -e ".[dev]"
python -m sentilyzer_ml.server         # listens on :50051
```

Set `SENTILYZER_ML_USE_STUB=1` to bypass HuggingFace and use a deterministic
heuristic — useful in CI and when the API server boots without network access.
