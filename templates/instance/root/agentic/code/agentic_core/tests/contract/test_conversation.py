import json
import unittest

from .harness import message
from substrate.conversation import conversation


def view(*lines, related=None):
    out = {"definition": {}, "messages": list(lines)}
    if related:
        out["related"] = related
    return out


class ConversationContract(unittest.TestCase):
    """The journal is the conversation: each line becomes a message with its role, in the order of the journal."""

    def test_each_kind_has_its_role_and_its_text(self):
        frame, msgs = conversation(view(
            message(1, "f", kind="framework", content={"text": "THE FRAME", "source": "s"}),
            message(2, "u", content="hello"),
            message(3, "p", kind="prompt", content={"step": "s", "instructions": "THE PROMPT", "model": {}}),
            message(4, "c", kind="completion", content={"step": "s", "output": {"b": 1, "a": [2]}, "usage": {}}),
            message(5, "i", kind="capacity_intention", content={"id": "i1", "target_capacity": "/root/x", "parameters": {"k": "v"}, "await_response": True}),
            message(6, "r", kind="capacity_response", content={"id": "i1", "response": {"status": "ok"}}),
            message(7, "a", kind="pilot", content={"content": "an answer"}),
            message(8, "k", kind="continuity", content="from a child", **{"from": "child_1"}),
            message(9, "s", kind="summary", content="what happened"),
        ))
        self.assertEqual(frame, ["THE FRAME"])
        self.assertEqual([(m["role"], m["content"]) for m in msgs], [
            ("user", "hello"),
            ("user", "THE PROMPT"),
            ("assistant", '{"a":[2],"b":1}'),                                                       # compact, keys ordered
            ("assistant", '{"await_response":true,"id":"i1","parameters":{"k":"v"},"target_capacity":"/root/x"}'),
            ("user", '{"id":"i1","response":{"status":"ok"}}'),
            ("assistant", "an answer"),
            ("user", '{"content":"from a child","from":"child_1"}'),
            ("user", "Summary of the previous continuity:\nwhat happened"),
        ])

    def test_an_answer_says_which_intention_it_answers(self):
        _, msgs = conversation(view(message(1, "r", kind="capacity_response", content={"response": {"status": "ok"}}, correlation={"intention_id": "i7"})))
        self.assertEqual(msgs[0]["content"], '{"intention_id":"i7","response":{"status":"ok"}}')        # the link is information: it is in the answer
        _, msgs = conversation(view(message(1, "r", kind="capacity_response", content={"response": {"status": "ok"}})))
        self.assertEqual(msgs[0]["content"], '{"response":{"status":"ok"}}')                         # an answer nobody asked for stays as it is

    def test_the_follow_up_of_the_machinery_is_not_part_of_the_conversation(self):
        _, msgs = conversation(view(*[message(i, f"m{i}", kind=k, content={"x": 1}) for i, k in enumerate(["event", "projection", "error", "control"], 1)]))
        self.assertEqual(msgs, [])

    def test_the_same_lines_always_give_the_same_text(self):
        lines = [message(1, "u", content={"b": 1, "a": 2}), message(2, "r", kind="capacity_response", content={"z": 1, "y": {"d": 1, "c": 2}})]
        self.assertEqual(json.dumps(conversation(view(*lines))), json.dumps(conversation(view(*reversed(lines[:1]), *lines[1:]))))
        self.assertEqual(conversation(view(*lines)), conversation(view(*lines)))                     # a prefix is a prefix: stable bytes

    def test_a_longer_journal_extends_the_messages_of_a_shorter_one_and_changes_none(self):
        lines = [message(i, f"u{i}", content=f"text {i}") for i in range(1, 6)]
        short, long = conversation(view(*lines[:3]))[1], conversation(view(*lines))[1]
        self.assertEqual(long[:3], short)                                                          # append only: what was sent is the prefix of what is sent

    def test_related_continuities_follow_the_own_lines(self):
        _, msgs = conversation(view(message(1, "u", content="mine"), related=[{"continuity_id": "k", "messages": [message(1, "k1", content="theirs")]}]))
        self.assertEqual([m["role"] for m in msgs], ["user", "user"])
        self.assertEqual(json.loads(msgs[1]["content"])["continuity_id"], "k")

    def test_no_frame_gives_no_frame_text(self):
        self.assertEqual(conversation(view(message(1, "u", content="x")))[0], [])


if __name__ == "__main__":
    unittest.main()
