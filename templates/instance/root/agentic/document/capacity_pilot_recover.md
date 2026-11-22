# Capacity — `pilot.recover`

## Intention

Explicitly restart Continuities whose run died. Nothing restarts them automatically when the wrapper restarts; they wait until a message arrives. `pilot.recover` is the call that restarts them when the architect chooses to do so.

## Form

A DSL Capacity with two sections:

1. `#find`: `continuity.stranded` identifies Continuities that cannot proceed on their own.
2. `#wake`: for each one, call `pilot.activate` with `wake: true` without waiting. A wake-up that acquires the run drives it to completion; waiting for it would make `pilot.recover` wait for every Continuity.

No message is created and no Intention is replayed. Recovery belongs to `pilot.activate`: the claim moves to the new run and a recovery stimulus reports that the previous run was interrupted.

## What formalization revealed before implementation

- `wake` was not declared among the inputs of `pilot.activate`.
- An absent optional input cannot be forwarded directly in DSL because `$input.x` fails; an `exists` condition selects between two calls.
- A `>for_each` alias must be named `item` for `$item` to resolve. A different alias did not resolve. With `on_error: continue` and a non-blocking wake-up the error was invisible; only integration scenario S46 exposed it.
