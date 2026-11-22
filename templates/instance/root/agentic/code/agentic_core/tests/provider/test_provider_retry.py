"""A transient failure of a provider is asked again; any other answer is not. Against the REAL helper of the llm context, with a mock
transport: no network, no key, no cost."""
import asyncio
import importlib.util
import os
import sys
import unittest

import httpx

HERE = os.path.dirname(os.path.abspath(__file__))
AGENT_CORE = os.path.dirname(os.path.dirname(HERE))
LLM_DIR = os.path.normpath(os.path.join(AGENT_CORE, "..", "..", "..", "llm", "code", "llm_llm"))
WRAPPER_LIB = os.path.normpath(os.path.join(AGENT_CORE, "..", "..", "..", "..", "wrapper", "python", "lib"))   # the runtime library of the wrappers
if WRAPPER_LIB not in sys.path:
    sys.path.insert(0, WRAPPER_LIB)


def load(name):
    if LLM_DIR not in sys.path:
        sys.path.insert(0, LLM_DIR)
    spec = importlib.util.spec_from_file_location(name, os.path.join(LLM_DIR, f"{name}.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


retry = load("retry")


class Provider:
    """A provider that answers the given statuses in turn (an exception in the list is a broken connection)."""
    def __init__(self, *answers, headers=None):
        self.answers, self.headers, self.posts, self.waits = list(answers), headers or {}, 0, []

    async def run(self, attempts=3):
        async def handler(request):
            self.posts += 1
            answer = self.answers.pop(0)
            if isinstance(answer, Exception):
                raise answer
            return httpx.Response(answer, headers=self.headers, json={"status": answer})

        async def sleep(seconds):
            self.waits.append(seconds)
        async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
            return await retry.post_with_retry(client, "http://provider.test/v1", headers={}, json={}, attempts=attempts, sleep=sleep)


class TransientRetry(unittest.IsolatedAsyncioTestCase):
    async def test_a_rate_limit_then_an_overload_then_success_is_one_inference(self):
        provider = Provider(429, 529, 200)
        response = await provider.run()
        self.assertEqual((response.status_code, provider.posts, provider.waits), (200, 3, [1.0, 2.0]))     # waiting longer each time

    async def test_a_refusal_is_returned_at_once(self):
        for status in (400, 401, 403, 404):
            provider = Provider(status, 200)
            response = await provider.run()
            self.assertEqual((response.status_code, provider.posts, provider.waits), (status, 1, []), status)

    async def test_a_failure_that_persists_is_returned_after_three_attempts(self):
        provider = Provider(503, 503, 503)
        response = await provider.run()
        self.assertEqual((response.status_code, provider.posts), (503, 3))                                  # the adapter then raises its usual error

    async def test_the_retry_after_of_the_provider_is_obeyed_up_to_a_ceiling(self):
        provider = Provider(429, 429, 200, headers={"retry-after": "2"})
        await provider.run()
        self.assertEqual(provider.waits, [2.0, 2.0])
        far = Provider(429, 200, headers={"retry-after": "9999"})
        await far.run()
        self.assertEqual(far.waits, [retry.MAX_DELAY_S])

    async def test_a_broken_connection_is_asked_again_and_raised_when_it_persists(self):
        provider = Provider(httpx.ConnectError("reset"), 200)
        self.assertEqual((await provider.run()).status_code, 200)
        broken = Provider(httpx.ConnectError("a"), httpx.ConnectError("b"), httpx.ConnectError("c"))
        with self.assertRaises(httpx.ConnectError):
            await broken.run()
        self.assertEqual(broken.posts, 3)

    def test_an_answer_cut_at_max_tokens_says_so(self):
        anthropic = load("infer_anthropic")
        cut = {"content": [{"type": "text", "text": '{"intentions": [{"id"'}], "stop_reason": "max_tokens", "usage": {}}
        with self.assertRaises(anthropic.CapacityCallError) as caught:                 # a stable code, not a bare ValueError
            anthropic._normalize_response(cut, 1, "json_schema")
        self.assertIn("max_tokens", str(caught.exception))
        with self.assertRaises(ValueError) as other:                                  # a malformed answer that was not cut stays what it was
            anthropic._normalize_response(dict(cut, stop_reason="end_turn"), 1, "json_schema")
        self.assertNotIn("max_tokens", str(other.exception))


if __name__ == "__main__":
    unittest.main()
