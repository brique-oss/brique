/* Copyright 2026 Nicolas Cassan. Licensed under Apache-2.0. */

import assert from "node:assert/strict";
import test from "node:test";

import { AuthoritySwitchTransport } from "../browser/AuthoritySwitchTransport.js";
import type {
	CirculationMessage,
	Transport,
	TransportConnectionState,
	Unsubscribe,
} from "../../pulse/runtime/index.js";

class ControlledTransport implements Transport {
	readonly sent: CirculationMessage[] = [];
	closed = false;
	private state: TransportConnectionState;
	private readonly messages = new Set<(message: CirculationMessage) => void>();
	private readonly states = new Set<(state: TransportConnectionState) => void>();

	constructor(state: TransportConnectionState) { this.state = state; }
	async send(message: CirculationMessage): Promise<void> {
		if (this.closed) throw new Error("closed");
		this.sent.push(message);
	}
	subscribe(callback: (message: CirculationMessage) => void): Unsubscribe {
		this.messages.add(callback);
		return () => { this.messages.delete(callback); };
	}
	subscribeToConnectionState(callback: (state: TransportConnectionState) => void): Unsubscribe {
		this.states.add(callback);
		callback(this.state);
		return () => { this.states.delete(callback); };
	}
	receive(message: CirculationMessage): void { for (const callback of this.messages) callback(message); }
	setState(state: TransportConnectionState): void {
		this.state = state;
		for (const callback of this.states) callback(state);
	}
	close(): void { this.closed = true; }
}

const message = { kind: "response", response: {
	intention_id: "authority-switch", status: "ok", to: { context: "@ui_root" },
} } as CirculationMessage;

test("browser authority switches only after identified bridge connection and tears down each channel", async () => {
	const publicTransports: ControlledTransport[] = [];
	const bridge = new ControlledTransport("disconnected");
	const authorities: string[] = [];
	const transport = new AuthoritySwitchTransport(() => {
		const current = new ControlledTransport("connected");
		publicTransports.push(current);
		return current;
	}, bridge, (authority) => authorities.push(authority));
	const states: TransportConnectionState[] = [];
	const received: CirculationMessage[] = [];
	transport.subscribeToConnectionState((state) => states.push(state));
	transport.subscribe((value) => received.push(value));

	await transport.send(message);
	assert.equal(publicTransports.length, 1);
	assert.equal(publicTransports[0].sent.length, 1);
	publicTransports[0].receive(message);
	assert.equal(received.length, 1);

	bridge.setState("connected");
	assert.equal(publicTransports[0].closed, true);
	assert.deepEqual(states.slice(-2), ["disconnected", "connected"]);
	await transport.send(message);
	assert.equal(bridge.sent.length, 1);
	publicTransports[0].receive(message);
	assert.equal(received.length, 1, "closed public channel still delivered")

	bridge.setState("disconnected");
	assert.equal(bridge.closed, true);
	assert.equal(publicTransports.length, 2);
	await transport.send(message);
	assert.equal(publicTransports[1].sent.length, 1);
	assert.deepEqual(authorities.filter((value) => value === "identified"), ["identified"]);
	assert.equal(authorities.at(-1), "public");

	transport.close();
	assert.equal(publicTransports[1].closed, true);
});
