"""Acknowledge a long-running capacity: an immediate `running`, then a heartbeat, until the call returns.

This is the one of the wrapper library (wrapper/keepalive.py, period BRIQUE_HEARTBEAT_S): the engine runtime turns the caller's timeout into an
inactivity budget that every `running` response renews, and the hosted function still returns once, normally, with the single final response.
"""
from wrapper.keepalive import keepalive

__all__ = ["keepalive"]
