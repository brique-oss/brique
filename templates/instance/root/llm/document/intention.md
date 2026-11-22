# Intention — `/root/llm`

## Context responsibility

Create a `/root/llm` context whose responsibility is to make language-model inference accessible in Brique.

The context receives an already prepared inference request.

This request contains, in particular:

- the prompt to send to the model;
- inference parameters;
- the expected model characteristics or requirements.

From this request, `/root/llm` is responsible for resolving a suitable inference resource, executing the inference, and returning the response produced by the model.

The overall movement is:

```text
prompt
+ inference parameters
+ model requirements
↓
/root/llm
↓
resolution / routing
↓
model
↓
inference
↓
model response
```

## Inference request

The context does not construct the domain or reasoning prompt passed to it.

The prompt is an already prepared input of the inference request.

A request may also specify parameters such as:

- temperature or equivalent generation parameters;
- generation limit;
- expected response format when supported by the engine;
- reasoning level when the model or provider exposes this characteristic;
- other inference-specific parameters.

It may also express requirements for the desired model.

## Model characteristics

An inference request must be able to express the characteristics required for its execution without necessarily naming a concrete model directly.

These characteristics may include:

- model family or type;
- expected capability level;
- context-window size;
- reasoning capabilities;
- supported modalities;
- cost;
- latency;
- location;
- deployment constraints;
- local or remote availability;
- required hardware capabilities.

These characteristics are the input to the resolution mechanism.

## Resolution and routing

`/root/llm` must be able to resolve the requested characteristics to a concrete inference resource.

The movement can be represented as follows:

```text
inference requirements
↓
compatible model
↓
provider / engine
↓
endpoint / worker
↓
hardware resource
```

Resolution may take into account:

- model compatibility with the request;
- availability;
- load;
- cost;
- latency;
- deployment constraints;
- whether the model is local or remote;
- the state of execution resources.

## Load management and placement

Inference must be distributable across several available resources.

The context must allow a worker or execution resource to be selected according to factors including:

- current load;
- the loaded model;
- worker capabilities;
- GPU or other resource availability;
- constraints carried by the request.

Placement is part of the technical resolution of inference.

## Affinity and caching

Several related calls may benefit from affinity with the same execution resource when this allows an existing cache to be reused, particularly a context cache or an equivalent mechanism provided by the inference engine.

The resolution mechanism must be able to include this affinity among its placement criteria.

Affinity remains an execution property: it can optimize an inference but is not required for the inference to run.

## Model diversity

The context must support the use of multiple models.

The model may be selected according to the characteristics of the transformation to execute.

A simple request may be routed to a lightweight model.

A request requiring greater capability, context, or reasoning may be routed to a more powerful model.

Model selection is therefore an inference-resolution decision, not a fixed property of the caller.

## Result

`/root/llm` returns the response produced by inference.

The transformation that requested the inference is responsible for understanding this response, transforming it, validating it against a domain contract, modifying Matters, or emitting new Intentions.

The LLM context stops at returning the inference result.

## Inference is purely transient

`/root/llm` retains nothing. It receives a request, calls the model, and returns the response: the request and response are **intermediate products**, consumed by the next step and never treated as state. No Matter is created, read, or written by the inference path.

- The **request** arrives as the `request` parameter. Below 64 KiB it travels by value; above that threshold, the wrapper runtime transports it as `@file` (the calling contexts and `/root/llm` belong to different wrappers) and deletes it after reading. Neither the caller nor the Capacity chooses this representation: it depends on the address.
- **Routing does not read the request**: the provider is a separate parameter (`provider`) and the sole selection input. The request remains opaque to the router, which forwards it unchanged; its content is validated by the provider Capacity. The caller is responsible for keeping `provider` consistent with the provider declared in the request.
- The **response** returns in the same way, as a return value.
- The **Capacity contract does not change**: Capacities declare an `object` parameter named `request`, not how it arrives. A request passed by value remains valid for every caller.
- A Capacity that reads its request must resolve the parameter (`await params.value("request")`): reading the dictionary directly would return the unresolved transit reference.
- Anything that must be retained—for example, the model response as a fact in a Continuity—is retained by the transformation that requested inference, never by `/root/llm`. Keeping a record of exchanges is a testing or observation need carried by a dedicated provider (`infer.recorded` in the `agentic` test context), not by the inference path.

## Central intention

`/root/llm` carries Brique's generic LLM inference mechanism.

Its responsibility can be summarized as follows:

```text
receive
    a prompt
    inference parameters
    model requirements

resolve
    the model
    the engine / provider
    the execution resource

execute
    inference

return
    the model response
```

It therefore forms the boundary between an inference request expressed in Brique and the technical resources capable of executing it.
