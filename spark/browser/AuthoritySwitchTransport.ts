/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

import type {
	CirculationMessage,
	Transport,
	TransportConnectionState,
	Unsubscribe,
} from "../../pulse/runtime/index.js";

export type BrowserAuthority = "public" | "identified" | "disconnected";

type ActiveTransport = {
	kind: "public" | "identified";
	transport: Transport;
	unsubMessage: Unsubscribe;
	unsubState?: Unsubscribe;
};

// AuthoritySwitchTransport is deliberately a transport concern: Pulse sees a
// real disconnect before a different authority becomes active and therefore
// invalidates flows, readers and subscriptions owned by the previous channel.
export class AuthoritySwitchTransport implements Transport {
	private readonly publicFactory: () => Transport;
	private bridge?: Transport;
	private bridgeState?: Unsubscribe;
	private active?: ActiveTransport;
	private readonly subscribers = new Set<(message: CirculationMessage) => void>();
	private readonly stateSubscribers = new Set<(state: TransportConnectionState) => void>();
	private state: TransportConnectionState = "disconnected";
	private closed = false;
	private readonly authorityChanged?: (authority: BrowserAuthority) => void;

	constructor(publicFactory: () => Transport, bridge?: Transport,
		authorityChanged?: (authority: BrowserAuthority) => void) {
		this.publicFactory = publicFactory;
		this.bridge = bridge;
		this.authorityChanged = authorityChanged;
		this.activatePublic();
		if (bridge?.subscribeToConnectionState) {
			this.bridgeState = bridge.subscribeToConnectionState((state) => {
				if (this.closed) return;
				if (state === "connected" && this.active?.kind !== "identified") {
					this.activateIdentified();
				} else if (state === "disconnected" && this.active?.kind === "identified") {
					this.bridgeState?.();
					this.bridgeState = undefined;
					this.bridge = undefined;
					this.activatePublic();
				}
			});
		}
	}

	send(message: CirculationMessage): Promise<void> {
		if (this.closed || !this.active) return Promise.reject(new Error("Browser authority transport is closed"));
		return this.active.transport.send(message);
	}

	subscribe(callback: (message: CirculationMessage) => void): Unsubscribe {
		this.subscribers.add(callback);
		return () => { this.subscribers.delete(callback); };
	}

	subscribeToConnectionState(callback: (state: TransportConnectionState) => void): Unsubscribe {
		this.stateSubscribers.add(callback);
		callback(this.state);
		return () => { this.stateSubscribers.delete(callback); };
	}

	close(): void {
		if (this.closed) return;
		this.closed = true;
		this.bridgeState?.();
		this.bridgeState = undefined;
		const active = this.detachActive();
		active?.transport.close();
		if (this.bridge && this.bridge !== active?.transport) this.bridge.close();
		this.bridge = undefined;
		this.setState("disconnected");
		this.authorityChanged?.("disconnected");
		this.subscribers.clear();
		this.stateSubscribers.clear();
	}

	private activatePublic(): void {
		if (this.closed) return;
		const previous = this.detachActive();
		previous?.transport.close();
		this.setState("disconnected");
		const transport = this.publicFactory();
		const active: ActiveTransport = {
			kind: "public",
			transport,
			unsubMessage: () => {},
		};
		this.active = active;
		active.unsubMessage = transport.subscribe((message) => {
				if (this.active === active) this.publish(message);
			});
		if (transport.subscribeToConnectionState) {
			active.unsubState = transport.subscribeToConnectionState((state) => {
				if (this.active === active) {
					this.setState(state);
					this.authorityChanged?.(state === "connected" ? "public" : "disconnected");
				}
			});
		}
		if (!transport.subscribeToConnectionState) {
			this.setState("connected");
			this.authorityChanged?.("public");
		}
	}

	private activateIdentified(): void {
		if (this.closed || !this.bridge) return;
		const previous = this.detachActive();
		previous?.transport.close();
		this.setState("disconnected");
		const transport = this.bridge;
		const active: ActiveTransport = {
			kind: "identified",
			transport,
			unsubMessage: () => {},
		};
		this.active = active;
		active.unsubMessage = transport.subscribe((message) => {
				if (this.active === active) this.publish(message);
			});
		this.setState("connected");
		this.authorityChanged?.("identified");
	}

	private detachActive(): ActiveTransport | undefined {
		const active = this.active;
		this.active = undefined;
		active?.unsubMessage();
		active?.unsubState?.();
		return active;
	}

	private publish(message: CirculationMessage): void {
		for (const subscriber of this.subscribers) subscriber(message);
	}

	private setState(state: TransportConnectionState): void {
		if (this.state === state) return;
		this.state = state;
		for (const subscriber of this.stateSubscribers) subscriber(state);
	}
}
