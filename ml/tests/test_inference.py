"""Unit tests for the heuristic stub backend.

These tests deliberately avoid HuggingFace; the real model is exercised by
integration smoke tests run against a live container.
"""

from sentilyzer_ml import inference


def test_score_label_positive():
    b = inference.HeuristicBackend()
    [pred] = b.classify(["I love this app, it is amazing"])
    assert pred.label == "positive"
    assert pred.polarity > 0
    assert sum(pred.probabilities) == 1 or abs(sum(pred.probabilities) - 1) < 1e-6


def test_score_label_negative():
    b = inference.HeuristicBackend()
    [pred] = b.classify(["This is the worst, terrible experience"])
    assert pred.label == "negative"
    assert pred.polarity < 0


def test_score_label_neutral_when_no_matches():
    b = inference.HeuristicBackend()
    [pred] = b.classify(["It is a chair"])
    assert pred.label == "neutral"
    assert pred.polarity == 0.0


def test_aspect_window_returns_per_aspect_predictions():
    b = inference.HeuristicBackend()
    [row] = b.classify_aspects([("Battery is amazing.", ["battery", "weight"])])
    assert [a for a, _ in row] == ["battery", "weight"]


def test_make_backend_stub_routes_to_heuristic():
    b = inference.make_backend(
        use_stub=True,
        general_model="ignored",
        aspect_model="ignored",
        device="cpu",
    )
    assert isinstance(b, inference.HeuristicBackend)


def test_prediction_as_dict_keys():
    b = inference.HeuristicBackend()
    [pred] = b.classify(["I love this"])
    d = pred.as_dict()
    assert set(d) == {"negative", "neutral", "positive"}
