"""Sentilyzer ML inference worker."""

import os
import sys

# Generated protobuf stubs use absolute imports (`from sentilyzer.v1 import …`),
# so we prepend the gen/ root to sys.path before any submodule imports the
# stubs. Doing it once here keeps every consumer simple.
_GEN_ROOT = os.path.join(os.path.dirname(__file__), "gen")
if _GEN_ROOT not in sys.path:
    sys.path.insert(0, _GEN_ROOT)

__version__ = "0.1.0"
