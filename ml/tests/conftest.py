"""Test bootstrap: make the generated gRPC stubs importable.

sentilyzer_ml/__init__.py prepends its gen/ root to sys.path, and the
generated `sentilyzer.v1` package only imports after that has happened.
Importing it here guarantees every test module can import the stubs
directly, in any order isort chooses, whether the file runs alone or in
the full suite.
"""

import sentilyzer_ml  # noqa: F401
