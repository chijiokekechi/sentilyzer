import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Sentiment(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    SENTIMENT_UNSPECIFIED: _ClassVar[Sentiment]
    SENTIMENT_NEGATIVE: _ClassVar[Sentiment]
    SENTIMENT_NEUTRAL: _ClassVar[Sentiment]
    SENTIMENT_POSITIVE: _ClassVar[Sentiment]
SENTIMENT_UNSPECIFIED: Sentiment
SENTIMENT_NEGATIVE: Sentiment
SENTIMENT_NEUTRAL: Sentiment
SENTIMENT_POSITIVE: Sentiment

class Score(_message.Message):
    __slots__ = ("label", "confidence", "polarity", "probabilities")
    class ProbabilitiesEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: float
        def __init__(self, key: _Optional[str] = ..., value: _Optional[float] = ...) -> None: ...
    LABEL_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    POLARITY_FIELD_NUMBER: _ClassVar[int]
    PROBABILITIES_FIELD_NUMBER: _ClassVar[int]
    label: Sentiment
    confidence: float
    polarity: float
    probabilities: _containers.ScalarMap[str, float]
    def __init__(self, label: _Optional[_Union[Sentiment, str]] = ..., confidence: _Optional[float] = ..., polarity: _Optional[float] = ..., probabilities: _Optional[_Mapping[str, float]] = ...) -> None: ...

class AspectScore(_message.Message):
    __slots__ = ("aspect", "score")
    ASPECT_FIELD_NUMBER: _ClassVar[int]
    SCORE_FIELD_NUMBER: _ClassVar[int]
    aspect: str
    score: Score
    def __init__(self, aspect: _Optional[str] = ..., score: _Optional[_Union[Score, _Mapping]] = ...) -> None: ...

class Document(_message.Message):
    __slots__ = ("id", "text", "language", "aspects", "metadata")
    class MetadataEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    ASPECTS_FIELD_NUMBER: _ClassVar[int]
    METADATA_FIELD_NUMBER: _ClassVar[int]
    id: str
    text: str
    language: str
    aspects: _containers.RepeatedScalarFieldContainer[str]
    metadata: _containers.ScalarMap[str, str]
    def __init__(self, id: _Optional[str] = ..., text: _Optional[str] = ..., language: _Optional[str] = ..., aspects: _Optional[_Iterable[str]] = ..., metadata: _Optional[_Mapping[str, str]] = ...) -> None: ...

class DocumentResult(_message.Message):
    __slots__ = ("id", "overall", "aspects", "analyzed_at")
    ID_FIELD_NUMBER: _ClassVar[int]
    OVERALL_FIELD_NUMBER: _ClassVar[int]
    ASPECTS_FIELD_NUMBER: _ClassVar[int]
    ANALYZED_AT_FIELD_NUMBER: _ClassVar[int]
    id: str
    overall: Score
    aspects: _containers.RepeatedCompositeFieldContainer[AspectScore]
    analyzed_at: _timestamp_pb2.Timestamp
    def __init__(self, id: _Optional[str] = ..., overall: _Optional[_Union[Score, _Mapping]] = ..., aspects: _Optional[_Iterable[_Union[AspectScore, _Mapping]]] = ..., analyzed_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class AnalyzeTextRequest(_message.Message):
    __slots__ = ("documents", "include_aspects")
    DOCUMENTS_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_ASPECTS_FIELD_NUMBER: _ClassVar[int]
    documents: _containers.RepeatedCompositeFieldContainer[Document]
    include_aspects: bool
    def __init__(self, documents: _Optional[_Iterable[_Union[Document, _Mapping]]] = ..., include_aspects: bool = ...) -> None: ...

class AnalyzeTextResponse(_message.Message):
    __slots__ = ("results", "aggregate")
    RESULTS_FIELD_NUMBER: _ClassVar[int]
    AGGREGATE_FIELD_NUMBER: _ClassVar[int]
    results: _containers.RepeatedCompositeFieldContainer[DocumentResult]
    aggregate: Aggregate
    def __init__(self, results: _Optional[_Iterable[_Union[DocumentResult, _Mapping]]] = ..., aggregate: _Optional[_Union[Aggregate, _Mapping]] = ...) -> None: ...

class SourcedDocument(_message.Message):
    __slots__ = ("document", "platform", "source_url", "author", "posted_at")
    DOCUMENT_FIELD_NUMBER: _ClassVar[int]
    PLATFORM_FIELD_NUMBER: _ClassVar[int]
    SOURCE_URL_FIELD_NUMBER: _ClassVar[int]
    AUTHOR_FIELD_NUMBER: _ClassVar[int]
    POSTED_AT_FIELD_NUMBER: _ClassVar[int]
    document: Document
    platform: str
    source_url: str
    author: str
    posted_at: _timestamp_pb2.Timestamp
    def __init__(self, document: _Optional[_Union[Document, _Mapping]] = ..., platform: _Optional[str] = ..., source_url: _Optional[str] = ..., author: _Optional[str] = ..., posted_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class AnalyzeTopicRequest(_message.Message):
    __slots__ = ("topic", "platforms", "limit_per_platform", "aspects", "language", "since_seconds")
    TOPIC_FIELD_NUMBER: _ClassVar[int]
    PLATFORMS_FIELD_NUMBER: _ClassVar[int]
    LIMIT_PER_PLATFORM_FIELD_NUMBER: _ClassVar[int]
    ASPECTS_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    SINCE_SECONDS_FIELD_NUMBER: _ClassVar[int]
    topic: str
    platforms: _containers.RepeatedScalarFieldContainer[str]
    limit_per_platform: int
    aspects: _containers.RepeatedScalarFieldContainer[str]
    language: str
    since_seconds: int
    def __init__(self, topic: _Optional[str] = ..., platforms: _Optional[_Iterable[str]] = ..., limit_per_platform: _Optional[int] = ..., aspects: _Optional[_Iterable[str]] = ..., language: _Optional[str] = ..., since_seconds: _Optional[int] = ...) -> None: ...

class AnalyzeTopicResponse(_message.Message):
    __slots__ = ("topic", "results", "aggregate", "by_platform", "by_aspect")
    class ByPlatformEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: Aggregate
        def __init__(self, key: _Optional[str] = ..., value: _Optional[_Union[Aggregate, _Mapping]] = ...) -> None: ...
    class ByAspectEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: Aggregate
        def __init__(self, key: _Optional[str] = ..., value: _Optional[_Union[Aggregate, _Mapping]] = ...) -> None: ...
    TOPIC_FIELD_NUMBER: _ClassVar[int]
    RESULTS_FIELD_NUMBER: _ClassVar[int]
    AGGREGATE_FIELD_NUMBER: _ClassVar[int]
    BY_PLATFORM_FIELD_NUMBER: _ClassVar[int]
    BY_ASPECT_FIELD_NUMBER: _ClassVar[int]
    topic: str
    results: _containers.RepeatedCompositeFieldContainer[SourcedDocumentResult]
    aggregate: Aggregate
    by_platform: _containers.MessageMap[str, Aggregate]
    by_aspect: _containers.MessageMap[str, Aggregate]
    def __init__(self, topic: _Optional[str] = ..., results: _Optional[_Iterable[_Union[SourcedDocumentResult, _Mapping]]] = ..., aggregate: _Optional[_Union[Aggregate, _Mapping]] = ..., by_platform: _Optional[_Mapping[str, Aggregate]] = ..., by_aspect: _Optional[_Mapping[str, Aggregate]] = ...) -> None: ...

class SourcedDocumentResult(_message.Message):
    __slots__ = ("document", "result")
    DOCUMENT_FIELD_NUMBER: _ClassVar[int]
    RESULT_FIELD_NUMBER: _ClassVar[int]
    document: SourcedDocument
    result: DocumentResult
    def __init__(self, document: _Optional[_Union[SourcedDocument, _Mapping]] = ..., result: _Optional[_Union[DocumentResult, _Mapping]] = ...) -> None: ...

class Aggregate(_message.Message):
    __slots__ = ("mean_polarity", "label_counts", "modal_label", "sample_size")
    class LabelCountsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    MEAN_POLARITY_FIELD_NUMBER: _ClassVar[int]
    LABEL_COUNTS_FIELD_NUMBER: _ClassVar[int]
    MODAL_LABEL_FIELD_NUMBER: _ClassVar[int]
    SAMPLE_SIZE_FIELD_NUMBER: _ClassVar[int]
    mean_polarity: float
    label_counts: _containers.ScalarMap[str, int]
    modal_label: Sentiment
    sample_size: int
    def __init__(self, mean_polarity: _Optional[float] = ..., label_counts: _Optional[_Mapping[str, int]] = ..., modal_label: _Optional[_Union[Sentiment, str]] = ..., sample_size: _Optional[int] = ...) -> None: ...

class ListPlatformsRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class PlatformInfo(_message.Message):
    __slots__ = ("id", "display_name", "enabled", "disabled_reason")
    ID_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    ENABLED_FIELD_NUMBER: _ClassVar[int]
    DISABLED_REASON_FIELD_NUMBER: _ClassVar[int]
    id: str
    display_name: str
    enabled: bool
    disabled_reason: str
    def __init__(self, id: _Optional[str] = ..., display_name: _Optional[str] = ..., enabled: bool = ..., disabled_reason: _Optional[str] = ...) -> None: ...

class ListPlatformsResponse(_message.Message):
    __slots__ = ("platforms",)
    PLATFORMS_FIELD_NUMBER: _ClassVar[int]
    platforms: _containers.RepeatedCompositeFieldContainer[PlatformInfo]
    def __init__(self, platforms: _Optional[_Iterable[_Union[PlatformInfo, _Mapping]]] = ...) -> None: ...

class HealthRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class HealthResponse(_message.Message):
    __slots__ = ("healthy", "ml_reachable", "version")
    HEALTHY_FIELD_NUMBER: _ClassVar[int]
    ML_REACHABLE_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    healthy: bool
    ml_reachable: bool
    version: str
    def __init__(self, healthy: bool = ..., ml_reachable: bool = ..., version: _Optional[str] = ...) -> None: ...
