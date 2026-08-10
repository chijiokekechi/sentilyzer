"""Training-pipeline stages (prep → label → distill → eval → promote).

These run on Modal in production but are plain functions first, so every
stage that can be verified without a GPU is unit-tested locally.
"""
