from sentilyzer.v1 import sentilyzer_pb2 as _sentilyzer_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class ClassifyRequest(_message.Message):
    __slots__ = ("texts", "language")
    TEXTS_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    texts: _containers.RepeatedScalarFieldContainer[str]
    language: str
    def __init__(self, texts: _Optional[_Iterable[str]] = ..., language: _Optional[str] = ...) -> None: ...

class ClassifyResponse(_message.Message):
    __slots__ = ("scores",)
    SCORES_FIELD_NUMBER: _ClassVar[int]
    scores: _containers.RepeatedCompositeFieldContainer[_sentilyzer_pb2.Score]
    def __init__(self, scores: _Optional[_Iterable[_Union[_sentilyzer_pb2.Score, _Mapping]]] = ...) -> None: ...

class AspectInput(_message.Message):
    __slots__ = ("text", "aspects")
    TEXT_FIELD_NUMBER: _ClassVar[int]
    ASPECTS_FIELD_NUMBER: _ClassVar[int]
    text: str
    aspects: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, text: _Optional[str] = ..., aspects: _Optional[_Iterable[str]] = ...) -> None: ...

class AspectResult(_message.Message):
    __slots__ = ("scores",)
    SCORES_FIELD_NUMBER: _ClassVar[int]
    scores: _containers.RepeatedCompositeFieldContainer[_sentilyzer_pb2.AspectScore]
    def __init__(self, scores: _Optional[_Iterable[_Union[_sentilyzer_pb2.AspectScore, _Mapping]]] = ...) -> None: ...

class ClassifyAspectsRequest(_message.Message):
    __slots__ = ("inputs",)
    INPUTS_FIELD_NUMBER: _ClassVar[int]
    inputs: _containers.RepeatedCompositeFieldContainer[AspectInput]
    def __init__(self, inputs: _Optional[_Iterable[_Union[AspectInput, _Mapping]]] = ...) -> None: ...

class ClassifyAspectsResponse(_message.Message):
    __slots__ = ("results",)
    RESULTS_FIELD_NUMBER: _ClassVar[int]
    results: _containers.RepeatedCompositeFieldContainer[AspectResult]
    def __init__(self, results: _Optional[_Iterable[_Union[AspectResult, _Mapping]]] = ...) -> None: ...

class ReadyRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ReadyResponse(_message.Message):
    __slots__ = ("ready", "general_model", "aspect_model", "device")
    READY_FIELD_NUMBER: _ClassVar[int]
    GENERAL_MODEL_FIELD_NUMBER: _ClassVar[int]
    ASPECT_MODEL_FIELD_NUMBER: _ClassVar[int]
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    ready: bool
    general_model: str
    aspect_model: str
    device: str
    def __init__(self, ready: _Optional[bool] = ..., general_model: _Optional[str] = ..., aspect_model: _Optional[str] = ..., device: _Optional[str] = ...) -> None: ...
